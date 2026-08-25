# Contributing

Thank you for helping improve api-mcp.

## Before opening a change

Use a GitHub issue for substantial behavior or public-contract changes. Security reports belong in a private GitHub Security Advisory, as described in [SECURITY.md](SECURITY.md).

Keep changes narrowly scoped. Preserve credential redaction, deny-by-default policy enforcement, profile isolation, and the rule that credentials never become generated tool inputs.

## Development

api-mcp requires Go 1.27 or newer.

```bash
go test ./...
go vet ./...
test -z "$(gofmt -l .)"
```

Add focused tests for behavior changes and for expected failures. Tests must not use live credentials or production APIs.

## Pull requests

Describe the user-visible change, security implications, and verification performed. By submitting a contribution, you agree that it is licensed under the repository's MIT License and that you have the right to provide it under those terms.
