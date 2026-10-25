# HTTP API and plugin host

Inherit `../../AGENTS.md` and `../AGENTS.md`.

- `web.go` owns HTTP routing, authentication, sessions, CSRF/host/origin checks,
  request/response limits and background dispatch integration. Depend on the
  `Backend` contract; repository and model operations remain in store/worker.
- `plugins.go` mounts compiled plugin routes and assets under their plugin ID.
  The host owns session authentication, CSRF, JSON encoding, error mapping,
  redaction, and the safe executor supplied to plugin handlers.
- Keep browser mutations behind session/CSRF checks. Signed inbound hooks are
  a separate authentication path; preserve host checks, raw-body signature
  semantics, body bounds and rejection before side effects.
- Never expose raw process errors, credential files or arbitrary runtime files.
  Static assets must come only from their configured bounded asset roots.
- API changes require matching `Backend` implementations, fake backends, UI
  consumers, and contract documentation. Task acceptance is not run completion.
  Update these instructions whenever API ownership or security rules change.
- Maintenance run details add `input_files` (original byte fingerprints),
  `remaining` (unprocessed changed document count) and `needs_review`
  (`source`, `reason`). These are private authenticated audit data. Consumers
  must tolerate their absence on old runs and report runs; findings are not
  approvals and a successful structural edit does not resolve a factual conflict.

Run `go test -race ./internal/web` from the repository root and the root Go
checks. Cover affected auth/CSRF/host/origin rejection, limits, redaction and
plugin routes; for changed browser flows also follow `../../web/AGENTS.md`.
