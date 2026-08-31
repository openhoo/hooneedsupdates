# Hoostack alignment audit - 2026-08-31

## Scope and method

Scope: `hooray`, `hoolicy`, `hooversion`, `hoocloak`, `hoomail`, `hoosharper`,
and `hoonarqube` at their public `main` revisions on 2026-08-31.

Evidence came from shallow clones of each current default branch, GitHub API
readback for repository rules, releases, pull requests, and security alerts, and
a HooNeedsUpdates scan against live registries. Counts below are dependency
occurrences, so repeated action uses count separately.

## Results

| Repository | Update automation | Detected | Applicable updates | Unresolved |
| --- | --- | ---: | ---: | ---: |
| hooray | Dependabot | 45 | 22 | 0 |
| hoolicy | Renovate | 57 | 26 | 0 |
| hooversion | none | 20 | 19 | 0 |
| hoocloak | Renovate | 68 | 40 | 0 |
| hoomail | Dependabot | 80 | 35 | 0 |
| hoosharper | none | 35 | 30 | 0 |
| hoonarqube | none | 57 | 27 | 0 |
| **Total** | mixed | **362** | **199** | **0** |

At the same snapshot, Dependabot had seven open update PRs in Hooray and five in
Hoomail. Renovate had six open update PRs in Hoolicy. The other four repositories
had no updater PR. Configuration and resulting behavior are therefore not
aligned across the stack.

## Confirmed gaps

### 1. Dependency updates have no shared contract

Hooray and Hoomail use different Dependabot grouping and schedule policies.
Hoolicy and Hoocloak use different Renovate policies. Hooversion, HooSharper,
and Hoonarqube have no updater configuration. The mixed setup cannot produce a
single organization inventory, shared ignore rationale, or common failure mode.

HooNeedsUpdates addresses inventory and reviewed edits first. It does not yet
claim Renovate-equivalent bot lifecycle or lockfile breadth.

### 2. GitHub Action pinning is inconsistent

Most action references use immutable 40-character SHAs. Hooversion still used
`actions/setup-go@v6`; Hoonarqube still used `actions/checkout@v6` and
`actions/cache@v5`. All seven repositories also pinned older released Hoolicy,
Hooray, Hoonarqube, or Hooversion revisions.

Ordinary tag updaters do not safely maintain OpenHoo's coupled action SHA,
release comment, and action `version` input. HooNeedsUpdates treats those as one
upstream release and applies byte-verified edits.

### 3. Default-branch governance is inconsistent

Hooversion had active repository rulesets requiring pull requests, CI checks,
and CodeQL on `main`. The other six repositories had neither classic branch
protection nor equivalent active protection rulesets. Hoomail's active Copilot
review ruleset is not a merge-protection substitute.

This is a repository-governance gap, not an updater feature. Add equivalent
rulesets separately before enabling updater automerge. HooNeedsUpdates keeps
automerge out of v0.1 for this reason.

### 4. Release provenance varies by project

Hoolicy publishes SBOM, Sigstore bundles, GitHub attestations, and a signed GHCR
image. Hoonarqube signs and attests release assets but has no release SBOM.
Hoocloak and Hoomail attest assets and publish images but do not sign them with
Sigstore in their repository workflows. Hooray, Hooversion, and HooSharper lack
the same asset-attestation/SBOM pattern.

The products have different distribution models, so identical jobs are not
always required. A minimum release profile should still define checksums,
license inclusion, provenance, SBOM applicability, and remote verification.

### 5. Public community contracts vary

All seven repositories contain `README.md`, `LICENSE`, and `SECURITY.md`. Only
Hoolicy also contained `CONTRIBUTING.md` and `CODE_OF_CONDUCT.md`. No reviewed
shared support or governance contract was present across the set.

License differences are recorded, not automatically treated as defects:
Hooray, Hoolicy, Hooversion, Hoomail, and Hoonarqube use Apache-2.0; Hoocloak and
HooSharper use MIT. A license migration requires an explicit project decision.

### 6. Security scanning is currently healthy

Live GitHub readback returned zero open code-scanning alerts and zero open
secret-scanning alerts for every repository in scope. This proves the current
GitHub alert queues were empty; it does not prove absence of every vulnerability
or secret.

## Recommended order

1. Adopt one read-only HooNeedsUpdates scan in every repository.
2. Resolve current Hoo action pin drift in reviewed PRs.
3. Add equivalent `main` rulesets before any automated merging.
4. Add lockfile-safe isolated update application in HooNeedsUpdates v0.2.
5. Replace mixed updater PR creation only after GitHub App permissions,
   idempotency, required-check readback, and recovery are proven.
6. Define tiered release-provenance and community-file profiles in Hoolicy.

## Verification boundary

Registry results are time-sensitive. Re-run:

```sh
hooneedsupdates scan --format json --fail-on unresolved REPOSITORY
```

The audit does not authorize automatic merging, branch-rule changes, license
migration, or mass dependency updates. Those remain reviewed repository changes.
