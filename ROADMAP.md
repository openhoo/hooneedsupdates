# Roadmap

Roadmap ordered by safety dependency, not dates.

## v0.1 - Inventory and reviewed edits

- Multi-ecosystem extraction and stable-version resolution.
- Preview-first, byte-verified atomic apply.
- Immutable GitHub Action pins and OpenHoo action version coupling.
- JSON evidence for CI and dashboards.

Exit: all seven pre-existing Hoostack repositories scan with zero unresolved
inputs, and a real action/module update survives the target repository's tests.

## v0.2 - Lockfile-safe updates

Status: implemented on main and qualified against real Go, Cargo, Bun/npm, and
NuGet repositories. Release remains a separate publication step.

- [x] Parse lockfiles separately from manifest constraints.
- [x] Regenerate each ecosystem twice in isolated Git worktrees.
- [x] Compare resulting manifests and lockfiles against the approved plan.
- [x] Leave the source worktree unchanged on tool failure, nondeterminism, or
  unexpected files; roll back partial source writes.

Exit: Go, Cargo, Bun/npm, and NuGet updates produce reproducible lockfile diffs
without executing repository-provided commands.

## Reviewed artifacts and container pins

- [x] Exact offline saved plans with source hashes and creation-state checks.
- [x] Unified diffs and explicit incomplete-inventory write refusal.
- [x] Docker Hub version tags plus image-index digest updates.
- [x] Honest unsupported Docker references and complete bounded catalogs.
- [x] Native Windows and minimum-Go CI; real five-manager qualification.

## v0.3 - Grouping and compatibility policy

- [x] Named dependency groups and shared-version families.
- [x] Compatibility windows, minimum age, and release-channel policy.
- Security-update priority using Hooray findings.
- Changelog and release-note evidence attached to plans.

Exit: grouped Hoo action version/SHA changes and language-family updates remain
atomic and individually reviewable.

## v0.4 - GitHub pull-request lifecycle

Status: implemented in the current source tree. GitHub App installation tokens
are consumed through the bundled scheduled workflow; bounded API backoff
persists cooldown state across workflow runs.

- [x] GitHub App installation-token authentication and least-privilege
  repository selection.
- [x] Idempotent branches, pull requests, labels, exact-SHA rebases, and
  stale-plan closure.
- [x] Native GitHub auto-merge behind exact content policy; repository checks,
  reviews, and rules remain authoritative and auto-merge defaults to disabled.
- [x] Rate-limit and abuse-limit backoff with resumable state.

Exit: repeated runs converge without duplicate PRs or bypassing branch rules.

## v0.5 - Organization dashboard

- Cross-repository inventory, age, ownership, and update backlog.
- Signed scan evidence and historical comparison.
- GitHub and GitLab adapters behind the same repository contract.

Exit: organization reports distinguish current, actionable, ignored, blocked,
unresolved, and policy-disallowed updates.

## v1.0 - Stable automation contract

- [x] Versioned configuration, report, and saved-plan schemas with drift checks.
- Long-term schema compatibility guarantees.
- Backward-compatible manager and datasource interfaces.
- [x] Documented saved-plan recovery and migration.
- Formal release/support policy.
- Proven cross-platform release and long-running GitHub App operation.

## Non-goals

- Executing arbitrary repository-defined post-update commands.
- Treating every newest version as safe or automatically mergeable.
- Replacing Hooray vulnerability analysis, Hoolicy policy, Hoonarqube code
  analysis, or Hooversion release semantics.
- Claiming Renovate's package-manager breadth before equivalent behavior exists.
