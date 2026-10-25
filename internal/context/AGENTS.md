# Context router and model worker

Inherit `../../AGENTS.md` and `../AGENTS.md`.

- `router.go` selects bounded context from an explicitly resolved checkout;
  `commands.go` implements the context CLI; `worker.go` runs model maintenance;
  `report.go` supports read-only report calls. Keep these roles distinct from
  store-owned Git transactions and HTTP handling.
- `maintenance.go` selects complete changed snapshots and related ancestors,
  tracks bounded backlog and review findings, and validates structural edits.
  Preserve every distinct textual line and exact frontmatter; only direct
  children with an explicit matching claim type may supply extractive summary
  prose. Candidates never authorize
  canonical writes. Oversized inputs are findings, never truncated replacements.
  Routing recognizes BOM/CRLF frontmatter while raw replacements preserve its
  original bytes. Packet bounds include fingerprints and findings.
  `agent --auto --maintenance-state <runtime-file>` consumes service-owned
  fingerprints; `--force` ignores them for that invocation. The worker never
  advances the successful-apply checkpoint itself.
- Task/device/model strings cannot choose a different repository root. Preserve
  traversal, symlink, sensitive-path/content, and size checks. Runtime reports
  and worker state must resolve outside the context checkout.
- Treat model responses as proposals or bounded edits for later validation;
  the model must not directly commit, push, or execute repository scripts.
  Preserve frontmatter and provenance checks and the distinction between an
  owner fact and an inference/candidate requiring review.
- Codex and DSH integrations must keep credentials out of prompts, reports and
  child environments not intended for the runner. Forward only validated,
  credential-free HTTP(S) proxy settings to the model runner so isolated
  containers can reach their configured provider. Read DSH provider routing
  through `internal/dshconfig`; use temporary overlays, not host-profile edits.
  Runner failures may expose only a fixed diagnostic class, never raw stderr or
  model output. Codex prompts travel over stdin, never command-line arguments.
  Preserve the bounded configured model/provider routing even when no model is
  explicitly selected; a selected model overrides only the model. Provider
  credential variable names cannot select proxy settings.
- Usage comes from actual runner events. A missing usage record stays missing;
  preserve cached/input/output semantics across the worker and store ledger.
- Changes to CLI output, packet/worker/report schemas or runner flags require
  updating callers in `cmd/folio` and `internal/store`, relevant tests and docs,
  and these instructions in the same change.

Run `go test -race ./internal/context` from the repository root, adding
`./internal/dshconfig` for profile changes, then the root Go checks. Use fake
runner executables and synthetic repositories; do not make paid model calls
or load personal context just to run tests.
