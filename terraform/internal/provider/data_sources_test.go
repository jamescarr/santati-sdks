package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// The fake serves organizations and event definitions two to a page, so
// getting all three of each proves the provider follows the cursor.
func TestDataSources(t *testing.T) {
	fake := newFakeControlPlane(t)
	fake.addTrail("billing", "us-east")
	fake.addTrail("audit", "eu-west")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(fake.URL()) + `
data "santati_trails" "all" {}
data "santati_organizations" "all" {}
data "santati_event_definitions" "all" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.santati_trails.all", "trails.#", "2"),
				resource.TestCheckResourceAttr("data.santati_trails.all", "trails.0.name", "billing"),
				resource.TestCheckResourceAttr("data.santati_trails.all", "trails.1.region", "eu-west"),
				resource.TestCheckResourceAttr("data.santati_trails.all", "trails.1.ingest_url", "https://ingest.example/webhooks/audit"),

				resource.TestCheckResourceAttr("data.santati_organizations.all", "organizations.#", "3"),
				resource.TestCheckResourceAttr("data.santati_organizations.all", "organizations.0.external_id", "org_acme"),
				resource.TestCheckResourceAttr("data.santati_organizations.all", "organizations.1.legal_hold", "true"),
				resource.TestCheckResourceAttr("data.santati_organizations.all", "organizations.1.retention_months", "24"),
				resource.TestCheckResourceAttr("data.santati_organizations.all", "organizations.2.id", "3"),
				resource.TestCheckResourceAttr("data.santati_organizations.all", "organizations.2.region", "eu-west"),

				resource.TestCheckResourceAttr("data.santati_event_definitions.all", "event_definitions.#", "3"),
				resource.TestCheckResourceAttr("data.santati_event_definitions.all", "event_definitions.0.action", "invoice.created"),
				resource.TestCheckResourceAttr("data.santati_event_definitions.all", "event_definitions.1.allowed_target_types.1", "customer"),
				resource.TestCheckResourceAttr("data.santati_event_definitions.all", "event_definitions.2.action", "user.login"),
				resource.TestCheckResourceAttr("data.santati_event_definitions.all", "event_definitions.2.is_active", "false"),
				resource.TestCheckResourceAttr("data.santati_event_definitions.all", "event_definitions.2.allowed_target_types.#", "0"),
			),
		}},
	})
}
