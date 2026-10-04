# santati-sdks

Santati SDKs for Python, TypeScript, Go, Rust, Elixir, Ruby and PHP. Each
package is an [openapi-generator](https://openapi-generator.tech) core
generated from the control plane's public spec, wrapped in a small
hand-written facade idiomatic to its language. The surface is **emitting
events** (one or a batch; with an outbox, `emit` is fire-and-forget) and
**fetching events** (list + iterate) with a team API key.

Read [`docs/sdk-surface.md`](docs/sdk-surface.md) for the behaviour every SDK
implements and [`conformance/README.md`](conformance/README.md) for the vectors
that keep the seven packages identical on the wire.

## Packages

| Directory | Registry | Install | Tag |
| --- | --- | --- | --- |
| [`python`](python) | PyPI `santati` | `pip install santati` | `python-vX.Y.Z` |
| [`typescript`](typescript) | npm `@santati/node` | `npm install @santati/node` | `typescript-vX.Y.Z` |
| [`go`](go) | Go module | `go get github.com/jamescarr/santati-sdks/go` | `go/vX.Y.Z` |
| [`rust`](rust) | crates.io `santati` | `cargo add santati` | `rust-vX.Y.Z` |
| [`elixir`](elixir) | Hex `santati` | `{:santati, "~> 0.1"}` | `elixir-vX.Y.Z` |
| [`ruby`](ruby) | RubyGems `santati` | `gem install santati` | `ruby-vX.Y.Z` |
| [`php`](php) | Packagist `santati/santati-php` | `composer require santati/santati-php` | `php-vX.Y.Z` |

Quickstart, Python:

```python
import santati

with santati.Santati("sat_sk_…", trail="billing") as client:
    result = client.events.emit("invoice.voided", organization_id="org_acme",
                                actor={"type": "user", "id": "usr_123"})
    print(result.event.id, result.duplicate)
    for event in client.events.iterate(trail="billing"):
        print(event.event)
```

Each package's README has the equivalent for its language.

## Development

```sh
mise install        # the pinned toolchain (PHP compiles from source; see below)
mise run deps       # fetch every package's dependencies
mise run check      # what CI runs: workflows, spec drift, every package, conformance
```

`mise run check` runs the same tasks as
[`ci.yml`](.github/workflows/ci.yml), one job per package. Useful individual
tasks: `mise run status` (versions, tags, published?), `mise run format`,
`mise run check:package <pkg>`, `mise run check:conformance <pkg>`.

PHP comes from mise's `vfox-php` plugin, which compiles from source (5–15
minutes) and needs Homebrew `autoconf bison re2c pkg-config libxml2 openssl@3
icu4c zlib libzip oniguruma freetype jpeg libpng webp gmp libsodium readline
bzip2 gd libiconv` on macOS.

## Spec and generated code

`spec/santati-v0.yaml` is vendored from the control plane's public spec
(`mise run spec:sync`), and `.mise/lib/generate.py` prunes it into the
generation profile `spec/santati-v0.sdk.yaml` before running
openapi-generator v7.25.0, one core per language. Generated trees are
committed and never hand-edited; `mise run check:drift` regenerates into a
temporary tree and diffs, so a stale tree fails CI.

## Releasing

See [`docs/releasing.md`](docs/releasing.md): the flow is
`mise run status` → `mise run release:prepare` → merge the PR →
`mise run release:tag` → `mise run release:watch` → `mise run release:verify`.

## License

Apache-2.0. Every package directory carries a copy.
