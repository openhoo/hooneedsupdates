# Changelog

## 0.3.1 (2026-09-03)

### Bug Fixes

- **updates:** harden discovery and installers (970c7e4)

### Other Changes

- pin setup action to v0.3.0 (#13) (f3d33df)

## 0.3.0 (2026-09-01)

### Features

- automate repository update pull requests (#9) (560fb44)
- persist GitHub rate-limit backoff (#10) (c000186)

### Other Changes

- **ci:** update Hoostack tool pins (8464e43)

## Unreleased

### Bug Fixes

- Support macOS root aliases in reproducible lockfile updates.
- Stop canceled scans without reporting incomplete findings or scheduling new
  resolutions; reject invalid concurrency before launching workers.
- Extract action references and OpenHoo version inputs from YAML structure,
  keeping scripts and unrelated steps outside the update plan.
- Bound configuration and manifest reads, reject trailing YAML documents, and
  prune excluded directory trees.
- Correct Cargo zero-patch and prerelease compatibility; discover npm and NuGet
  prerelease/build versions; reject malformed upstream versions.
- Complete Docker tag pagination within a bounded same-registry traversal.
- Share bounded GitHub retries with standalone scans and preserve rate-limit
  deferrals when release discovery falls back to tags.
- Constrain GitHub requests and redirects to the configured HTTPS host.
- Isolate automation Git commands from inherited repository settings and require
  an absence lease when publishing a new managed branch.
- Respect existing draft PRs when deciding whether to enable auto-merge.
- Return successful exit status for subcommand help.

### Maintenance

- Update `golang.org/x/mod` to v0.41.0 and raise the minimum supported Go
  version to 1.26.0; migrate minimum-version CI and contributor documentation.

- Refresh workflow tool versions and immutable action revisions using two
  reproducible updater worktrees.
- Add focused macOS filesystem regression CI and cancel superseded CI runs.
- Record the October 2026 review and compatibility follow-up work.

### Features

- Save exact reviewed manifest and lockfile output for offline apply, with strict
  schema/checksum/source validation and unified diffs.
- Resolve and atomically update Docker tag/digest pins; report unsupported
  container references and complete anonymous catalogs through OCI fallback.
- Add cumulative package rules for atomic groups, shared versions, release age,
  channels, and compatibility windows. Block incomplete selected inventory.
- Lease stale branch deletions and paginate PR ownership history within bounds.
- Qualify native Windows transactions, Go 1.25, and real Go/Cargo/npm/Bun/NuGet
  lockfile regeneration; isolate Git warnings from machine-readable output.
- Split extraction, resolution, and lockfile grouping by ecosystem; publish
  generated configuration/report/plan schemas and recovery/migration guides.
- Add read-only-by-default multi-repository update reconciliation with exact-SHA
  branch leases, managed pull requests, stale-plan closure, and deterministic
  commits.
- Add configurable native GitHub auto-merge policy by update type, manager,
  dependency expression, maximum update count, and lockfile requirement.
- Add a fail-closed automation selection so tool-pin PRs stay independent from
  unrelated package-manager updates and unresolved selected inputs remain fatal.
- Add scheduled Hoostack reconciliation through a repository-scoped GitHub App
  installation token.

## 0.2.0 (2026-08-31)

### Features

- **updates:** add reproducible lockfile application (#6) (5eac341)

## 0.1.2 (2026-08-31)

### Bug Fixes

- align Hoostack policy and release supply chain (#3) (26f701d)
- **release:** honor protected main branch (f3650c9)

All notable changes follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and Semantic Versioning.

## [0.1.1] - 2026-08-31

### Fixed

- Binaries installed with `go install ...@v0.1.1` now report their module version
  when no release linker override is present.

## [0.1.0] - 2026-08-31

### Added

- Preview-first dependency scans for Go, Cargo, npm/Bun, NuGet, GitHub Actions,
  Docker Hub, and configured GitHub-release fields.
- Deterministic table and JSON reports with policy-oriented exit modes.
- Fail-closed atomic manifest edits and immutable GitHub Action pin updates.
- Hoostack dogfooding, signed release assets, attestations, SBOM, and GHCR image.
- Evidence-backed Hoostack alignment review.
