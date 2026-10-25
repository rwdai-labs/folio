# Browser console

Inherit `../AGENTS.md`.

- The UI is dependency-free static HTML/CSS and classic JavaScript, served by
  the Go binary. `index.html` is structure, `styles.css` presentation, `app.js`
  transport/state/rendering, and `i18n.js` the English/Chinese translation API.
  Do not add a frontend build pipeline for routine UI work.
- Preserve same-origin requests, CSRF headers for mutations, session-expiry
  cleanup, bounded polling, and busy/error/empty states. Show task, run,
  deferred and paused states according to backend data.
- Run details show optional `needs_review` findings and `remaining` backlog.
  Render source/reason as text, translate known finding codes in both languages,
  retain unknown model explanations, and tolerate older runs without these fields.
- Render repository/model/hook/report/diff text through safe text nodes.
  Never interpret it as HTML. Keep transient passwords and one-time hook
  secrets out of persistent browser storage and logs.
- Update both language dictionaries, labels and accessibility text with new
  copy. Preserve keyboard focus, form labels and model free-form input.
- Wire elements through `on(id, type, handler)`, never `$("id").addEventListener`:
  a renamed id must not abort boot and leave the page half-wired. Every id that
  `app.js` references has to exist in `index.html`; `cmd/folio/ui_test.go`
  enforces both rules.
- A session is entered in two places, boot with a restored cookie and a fresh
  login; both call `enterSession()` so plugins are discovered and told
  `folio:session`. Logout and expiry dispatch the same event with
  `authenticated: false`.
- The plugin host surface is `window.folio.api`, `connectRepository`,
  `refreshState`, `refreshTasks`, `formatTime`, `t`, plus the `folio:session`
  and `folio:languagechange` events. Plugins mount into reserved markup; the
  shell keeps no repository form of its own, only the connect transaction.
- Built-in GitHub UI belongs to `../plugins/github/frontend/`, not core UI.
  Coordinate shared plugin-host changes with its directory instructions.
- When changing API fields, rendering contracts or file responsibilities,
  update this file and `../internal/web/AGENTS.md` as applicable in the same
  change; user-visible behaviour changes also update the relevant README.

From the repository root run `node --check web/app.js` and
`node --check web/i18n.js`. For API changes run `go test -race ./internal/web`.
Check affected flows in a browser with synthetic data: both languages, keyboard
interaction, loading/empty/error/signed-out states, and changed task/run views.
Syntax checks alone do not establish browser acceptance; report what ran.
