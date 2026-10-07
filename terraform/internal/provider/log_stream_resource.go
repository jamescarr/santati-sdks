package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jamescarr/terraform-provider-santati/internal/api"
)

var (
	_ resource.Resource                = (*logStreamResource)(nil)
	_ resource.ResourceWithConfigure   = (*logStreamResource)(nil)
	_ resource.ResourceWithImportState = (*logStreamResource)(nil)
)

// NewLogStreamResource is the santati_log_stream resource factory.
func NewLogStreamResource() resource.Resource { return &logStreamResource{} }

type logStreamResource struct {
	client *api.Client
}

type logStreamModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Trail               types.String `tfsdk:"trail"`
	Status              types.String `tfsdk:"status"`
	DestinationType     types.String `tfsdk:"destination_type"`
	Config              types.Object `tfsdk:"config"`
	Auth                types.Object `tfsdk:"auth"`
	MatchRules          types.Object `tfsdk:"match_rules"`
	ConsecutiveFailures types.Int64  `tfsdk:"consecutive_failures"`
	LastError           types.String `tfsdk:"last_error"`
	LastErrorAt         types.String `tfsdk:"last_error_at"`
	ResumeCursor        types.String `tfsdk:"resume_cursor"`
	CreatedAt           types.String `tfsdk:"created_at"`
	UpdatedAt           types.String `tfsdk:"updated_at"`
}

func (r *logStreamResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_log_stream"
}

func (r *logStreamResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	otherMatchRules := func(names ...string) validator.List {
		expressions := make([]path.Expression, len(names))
		for i, name := range names {
			expressions[i] = path.MatchRelative().AtParent().AtName(name)
		}
		return listvalidator.AtLeastOneOf(expressions...)
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "A log stream: forwards a trail's events to a destination, currently an HTTPS webhook.\n\n" +
			"Every apply sends the whole configuration, and the control plane replaces `config` and `match_rules` as a unit, " +
			"so removing `headers` or a match rule clears it on the server. " +
			"Creating a stream needs an API key with the `manage` scope that is not restricted to specific trails.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The stream's id, as assigned by the control plane. Use it to import: `terraform import santati_log_stream.example 12`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The stream's name, unique within the team.",
				Required:            true,
			},
			"trail": schema.StringAttribute{
				MarkdownDescription: "The name of the trail whose events this stream forwards. The trail must exist.",
				Required:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "`active` forwards events, `inactive` pauses the stream. Defaults to `active`. " +
					"Delivery moves a stream to `error` after repeated failures and to `invalid` when its destination is unusable; " +
					"the next apply sees that as a difference from the configured status and sets it back.",
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString("active"),
				Validators: []validator.String{stringvalidator.OneOf("active", "inactive")},
			},
			"destination_type": schema.StringAttribute{
				MarkdownDescription: "The destination's type. Only `https` exists. Defaults to `https`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("https"),
				Validators:          []validator.String{stringvalidator.OneOf("https")},
			},
			"config": schema.SingleNestedAttribute{
				MarkdownDescription: "The destination's settings.",
				Required:            true,
				Attributes: map[string]schema.Attribute{
					"url": schema.StringAttribute{
						MarkdownDescription: "The `https://` URL each batch of events is POSTed to.",
						Required:            true,
					},
					"content_type": schema.StringAttribute{
						MarkdownDescription: "`json` sends one JSON object per batch; `ndjson` sends one JSON object per event, one per line. Defaults to `json`.",
						Optional:            true,
						Computed:            true,
						Default:             stringdefault.StaticString("json"),
						Validators:          []validator.String{stringvalidator.OneOf("json", "ndjson")},
					},
					"headers": schema.MapAttribute{
						MarkdownDescription: "Extra headers sent with every request. The names `content-encoding`, `content-type`, `user-agent` " +
							"and the `x-santati-*` delivery headers are reserved and refused. Put credentials in `auth`, not here.",
						ElementType: types.StringType,
						Optional:    true,
						Validators:  []validator.Map{mapvalidator.SizeAtLeast(1)},
					},
					"timeout_seconds": schema.Int64Attribute{
						MarkdownDescription: "The request timeout, 1 to 60 seconds. Defaults to 15, the control plane's effective default.",
						Optional:            true,
						Computed:            true,
						Default:             int64default.StaticInt64(api.DefaultTimeoutSeconds),
						Validators:          []validator.Int64{int64validator.Between(1, 60)},
					},
				},
			},
			"auth": schema.SingleNestedAttribute{
				MarkdownDescription: "A credential sent in a header with every request. The control plane never returns it: " +
					"Terraform stores the configured value in state to detect changes made here, " +
					"and cannot see a credential changed outside Terraform. Removing the block clears the stored credential.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"header_name": schema.StringAttribute{
						MarkdownDescription: "The header the credential is sent in: 1-64 letters, digits or hyphens. Defaults to `Authorization`.",
						Optional:            true,
						Computed:            true,
						Default:             stringdefault.StaticString("Authorization"),
					},
					"header_value": schema.StringAttribute{
						MarkdownDescription: "The credential, including any scheme, for example `Bearer abc123`.",
						Required:            true,
						Sensitive:           true,
					},
				},
			},
			"match_rules": schema.SingleNestedAttribute{
				MarkdownDescription: "Which of the trail's events this stream forwards; an event must satisfy every clause that is set. " +
					"Omit the block to forward every event. An empty block is not allowed.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"actions": schema.ListAttribute{
						MarkdownDescription: "Event actions to forward, as glob patterns such as `invoice.*`. At most 50.",
						ElementType:         types.StringType,
						Optional:            true,
						Validators: []validator.List{
							listvalidator.SizeAtLeast(1),
							otherMatchRules("actor_types", "organization_ids", "metadata"),
						},
					},
					"actor_types": schema.ListAttribute{
						MarkdownDescription: "Actor types to forward: `user`, `system` or `anonymous`.",
						ElementType:         types.StringType,
						Optional:            true,
						Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
					},
					"organization_ids": schema.ListAttribute{
						MarkdownDescription: "External organization ids to forward. At most 50.",
						ElementType:         types.StringType,
						Optional:            true,
						Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
					},
					"metadata": schema.ListNestedAttribute{
						MarkdownDescription: "Metadata clauses an event must satisfy. At most 20.",
						Optional:            true,
						Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"key": schema.StringAttribute{
									MarkdownDescription: "The metadata key to look up on the event.",
									Required:            true,
								},
								"op": schema.StringAttribute{
									MarkdownDescription: "`eq` matches the value exactly, `prefix` matches its start, `contains` matches anywhere in it.",
									Required:            true,
									Validators:          []validator.String{stringvalidator.OneOf("eq", "prefix", "contains")},
								},
								"value": schema.StringAttribute{
									MarkdownDescription: "The value the event's metadata key is compared against.",
									Required:            true,
								},
							},
						},
					},
				},
			},
			"consecutive_failures": schema.Int64Attribute{
				MarkdownDescription: "Failed batches in a row since the last delivery; five or more moves a stream to `error`.",
				Computed:            true,
			},
			"last_error": schema.StringAttribute{
				MarkdownDescription: "The message from the most recent failed delivery; empty when none has failed.",
				Computed:            true,
			},
			"last_error_at": schema.StringAttribute{
				MarkdownDescription: "When deliveries last failed; null when none has.",
				Computed:            true,
			},
			"resume_cursor": schema.StringAttribute{
				MarkdownDescription: "The occurred-at of the newest event delivered; null before the first delivery.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the stream was created.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "When the stream was last edited.",
				Computed:            true,
			},
		},
	}
}

func (r *logStreamResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *logStreamResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan logStreamModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	write, diags := expandStream(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Auth.IsNull() {
		write.Auth, diags = expandAuth(ctx, plan.Auth)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	stream, err := r.client.CreateStream(ctx, write)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create log stream", err.Error())
		return
	}

	// The server never returns auth: the state keeps what the plan held.
	state := plan
	resp.Diagnostics.Append(flattenStream(ctx, stream, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *logStreamResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state logStreamModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read log stream", fmt.Sprintf("State holds a non-integer id %q.", state.ID.ValueString()))
		return
	}
	stream, err := r.client.GetStream(ctx, id)
	if api.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read log stream", err.Error())
		return
	}

	// auth stays as the prior state holds it: the server never returns it.
	resp.Diagnostics.Append(flattenStream(ctx, stream, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *logStreamResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior logStreamModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id, err := strconv.ParseInt(prior.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update log stream", fmt.Sprintf("State holds a non-integer id %q.", prior.ID.ValueString()))
		return
	}

	write, diags := expandStream(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Omitting auth keeps the stored secret; `{}` clears it.
	switch {
	case plan.Auth.IsNull() && prior.Auth.IsNull():
	case plan.Auth.IsNull():
		write.Auth = &api.HTTPSAuth{}
	case !plan.Auth.Equal(prior.Auth):
		write.Auth, diags = expandAuth(ctx, plan.Auth)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	stream, err := r.client.UpdateStream(ctx, id, write)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update log stream", err.Error())
		return
	}

	state := plan
	resp.Diagnostics.Append(flattenStream(ctx, stream, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *logStreamResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state logStreamModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Unable to delete log stream", fmt.Sprintf("State holds a non-integer id %q.", state.ID.ValueString()))
		return
	}
	if err := r.client.DeleteStream(ctx, id); err != nil && !api.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete log stream", err.Error())
	}
}

// ImportState takes the stream's integer id.
func (r *logStreamResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected a log stream's integer id, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
