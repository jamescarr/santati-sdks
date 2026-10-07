// Package provider is the Santati Terraform provider, built on
// terraform-plugin-framework (protocol 6).
package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jamescarr/terraform-provider-santati/internal/api"
)

const defaultBaseURL = "https://api.santati.io"

var _ provider.Provider = (*santatiProvider)(nil)

type santatiProvider struct {
	version string
}

type providerModel struct {
	APIKey  types.String `tfsdk:"api_key"`
	BaseURL types.String `tfsdk:"base_url"`
}

// New returns the provider factory for the given release version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &santatiProvider{version: version}
	}
}

func (p *santatiProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "santati"
	resp.Version = p.version
}

func (p *santatiProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Santati provider manages the audit-log control plane: trails that receive events and log streams that forward them to a destination.",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				MarkdownDescription: "A team API key with the `manage` scope that is not restricted to specific trails. " +
					"May also be set with the `SANTATI_API_KEY` environment variable.",
				Optional:  true,
				Sensitive: true,
			},
			"base_url": schema.StringAttribute{
				MarkdownDescription: "The control plane's origin. May also be set with the `SANTATI_BASE_URL` " +
					"environment variable. Defaults to `" + defaultBaseURL + "`. A trailing `/` is ignored.",
				Optional: true,
			},
		},
	}
}

func (p *santatiProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.APIKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Unknown Santati API key",
			"The provider cannot be configured while api_key is unknown. Set it to a static value, or use the SANTATI_API_KEY environment variable.")
	}
	if config.BaseURL.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("base_url"), "Unknown Santati base URL",
			"The provider cannot be configured while base_url is unknown. Set it to a static value, or use the SANTATI_BASE_URL environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	apiKey := os.Getenv("SANTATI_API_KEY")
	if !config.APIKey.IsNull() {
		apiKey = config.APIKey.ValueString()
	}
	baseURL := os.Getenv("SANTATI_BASE_URL")
	if !config.BaseURL.IsNull() {
		baseURL = config.BaseURL.ValueString()
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	if apiKey == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing Santati API key",
			"Set api_key in the provider block or the SANTATI_API_KEY environment variable.")
		return
	}

	client := api.New(api.Options{
		BaseURL:   baseURL,
		APIKey:    apiKey,
		UserAgent: "santati-terraform/" + p.version,
	})
	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *santatiProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewTrailResource,
		NewLogStreamResource,
	}
}

func (p *santatiProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewTrailsDataSource,
		NewOrganizationsDataSource,
		NewEventDefinitionsDataSource,
	}
}

// clientFrom is the shared body of every resource's and data source's
// Configure: nil provider data (the validation phase) yields no client and no
// error.
func clientFrom(providerData any, diags *diag.Diagnostics) *api.Client {
	if providerData == nil {
		return nil
	}
	client, ok := providerData.(*api.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("Expected *api.Client, got %T. Please report this issue to the provider developers.", providerData))
		return nil
	}
	return client
}
