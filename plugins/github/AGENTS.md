# Built-in GitHub capability

Inherit `../../AGENTS.md` and `../AGENTS.md`.

- `github.go` owns plugin registration, embedded frontend and credential
  setup; `auth.go` owns device sign-in and repository discovery; `storage.go`
  implements the remote contract. Frontend assets live in `frontend/`.
- Support both owner device login and unattended token setup. Credentials stay
  in private runtime files/helpers, never in responses, command arguments,
  model environments, logs or fixtures. Report only masked authentication state.
- Use the host `storage.Executor` for subprocesses. Folio owns local checkout
  validation, commits, rollback and auditing; this plugin owns remote identity,
  authentication, sync and verified publication.
- Keep routes/assets inside the host plugin namespaces. Preserve the core
  manual repository form and English/Chinese plugin UI with safe text rendering.
- While mounted, this plugin owns the repository UX: one sign-in entry in the
  repository card, the device code, then a repository dropdown wired to the host
  connect action (`window.folio.connectRepository`). The GitHub card is the
  sign-in detail surface and stays hidden while signed out. Hide the host's
  manual form here; it exists for deployments without the plugin.
- Let `folio:session` tell the plugin when the host session is gained or lost;
  drop the picker on the signed-out signal.
- Storage, authentication or plugin UI contract changes require updating this
  file, host/caller tests and `../../docs/integration-plugins.md` or deployment
  documentation as applicable. Do not put GitHub-specific state into the worker.

From the repository root run `go test -race ./plugins/github ./internal/web`
and `node --check plugins/github/frontend/plugin.js` when UI changes, then
the root Go checks. Use fake GitHub endpoints and temporary credential files.
