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

- Parse lockfiles separately from manifest constraints.
- Regenerate each ecosystem in an isolated Git worktree.
- Compare resulting manifests and lockfiles against the approved plan.
- Roll back the isolated worktree on tool failure or unexpected files.

Exit: Go, Cargo, Bun/npm, and NuGet updates produce reproducible lockfile diffs
without executing repository-provided commands.

## v0.3 - Grouping and compatibility policy

- Named dependency groups and shared-version families.
- Compatibility windows, minimum age, and release-channel policy.
- Security-update priority using Hooray findings.
- Changelog and release-note evidence attached to plans.

Exit: grouped Hoo action version/SHA changes and language-family updates remain
atomic and individually reviewable.

## v0.4 - GitHub pull-request lifecycle

- GitHub App authentication and least-privilege repository selection.
- Idempotent branches, pull requests, labels, rebases, and stale-plan closure.
- Required-check readback before automerge; automerge disabled by default.
- Rate-limit and abuse-limit backoff with resumable state.

Exit: repeated runs converge without duplicate PRs or bypassing branch rules.

## v0.5 - Organization dashboard

- Cross-repository inventory, age, ownership, and update backlog.
- Signed scan evidence and historical comparison.
- GitHub and GitLab adapters behind the same repository contract.

Exit: organization reports distinguish current, actionable, ignored, blocked,
unresolved, and policy-disallowed updates.

## v1.0 - Stable automation contract

- Stable configuration and JSON schemas.
- Backward-compatible manager and datasource interfaces.
- Documented recovery, migration, and support policy.
- Proven cross-platform release and long-running GitHub App operation.

## Non-goals

- Executing arbitrary repository-defined post-update commands.
- Treating every newest version as safe or automatically mergeable.
- Replacing Hooray vulnerability analysis, Hoolicy policy, Hoonarqube code
  analysis, or Hooversion release semantics.
- Claiming Renovate's package-manager breadth before equivalent behavior exists.
