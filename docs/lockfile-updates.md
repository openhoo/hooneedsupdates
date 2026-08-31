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
silently replaced.

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
