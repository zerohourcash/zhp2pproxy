# Contributing

## Development

Use the Go version declared in `go.mod`. Keep runtime state outside the source
tree and never use real RPC credentials in tests or examples.

Before submitting a change, run:

```bash
./scripts/ci.sh
```

Changes to RPC allowlists, update verification, authentication, node tunnels,
filesystem access or installers require tests covering both allowed and denied
behavior. Pull requests should explain the threat model impact.

## Commits and releases

- Keep each commit reviewable and do not commit generated binaries.
- Update documentation when configuration or network behavior changes.
- Releases are built only from a clean tagged commit.
- Public artifacts require signed metadata and signed Windows executables.
- Never bypass release safety checks for a public build.

By contributing, you agree that your contribution is licensed under
Apache-2.0.
