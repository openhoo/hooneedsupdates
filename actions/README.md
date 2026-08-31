# GitHub Action

`openhoo/hooneedsupdates/actions/setup` downloads a release archive, verifies it
against the release `SHA256SUMS`, adds the binary to `PATH`, and exposes its path.

Pin the action itself to the release commit SHA and pass the matching release
version. See the root README for a workflow example.
