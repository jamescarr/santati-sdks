package provider

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const (
	invoiceVoided = "invoice.voided"
	schemasPath   = "/api/v0/event-definitions/invoice.voided/schema-versions/"
	schemaRef     = "santati_event_schema.test"
)

// The documents of the lifecycle test, as HCL and as what the server should
// hold once decoded.
const (
	docA = `jsonencode({ type = "object", properties = { metadata = { type = "object", required = ["invoice_number"] } } })`
	docB = `jsonencode({
  type = "object"
  properties = {
    metadata = {
      type       = "object"
      required   = ["invoice_number"]
      properties = { invoice_number = { type = "string" } }
    }
  }
})`
	docC = `jsonencode({
  type = "object"
  properties = {
    metadata = {
      type       = "object"
      required   = ["invoice_number", "void_reason"]
      properties = { invoice_number = { type = "string" } }
    }
  }
})`
)

var docAValue = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"metadata": map[string]any{"type": "object", "required": []any{"invoice_number"}},
	},
}

func eventSchemaConfig(url, action, schemaHCL string, publish *bool) string {
	publishLine := ""
	if publish != nil {
		publishLine = fmt.Sprintf("publish = %t", *publish)
	}
	return providerConfig(url) + fmt.Sprintf(`
resource "santati_event_schema" "test" {
  action = %q
  schema = %s
  %s
}
`, action, schemaHCL, publishLine)
}

func ptr[T any](v T) *T { return &v }

// requestBody returns the body of the newest request with the method and path.
func requestBody(fake *fakeControlPlane, method, path string) (map[string]any, error) {
	for _, r := range reversed(fake.all()) {
		if r.Method == method && r.Path == path {
			return r.Body, nil
		}
	}
	return nil, fmt.Errorf("no %s %s request was recorded", method, path)
}

func reversed(requests []recordedRequest) []recordedRequest {
	out := make([]recordedRequest, len(requests))
	for i, r := range requests {
		out[len(requests)-1-i] = r
	}
	return out
}

// wantCount checks the number of requests with exactly this method and path.
func wantCount(fake *fakeControlPlane, method, path string, want int) error {
	if got := fake.countPath(method, path); got != want {
		return fmt.Errorf("%d %s %s requests, want %d", got, method, path, want)
	}
	return nil
}

func TestEventSchemaLifecycle(t *testing.T) {
	fake := newFakeControlPlane(t)
	fake.addDefinition(invoiceVoided)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			// The published version is left in place, not deleted.
			if n := fake.count(http.MethodDelete, "/api/v0/event-definitions/"); n != 0 {
				return fmt.Errorf("%d DELETE requests, want 0", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: eventSchemaConfig(fake.URL(), invoiceVoided, docA, nil),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schemaRef, "version", "1"),
					resource.TestCheckResourceAttr(schemaRef, "status", "published"),
					resource.TestCheckResourceAttr(schemaRef, "publish", "true"),
					resource.TestCheckResourceAttrSet(schemaRef, "published_at"),
					resource.TestCheckNoResourceAttr(schemaRef, "deprecated_at"),
					func(*terraform.State) error {
						var got []string
						for _, r := range fake.mutations() {
							got = append(got, r.Method+" "+r.Path)
						}
						want := []string{"POST " + schemasPath, "POST " + schemasPath + "1/publish/"}
						if !reflect.DeepEqual(got, want) {
							return fmt.Errorf("mutations = %q, want %q", got, want)
						}
						return nil
					},
				),
			},
			{
				// A published version is immutable: the change is a new version,
				// and the server deprecates the old one.
				Config: eventSchemaConfig(fake.URL(), invoiceVoided, docB, nil),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schemaRef, "version", "2"),
					resource.TestCheckResourceAttr(schemaRef, "status", "published"),
					func(*terraform.State) error {
						if got := fake.versionStatus(invoiceVoided, 1); got != "deprecated" {
							return fmt.Errorf("version 1 is %q, want deprecated", got)
						}
						return nil
					},
				),
			},
			{
				Config: eventSchemaConfig(fake.URL(), invoiceVoided, docC, ptr(false)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schemaRef, "version", "3"),
					resource.TestCheckResourceAttr(schemaRef, "status", "draft"),
					resource.TestCheckResourceAttr(schemaRef, "publish", "false"),
					resource.TestCheckNoResourceAttr(schemaRef, "published_at"),
					func(*terraform.State) error {
						return wantCount(fake, http.MethodPost, schemasPath+"3/publish/", 0)
					},
				),
			},
			{
				// A draft is edited in place.
				Config: eventSchemaConfig(fake.URL(), invoiceVoided, docA, ptr(false)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schemaRef, "version", "3"),
					resource.TestCheckResourceAttr(schemaRef, "status", "draft"),
					func(*terraform.State) error {
						body, err := requestBody(fake, http.MethodPut, schemasPath+"3/")
						if err != nil {
							return err
						}
						if !reflect.DeepEqual(body["schema"], any(docAValue)) {
							return fmt.Errorf("PUT schema = %v, want %v", body["schema"], docAValue)
						}
						return nil
					},
				),
			},
			{
				// Publishing an existing draft writes no new version.
				Config: eventSchemaConfig(fake.URL(), invoiceVoided, docA, ptr(true)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schemaRef, "version", "3"),
					resource.TestCheckResourceAttr(schemaRef, "status", "published"),
					func(*terraform.State) error {
						if err := wantCount(fake, http.MethodPost, schemasPath+"3/publish/", 1); err != nil {
							return err
						}
						return wantCount(fake, http.MethodPost, schemasPath, 3)
					},
				),
			},
			{
				ResourceName: schemaRef, ImportState: true, ImportStateId: invoiceVoided + "/3",
				ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "action",
			},
			{
				// A bare action imports the published version.
				ResourceName: schemaRef, ImportState: true, ImportStateId: invoiceVoided,
				ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "action",
			},
		},
	})
}

func TestEventSchemaSupersededOutOfBandIsRepublished(t *testing.T) {
	fake := newFakeControlPlane(t)
	fake.addDefinition(invoiceVoided)
	config := eventSchemaConfig(fake.URL(), invoiceVoided, docA, nil)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, Check: resource.TestCheckResourceAttr(schemaRef, "version", "1")},
			{
				PreConfig: func() { fake.publishOutOfBand(invoiceVoided, map[string]any{"type": "object"}) },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schemaRef, "version", "3"),
					resource.TestCheckResourceAttr(schemaRef, "status", "published"),
					func(*terraform.State) error {
						if err := wantCount(fake, http.MethodPost, schemasPath+"3/publish/", 1); err != nil {
							return err
						}
						if got := fake.versionStatus(invoiceVoided, 2); got != "deprecated" {
							return fmt.Errorf("version 2 is %q, want deprecated", got)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestEventSchemaDraftEditedOutOfBandIsRestored(t *testing.T) {
	fake := newFakeControlPlane(t)
	fake.addDefinition(invoiceVoided)
	config := eventSchemaConfig(fake.URL(), invoiceVoided, docA, ptr(false))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if err := wantCount(fake, http.MethodDelete, schemasPath+"1/", 1); err != nil {
				return err
			}
			if n := fake.count(http.MethodDelete, "/api/v0/event-definitions/"); n != 1 {
				return fmt.Errorf("%d DELETE requests, want 1", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config, Check: resource.TestCheckResourceAttr(schemaRef, "status", "draft")},
			{
				PreConfig: func() { fake.editDraft(invoiceVoided, 1, map[string]any{"type": "object"}) },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schemaRef, "version", "1"),
					func(*terraform.State) error {
						body, err := requestBody(fake, http.MethodPut, schemasPath+"1/")
						if err != nil {
							return err
						}
						if !reflect.DeepEqual(body["schema"], any(docAValue)) {
							return fmt.Errorf("PUT schema = %v, want %v", body["schema"], docAValue)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestEventSchemaReformattedDocumentHasNoDiff(t *testing.T) {
	fake := newFakeControlPlane(t)
	fake.addDefinition(invoiceVoided)

	// Keys out of alphabetical order and extra spaces: the server hands back
	// its own spelling of the document.
	const first = `{ "type" : "object",  "properties": {} }` + "\n"
	const second = `{"properties":{},   "type":"object"}` + "\n"
	heredoc := func(doc string) string { return "<<EOT\n" + doc + "EOT" }

	onlyTheFirstWrites := func(*terraform.State) error {
		if n := len(fake.mutations()); n != 2 {
			return fmt.Errorf("%d mutations, want the create and the publish only", n)
		}
		return nil
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The framework's empty-plan check after apply fails if the
				// stored document were not recognised as the configured one.
				Config: eventSchemaConfig(fake.URL(), invoiceVoided, heredoc(first), nil),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schemaRef, "schema", first),
					onlyTheFirstWrites,
				),
			},
			{
				// Respelling the document is a plan-time diff but writes nothing.
				Config: eventSchemaConfig(fake.URL(), invoiceVoided, heredoc(second), nil),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schemaRef, "schema", second),
					resource.TestCheckResourceAttr(schemaRef, "version", "1"),
					onlyTheFirstWrites,
				),
			},
		},
	})
}

func TestEventSchemaUndefinedAction(t *testing.T) {
	fake := newFakeControlPlane(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      eventSchemaConfig(fake.URL(), invoiceVoided, docA, nil),
			ExpectError: wrapped(`Define the action "invoice.voided" in the event catalog first.`),
		}},
	})
}

func TestEventSchemaImportErrors(t *testing.T) {
	fake := newFakeControlPlane(t)
	fake.addDefinition(invoiceVoided)
	config := eventSchemaConfig(fake.URL(), invoiceVoided, docA, nil)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config, ResourceName: schemaRef, ImportState: true, ImportStateId: invoiceVoided + "/abc",
				ExpectError: wrapped(`Expected "<action>" or "<action>/<version>", got "invoice.voided/abc".`),
			},
			{
				Config: config, ResourceName: schemaRef, ImportState: true, ImportStateId: invoiceVoided + "/0",
				ExpectError: wrapped(`Expected "<action>" or "<action>/<version>", got "invoice.voided/0".`),
			},
			{
				Config: config, ResourceName: schemaRef, ImportState: true, ImportStateId: invoiceVoided,
				ExpectError: wrapped(`Action "invoice.voided" has no schema versions.`),
			},
		},
	})
}
