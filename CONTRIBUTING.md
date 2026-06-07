# Contributing

This is an unofficial client for Cambly's private web API. Keep changes small,
well-tested, and careful around authentication material.

## Development

```sh
go test ./...
go build -o bin/cambly ./cmd/cambly
```

Do not commit live cookies, sessions, capture logs, HAR files, or local
credentials. The `recon/` capture output is intentionally ignored because it can
contain auth tokens and personal account data.

## API Changes

If Cambly changes an endpoint, update the relevant client method, CLI command,
and `API.md` notes together. Prefer documenting only the fields this project
actually uses.
