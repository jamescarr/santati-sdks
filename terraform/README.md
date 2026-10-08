# terraform-provider-santati

The Terraform provider for the Santati control plane. It manages **trails**,
**log streams** and **event schemas** and reads **trails**,
**organizations** and **event definitions**.

```hcl
terraform {
  required_providers {
    santati = {
      source = "jamescarr/santati"
    }
  }
}

provider "santati" {
  # api_key defaults to $SANTATI_API_KEY, base_url to $SANTATI_BASE_URL and
  # then https://api.santati.io.
}

resource "santati_trail" "billing" {
  name = "billing"
}
```

| Kind | Name |
| --- | --- |
| resource | `santati_trail`, `santati_log_stream`, `santati_event_schema` |
| data source | `santati_trails`, `santati_organizations`, `santati_event_definitions` |

The full reference is in [`docs/`](docs/) and on the
[Terraform Registry](https://registry.terraform.io/providers/jamescarr/santati/latest/docs).

## Authentication

Use a team API key with the `manage` scope that is not restricted to specific
trails. Pass it as `api_key` or in `SANTATI_API_KEY`; the provider sends it as
`Authorization: Api-Key <key>`.

## Behaviour worth knowing

- A trail has no update call: every attribute change replaces it.
- A log stream's `auth.header_value` is write-only on the server. The provider
  stores it (sensitive) in state to detect changes you make in configuration,
  but cannot see a secret changed outside Terraform.
- `status` defaults to `active`. If delivery moved a stream to `error` or
  `invalid`, the next apply sets it back to `active`.
- `config.timeout_seconds` defaults to 15, the control plane's effective
  default.
- `santati_event_schema` holds the current JSON Schema of one action, which
  must already be in the event catalog; use one resource per action. Changing
  a published document publishes a new version, and the server deprecates the
  old one. `publish = false` keeps a draft that is edited in place. Published
  versions cannot be deleted, so destroying the resource leaves a published
  version enforced on the server and only forgets it. If someone else
  publishes a newer version, the next apply publishes your document again as
  another new version.
- The provider is not an event SDK: it implements none of
  `docs/sdk-surface.md`'s operations and is exempt from `conformance/`.

## Development

```sh
mise run check:package terraform   # format, vet, tests, docs freshness, GoReleaser build
mise run format terraform          # gofmt, terraform fmt, regenerate docs/
```

The tests run the real Terraform CLI against an in-process fake control plane,
so they need `terraform` on `PATH` (mise installs it) and no credentials.

To try a local build against a real team, build the provider and point a
`dev_overrides` block at it:

```sh
go build -o /tmp/tfp/terraform-provider-santati .
cat > /tmp/tfp/dev.tfrc <<'RC'
provider_installation {
  dev_overrides { "jamescarr/santati" = "/tmp/tfp" }
  direct {}
}
RC
TF_CLI_CONFIG_FILE=/tmp/tfp/dev.tfrc SANTATI_API_KEY=<key> terraform plan
```

## Releases

Tags are `terraform-vX.Y.Z`. The release workflow exports this directory to the
read-only mirror
[`jamescarr/terraform-provider-santati`](https://github.com/jamescarr/terraform-provider-santati)
and runs GoReleaser there; the Terraform Registry ingests the mirror's signed
release. See [`docs/releasing.md`](https://github.com/jamescarr/santati-sdks/blob/main/docs/releasing.md).
