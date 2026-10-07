package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/jamescarr/terraform-provider-santati/internal/api"
)

var (
	_ datasource.DataSource              = (*organizationsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*organizationsDataSource)(nil)
)

// NewOrganizationsDataSource is the santati_organizations data source factory.
func NewOrganizationsDataSource() datasource.DataSource { return &organizationsDataSource{} }

type organizationsDataSource struct {
	client *api.Client
}

type organizationsDataSourceModel struct {
	Organizations []organizationDataModel `tfsdk:"organizations"`
}

type organizationDataModel struct {
	ID              int64  `tfsdk:"id"`
	ExternalID      string `tfsdk:"external_id"`
	Name            string `tfsdk:"name"`
	RetentionMonths int64  `tfsdk:"retention_months"`
	Region          string `tfsdk:"region"`
	LegalHold       bool   `tfsdk:"legal_hold"`
	CreatedAt       string `tfsdk:"created_at"`
}

func (d *organizationsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organizations"
}

func (d *organizationsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Every organization of the team. The control plane pages the list; the provider reads every page.",
		Attributes: map[string]schema.Attribute{
			"organizations": schema.ListNestedAttribute{
				MarkdownDescription: "The organizations, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.Int64Attribute{
							MarkdownDescription: "The organization's id, the integer an event envelope's `organization_id` refers to.",
							Computed:            true,
						},
						"external_id": schema.StringAttribute{
							MarkdownDescription: "The team's own identifier for the organization.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The organization's display name; empty when the team has not set one.",
							Computed:            true,
						},
						"retention_months": schema.Int64Attribute{
							MarkdownDescription: "How many months the organization's events are kept before purging.",
							Computed:            true,
						},
						"region": schema.StringAttribute{
							MarkdownDescription: "The region the organization's events are stored in; empty when unset.",
							Computed:            true,
						},
						"legal_hold": schema.BoolAttribute{
							MarkdownDescription: "Whether a legal hold suspends retention for the organization.",
							Computed:            true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "When the organization was first created for the team.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *organizationsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *organizationsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	organizations, err := d.client.ListOrganizations(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read organizations", err.Error())
		return
	}

	state := organizationsDataSourceModel{Organizations: make([]organizationDataModel, len(organizations))}
	for i, org := range organizations {
		state.Organizations[i] = organizationDataModel{
			ID:              org.ID,
			ExternalID:      org.ExternalID,
			Name:            org.Name,
			RetentionMonths: org.RetentionMonths,
			Region:          org.Region,
			LegalHold:       org.LegalHold,
			CreatedAt:       org.CreatedAt,
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
