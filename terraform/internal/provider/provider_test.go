package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// The tests run the real Terraform CLI against an in-process fake control
// plane (fake_test.go): resource.UnitTest needs neither TF_ACC nor credentials.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"santati": providerserver.NewProtocol6WithError(New("test")()),
}

func providerConfig(url string) string {
	return fmt.Sprintf(`
provider "santati" {
  api_key  = %q
  base_url = %q
}
`, fakeAPIKey, url)
}

// wrapped matches text that Terraform may have re-wrapped across lines.
func wrapped(text string) *regexp.Regexp {
	words := strings.Fields(text)
	for i, word := range words {
		words[i] = regexp.QuoteMeta(word)
	}
	return regexp.MustCompile(strings.Join(words, `\s+`))
}

func TestProviderRequiresAPIKey(t *testing.T) {
	t.Setenv("SANTATI_API_KEY", "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "santati" {}
data "santati_trails" "all" {}
`,
			ExpectError: wrapped("Missing Santati API key"),
		}},
	})
}

func TestProviderReadsAPIKeyAndBaseURLFromTheEnvironment(t *testing.T) {
	fake := newFakeControlPlane(t)
	fake.addTrail("billing", "us-east")
	t.Setenv("SANTATI_API_KEY", fakeAPIKey)
	t.Setenv("SANTATI_BASE_URL", fake.URL()+"/")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "santati" {}
data "santati_trails" "all" {}
`,
			Check: resource.TestCheckResourceAttr("data.santati_trails.all", "trails.#", "1"),
		}},
	})
}

func TestProviderSurfacesAuthFailures(t *testing.T) {
	fake := newFakeControlPlane(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "santati" {
  api_key  = "wrong"
  base_url = %q
}
data "santati_trails" "all" {}
`, fake.URL()),
			ExpectError: wrapped("HTTP 403: You do not have permission to perform this action."),
		}},
	})
}
