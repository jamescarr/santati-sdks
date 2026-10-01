# Package helpers shared by .mise/tasks/* and the GitHub workflows.
#
# Sourced only; sets no shell options. Every releasable artifact is a
# top-level directory of $ROOT; its name is the git tag prefix
# (`<name>-vX.Y.Z`, or `go/vX.Y.Z` for the Go module) and its kind comes from
# the files in it: pyproject.toml -> python, package.json -> npm, go.mod ->
# go, Cargo.toml -> cargo, mix.exs -> hex, *.gemspec -> gem,
# composer.json -> composer.
#
# Written for bash 3.2 (macOS /bin/bash): no mapfile, no associative arrays.

ROOT="${MISE_PROJECT_ROOT:-$(git rev-parse --show-toplevel)}"
GITHUB_REPO=jamescarr/santati-sdks
SPEC_REPO=jamescarr/santati-control-plane
PHP_MIRROR=jamescarr/santati-php

# stderr in both cases: a failure inside `x=$(pkg_...)` must still be seen, and
# the Actions runner picks `::error::` up from stderr as well as stdout.
fail() {
  if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
    printf '::error::%s\n' "$*" >&2
  else
    printf '\nFAIL: %s\n' "$*" >&2
  fi
  exit 1
}

# The kind of a package directory, or nothing when it holds no manifest.
_pkg_kind_of() {
  local dir="$ROOT/$1"
  if [ -f "$dir/pyproject.toml" ]; then
    echo python
  elif [ -f "$dir/package.json" ]; then
    echo npm
  elif [ -f "$dir/go.mod" ]; then
    echo go
  elif [ -f "$dir/Cargo.toml" ]; then
    echo cargo
  elif [ -f "$dir/mix.exs" ]; then
    echo hex
  elif [ -n "$(find "$dir" -maxdepth 1 -name '*.gemspec' -print -quit 2>/dev/null)" ]; then
    echo gem
  elif [ -f "$dir/composer.json" ]; then
    echo composer
  fi
}

# Every top-level directory that holds a package, in byte order, whatever its
# kind.
_pkg_all() {
  local d name
  for d in "$ROOT"/*/; do
    [ -d "$d" ] || continue
    d="${d%/}"
    name="${d##*/}"
    case "$name" in .*) continue ;; esac
    [ -n "$(_pkg_kind_of "$name")" ] || continue
    printf '%s\n' "$name"
  done | LC_ALL=C sort
}

pkg_dir() {
  local name="${1:-}"
  case "$name" in
    "" | */* | .*) fail "unknown package '$name' (known: $(echo $(_pkg_all)))" ;;
  esac
  printf '%s\n' "$(_pkg_all)" | grep -qx "$name" || fail "unknown package $name (known: $(echo $(_pkg_all)))"
  printf '%s\n' "$name"
}

pkg_kind() {
  local name
  name=$(pkg_dir "$1") || exit 1
  _pkg_kind_of "$name" | grep . || fail "$name has no pyproject.toml, package.json, go.mod, Cargo.toml, mix.exs, *.gemspec, or composer.json"
}

pkg_names() {
  _pkg_all
}

# pkg_names as a one-line JSON array (the CI matrix).
pkg_json() {
  local name sep=""
  printf '['
  for name in $(pkg_names); do
    printf '%s"%s"' "$sep" "$name"
    sep=","
  done
  printf ']\n'
}

pkg_version() {
  local dir kind v
  dir=$(pkg_dir "$1") || exit 1
  kind=$(pkg_kind "$1") || exit 1
  case "$kind" in
    python) v=$(sed -n 's/^version = "\(.*\)"$/\1/p' "$ROOT/$dir/pyproject.toml" | sed -n 1p) ;;
    npm) v=$(node -p "require('$ROOT/$dir/package.json').version") ;;
    go) v=$(sed -n 's/^const Version = "\(.*\)"$/\1/p' "$ROOT/$dir/version.go" | sed -n 1p) ;;
    # The range matters: `[[test]] name = …` is also a column-0 `name =`, and
    # only the [package] table's `version` is the crate's own.
    cargo) v=$(sed -n '/^\[package\]/,/^\[/ s/^version = "\(.*\)"$/\1/p' "$ROOT/$dir/Cargo.toml" | sed -n 1p) ;;
    hex) v=$(sed -n 's/^  @version "\(.*\)"$/\1/p' "$ROOT/$dir/mix.exs" | sed -n 1p) ;;
    gem) v=$(sed -n 's/^  VERSION = "\(.*\)"$/\1/p' "$ROOT/$dir/lib/santati/version.rb" | sed -n 1p) ;;
    composer) v=$(sed -n "s/^    public const VERSION = '\(.*\)';\$/\1/p" "$ROOT/$dir/src/Version.php" | sed -n 1p) ;;
  esac
  [ -n "$v" ] || fail "could not read the version of $1 from $dir"
  printf '%s\n' "$v"
}

# Go modules are tagged `<subdir>/vX.Y.Z`; every other package `<name>-vX.Y.Z`.
pkg_tag() {
  local v="${2:-}"
  if [ -z "$v" ]; then
    v=$(pkg_version "$1") || exit 1
  fi
  if [ "$(pkg_kind "$1")" = go ]; then
    printf 'go/v%s\n' "$v"
  else
    printf '%s-v%s\n' "$1" "$v"
  fi
}

pkg_from_tag() {
  local tag="${1:-}" name
  case "$tag" in
    go/v*) name=go ;;
    *) name="${tag%-v*}" ;;
  esac
  pkg_dir "$name" >/dev/null || exit 1
  printf '%s\n' "$name"
}

pkg_version_from_tag() {
  local tag="${1:-}" name
  name=$(pkg_from_tag "$tag") || exit 1
  if [ "$name" = go ]; then
    printf '%s\n' "${tag#go/v}"
  else
    printf '%s\n' "${tag#"$name"-v}"
  fi
}

pkg_has_heading() {
  local dir
  dir=$(pkg_dir "$1") || exit 1
  grep -q "^## \[$2\]" "$ROOT/$dir/CHANGELOG.md"
}

# The CHANGELOG section for a version, without its heading: the GitHub
# release notes.
pkg_notes() {
  local dir
  dir=$(pkg_dir "$1") || exit 1
  pkg_has_heading "$1" "$2" || fail "$dir/CHANGELOG.md has no '## [$2]' heading"
  awk -v v="$2" '$0 ~ "^## \\[" v "\\]" {f=1; next} f && /^## \[/ {exit} f' "$ROOT/$dir/CHANGELOG.md"
}

# HTTP status of NAME@VERSION on its registry: 200 published, 404 not.
pkg_registry_code() {
  local dir kind url module_name app_name composer_name body tmpfile
  dir=$(pkg_dir "$1") || exit 1
  kind=$(pkg_kind "$1") || exit 1
  case "$kind" in
    python)
      module_name=$(sed -n 's/^name = "\(.*\)"$/\1/p' "$ROOT/$dir/pyproject.toml" | sed -n 1p)
      url="https://pypi.org/pypi/$module_name/$2/json"
      ;;
    npm)
      module_name=$(node -p "require('$ROOT/$dir/package.json').name")
      url="https://registry.npmjs.org/$module_name/$2"
      ;;
    go)
      module_name=$(sed -n 's/^module \(.*\)$/\1/p' "$ROOT/$dir/go.mod" | sed -n 1p)
      url="https://proxy.golang.org/$module_name/@v/v$2.info"
      ;;
    cargo)
      # The range matters: the `[[test]]` table's `name` is column-0 too.
      module_name=$(sed -n '/^\[package\]/,/^\[/ s/^name = "\(.*\)"$/\1/p' "$ROOT/$dir/Cargo.toml" | sed -n 1p)
      url="https://crates.io/api/v1/crates/$module_name/$2"
      ;;
    hex)
      app_name=$(sed -n 's/^ *app: :\([a-z_]*\),.*/\1/p' "$ROOT/$dir/mix.exs" | sed -n 1p)
      url="https://hex.pm/api/packages/$app_name/releases/$2"
      ;;
    gem)
      module_name=$(sed -n 's/^ *[a-z_]*\.name *= *"\(.*\)"$/\1/p' "$ROOT/$dir"/*.gemspec | sed -n 1p)
      url="https://rubygems.org/api/v2/rubygems/$module_name/versions/$2.json"
      ;;
    composer)
      # Packagist has no per-version URL: the p2 metadata lists what it knows,
      # and the version is published exactly when it appears there.
      composer_name=$(node -p "require('$ROOT/$dir/composer.json').name")
      tmpfile=$(mktemp)
      curl -s -A "santati-sdks-release (https://github.com/$GITHUB_REPO)" \
        -o "$tmpfile" "https://repo.packagist.org/p2/$composer_name.json" || true
      node -e '
        const fs = require("fs");
        let versions = [];
        try {
          const body = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
          versions = (body.packages?.[process.argv[2]] ?? []).map((p) => p.version);
        } catch {}
        const want = process.argv[3];
        process.stdout.write(versions.includes(want) || versions.includes("v" + want) ? "200" : "404");
      ' "$tmpfile" "$composer_name" "$2"
      rm -f "$tmpfile"
      return 0
      ;;
  esac
  # curl prints 000 and exits non-zero when the registry is unreachable; the
  # 000 is the useful part. crates.io answers 403 to curl's own user agent, so
  # every lookup carries one that names this repo.
  curl -s -A "santati-sdks-release (https://github.com/$GITHUB_REPO)" -o /dev/null -w '%{http_code}' "$url" || true
}

pkg_workflow() {
  printf 'release-%s.yml\n' "$(pkg_dir "$1")"
}

pkg_secrets() {
  case "$(pkg_kind "$1")" in
    hex) echo HEX_API_KEY ;;
    npm) echo NPM_TOKEN ;;
    cargo) echo CARGO_REGISTRY_TOKEN ;;
    # PyPI and RubyGems publish via Trusted Publishing (OIDC): no repo secret.
    python | gem | go) : ;;
    composer) echo PHP_MIRROR_DEPLOY_KEY ;;
  esac
}

# The mise tools a package's checks need, comma-separated: CI's
# MISE_ENABLE_TOOLS. `node` is always there for conformance/check.mjs; the
# package tools run the suite.
pkg_tools() {
  case "$(pkg_kind "$1")" in
    python) echo uv,node ;;
    npm) echo node ;;
    go) echo go,node ;;
    cargo) echo rust,node ;;
    hex) echo erlang,elixir,node ;;
    gem) echo ruby,node ;;
    composer) echo php,node ;;
  esac
}

# A tag is a promise about a commit: it must point at what CI ran on. Checked
# against origin/main rather than a branch name, because the flow is "merge the
# release PR, then tag the merged commit".
pkg_guard_main() {
  local local_sha remote_sha
  [ -z "$(git -C "$ROOT" status --porcelain)" ] || fail "working tree is dirty; commit or stash first"
  git -C "$ROOT" fetch --quiet origin main
  local_sha=$(git -C "$ROOT" rev-parse HEAD)
  remote_sha=$(git -C "$ROOT" rev-parse origin/main)
  [ "$local_sha" = "$remote_sha" ] || fail "HEAD ($local_sha) != origin/main ($remote_sha); merge and pull first"
  printf '  clean tree, HEAD == origin/main\n'
}
