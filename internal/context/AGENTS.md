# Context router and model worker

Inherit `../../AGENTS.md` and `../AGENTS.md`.

- `router.go` selects bounded context from an explicitly resolved checkout;
  `commands.go` implements the context CLI; `worker.go` runs model maintenance;
  `report.go` supports read-only report calls. Keep these roles distinct from
  store-owned Git transactions and HTTP handling.
- Task/device/model strings cannot choose a different repository root. Preserve
  traversal, symlink, sensitive-path/content, and size checks. Runtime reports
  and worker state must resolve outside the context checkout.
- Treat model responses as proposals or bounded edits for later validation;
  the model must not directly commit, push, or execute repository scripts.
  Preserve frontmatter and provenance checks and the distinction between an
  owner fact and an inference/candidate requiring review.
- Codex and DSH integrations must keep credentials out of prompts, reports and
  child environments not intended for the runner. Read DSH provider routing
  through `internal/dshconfig`; use temporary overlays, not host-profile edits.
- Usage comes from actual runner events. A missing usage record stays missing;
  preserve cached/input/output semantics across the worker and store ledger.
- Changes to CLI output, packet/worker/report schemas or runner flags require
  updating callers in `cmd/folio` and `internal/store`, relevant tests and docs,
  and these instructions in the same change.

Run `go test -race ./internal/context` from the repository root, adding
`./internal/dshconfig` for profile changes, then the root Go checks. Use fake
runner executables and synthetic repositories; do not make paid model calls
or load personal context just to run tests.
