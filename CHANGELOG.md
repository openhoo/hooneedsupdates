# Changelog

## 0.3.0 (2026-09-01)

### Features

- automate repository update pull requests (#9) (560fb44)
- persist GitHub rate-limit backoff (#10) (c000186)

### Other Changes

- **ci:** update Hoostack tool pins (8464e43)

## Unreleased

### Features

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
