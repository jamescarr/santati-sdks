package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jamescarr/terraform-provider-santati/internal/api"
)

var (
	_ resource.Resource                = (*trailResource)(nil)
	_ resource.ResourceWithConfigure   = (*trailResource)(nil)
	_ resource.ResourceWithImportState = (*trailResource)(nil)
)

// NewTrailResource is the santati_trail resource factory.
func NewTrailResource() resource.Resource { return &trailResource{} }

type trailResource struct {
	client *api.Client
}

type trailModel struct {
	Name      types.String `tfsdk:"name"`
	Region    types.String `tfsdk:"region"`
	IngestURL types.String `tfsdk:"ingest_url"`
}

func (r *trailResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_trail"
}

func (r *trailResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An audit trail: the stream that producers send events to. " +
			"The control plane has no update call for trails, so changing any argument replaces the trail. " +
			"Events already indexed on a deleted trail are not removed.\n\n" +
			"Creating a trail needs an API key with the `manage` scope that is not restricted to specific trails.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "The trail's name: 1-64 letters, digits, `-` or `_`. Changing it replaces the trail.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "The region the trail lives in. Omit it for the deployment's default region; " +
					"the resolved region is then stored here. Changing a configured region replaces the trail.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"ingest_url": schema.StringAttribute{
				MarkdownDescription: "The URL producers POST events to.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *trailResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *trailResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan trailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	trail, err := r.client.CreateTrail(ctx, api.TrailCreate{
		Name:   plan.Name.ValueString(),
		Region: plan.Region.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create trail", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, trailToModel(*trail))...)
}

func (r *trailResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state trailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	trails, err := r.client.ListTrails(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read trail", err.Error())
		return
	}

	name, region := state.Name.ValueString(), state.Region.ValueString()
	var matches []api.Trail
	for _, trail := range trails {
		if trail.Name == name && (region == "" || trail.Region == region) {
			matches = append(matches, trail)
		}
	}

	switch {
	case len(matches) == 0:
		resp.State.RemoveResource(ctx)
	case len(matches) > 1:
		// Only an import by bare name leaves the region empty.
		regions := make([]string, len(matches))
		for i, trail := range matches {
			regions[i] = trail.Region
		}
		sort.Strings(regions)
		resp.Diagnostics.AddError("Ambiguous trail import",
			fmt.Sprintf("trail %q exists in regions %s; import it as <region>/%s", name, strings.Join(regions, ", "), name))
	default:
		resp.Diagnostics.Append(resp.State.Set(ctx, trailToModel(matches[0]))...)
	}
}

// Update is never called with a change: every configurable argument requires
// replacement. It only keeps the framework's contract.
func (r *trailResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan trailModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *trailResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state trailModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteTrail(ctx, state.Name.ValueString(), state.Region.ValueString())
	if err != nil && !api.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete trail", err.Error())
	}
}

// ImportState accepts `<name>` or `<region>/<name>`.
func (r *trailResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	region, name := "", req.ID
	if before, after, found := strings.Cut(req.ID, "/"); found {
		region, name = before, after
	}
	if name == "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf(`Expected "<name>" or "<region>/<name>", got %q.`, req.ID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
	if region != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), region)...)
	}
}

func trailToModel(trail api.Trail) *trailModel {
	return &trailModel{
		Name:      types.StringValue(trail.Name),
		Region:    types.StringValue(trail.Region),
		IngestURL: types.StringValue(trail.IngestURL),
	}
}
