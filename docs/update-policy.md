# Groups and compatibility policy

`packageRules` adds opt-in policy to the version-1 configuration. All matching
rules apply cumulatively. Dependency expressions are Go regular expressions;
`managers` restricts their scope. Defaults retain the global
`includePrereleases` behavior.

```yaml
version: 1
packageRules:
  - dependency: '^openhoo/hooversion$'
    managers: [github-actions, custom]
    group: hooversion
    sharedVersion: true
    channel: stable
    minimumAge: 48h
    minVersion: '1.0.0'
    maxVersion: '1.99.99'
  - dependency: '^@example/'
    managers: [npm]
    group: frontend
    minimumAge: 72h
```

| Field | Meaning |
| --- | --- |
| `dependency` | Required dependency-name regular expression. |
| `managers` | Optional manager list; empty matches all managers. |
| `group` | Named family that must remain complete during selection and apply. |
| `sharedVersion` | All family members must resolve to the same normalized version; requires a group. |
| `minimumAge` | Minimum target publication age as a Go duration, from `0s` to `8760h`. |
| `channel` | `stable` excludes prereleases; `prerelease` allows them. Overrides the global flag for matching entries. |
| `minVersion`, `maxVersion` | Inclusive semantic version bounds; optional `v` prefix. |

The scanner first resolves the newest target for the selected channel. If that
target is outside a version window or is too young, it reports `blocked`; it does
not silently select an older catalog version. A compatibility window constrains
version numbers, not API or source compatibility. Run the target repository's
checks before merging.

For Docker, the tag suffix (for example `-alpine`) remains the image variant.
That suffix is retained even under `channel: stable`; Docker variant suffixes are
not treated as ordinary semantic-version prerelease channels.

Conflicting matching groups or channels block the dependency. Every matching
age and version-window rule must pass. Future timestamps, absent timestamps, or
metadata retrieval failures cannot satisfy a positive minimum age.

## Publication evidence

Age checks request additional registry metadata only when needed:

| Datasource | Evidence |
| --- | --- |
| Go | Selected version's proxy `.info` `Time`. |
| crates.io | Selected non-yanked version's `created_at`. |
| npm | Packument `time[version]`; stable targets still respect the registry's `latest` tag. |
| GitHub | Selected release's `published_at`; tag-only repositories cannot supply release publication evidence. |
| Docker Hub | Selected tag's `last_updated`, representing the pinned tag output. |
| NuGet flat container | No supported publication timestamp; positive age rules block updates. |

Metadata fetch failures report `unresolved`; absent or insufficient evidence
reports `blocked`. Both refuse source writes and auto-merge for the selected
plan. Registry timestamps are upstream claims, not signed attestations.

## Family closure

A group containing an unresolved, blocked, unsupported, or ignored member blocks
its remaining actionable members. With `sharedVersion`, all resolved targets
must agree. Selection may include an entire group or exclude an entire group;
selecting only some members retains the whole family as blocked evidence.
Current members remain available for group checks even with an update-type
filter. Blocked and unsupported inputs cannot disappear behind type selection.

Use the same group for an OpenHoo action SHA, literal `with.version`, and a custom
version variable. This repository configures a shared-version family for each
managed OpenHoo tool. Grouping is opt-in in other repositories; fresh reports
record the resulting group, publication evidence, and every matched rule in
`policy`. Plan digests bind those rule decisions, and saved plans retain that
evidence together with the reviewed output.

## Container inventory

Dockerfile scanning recognizes literal version tags and
`image:tag@sha256:<64 hex>` pins, preserving platform selectors, aliases, stage
references, and comments. Tag and digest updates occupy one edit span. A changed
digest on the same tag is actionable; a downgrade is rejected.

Public Docker Hub images and its explicit hostname aliases are supported. The
resolver traverses the bounded Hub tag catalog; when Hub's anonymous REST offset
limit prevents a complete catalog, it uses the public OCI registry tag endpoint
with an anonymous pull-scoped token. Pagination stays within its origin and
repository. The selected Hub tag supplies the image index digest, preserving
multi-platform image selection.

Digest-only images, non-version tags, variable references, malformed pins, and
private or other registries remain visible as `unsupported`. `scratch` and prior
build-stage references are not external dependencies. Unsupported inventory
blocks source writes unless deliberately ignored with a reason. Discovery still
covers the supported file and dependency forms, not every possible build input.
