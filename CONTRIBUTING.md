# Contributing

## Development

Use Go 1.25.13 or newer. Fork repository, create a focused branch, and run before opening a pull request:

```sh
test -z "$(gofmt -l .)"
go vet ./...
go test -race ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
```

Never commit Zendesk cookies, OAuth/API tokens, customer content, signed attachment URLs, copied browser cURL requests, or local auth files.

## Pull requests

- Explain behavior and security impact.
- Add tests for new behavior and failure modes.
- Preserve read-only defaults and prepare/confirm guards for mutations.
- Update README and changelog for user-visible changes.
- Keep commits free of generated binaries and sensitive fixtures.

Reports involving a vulnerability or exposed credential belong in private vulnerability reporting, not a public issue. See [SECURITY.md](SECURITY.md).
