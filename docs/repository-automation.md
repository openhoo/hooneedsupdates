# Repository automation

`update-repos` turns reviewed HooNeedsUpdates plans into one managed pull request
per repository. It never pushes to the default branch and has no direct-merge
fallback.

```sh
# Read-only: clone, resolve, reproduce lockfiles, and show intended PR actions.
hooneedsupdates update-repos openhoo/hooversion openhoo/hoolicy

# Reconcile bot branches and pull requests.
GH_TOKEN="$INSTALLATION_TOKEN" hooneedsupdates update-repos --write
```

Without positional repositories, the command uses `automation.repositories`.
`--format json` emits durable per-repository results. One repository failure does
not stop later repositories, but the command exits non-zero when any repository
failed.

`automation.selection` defines which dependencies belong to this PR channel:

```yaml
automation:
  selection:
    updateTypes: []
    managers: [github-actions, custom]
    dependencies: ['^openhoo/']
```

Empty fields mean all values. Matching unresolved inputs remain selected even
when `updateTypes` is restricted, because an unknown version must never disappear
behind a type filter. Unselected findings remain visible in normal `scan` output
but neither enter nor block this managed PR. This lets a Hoostack tool-pin PR
remain independent from unrelated Cargo, Go, npm, or NuGet updates.

## Lifecycle

For every configured repository HooNeedsUpdates:

1. reads repository metadata and clones the current default branch;
2. resolves every configured dependency and refuses partial plans with any
   unresolved datasource;
3. applies the plan, by default regenerating lockfiles twice in detached
   worktrees and requiring byte-identical output;
4. rejects every changed path not returned by the reviewed apply operation;
5. creates a deterministic commit and pushes only
   `hooneedsupdates/updates`;
6. creates or updates one marked pull request;
7. uses `--force-with-lease` with the exact remote branch SHA when the default
   branch or plan changed;
8. closes the marked PR and deletes its matching branch when the plan becomes
   current.

An existing branch without a marked open or matching historical PR is never
overwritten. A closed PR proves branch ownership only while the remote branch
still points at that PR's recorded head SHA.

## Exact auto-merge policy

Auto-merge is opt-in and evaluated against every update in the combined PR:

```yaml
automation:
  autoMerge:
    enabled: true
    updateTypes: [patch, minor]
    managers: [github-actions, custom]
    dependencies:
      - '^openhoo/(hooversion|hoolicy|hooray|hoonarqube)$'
    maxUpdates: 10
    requireLockfiles: true
  mergeMethod: squash
```

Every outdated entry must have an allowed update type and manager and match at
least one dependency expression. The whole PR remains manual when one entry
fails, the update count exceeds `maxUpdates`, resolution is incomplete,
reproducible lockfile application fails, or the repository does not allow native
GitHub auto-merge.

Eligible PRs use GitHub's `enablePullRequestAutoMerge` mutation. GitHub still
waits for the repository's required checks, reviews, conversation resolution,
deployment gates, and up-to-date-branch rules. HooNeedsUpdates never substitutes
its own green status for those controls. If a later plan stops matching the
policy, HooNeedsUpdates disables an existing auto-merge request.

`draft: true` and enabled auto-merge are rejected as contradictory. Major
updates require explicit inclusion in `updateTypes`.

## Authentication and scheduled operation

Read-only previews may use public GitHub access. `--write` requires `GH_TOKEN`
or `GITHUB_TOKEN`. Use a short-lived GitHub App installation token scoped to the
listed repositories. Required repository permissions:

- Metadata: read
- Contents: write
- Pull requests: write
- Workflows: write when workflow files may change
- Issues: write only when `automation.labels` is non-empty

The bundled `Update Hoostack` workflow runs every six hours and accepts the
`hoostack-tool-released` repository-dispatch event. It remains a successful
no-op until both are configured in `openhoo/hooneedsupdates`:

- repository variable `HOONEEDSUPDATES_APP_CLIENT_ID`
- repository secret `HOONEEDSUPDATES_APP_PRIVATE_KEY`

Install the App only on the repositories listed in the workflow. Enable native
auto-merge on each repository where policy-approved PRs should merge. Branch
protection or rulesets remain the source of truth for required checks and
reviews.

## Recovery

- Disable `automation.autoMerge.enabled` to keep all managed PRs manual. The
  next run removes pending native auto-merge requests.
- Remove a repository from `automation.repositories` to stop managing it. This
  intentionally does not delete its existing PR or branch.
- Set `closeStale: false` to retain a managed PR after its update plan becomes
  current.
- Delete or edit neither the marker nor bot branch while automation owns the
  PR. HooNeedsUpdates fails closed when ownership evidence is inconsistent.
