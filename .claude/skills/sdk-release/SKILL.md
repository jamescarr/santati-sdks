---
name: sdk-release
description: Release one or more Santati SDKs (python, typescript, go, rust, elixir, ruby, php) — bump versions, date the CHANGELOGs, open and merge the release PR, tag, watch the publish, verify the registry. Use when the user asks to release, publish, ship, bump, or tag an SDK, or asks why a release is stuck.
allowed-tools: [
    Bash(mise run *),
    Bash(git *),
    Bash(gh *),
]
---

## When to use

The user wants a new version of one or more SDKs published. One PR can carry
several packages; each package is versioned on its own.

## Steps

1. **See where things stand.**

   ```sh
   mise run status
   ```

   Prints one row per package: kind, version, last tag, whether that version
   is published, and commits since the tag. `CHANGES` is the number of commits
   touching the package since its last tag.

2. **Make sure each package's `CHANGELOG.md` has `[Unreleased]` entries.**
   `release:prepare` refuses a package with none. Write them from the commits:

   ```sh
   git log <last tag>..HEAD -- <dir>
   ```

   Keep-a-changelog style, `### Added` / `### Fixed` / `### Changed` under
   `## [Unreleased]`.

3. **Prepare the release PR.**

   ```sh
   mise run release:prepare minor python typescript
   ```

   `<bump>` is `patch`, `minor`, `major`, or an explicit `X.Y.Z`. It refuses a
   dirty tree, a `HEAD` that is not `origin/main`, and a missing `[Unreleased]`
   section; it then sets each version file, dates a `## [X.Y.Z]` section,
   commits on `release/python-v0.1.0-and-1-more`, pushes, and opens a PR. It
   prints `committed "chore(release): …" on <branch>` and the PR URL.

4. **Wait for CI on the PR, then merge it.**

   ```sh
   gh pr checks --watch
   gh pr merge --squash
   ```

5. **Tag from the merged main.**

   ```sh
   git switch main && git pull
   mise run release:tag python typescript
   ```

   `release:tag` runs `release:preflight` first (clean tree, `HEAD ==
   origin/main`, heading present, tags free, versions unpublished, secrets
   set, PHP mirror reachable) and then pushes one tag per package, printing
   `pushed <tag>; watch it: mise run release:watch <pkg>`.

6. **Watch each publish.**

   ```sh
   mise run release:watch python
   ```

   Follows that tag's workflow run to completion (`gh run watch
   --exit-status`).

7. **Verify the registries.**

   ```sh
   mise run release:verify python typescript
   ```

   Expects HTTP 200 for each version from `pkg_registry_code`; prints
   `  <pkg>  <version>  200`.

## If it fails

- `working tree is dirty` / `HEAD (…) != origin/main (…)` — commit or stash,
  or `git switch main && git pull` first. `release:prepare` and `release:tag`
  both refuse to guess.
- `has no [Unreleased] entries` — step 2.
- `tag … already exists` — that version is already tagged; bump again.
- `is already published (HTTP 200)` — the version is live on its registry;
  `release:prepare` for a new one.
- `repo secret <NAME> is not set` — add it (docs/releasing.md) and re-run.
- `mirror jamescarr/santati-php does not exist` — the PHP mirror repo is not
  created yet; docs/releasing.md, "Packagist (php)".
- A publish run that fails after the tag exists: fix the cause and re-run the
  workflow from the Actions UI (`gh run rerun <id>`) rather than re-tagging.
  The registry check in `release:verify` tells you whether it published.

## Done when

`mise run release:verify <pkgs…>` prints `200` for every package, and each
package's GitHub release exists with the CHANGELOG section as its notes.
