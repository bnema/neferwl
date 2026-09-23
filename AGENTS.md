# Repository rules

- Use Go 1.27 only; normal builds and tests use CGO_ENABLED=0.
- Keep a lightweight hexagonal architecture: core imports only stdlib and ports.
- Owner goroutines own window state; no mutex on window state.
- Generate mocks only via Mockery v3 from ports; never handwrite mocks.
- Use zerowrap logging with a `component` field on every entry.
- Run tests with `-race`.
- Use signed conventional commits: `type(scope): description`.
