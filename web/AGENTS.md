# Browser console

Inherit `../AGENTS.md`.

- The UI is dependency-free static HTML/CSS and classic JavaScript, served by
  the Go binary. `index.html` is structure, `styles.css` presentation, `app.js`
  transport/state/rendering and hash navigation, and `i18n.js` the English/Chinese
  translation API. `browser-check.cjs` is a synthetic Chromium acceptance check.
  Do not add a frontend build pipeline for routine UI work.
- Preserve same-origin requests, CSRF headers for mutations, session-expiry
  cleanup, bounded polling, and busy/error/empty states. Show task, run,
  deferred and paused states according to backend data.
- Name Folio explicitly in session/login/logout labels. Repository selection
  and storage authorization are separate: a saved checkout with missing
  credentials needs authorization, and must not show a green connected badge.
  A configured deployment token does not prove live repository access.
- Daily navigation is dashboard (tasks and usage), run history, owner review,
  and settings. With no ready repository/authorization/installed runner, default
  to settings; configured sessions default to the dashboard. Setup state is
  derived from server data, not a persistent browser completion flag. Keep the
  GitHub plugin mounts, repository picker, notifications and hooks in settings;
  optional integrations never block the dashboard. Runner availability is not
  provider credential verification.
- The dashboard board groups queued/running/failed/finished records into four
  columns, with a separately labelled human-review snapshot in Needs attention.
  Preserve backend priorities and wait/pause semantics; no drag-to-complete or
  fabricated approvals. Include unmatched recent direct runs. Live task attempts
  correlate through `run.task_id`; a retry's old `task.run_id` is historical until
  a current run is identified. Search and priority filters are local view state.
  Card inspection uses a native dialog with safe run evidence; keep board scroll
  and keyboard focus on refresh and close, and close private dialogs on logout.
- Owner review uses `/api/automation.review_run`, the last retained successful
  maintenance snapshot, not the selected historical run or a live approval queue.
  Explain the snapshot timestamp and the external repository review workflow.
  Recheck runs maintenance (which already pulls) with the dashboard runner/model;
  show token implications, busy/failure states and preserve prior findings on
  failure. Do not invent an approve/dismiss action or promote candidates.
  Fixed GitHub input links require `run.repository.plugin/full_name` and a valid
  `run.pull.head`; current-file links are explicitly labelled and must not point
  at a different known repository. Old records without identity cannot claim a
  historical source link. Validate and encode paths, use noopener/noreferrer.
- Run details show optional `needs_review` findings and `remaining` backlog.
  Render source/reason as text, translate known finding codes in both languages,
  retain unknown model explanations, and tolerate older runs without these fields.
  Distinguish `continuation_queued` automatic backlog from human review; never
  imply that remaining documents require approval or will start immediately.
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

For the reproducible browser check, install a temporary driver outside the repo
and a Chromium browser (or use an existing one):

```sh
npm install --prefix /tmp/folio-ui-browser --no-audit --no-fund playwright-core
NODE_PATH=/tmp/folio-ui-browser/node_modules FOLIO_BROWSER_EXECUTABLE=/path/to/chromium node web/browser-check.cjs
```

The check serves only allowlisted static assets on loopback and intercepts API
calls with synthetic data; it performs no real GitHub authorization, model call,
repository write, or delivery. It covers fresh/restored sessions, pending auth,
keyboard repository selection, default routes, optional settings, board grouping,
search/priority filters, attempt drawer and keyboard return, pause/resume,
review snapshots and safe links, recheck success/failure, usage errors/recovery,
both languages, mobile overflow, state-load failure and session expiry. Set
`FOLIO_SCREENSHOTS=1` to save synthetic screenshots under `/tmp`. Actual deployed
provider authorization and external manual review still need runtime acceptance.
