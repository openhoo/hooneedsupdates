# HooNeedsUpdates

[![CI](https://github.com/openhoo/hooneedsupdates/actions/workflows/ci.yml/badge.svg)](https://github.com/openhoo/hooneedsupdates/actions/workflows/ci.yml)

HooNeedsUpdates is a preview-first dependency update planner for repositories that
need one consistent view across language manifests, containers, GitHub Actions,
and OpenHoo's own pinned actions.

It answers three questions without changing a repository:

1. Which direct dependencies and build inputs are discoverable?
2. Which stable upstream version is current?
3. Which exact bytes would an update change?

`apply` is also preview-only unless `--write` is explicitly supplied. Writes
verify the scanned bytes again, reject symlinks and non-regular files, and replace
files atomically. GitHub Actions are moved to immutable commit SHAs while keeping
their release tag as an auditable comment. OpenHoo action `version` inputs are
updated with the action revision.

## Supported inputs

| Manager | Files | Datasource | Apply support |
| --- | --- | --- | --- |
| Go modules | `go.mod` | Go module proxy | Direct requirements |
| Cargo | `Cargo.toml` | crates.io | Direct non-path requirements outside the current compatible range |
| npm/Bun | `package.json` | npm registry | Direct dependency ranges |
| NuGet | `*.csproj`, `Directory.Packages.props` | NuGet flat container | `PackageReference` and `PackageVersion` |
| GitHub Actions | workflows and `action.yml` | GitHub releases/tags | Immutable SHA plus release comment |
| Containers | `Dockerfile*` | Docker Hub | Version-like tags on the same image channel |
| Custom | configured regex capture | GitHub releases | Named `currentValue` capture |

The first release deliberately does not regenerate lockfiles, merge pull
requests, automerge, or execute repository-provided commands. After `--write`,
run the ecosystem's lockfile command and complete the repository's full test
suite before committing. This boundary keeps an untrusted repository from
turning updater configuration into remote code execution.

## Install

Download a release archive and verify it against `SHA256SUMS`, or install with Go:

```sh
go install github.com/openhoo/hooneedsupdates/cmd/hooneedsupdates@v0.1.2
```

Successful non-release CI on `main` now runs Hooversion automatically. The
version workflow creates the release commit and immutable tag, then dispatches
the signed release workflow for that exact tag. Manual version runs default to
dry-run; existing-tag rebuilds remain explicit in the release workflow.

Container:

```sh
docker run --rm --user "$(id -u):$(id -g)" \
  -e GITHUB_TOKEN \
  -v "$PWD:/work:ro" -w /work \
  ghcr.io/openhoo/hooneedsupdates:v0.1.2 scan .
```

`GITHUB_TOKEN` or `GH_TOKEN` is optional for public repositories, but avoids the
anonymous GitHub API rate limit. Never store tokens in `hooneedsupdates.yaml`.

## Use

```sh
hooneedsupdates init
hooneedsupdates scan .
hooneedsupdates scan --format json --fail-on unresolved .
hooneedsupdates apply .
hooneedsupdates apply --write .
```

Exit behavior:

- Default `--fail-on never`: findings are reported and exit status remains zero.
- `--fail-on outdated`: exits `2` when applicable updates exist.
- `--fail-on unresolved`: exits `3` when a datasource could not be resolved.
- Invalid input or configuration exits `2`; operational failure exits `1`.

Example configuration:

```yaml
version: 1
managers: [gomod, cargo, npm, nuget, github-actions, docker]
excludePaths:
  - '(^|/)(fixtures|testdata|\.oracle)(/|$)'
allowedUpdateTypes: [patch, minor, major]
concurrency: 8
requestTimeout: 15s
includePrereleases: false
ignore:
  - dependency: '^example/legacy$'
    managers: [github-actions]
    reason: Removal tracked in issue 123
customManagers:
  - name: hooversion-version
    datasource: github-releases
    dependencyName: openhoo/hooversion
    filePatterns: ['^\.github/workflows/.*\.ya?ml$']
    matchStrings:
      - 'HOOVERSION_VERSION:\s*["'']?(?P<currentValue>[^\s"'']+)'
```

Configuration rejects unknown fields, invalid regular expressions, unknown
managers, unreasoned ignores, and custom matchers without a named
`currentValue` capture.

## GitHub Actions

Pin the setup action to the commit behind the desired HooNeedsUpdates release:

```yaml
- uses: openhoo/hooneedsupdates/actions/setup@RELEASE_COMMIT_SHA # v0.1.2
  with:
    version: 0.1.2
- run: hooneedsupdates scan --fail-on unresolved .
  env:
    GITHUB_TOKEN: ${{ github.token }}
```

Using `--fail-on unresolved` keeps registry outages and unsupported sources
visible without blocking merely because a normal update exists. Scheduled
automation can consume JSON output to build a dashboard or reviewed update PR.

## Security model

- Registry responses are untrusted and bounded to 32 MiB per response.
- Requests have a configured timeout and bounded concurrency.
- Fixture, `testdata`, oracle, VCS, vendor, build, and package cache trees are
  excluded by default.
- Symlinked manifests are never followed.
- Apply verifies original byte ranges and rejects overlapping edits.
- Files are replaced atomically while preserving their permission bits.
- No repository configuration can run shell commands.
- Pre-releases are excluded unless explicitly enabled.
- GitHub Action updates resolve annotated tags to their final commit object.

Report vulnerabilities through [GitHub private vulnerability reporting](https://github.com/openhoo/hooneedsupdates/security/advisories/new).

## Project status

Version `0.1.x` is useful for deterministic inventory, update planning, and
reviewed manifest edits. Lockfile-aware isolated worktrees, grouping, update
PR lifecycle, registry authentication beyond GitHub, and organization-wide
dashboards are tracked in [ROADMAP.md](ROADMAP.md).

The Hoostack alignment review that led to this project is recorded in
[docs/hoostack-audit-2026-08-31.md](docs/hoostack-audit-2026-08-31.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
