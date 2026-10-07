package provider

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// streamSpec renders a santati_log_stream.test; the zero value is the
// smallest valid stream.
type streamSpec struct {
	name       string
	status     string // empty: omit, so the default applies
	headers    bool
	secret     string // empty: no auth block
	matchRules string // raw HCL object; empty: no block
}

func (s streamSpec) hcl(url string) string {
	name := s.name
	if name == "" {
		name = "siem"
	}
	var b strings.Builder
	b.WriteString(providerConfig(url))
	fmt.Fprintf(&b, "resource \"santati_log_stream\" \"test\" {\n  name  = %q\n  trail = \"billing\"\n", name)
	if s.status != "" {
		fmt.Fprintf(&b, "  status = %q\n", s.status)
	}
	b.WriteString("  config = {\n    url = \"https://siem.example/hook\"\n")
	if s.headers {
		b.WriteString("    headers = {\n      \"X-Team\" = \"audit\"\n    }\n")
	}
	b.WriteString("  }\n")
	if s.secret != "" {
		fmt.Fprintf(&b, "  auth = {\n    header_name  = \"X-Api-Key\"\n    header_value = %q\n  }\n", s.secret)
	}
	if s.matchRules != "" {
		fmt.Fprintf(&b, "  match_rules = %s\n", s.matchRules)
	}
	b.WriteString("}\n")
	return b.String()
}

const siemMatchRules = `{
    actions  = ["invoice.*"]
    metadata = [{ key = "region", op = "eq", value = "us-east" }]
  }`

// lastBody returns a check that hands the newest request's body to inspect.
func lastBody(fake *fakeControlPlane, method, prefix string, inspect func(body map[string]any) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		req, err := fake.last(method, prefix)
		if err != nil {
			return err
		}
		return inspect(req.Body)
	}
}

func wantKey(body map[string]any, key string, want any) error {
	if got, ok := body[key]; !ok || !reflect.DeepEqual(got, want) {
		return fmt.Errorf("body[%q] = %#v (present: %v), want %#v; body %v", key, got, ok, want, body)
	}
	return nil
}

func wantNoKey(body map[string]any, key string) error {
	if got, ok := body[key]; ok {
		return fmt.Errorf("body carries %q = %#v; body %v", key, got, body)
	}
	return nil
}

// TestLogStreamLifecycle walks one stream through every PATCH semantics that
// the control plane defines: config and match_rules are replaced wholesale
// whenever sent, auth only when it changed, and `{}` clears it.
func TestLogStreamLifecycle(t *testing.T) {
	fake := newFakeControlPlane(t)
	const path = "/api/v0/streams/"
	const stream = "santati_log_stream.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: streamSpec{headers: true, secret: "s3cret", matchRules: siemMatchRules}.hcl(fake.URL()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(stream, "id", "1"),
					resource.TestCheckResourceAttr(stream, "status", "active"),
					resource.TestCheckResourceAttr(stream, "destination_type", "https"),
					resource.TestCheckResourceAttr(stream, "config.content_type", "json"),
					resource.TestCheckResourceAttr(stream, "config.timeout_seconds", "15"),
					resource.TestCheckResourceAttr(stream, "config.headers.X-Team", "audit"),
					resource.TestCheckResourceAttr(stream, "auth.header_value", "s3cret"),
					resource.TestCheckResourceAttr(stream, "match_rules.actions.0", "invoice.*"),
					resource.TestCheckResourceAttr(stream, "match_rules.metadata.0.op", "eq"),
					resource.TestCheckNoResourceAttr(stream, "match_rules.actor_types"),
					lastBody(fake, http.MethodPost, path, func(body map[string]any) error {
						if err := wantKey(body, "auth", map[string]any{"header_name": "X-Api-Key", "header_value": "s3cret"}); err != nil {
							return err
						}
						config, _ := body["config"].(map[string]any)
						return wantKey(config, "timeout_seconds", float64(15))
					}),
				),
			},
			{
				// Only the status changed: the stored secret is left alone.
				Config: streamSpec{status: "inactive", headers: true, secret: "s3cret", matchRules: siemMatchRules}.hcl(fake.URL()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(stream, "status", "inactive"),
					lastBody(fake, http.MethodPatch, path+"1/", func(body map[string]any) error {
						if err := wantNoKey(body, "auth"); err != nil {
							return err
						}
						return wantKey(body, "status", "inactive")
					}),
				),
			},
			{
				// A new secret is sent, and only because it changed.
				Config: streamSpec{headers: true, secret: "rotated", matchRules: siemMatchRules}.hcl(fake.URL()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(stream, "auth.header_value", "rotated"),
					lastBody(fake, http.MethodPatch, path+"1/", func(body map[string]any) error {
						return wantKey(body, "auth", map[string]any{"header_name": "X-Api-Key", "header_value": "rotated"})
					}),
				),
			},
			{
				// Delivery moved the stream to error; the next apply sets it back.
				PreConfig: func() { fake.setStatus(1, "error") },
				Config:    streamSpec{headers: true, secret: "rotated", matchRules: siemMatchRules}.hcl(fake.URL()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(stream, "status", "active"),
					lastBody(fake, http.MethodPatch, path+"1/", func(body map[string]any) error {
						if err := wantNoKey(body, "auth"); err != nil {
							return err
						}
						return wantKey(body, "status", "active")
					}),
				),
			},
			{
				// Removing headers clears them: the config is replaced, not merged.
				Config: streamSpec{secret: "rotated", matchRules: siemMatchRules}.hcl(fake.URL()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(stream, "config.headers.%"),
					lastBody(fake, http.MethodPatch, path+"1/", func(body map[string]any) error {
						config, _ := body["config"].(map[string]any)
						return wantNoKey(config, "headers")
					}),
				),
			},
			{
				// Removing auth clears the stored secret; removing match_rules forwards everything.
				Config: streamSpec{}.hcl(fake.URL()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(stream, "auth.header_value"),
					resource.TestCheckNoResourceAttr(stream, "match_rules.actions.#"),
					lastBody(fake, http.MethodPatch, path+"1/", func(body map[string]any) error {
						if err := wantKey(body, "auth", map[string]any{}); err != nil {
							return err
						}
						return wantKey(body, "match_rules", map[string]any{})
					}),
				),
			},
			{
				ResourceName:            stream,
				ImportState:             true,
				ImportStateId:           "1",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"auth"},
			},
		},
	})
}

// A stream the dashboard created stores its config as strings; once imported,
// a configuration that spells out the same settings shows no difference.
func TestLogStreamImportOfADashboardStream(t *testing.T) {
	fake := newFakeControlPlane(t)
	id := fake.seedStream(map[string]any{
		"name":             "siem",
		"trail":            "billing",
		"destination_type": "https",
		"config": map[string]any{
			"url":             "https://siem.example/hook",
			"content_type":    "",
			"headers":         "X-Team: audit\nX-Env: prod",
			"timeout_seconds": "",
		},
	})
	config := providerConfig(fake.URL()) + `
resource "santati_log_stream" "test" {
  name  = "siem"
  trail = "billing"
  config = {
    url     = "https://siem.example/hook"
    headers = { "X-Team" = "audit", "X-Env" = "prod" }
  }
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       "santati_log_stream.test",
				ImportState:        true,
				ImportStateId:      fmt.Sprint(id),
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d resources, want 1", len(states))
					}
					attrs := states[0].Attributes
					for key, want := range map[string]string{
						"config.content_type":    "json",
						"config.timeout_seconds": "15",
						"config.headers.X-Team":  "audit",
						"config.headers.X-Env":   "prod",
						"status":                 "active",
					} {
						if attrs[key] != want {
							return fmt.Errorf("%s = %q, want %q", key, attrs[key], want)
						}
					}
					return nil
				},
			},
			{Config: config, PlanOnly: true},
		},
	})
}

func TestLogStreamDeletedOutOfBandIsRecreated(t *testing.T) {
	fake := newFakeControlPlane(t)
	config := streamSpec{}.hcl(fake.URL())

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, Check: resource.TestCheckResourceAttr("santati_log_stream.test", "id", "1")},
			{
				PreConfig: func() { fake.deleteStream(1) },
				Config:    config,
				Check:     resource.TestCheckResourceAttr("santati_log_stream.test", "id", "2"),
			},
		},
	})
}

func TestLogStreamDuplicateNameIsReported(t *testing.T) {
	fake := newFakeControlPlane(t)
	fake.seedStream(map[string]any{"name": "siem", "trail": "billing", "destination_type": "https"})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      streamSpec{}.hcl(fake.URL()),
			ExpectError: wrapped("name: A log stream with that name already exists"),
		}},
	})
}

func TestLogStreamEmptyMatchRulesAreRejectedAtPlan(t *testing.T) {
	fake := newFakeControlPlane(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      streamSpec{matchRules: "{}"}.hcl(fake.URL()),
			ExpectError: wrapped("At least one attribute out of"),
		}},
	})
	if n := len(fake.mutations()); n != 0 {
		t.Fatalf("%d requests reached the API before validation failed", n)
	}
}

func TestLogStreamImportRejectsANonIntegerID(t *testing.T) {
	fake := newFakeControlPlane(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:        streamSpec{}.hcl(fake.URL()),
			ResourceName:  "santati_log_stream.test",
			ImportState:   true,
			ImportStateId: "siem",
			ExpectError:   wrapped(`Expected a log stream's integer id, got "siem".`),
		}},
	})
}
