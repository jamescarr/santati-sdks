package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jamescarr/terraform-provider-santati/internal/api"
)

var (
	_ datasource.DataSource              = (*eventDefinitionsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*eventDefinitionsDataSource)(nil)
)

// NewEventDefinitionsDataSource is the santati_event_definitions data source factory.
func NewEventDefinitionsDataSource() datasource.DataSource { return &eventDefinitionsDataSource{} }

type eventDefinitionsDataSource struct {
	client *api.Client
}

type eventDefinitionsDataSourceModel struct {
	EventDefinitions []eventDefinitionDataModel `tfsdk:"event_definitions"`
}

type eventDefinitionDataModel struct {
	Action             string   `tfsdk:"action"`
	Description        string   `tfsdk:"description"`
	AllowedTargetTypes []string `tfsdk:"allowed_target_types"`
	IsActive           bool     `tfsdk:"is_active"`
	CreatedAt          string   `tfsdk:"created_at"`
	UpdatedAt          string   `tfsdk:"updated_at"`
}

func (d *eventDefinitionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_event_definitions"
}

func (d *eventDefinitionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The team's event catalog: every action an event may name. The control plane pages the list; the provider reads every page.",
		Attributes: map[string]schema.Attribute{
			"event_definitions": schema.ListNestedAttribute{
				MarkdownDescription: "The event definitions, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"action": schema.StringAttribute{
							MarkdownDescription: "The action name, for example `invoice.voided`.",
							Computed:            true,
						},
						"description": schema.StringAttribute{
							MarkdownDescription: "What the action means, for the people reading the catalog.",
							Computed:            true,
						},
						"allowed_target_types": schema.ListAttribute{
							MarkdownDescription: "The target types an event with this action may name; empty means any target type.",
							ElementType:         types.StringType,
							Computed:            true,
						},
						"is_active": schema.BoolAttribute{
							MarkdownDescription: "Whether the action is in force. When a team has any definitions, an action whose definition is not active is treated as undefined.",
							Computed:            true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "When the action was first defined.",
							Computed:            true,
						},
						"updated_at": schema.StringAttribute{
							MarkdownDescription: "When the definition was last changed.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *eventDefinitionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *eventDefinitionsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	definitions, err := d.client.ListEventDefinitions(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read event definitions", err.Error())
		return
	}

	state := eventDefinitionsDataSourceModel{EventDefinitions: make([]eventDefinitionDataModel, len(definitions))}
	for i, definition := range definitions {
		targets := definition.AllowedTargetTypes
		if targets == nil {
			targets = []string{}
		}
		state.EventDefinitions[i] = eventDefinitionDataModel{
			Action:             definition.Action,
			Description:        definition.Description,
			AllowedTargetTypes: targets,
			IsActive:           definition.IsActive,
			CreatedAt:          definition.CreatedAt,
			UpdatedAt:          definition.UpdatedAt,
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
