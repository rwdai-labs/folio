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
- Keep routes/assets inside the host plugin namespaces. Preserve the
  English/Chinese plugin UI with safe text rendering.
- Name GitHub explicitly in sign-in/sign-out status and controls. The repository
  slot must explain that missing or pending GitHub authorization prevents sync
  and organization, even when a repository was previously selected. Deployment
  token status describes configuration, not verified remote access.
- The host mounts the GitHub card and repository slot inside Settings, also
  used for first-time setup. Hidden parent navigation does not end a Folio
  session; use the existing session events for auth lifecycle.
- While mounted, this plugin owns the repository UX: one sign-in entry in the
  repository card, the device code, then a searchable repository picker wired to
  the host connect action (`window.folio.connectRepository`). The picker filters
  the whole loaded list locally (contiguous and scattered matches, keyboard
  selection) and marks private repositories, because an account can have far
  more repositories than a select box can show. `/repositories` returns every
  page up to a bounded cap with `count`, `private` and `truncated`, so the list
  is never silently shortened. The GitHub card is the sign-in detail surface and
  stays hidden while signed out. The shell has no repository form; this card and
  the picker are the whole connect UI.
- A deployment token (`GIT_AUTH_TOKEN`) fixes the account and its repository
  scope. In that mode the card explains the limit instead of offering a sign-in
  button that cannot change the credential; the picker repeats the scope note.
- Let `folio:session` tell the plugin when the host session is gained or lost;
  drop the picker on the signed-out signal.
- Storage, authentication or plugin UI contract changes require updating this
  file, host/caller tests and `../../docs/integration-plugins.md` or deployment
  documentation as applicable. Do not put GitHub-specific state into the worker.

From the repository root run `go test -race ./plugins/github ./internal/web`
and `node --check plugins/github/frontend/plugin.js` when UI changes, then
the root Go checks. Use fake GitHub endpoints and temporary credential files.
