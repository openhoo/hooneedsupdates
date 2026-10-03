# Saved plans and recovery

Saved plans carry the exact approved file output, including regenerated lockfiles.
Applying a saved plan makes no registry requests and invokes no package managers.
The plan binds that output to the absolute repository root, original whole-file
hashes, permissions, and creation state.

## Review and apply

```sh
# Manifest output only. The scan writes this new artifact, never source files.
hooneedsupdates scan --plan /tmp/dependency-plan.json .

# Include lockfiles produced by two byte-identical isolated regenerations.
hooneedsupdates scan --plan /tmp/dependency-plan-with-lockfiles.json --lockfiles .

# Read the exact proposed changes, then apply those same bytes offline.
hooneedsupdates apply --plan /tmp/dependency-plan-with-lockfiles.json --diff .
hooneedsupdates apply --plan /tmp/dependency-plan-with-lockfiles.json --write .
```

`--diff` also works with a fresh `apply` preview. Text files use unified diffs,
including new files and missing final newlines; binary files receive hash
summaries. In diff mode stdout contains only the diff or binary summaries.
External Git tools may perform checkout line-ending conversion; saved-plan apply
always writes the reviewed bytes.

A saved plan defines its configuration decision and lockfile output. Combining
`apply --plan` with `--config` or `--lockfiles` is rejected. The fresh
`apply --diff --write` path also previews first and writes the captured output;
it does not regenerate after showing the diff.

## Validation and trust

- Plans use schema version 1; their reports use schema version 2. JSON schemas
  are published in `schemas/`. Unknown fields and trailing data are rejected.
- The complete artifact is limited to 128 MiB; each output file to 64 MiB.
- Saved paths must be canonical, repository relative, unique, and contained in
  the selected root. Symlink components and non-regular targets are rejected.
- Any unresolved, blocked, or unsupported selected input refuses an apply or
  saved-plan creation. An explicit ignore with a reason can exclude a known
  unsupported input. Inspect the remaining inventory before doing so.
- Existing files must retain their full content hash and mode. A new target must
  remain absent. All targets are rechecked before source writes.
- Saving uses exclusive creation and mode `0600` where Unix permissions are
  supported. An existing artifact is never overwritten.

The checksum detects corruption; it is not a signature or proof of authorship.
Treat a plan as a trusted, reviewed artifact. Anyone who can rewrite a plan and
recompute its checksum can change its output. Keep it in the same protected
review and artifact channel as the proposed commit. A plan may contain sensitive
manifest or lockfile contents; avoid publishing private artifacts.

## Stale plans and recovery

If a file changes after review, apply refuses the whole plan. Generate a new
artifact at a new path and review it again. A plan cannot be reused after a
successful apply, relocated to another checkout, or made fresh by editing its
source hashes. Root aliases are deliberately not portable saved-plan identities.

Ordinary write errors trigger rollback of earlier writes. If rollback also
fails, the error names both the write and rollback failures: inspect the affected
files and `git diff`, restore the reviewed source state from version control, and
create a new plan. Multi-file updates are not a crash-atomic database transaction;
a process crash or power loss can leave partially written files.

Unix writes flush file contents and directory metadata. Windows flushes file
contents before replacement; directory metadata persistence across power loss is
not guaranteed. Unix permission bits do not model Windows ACLs.

## Migration

Existing version-1 YAML configurations remain valid. `packageRules` is optional.
Report schema 2 adds `currentDigest`, `group`, `publishedAt`, matched `policy` rules, and the `blocked` and
`unsupported` statuses/counts. Consumers should tolerate optional fields and
handle every documented status.

The internal plan-digest domain changed from `hooneedsupdates-plan-v1` to
`hooneedsupdates-plan-v2` so digest pins and group assignments are part of approval.
An older digest is rejected by fresh apply. Rescan rather than reusing a report
from a different binary. Reports deliberately omit edit spans and source values;
they are evidence, while saved plans are the offline apply input.

Regenerate schemas with `go run ./scripts/schema`. CI tests reject model/schema
drift. Runtime semantic checks remain authoritative for policy conflicts,
version ordering, path containment, source binding, and checksum verification.
