package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/jamescarr/terraform-provider-santati/internal/api"
)

var (
	_ datasource.DataSource              = (*trailsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*trailsDataSource)(nil)
)

// NewTrailsDataSource is the santati_trails data source factory.
func NewTrailsDataSource() datasource.DataSource { return &trailsDataSource{} }

type trailsDataSource struct {
	client *api.Client
}

type trailsDataSourceModel struct {
	Trails []trailDataModel `tfsdk:"trails"`
}

type trailDataModel struct {
	Name      string `tfsdk:"name"`
	Region    string `tfsdk:"region"`
	IngestURL string `tfsdk:"ingest_url"`
}

func (d *trailsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_trails"
}

func (d *trailsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Every trail the API key may write to, across the configured regions.",
		Attributes: map[string]schema.Attribute{
			"trails": schema.ListNestedAttribute{
				MarkdownDescription: "The trails, in the order the API returns them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							MarkdownDescription: "The trail's name.",
							Computed:            true,
						},
						"region": schema.StringAttribute{
							MarkdownDescription: "The region the trail lives in.",
							Computed:            true,
						},
						"ingest_url": schema.StringAttribute{
							MarkdownDescription: "The URL producers POST events to.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *trailsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *trailsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	trails, err := d.client.ListTrails(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read trails", err.Error())
		return
	}

	state := trailsDataSourceModel{Trails: make([]trailDataModel, len(trails))}
	for i, trail := range trails {
		state.Trails[i] = trailDataModel{Name: trail.Name, Region: trail.Region, IngestURL: trail.IngestURL}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
