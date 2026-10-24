# External GitHub issue bridge

Inherit `../../AGENTS.md` and `../AGENTS.md`; read `README.md` before changes.

- This is an independent Go module (`folio-bridge`), not a service package.
  Do not import Folio internals or share its data directory or Git credential.
- The bridge owns GitHub polling, its read-only issue token, state and retry
  policy. Its only Folio write contract is the signed hook endpoint documented
  in `../../docs/integration-plugins.md`; it does not query tasks or runs.
- First successful poll per repository records a baseline without replaying
  history. Stable event identities make retries idempotent. Respect rate-limit
  reset times and distinguish permanent rejection from transient failures.
- A successful hook response means durable acceptance, not execution success.
  Keep credentials out of logs, fixtures, command lines and build artifacts.
- Configuration/flags, event identities, state or retry changes require updating
  this file and `README.md`; protocol changes also update service/client
  instructions and the integration contract in the same change.

Run separately inside this directory (root `go test ./...` does not include it):

```sh
go test -race ./...
go vet ./...
go build -o /tmp/folio-github-bridge .
```

Use fake GitHub/Folio servers and temporary state for validation. Write build
outputs outside the source tree; never run a real issue poll to test changes.
