# Contributing

Open an issue before a large manager, datasource, or apply-behavior change.
Small fixes may go directly to a pull request.

## Development

Requirements: Go 1.25 or newer.

```sh
gofmt -w cmd internal
go vet ./...
go test ./... -race -count=1
go build ./...
```

New extraction or apply behavior needs fixtures for accepted input, rejected or
ignored input, byte-range correctness, and fail-closed behavior. Network
datasources need bounded response handling and deterministic tests.

Commits use Conventional Commits. Pull requests must explain compatibility and
security impact. Maintainers squash-merge pull requests using the Conventional
Commit pull-request title so the protected `main` history remains policy-valid.
Generated lockfile changes must accompany manifest changes.
