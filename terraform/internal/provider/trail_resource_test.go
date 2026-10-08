package provider

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func trailConfig(url, name, region string) string {
	regionLine := ""
	if region != "" {
		regionLine = fmt.Sprintf("region = %q", region)
	}
	return providerConfig(url) + fmt.Sprintf(`
resource "santati_trail" "test" {
  name = %q
  %s
}
`, name, regionLine)
}

func TestTrailCreateAndImport(t *testing.T) {
	fake := newFakeControlPlane(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: trailConfig(fake.URL(), "billing", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("santati_trail.test", "name", "billing"),
					resource.TestCheckResourceAttr("santati_trail.test", "region", "us-east"),
					resource.TestCheckResourceAttr("santati_trail.test", "ingest_url", "https://ingest.example/webhooks/billing"),
					func(*terraform.State) error {
						req, err := fake.last(http.MethodPost, "/api/v0/trails/")
						if err != nil {
							return err
						}
						if _, sent := req.Body["region"]; sent {
							return fmt.Errorf("an unset region was sent: %v", req.Body)
						}
						return nil
					},
				),
			},
			{
				ResourceName: "santati_trail.test", ImportState: true, ImportStateId: "billing",
				ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "name",
			},
			{
				ResourceName: "santati_trail.test", ImportState: true, ImportStateId: "us-east/billing",
				ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "name",
			},
		},
	})
}

func TestTrailImportOfAnAmbiguousNameFails(t *testing.T) {
	fake := newFakeControlPlane(t)
	fake.addTrail("billing", "us-east")
	fake.addTrail("billing", "eu-west")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:        trailConfig(fake.URL(), "billing", ""),
			ResourceName:  "santati_trail.test",
			ImportState:   true,
			ImportStateId: "billing",
			ExpectError:   wrapped(`trail "billing" exists in regions eu-west, us-east; import it as <region>/billing`),
		}},
	})
}

func TestTrailRenameReplaces(t *testing.T) {
	fake := newFakeControlPlane(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: trailConfig(fake.URL(), "billing", "")},
			{
				Config: trailConfig(fake.URL(), "payments", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("santati_trail.test", "name", "payments"),
					func(*terraform.State) error {
						mutations := fake.mutations()
						if len(mutations) != 3 {
							return fmt.Errorf("want create, delete, create; got %v", mutations)
						}
						del, create := mutations[1], mutations[2]
						if del.Method != http.MethodDelete || del.Path != "/api/v0/trails/billing/?region=us-east" {
							return fmt.Errorf("second mutation = %s %s, want the old trail's DELETE", del.Method, del.Path)
						}
						if create.Method != http.MethodPost || create.Body["name"] != "payments" {
							return fmt.Errorf("third mutation = %s %v, want the new trail's POST", create.Method, create.Body)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestTrailRegionIsReplacedOnlyWhenConfigured(t *testing.T) {
	fake := newFakeControlPlane(t)
	var mutationsAfterMove int

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: trailConfig(fake.URL(), "billing", "")},
			{
				Config: trailConfig(fake.URL(), "billing", "eu-west"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("santati_trail.test", "region", "eu-west"),
					func(*terraform.State) error {
						req, err := fake.last(http.MethodPost, "/api/v0/trails/")
						if err != nil {
							return err
						}
						if req.Body["region"] != "eu-west" {
							return fmt.Errorf("POST body = %v, want region eu-west", req.Body)
						}
						mutationsAfterMove = len(fake.mutations())
						return nil
					},
				),
			},
			{
				// Dropping the argument keeps the resolved region: no diff, no calls.
				Config: trailConfig(fake.URL(), "billing", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("santati_trail.test", "region", "eu-west"),
					func(*terraform.State) error {
						if n := len(fake.mutations()); n != mutationsAfterMove {
							return fmt.Errorf("%d mutations after removing region, want %d", n, mutationsAfterMove)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestTrailDeletedOutOfBandIsRecreated(t *testing.T) {
	fake := newFakeControlPlane(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: trailConfig(fake.URL(), "billing", "")},
			{
				PreConfig: func() { fake.deleteTrail("billing") },
				Config:    trailConfig(fake.URL(), "billing", ""),
				Check: func(*terraform.State) error {
					if n := fake.count(http.MethodPost, "/api/v0/trails/"); n != 2 {
						return fmt.Errorf("%d trail creations, want 2", n)
					}
					return nil
				},
			},
		},
	})
}

func TestTrailCreateOfAnExistingNameFails(t *testing.T) {
	fake := newFakeControlPlane(t)
	fake.addTrail("billing", "us-east")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      trailConfig(fake.URL(), "billing", ""),
			ExpectError: wrapped("HTTP 409"),
		}},
	})
}
