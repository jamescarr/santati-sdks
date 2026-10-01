# Releasing

Every SDK under this repo is versioned on its own with its own `CHANGELOG.md`.
The directory name is the git tag prefix — except Go, whose subdirectory
module is tagged `go/vX.Y.Z` — and the files in the directory decide where it
publishes:

| Package | Kind | Tag | Published by | Registry |
| --- | --- | --- | --- | --- |
| `python` | `pyproject.toml` | `python-vX.Y.Z` | [`release-python.yml`](https://github.com/jamescarr/santati-sdks/blob/main/.github/workflows/release-python.yml) | PyPI `santati` |
| `typescript` | `package.json` | `typescript-vX.Y.Z` | [`release-typescript.yml`](https://github.com/jamescarr/santati-sdks/blob/main/.github/workflows/release-typescript.yml) | npm `@santati/node` |
| `go` | `go.mod` | `go/vX.Y.Z` | [`release-go.yml`](https://github.com/jamescarr/santati-sdks/blob/main/.github/workflows/release-go.yml) | the module proxy (the tag is the release) |
| `rust` | `Cargo.toml` | `rust-vX.Y.Z` | [`release-rust.yml`](https://github.com/jamescarr/santati-sdks/blob/main/.github/workflows/release-rust.yml) | crates.io `santati` |
| `elixir` | `mix.exs` | `elixir-vX.Y.Z` | [`release-elixir.yml`](https://github.com/jamescarr/santati-sdks/blob/main/.github/workflows/release-elixir.yml) | Hex `santati` |
| `ruby` | `*.gemspec` | `ruby-vX.Y.Z` | [`release-ruby.yml`](https://github.com/jamescarr/santati-sdks/blob/main/.github/workflows/release-ruby.yml) | RubyGems `santati` |
| `php` | `composer.json` | `php-vX.Y.Z` | [`release-php.yml`](https://github.com/jamescarr/santati-sdks/blob/main/.github/workflows/release-php.yml) | Packagist `santati/santati-php` (mirror) |

Unlike a monorepo whose packages depend on each other, nothing here depends on
anything else, so there is no ordering constraint: any subset can be tagged in
any order.

## The flow

```sh
mise run status                                  # version, last tag, published?, commits since
mise run release:prepare minor python            # patch|minor|major|X.Y.Z, one or more packages
# review and merge the PR it opens, then:
git switch main && git pull
mise run release:tag python
mise run release:watch python
mise run release:verify python
```

1. **`release:prepare <bump> <pkgs>…`** refuses a dirty tree or a `HEAD` that
   isn't `origin/main`, and refuses a package whose `CHANGELOG.md` has nothing
   under `[Unreleased]`. For each package it sets the version file (`version`
   in `pyproject.toml` via `uv version`, `src/version.ts` + `package.json` via
   `npm version`, `Cargo.toml` + `Cargo.lock`, `version.go`, `@version` in
   `mix.exs`, `lib/santati/version.rb` + `Gemfile.lock`, or `src/Version.php`),
   opens a dated `## [X.Y.Z]` heading under `[Unreleased]`, and points the
   footer compare links at the new tag. It commits all of them on
   `release/<first tag>`, pushes, and opens a PR whose body is the release
   notes. `--no-pr` stops after the local commit.
2. **Merge the PR.** CI runs on it like any other.
3. **`release:tag <pkgs>…`** from the merged `main` runs `release:preflight`
   first: clean tree, `HEAD == origin/main`, a `## [X.Y.Z]` heading, the tag
   free locally and on `origin`, the version not yet on its registry (skipped
   for Go — the module proxy caches a 404), and the package's repo secrets
   present. Then it pushes one tag per package.
4. The tag triggers that package's workflow. It verifies the tag against the
   version and CHANGELOG rather than trusting it, runs the full test suite
   ([`ci.yml`](https://github.com/jamescarr/santati-sdks/blob/main/.github/workflows/ci.yml)
   via `workflow_call`), publishes, and cuts a GitHub release from the
   CHANGELOG section (`mise run release:notes <pkg> [version]` prints the same
   text).
5. **`release:watch <pkg>`** follows that run; **`release:verify <pkgs>…`**
   confirms the version is live on its registry.

## Before the first publish

**This repository must be public.** The Go module proxy, Packagist and npm
provenance cannot read a private repository, and Hex, crates.io, PyPI and
RubyGems all serve public metadata. Until then, `release:tag` works and the
workflows run, but Go/PHP/TypeScript publishes fail.

Nothing in this repo is published by the skeleton itself: the versions all
start at `0.0.0`, so the first `release:prepare minor` produces `0.1.0`.

## Secrets

Repository secrets, under Settings → Secrets and variables → Actions.
`release:preflight` checks the ones the package needs before any tag exists
(`pkg_secrets` in `.mise/lib/pkg.sh` is the source of truth).

| Secret | Used by | What it is |
| --- | --- | --- |
| `NPM_TOKEN` | typescript | npm **granular automation** token (bypasses 2FA-on-publish), scoped to `@santati/node` once it exists, unscoped for the first publish |
| `CARGO_REGISTRY_TOKEN` | rust | crates.io API token with `publish-new` (first release) and `publish-update` |
| `HEX_API_KEY` | elixir | Hex key scoped to `api:write` |
| `PHP_MIRROR_DEPLOY_KEY` | php | write deploy key of the `jamescarr/santati-php` mirror |
| `SPEC_SYNC_TOKEN` | spec-sync workflow | fine-grained PAT: contents read on `jamescarr/santati-control-plane`, contents + pull requests write on this repo |

PyPI, RubyGems and Go need no repository secret: PyPI and RubyGems publish via
Trusted Publishing (OIDC).

## One-time setup

### PyPI (python)

The first publish creates the `santati` project from a **pending** trusted
publisher, because there is no project settings page until then. From the
account that will own the package:

1. <https://pypi.org/manage/account/publishing/> → **Add a new pending publisher**.

   | Field | Value |
   | --- | --- |
   | PyPI Project Name | `santati` |
   | Owner | `jamescarr` |
   | Repository name | `santati-sdks` |
   | Workflow name | `release-python.yml` |
   | Environment name | `pypi` |

2. Check the name is still free before the first tag:
   `curl -s -o /dev/null -w '%{http_code}\n' https://pypi.org/pypi/santati/json` → `404`.

### RubyGems (ruby)

RubyGems Trusted Publishing needs the gem to exist first, so either publish
`0.1.0` once with a manual `gem push`, or pre-create the gem name, then on
<https://rubygems.org> → the gem → **Trusted publishers** → GitHub:

| Field | Value |
| --- | --- |
| Repository | `jamescarr/santati-sdks` |
| Workflow | `release-ruby.yml` |

`release-ruby.yml` then authenticates with OIDC alone
(`rubygems/configure-rubygems-credentials@v2`, whose default mode is trusted
publishing).

### npm (typescript)

The npm organization `santati` does not exist yet. Create it, create the
`NPM_TOKEN` granular automation token, and note that `npm publish
--provenance` requires a public repository.

### crates.io (rust)

Create `CARGO_REGISTRY_TOKEN` at <https://crates.io/settings/tokens> with
`publish-new` and `publish-update`. `publish-new` is needed for the first
release only. Check the name: `curl -s -A 'santati-sdks-release (https://github.com/jamescarr/santati-sdks)' -o /dev/null -w '%{http_code}\n' https://crates.io/api/v1/crates/santati/0.1.0` → `404`. The `-A` is not optional: crates.io answers 403 to curl's own user agent.

### Hex (elixir)

Create `HEX_API_KEY` from the Hex.pm dashboard (Keys), scoped to `api:write`.
Check the name: `curl -s -o /dev/null -w '%{http_code}\n' https://hex.pm/api/packages/santati` → `404`.

### Packagist (php)

Packagist only reads the root of a repository, so `php/` is published through
the read-only mirror `jamescarr/santati-php`:

1. Create the empty public repo `jamescarr/santati-php`.
2. Add a **write** deploy key to it and store the private half as
   `PHP_MIRROR_DEPLOY_KEY`.
3. Submit `https://github.com/jamescarr/santati-php` to Packagist and enable
   its GitHub webhook, so the mirror's tag push triggers an update.
4. `release:preflight php` fails with "mirror jamescarr/santati-php does not
   exist; see docs/releasing.md" until step 1 is done.

### Go

No setup and no secret: the tag `go/vX.Y.Z` in this public repository is the
release; `release-go.yml` only warms the module proxy.

## Spec syncs

`docs/openapi/v0.yaml` in the control plane is vendored here as
`spec/santati-v0.yaml`, and `.mise/lib/generate.py` prunes it into the
generation profile. `spec-sync.yml` runs the flow weekly (and on demand) and
opens a PR when the spec or any generated core changed; it needs
`SPEC_SYNC_TOKEN` because `github.token` cannot trigger CI on the PR it
creates. The manual equivalent is:

```sh
mise run spec:sync [ref]   # default: main of jamescarr/santati-control-plane
mise run generate
mise run check
```
