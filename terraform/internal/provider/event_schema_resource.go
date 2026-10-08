package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jamescarr/terraform-provider-santati/internal/api"
)

var (
	_ resource.Resource                = (*eventSchemaResource)(nil)
	_ resource.ResourceWithConfigure   = (*eventSchemaResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*eventSchemaResource)(nil)
	_ resource.ResourceWithImportState = (*eventSchemaResource)(nil)
)

// Schema version statuses, as the control plane spells them.
const (
	schemaStatusDraft      = "draft"
	schemaStatusPublished  = "published"
	schemaStatusDeprecated = "deprecated"
)

// NewEventSchemaResource is the santati_event_schema resource factory.
func NewEventSchemaResource() resource.Resource { return &eventSchemaResource{} }

type eventSchemaResource struct {
	client *api.Client
}

type eventSchemaModel struct {
	Action              types.String         `tfsdk:"action"`
	Schema              jsontypes.Normalized `tfsdk:"schema"`
	Publish             types.Bool           `tfsdk:"publish"`
	Version             types.Int64          `tfsdk:"version"`
	Status              types.String         `tfsdk:"status"`
	PublishedAt         types.String         `tfsdk:"published_at"`
	DeprecatedAt        types.String         `tfsdk:"deprecated_at"`
	DeprecationDeadline types.String         `tfsdk:"deprecation_deadline"`
	CreatedAt           types.String         `tfsdk:"created_at"`
	UpdatedAt           types.String         `tfsdk:"updated_at"`
}

func (r *eventSchemaResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_event_schema"
}

func (r *eventSchemaResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The current JSON Schema of one event action, managed as numbered schema versions. " +
			"The action must already be defined in the event catalog. " +
			"Use one `santati_event_schema` per action.\n\n" +
			"Changing a published document creates and publishes a new version, and the server deprecates the old one, " +
			"which still accepts producers pinned to it until its deadline.\n\n" +
			"Published versions cannot be deleted: destroying this resource deletes an unpublished draft, " +
			"but leaves a published version in force and only forgets it.",
		Attributes: map[string]schema.Attribute{
			"action": schema.StringAttribute{
				MarkdownDescription: "The event action the schema applies to, e.g. `invoice.voided`. Changing it replaces the resource.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"schema": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "A JSON Schema 2020-12 document as a JSON object, usually built with `jsonencode(...)`. " +
					"It validates the event's `metadata`, `actor.metadata` and each `targets[].metadata`, whose values are always strings. " +
					"Every `$ref` must point inside the document. " +
					"Key order and whitespace are ignored. The provider does no other validation; the control plane is the validator.",
				Required: true,
			},
			"publish": schema.BoolAttribute{
				MarkdownDescription: "Publish the document so ingest validates against it. Defaults to `true`. " +
					"With `false`, the document is kept as a draft that validates nothing and is edited in place. " +
					"Setting it to `false` never unpublishes a version.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"version": schema.Int64Attribute{
				MarkdownDescription: "The schema version number this resource currently tracks. Numbers are never reused.",
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "The tracked version's status: `draft`, `published` or `deprecated`. " +
					"A version becomes `deprecated` only when a newer version is published.",
				Computed: true,
			},
			"published_at": schema.StringAttribute{
				MarkdownDescription: "When the tracked version was published, or null while it is a draft.",
				Computed:            true,
			},
			"deprecated_at": schema.StringAttribute{
				MarkdownDescription: "When the tracked version was deprecated, or null.",
				Computed:            true,
			},
			"deprecation_deadline": schema.StringAttribute{
				MarkdownDescription: "Until when producers pinned to the tracked version are still accepted after it was deprecated, or null.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the tracked version was created.",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "When the tracked version was last changed.",
				Computed:            true,
			},
		},
	}
}

func (r *eventSchemaResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// ModifyPlan re-asserts a version that was superseded out of band. When
// someone else publishes a newer version, Read finds the tracked version
// `deprecated`; with the document unchanged and `publish = true` there would
// be no diff, and the document that ingest validates against would silently
// differ from the configuration. Planning `published` forces an Update, which
// publishes the document again as a new version.
func (r *eventSchemaResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}

	var plan, state eventSchemaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Status.ValueString() != schemaStatusDeprecated ||
		plan.Publish.IsUnknown() || !plan.Publish.ValueBool() ||
		plan.Schema.IsUnknown() {
		return
	}

	changed, diags := schemaChanged(ctx, plan.Schema, state.Schema)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() || changed {
		return
	}

	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringValue(schemaStatusPublished))...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("version"), types.Int64Unknown())...)
	for _, name := range []string{"published_at", "deprecated_at", "deprecation_deadline", "created_at", "updated_at"} {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), types.StringUnknown())...)
	}
}

func (r *eventSchemaResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan eventSchemaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	action := plan.Action.ValueString()

	cur, err := r.client.CreateSchemaVersion(ctx, action, json.RawMessage(plan.Schema.ValueString()))
	if err != nil {
		addSchemaWriteError(&resp.Diagnostics, "create", action, err)
		return
	}

	state := plan
	if plan.Publish.ValueBool() {
		published, err := r.client.PublishSchemaVersion(ctx, action, cur.Version)
		if err != nil {
			// Save the draft so Terraform taints the resource: the next apply
			// deletes it before creating again, leaving no orphan draft.
			state.Publish = types.BoolValue(false)
			resp.Diagnostics.Append(flattenSchemaVersion(cur, &state)...)
			state.Schema = plan.Schema
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			addSchemaWriteError(&resp.Diagnostics, "publish", action, err)
			return
		}
		cur = published
	}

	resp.Diagnostics.Append(flattenSchemaVersion(cur, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *eventSchemaResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state eventSchemaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cur, err := r.client.GetSchemaVersion(ctx, state.Action.ValueString(), state.Version.ValueInt64())
	if api.IsNotFound(err) {
		// The draft, the version or the whole action is gone.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read event schema", err.Error())
		return
	}

	resp.Diagnostics.Append(flattenSchemaVersion(cur, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Publish.IsNull() {
		// Only an imported resource has no `publish` yet.
		state.Publish = types.BoolValue(cur.Status != schemaStatusDraft)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *eventSchemaResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior eventSchemaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	changed, diags := schemaChanged(ctx, plan.Schema, prior.Schema)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	action := plan.Action.ValueString()
	doc := json.RawMessage(plan.Schema.ValueString())
	publish := plan.Publish.ValueBool()
	version, status := prior.Version.ValueInt64(), prior.Status.ValueString()

	var (
		cur *api.SchemaVersion
		err error
	)
	switch {
	case changed && status == schemaStatusDraft:
		cur, err = r.client.UpdateSchemaVersion(ctx, action, version, doc)
	case changed, status == schemaStatusDeprecated && publish:
		// A published or deprecated version is immutable: the new document is
		// a new version. An unchanged deprecated version was superseded out of
		// band (see ModifyPlan) and is published again the same way.
		cur, err = r.client.CreateSchemaVersion(ctx, action, doc)
	}
	if err != nil {
		addSchemaWriteError(&resp.Diagnostics, "update", action, err)
		return
	}
	if cur != nil {
		version, status = cur.Version, cur.Status
	}

	if publish && status == schemaStatusDraft {
		published, err := r.client.PublishSchemaVersion(ctx, action, version)
		if err != nil {
			addSchemaWriteError(&resp.Diagnostics, "publish", action, err)
			if cur != nil {
				// Keep track of the draft that was just written, as unpublished,
				// so the next apply publishes it instead of writing another.
				state := plan
				state.Publish = types.BoolValue(false)
				resp.Diagnostics.Append(flattenSchemaVersion(cur, &state)...)
				state.Schema = plan.Schema
				resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			}
			return
		}
		cur = published
	}

	if cur == nil {
		// Nothing was written (`publish` went from true to false, or the
		// document was only reformatted): read the computed values back.
		cur, err = r.client.GetSchemaVersion(ctx, action, version)
		if err != nil {
			resp.Diagnostics.AddError("Unable to update event schema", err.Error())
			return
		}
	}

	state := plan
	resp.Diagnostics.Append(flattenSchemaVersion(cur, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete removes an unpublished draft. A published or deprecated version
// cannot be deleted on the control plane, so it is left in place and only
// forgotten.
func (r *eventSchemaResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state eventSchemaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	action, version := state.Action.ValueString(), state.Version.ValueInt64()

	if state.Status.ValueString() == schemaStatusDraft {
		err := r.client.DeleteSchemaVersion(ctx, action, version)
		var apiErr *api.APIError
		switch {
		case err == nil, api.IsNotFound(err):
			return
		case errors.As(err, &apiErr) && apiErr.Status == 409:
			// Published out of band since the last refresh.
		default:
			resp.Diagnostics.AddError("Unable to delete event schema", err.Error())
			return
		}
	}

	if state.Status.ValueString() == schemaStatusDeprecated {
		resp.Diagnostics.AddWarning("Deprecated schema version left in place",
			fmt.Sprintf("Version %d of %q is deprecated and cannot be deleted. It was removed from Terraform state only.", version, action))
		return
	}
	resp.Diagnostics.AddWarning("Published schema version left in place",
		fmt.Sprintf("Version %d of %q is published and cannot be deleted; it keeps validating ingest. "+
			"It was removed from Terraform state only. "+
			"Publish a newer version, or delete the event definition, to stop enforcing it.", version, action))
}

// ImportState accepts `<action>` or `<action>/<version>`. A bare action
// imports the version ingest validates against: the newest published one, else
// the newest deprecated one, else the newest draft.
func (r *eventSchemaResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	invalid := func() {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf(`Expected "<action>" or "<action>/<version>", got %q.`, req.ID))
	}

	action := req.ID
	var version int64
	if i := strings.LastIndex(req.ID, "/"); i >= 0 {
		parsed, err := strconv.ParseInt(req.ID[i+1:], 10, 64)
		if err != nil || parsed < 1 {
			invalid()
			return
		}
		action, version = req.ID[:i], parsed
	}
	if action == "" {
		invalid()
		return
	}

	if version == 0 {
		versions, err := r.client.ListSchemaVersions(ctx, action)
		if err != nil {
			resp.Diagnostics.AddError("Unable to import event schema", err.Error())
			return
		}
		if len(versions) == 0 {
			resp.Diagnostics.AddError("Event schema not found", fmt.Sprintf("Action %q has no schema versions.", action))
			return
		}
		version = pickImportedVersion(versions).Version
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("action"), action)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("version"), version)...)
}

// pickImportedVersion chooses from a newest-first, non-empty list, mirroring
// the control plane's own choice of the version to validate against and
// falling back to drafts.
func pickImportedVersion(versions []api.SchemaVersion) api.SchemaVersion {
	for _, status := range []string{schemaStatusPublished, schemaStatusDeprecated} {
		for _, v := range versions {
			if v.Status == status {
				return v
			}
		}
	}
	return versions[0]
}

// flattenSchemaVersion copies a version into the model, leaving `action` and
// `publish` alone: neither is part of a version.
func flattenSchemaVersion(v *api.SchemaVersion, m *eventSchemaModel) diag.Diagnostics {
	var diags diag.Diagnostics

	var compact bytes.Buffer
	if err := json.Compact(&compact, v.Schema); err != nil {
		diags.AddError("Unable to read event schema", fmt.Sprintf("The control plane returned an invalid schema for version %d: %s", v.Version, err))
		return diags
	}

	m.Version = types.Int64Value(v.Version)
	m.Status = types.StringValue(v.Status)
	// The framework keeps the prior document when this one is semantically equal.
	m.Schema = jsontypes.NewNormalizedValue(compact.String())
	m.PublishedAt = nullableString(v.PublishedAt)
	m.DeprecatedAt = nullableString(v.DeprecatedAt)
	m.DeprecationDeadline = nullableString(v.DeprecationDeadline)
	m.CreatedAt = types.StringValue(v.CreatedAt)
	m.UpdatedAt = types.StringValue(v.UpdatedAt)
	return diags
}

// schemaChanged reports whether the planned document differs from the stored
// one in more than key order and whitespace.
func schemaChanged(ctx context.Context, plan, state jsontypes.Normalized) (bool, diag.Diagnostics) {
	equal, diags := plan.StringSemanticEquals(ctx, state)
	return !equal, diags
}

// addSchemaWriteError reports a failed create, update or publish call. A 404
// on those means the action has no event definition.
func addSchemaWriteError(diags *diag.Diagnostics, verb, action string, err error) {
	detail := err.Error()
	if api.IsNotFound(err) {
		detail += fmt.Sprintf("\n\nDefine the action %q in the event catalog first.", action)
	}
	diags.AddError("Unable to "+verb+" event schema", detail)
}
