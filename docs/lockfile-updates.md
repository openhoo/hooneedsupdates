# Lockfile-safe updates

`hooneedsupdates apply --lockfiles` regenerates lockfiles without trusting a
repository to define commands. Preview remains the default. Add `--write` only
after reviewing the plan.

## Contract

1. The input must be the repository root and the approved manifests and
   existing lockfiles must match `HEAD`.
2. HooNeedsUpdates creates two detached worktrees from `HEAD`, applies the same
   byte-verified plan, and runs fixed package-manager commands in each.
3. Only approved manifests and manager-specific lockfiles may change. Required
   lockfiles must exist, manifest bytes must equal the approved plan, and each
   generated file is limited to 64 MiB.
4. Both runs must return the same path set, bytes, modes, and creation state.
5. `--write` revalidates every source file, then atomically writes the verified
   result. A partial multi-file failure triggers rollback.

Unrelated dirty files are allowed because the detached worktrees start from
`HEAD`. Dirty planned manifests or lockfiles are rejected, so no local work is
silently replaced. Root aliases such as macOS `/var` and `/private/var` are
compared by directory identity; a subdirectory still cannot stand in for the
repository root. Inherited Git repository and index overrides cannot redirect
these operations to another checkout.

A dependency requiring a newer Go directive can cause `go mod tidy` to change
`go.mod` beyond the approved version edit. This is rejected before source writes.
Upgrade the toolchain requirement separately and review its compatibility impact
before retrying the dependency update.

## Manager commands

| Manager | Fixed operation | Expected output |
| --- | --- | --- |
| Go | `go mod tidy` with local toolchain and isolated module/build caches | `go.sum`, optional `go.work.sum` |
| Cargo | exact temporary manifest pins plus `cargo update --package … --precise …` | workspace or package `Cargo.lock` |
| Bun | `bun install --lockfile-only --ignore-scripts` with isolated cache | existing `bun.lock`/`bun.lockb`, or declared `bun.lock` |
| npm | `npm install --package-lock-only --ignore-scripts --allow-git=none` | existing or declared `package-lock.json` |
| NuGet | `dotnet restore` of a generated static project with fixed nuget.org config | per-project `packages.lock.json` |

Every command has bounded output and uses `lockfileTimeout` (default `5m`,
maximum `30m`). Package-manager caches, home directories, temp directories, and
Git hooks are isolated per regeneration.

## Fail-closed boundaries

- Git content filters and repository `.cargo/config` or `.cargo/config.toml`
  are rejected.
- Package-manager changes outside the approved manifest/lockfile set are
  rejected.
- Cargo manifests are temporarily exact-pinned so a compatible but newer
  version cannot replace the reviewed target.
- NuGet supports literal `Microsoft.NET.Sdk` projects, literal target
  frameworks, static package/project references, and static central package
  versions. MSBuild expressions, conditions, custom SDKs, and executable
  project targets are not evaluated.
- A package-manager failure, timeout, missing output, nondeterministic result,
  symlink, oversized file, or source race produces no intended source write.

The updater does not claim source or runtime compatibility. After a successful
write, use the target repository's locked restore, build, test, security, and
platform checks before review or merge.

## Executable qualification

CI invokes real Go, Cargo, npm, Bun, and .NET runtimes, requires all five to be
available, and regenerates each fixture twice. Go includes a workspace. Bun
includes a lifecycle-script trap; NuGet includes both project and imported
MSBuild target traps. Qualification fails if those traps run. Local macOS qualification also passed
with Bun 1.4.2 and .NET SDK 10.0.401.

The supported npm command requires npm 12.0.2 or newer for `--allow-git=none`.
The CI matrix pins npm 12.0.2, Bun 1.3.14, .NET SDK 10.0.x, and Go 1.27.x; Cargo
uses the runner's stable toolchain. The project itself is tested with Go 1.25.x.
Older package managers with missing required flags fail before source writes;
this qualification does not establish every older runtime/version combination.

Verified source bytes are copied into each detached worktree before approved
edits so Git's checkout CRLF conversion cannot shift their edit spans. Git stdout
is parsed separately from warnings on stderr. A tool that makes additional
manifest changes, including line-ending changes, still fails the approved-byte
contract.

Save the completed regeneration with `scan --plan PATH --lockfiles` to review
and apply the exact output later without invoking those runtimes again. See
[saved plans and recovery](saved-plans.md).
