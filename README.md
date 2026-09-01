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
files atomically. `apply --lockfiles` performs the approved edit twice in fresh,
detached Git worktrees and accepts only byte-identical manifest and lockfile
results. GitHub Actions are moved to immutable commit SHAs while keeping their
release tag as an auditable comment. OpenHoo action `version` inputs are updated
with the action revision.

`update-repos` turns those byte-verified plans into one managed pull request per
repository. It is read-only unless `--write` is supplied. Optional native GitHub
auto-merge is content-gated by update type, manager, dependency expression, and
maximum update count; repository checks and reviews remain authoritative.

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

Lockfile mode supports `go.sum`/`go.work.sum`, `Cargo.lock`, `bun.lock`/
`bun.lockb`, `package-lock.json`, and NuGet `packages.lock.json`. It invokes only
fixed package-manager commands with scripts and Git hooks disabled, isolated
caches, bounded output, and a configured timeout. NuGet restore uses generated
static `Microsoft.NET.Sdk` projects instead of evaluating repository MSBuild
targets. Unsupported dynamic or conditional NuGet inputs and repository Cargo
configuration fail closed. Detailed boundaries live in
[docs/lockfile-updates.md](docs/lockfile-updates.md).

HooNeedsUpdates never pushes directly to a default branch and does not execute
configured repository commands. It can request native GitHub auto-merge for an
eligible managed PR, but has no direct-merge fallback or branch-rule bypass.
Lockfile success proves a reproducible dependency graph, not source
compatibility.

## Install

Download a release archive and verify it against `SHA256SUMS`, or install with Go:

```sh
go install github.com/openhoo/hooneedsupdates/cmd/hooneedsupdates@v0.3.0
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
  ghcr.io/openhoo/hooneedsupdates:v0.3.0 scan .
```

`GITHUB_TOKEN` or `GH_TOKEN` is optional for public repositories, but avoids the
anonymous GitHub API rate limit. Never store tokens in `hooneedsupdates.yaml`.

## Use

```sh
hooneedsupdates init
hooneedsupdates scan .
hooneedsupdates scan --format json --fail-on unresolved .
hooneedsupdates apply .
hooneedsupdates apply --lockfiles .
hooneedsupdates apply --lockfiles --write .
hooneedsupdates apply --write .
hooneedsupdates update-repos openhoo/hooversion openhoo/hoolicy
GH_TOKEN="$INSTALLATION_TOKEN" hooneedsupdates update-repos --write
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
lockfileTimeout: 5m
includePrereleases: false
automation:
  repositories: [openhoo/hooversion, openhoo/hoolicy]
  branchPrefix: hooneedsupdates
  lockfiles: true
  selection:
    managers: [github-actions, custom]
    dependencies: ['^openhoo/']
  autoMerge:
    enabled: true
    updateTypes: [patch, minor]
    managers: [github-actions, custom]
    dependencies: ['^openhoo/']
    maxUpdates: 10
    requireLockfiles: true
  rateLimit:
    stateFile: .hooneedsupdates/rate-limit.json
    maxRetries: 2
    maxWait: 30s
  mergeMethod: squash
  closeStale: true
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
`currentValue` capture. Auto-merge is rejected for draft PRs or unsafe policy
values. Fleet runs persist GitHub cooldowns when `rateLimit.stateFile` is set;
longer waits return `deferred` results without failing the scheduled run. See
[repository automation](docs/repository-automation.md) for exact PR lifecycle,
authentication, retry policy, and recovery behavior.

## GitHub Actions

Pin the setup action to the commit behind the desired HooNeedsUpdates release:

```yaml
- uses: openhoo/hooneedsupdates/actions/setup@5f29337d0c39c47c691947aae0a201d2cfca8d64 # v0.3.0
  with:
    version: 0.3.0
- run: hooneedsupdates scan --fail-on unresolved .
  env:
    GITHUB_TOKEN: ${{ github.token }}
```

Using `--fail-on unresolved` keeps registry outages and unsupported sources
visible without blocking merely because a normal update exists. Scheduled
automation can use `update-repos` to reconcile reviewed update PRs. The bundled
Hoostack workflow uses a repository-scoped GitHub App token when its App client
ID and private-key secret are configured.

## Security model

- Registry responses are untrusted and bounded to 32 MiB per response.
- Requests have a configured timeout and bounded concurrency.
- Fixture, `testdata`, oracle, VCS, vendor, build, and package cache trees are
  excluded by default.
- Symlinked manifests are never followed.
- Apply verifies original byte ranges and rejects overlapping edits.
- Files are replaced atomically while preserving their permission bits.
- Lockfile mode disables lifecycle scripts and Git hooks, isolates package
  caches, rejects Git content filters, and accepts only expected paths.
- NuGet lockfiles come from sanitized static project graphs; original MSBuild
  projects are never evaluated.
- No updater configuration can run shell commands.
- Fleet writes require an explicit `--write` and a token; preview remains
  remote-read-only.
- GitHub REST, GraphQL, and release-resolution requests share bounded retries;
  longer primary or secondary rate-limit cooldowns persist atomically.
- Managed branches use exact-SHA force-with-lease and are never overwritten
  without matching PR ownership evidence.
- Any unexpected changed path, unresolved dependency, or non-reproducible
  lockfile result stops that repository before publication.
- Auto-merge uses GitHub's native request and is disabled when a new plan no
  longer satisfies policy.
- Pre-releases are excluded unless explicitly enabled.
- GitHub Action updates resolve annotated tags to their final commit object.

Report vulnerabilities through [GitHub private vulnerability reporting](https://github.com/openhoo/hooneedsupdates/security/advisories/new).

## Project status

Current source tree implements deterministic inventory, reviewed manifest edits,
reproducible Go, Cargo, Bun/npm, and static NuGet lockfile changes, plus the
idempotent GitHub update-PR lifecycle with resumable rate-limit state. Grouped
update families, minimum-age policy, GitLab automation, and organization-wide
dashboards remain tracked in [ROADMAP.md](ROADMAP.md).

The Hoostack alignment review that led to this project is recorded in
[docs/hoostack-audit-2026-08-31.md](docs/hoostack-audit-2026-08-31.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
