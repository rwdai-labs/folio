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
- Name Folio explicitly in session/login/logout labels. The login form has a
  username and password; on first run the setup form creates the single owner
  account and is only expected to work from a local/private address. There is
  no printed setup code. Repository selection and storage authorization are
  separate: a saved checkout with missing credentials needs authorization, and
  must not show a green connected badge.
  A configured deployment token does not prove live repository access.
- Daily navigation is dashboard (compact current work, quick actions and usage),
  assistant chat, activity (Tasks / Runs / Review tabs), and settings. Hash aliases
  `#runs` and `#review` open the matching Activity tab and `#activity` opens Tasks.
  The raw action registry is reachable only from Settings' Advanced tab
  (`#actions`), not the sidebar. At 720px and below the sidebar becomes a fixed
  four-item bottom tab bar. Feedback is a fixed launcher
  and movable floating window available alongside those views. `/api/state.capabilities` independently
  describes `assistant`, `report`, `maintenance`, and `context` with `available`,
  fixed `reason`, and `verification`. Availability means configured readiness,
  never live provider acceptance. Any usable daily capability defaults to the
  dashboard; otherwise default to settings. Legacy responses fall back to saved
  agent/model bindings and repository/runner metadata. Setup state is derived
  from server data, not a persistent browser completion flag. Keep the GitHub
  plugin mounts, repository picker, notifications and hooks in settings; optional
  integrations never block the dashboard. Runner availability is not provider credential verification.
- The dashboard action card is driven by `GET /api/catalog` and grouped by
  `presentation.group` (or `group`, default `actions`), preserving catalog order.
  Render compact quick-action launchers (server label, description, priority or
  requires hint) without inline forms; one launcher opens the shared native
  `<dialog id="catalog-dialog">`, which contains that action's form. Reuse
  `catalogActionCard`/`catalogFieldNode` in the dialog; keep Escape, backdrop and
  Close safe, return focus to the opening launcher, and close the dialog on
  logout. Render server `label`/`description`/field metadata as safe text; when present,
  resolve `label_key`, `description_key` and `submit_label_key` with
  `translateKnown`. Support text, textarea, select and boolean fields, including
  required, max_length, options and placeholder; omit an optional empty select
  from the request. Submit `{fields}` to `POST /api/actions/{id}/run`. A returned
  `task` is accepted into the queue, not completed: reuse `chatTaskCard`, refresh
  tasks and retain pause/window/attempt semantics. An action is disabled when
  `enabled` is false or any `requires` capability (assistant, report,
  maintenance, context, workshop) is unavailable, with a generic capability
  reason only. If the catalog is missing or returns 404, show one generic hint and
  never resurrect hardcoded report/maintenance cards. No action-specific IDs or
  labels belong in the shell.
- Feedback is a screenshot-first movable floating window available from every
  authenticated view, opened by the fixed launcher and closed without discarding
  the draft. Drag only by the header, keep the window inside the viewport on
  pointer/resize, and keep Escape/Close focus behaviour. The compact panel has a
  prominent dropzone with the primary `截取当前页面` action plus paste/drop/choose
  file, a preview/annotation canvas, only two visible textareas (`发生了什么？`
  and `你希望怎样？`), and a collapsed `更多` disclosure for title, priority and
  acceptance. The area is derived from the active view, never chosen separately.
  Keep submit sticky/visible without scrolling; on mobile the window is a
  full-width sheet. There is no separate element-selection mode.
- Current-page capture uses `navigator.mediaDevices.getDisplayMedia` with
  `preferCurrentTab: true` (the browser may still show a picker), hides the
  feedback panel and launcher, draws one frame at its native resolution (bounded
  to the existing 4096px/8MP image limits), stops all tracks, restores the
  panel, and feeds a PNG through the existing FileReader/canvas pipeline. Never
  use `blob:` or object URLs; CSP permits only `self` and `data:`. If capture is
  unsupported or denied, show a translated non-blocking error and keep
  paste/drop/file selection available. No external libraries or build step.
- Immediately before a page capture, collect at most 24 visible semantic
  elements outside the feedback UI (buttons, links, non-password inputs,
  textareas, selects, headings, cards/sections) with tag, id, role, accessible
  name, clamped visible text, stable selector (`id`, `data-testid`,
  `data-action`, `data-view`, then a bounded CSS path), nearest section/card
  heading and non-negative viewport-relative rect. Never collect input values,
  passwords, raw HTML or hidden elements. POST multipart FormData with `title`,
  `area`, `current`, `expected`, `acceptance`, `priority`, an optional PNG/JPEG
  `image`, and `context` when an image is present. `context` is a JSON string
  shaped as `{view,route,theme,language,viewport:{width,height,dpr},elements:[...]}`,
  where `view` is a feedback area, bounded by 24 KiB. Uploaded/pasted files send
  only the base view/route/theme/language/viewport fields with no elements.
  Show an expandable `已附带 N 个页面元素 / 当前视图：...` evidence summary after
  capture; it is context only and never becomes a selection UI or drawn boxes.
  Clear context when the image is removed or replaced and on logout/expiry.
- On submit derive a title from the first context element or the current text
  (at least 4 characters), derive the area from the active view, derive
  acceptance when empty, and default priority to P2. All remain editable under
  `更多`. Show an accepted task card or the rejection reason without clearing
  drafted fields (including derived values); translate known
  `feedback.reason.<code>` values and show an unknown code as text. Validate
  required fields and the 8 MiB image limit client-side. Clear images, canvas,
  fields and context on logout or session expiry and never persist screenshots
  in browser storage.
- Structured `task.created` SSE events carry `task_id`, `title`, and `status`;
  session history's top-level `tasks` restores durable task cards. Cards open
  the existing task dialog. Results use authenticated
  `/api/automation/runs/{runID}/result` (`run_id`, `format`, `text`) and follow-up
  chat names the stable run ID for `report.get`. Always fetch the result artifact;
  audit `report_text` is only a labelled fallback preview on result failure.
  Label `historical_snapshot`/`truncated` results as partial historical evidence.
  Use session epochs for asynchronous history/results so logout and a later login
  cannot restore old responses. Render report Markdown as readable safe text;
  never infer completion from task acceptance.
- The assistant view is titled Assistant in navigation, heading and chat labels
  while the API/route stays `chat`. It renders only server text/tool events as
  text nodes. Keep the SSE reader bounded, show unavailable/error states, and
  never put raw model output or tool arguments into the page as HTML. The
  composer starts at two rows, grows to about eight rows/160px and keeps Send
  inline; the empty state offers three translated prompt chips that fill and
  focus the composer without sending, and it hides once the conversation has
  content. Settings is grouped into Connections, Notifications, Triggers and
  Advanced tabs; keep the readiness summary and setup steps visible, show only the
  active group's cards, and make section deep links select and focus their tab.
  The Advanced tab includes the
  main-agent runtime form: OpenAI-compatible or command mode, provider preset,
  base URL, model, write-only API key and timeout. Never prefill the key from a
  response; show key presence only. The actions view edits one owner action as
  JSON; validate/publish/disable/restore and the HTTP host allowlist are explicit
  controls, and version names are rendered as text. Do not add a second agent or
  a background poller for these views.
- Task/run details show reported tokens and, when available, an estimated cost;
  the usage dashboard adds a cost tile and column. Label cost as an estimate,
  keep unknown prices visibly unavailable, and never derive token counts from
  cost. The catalog is an automatic background input, not a settings card;
  render cost errors as translated text, not raw upstream bodies.
- The dashboard keeps a compact current-work card (counts and at most three
  queued/running or failed rows linking to Activity) plus a work console with a
  compact status bar (tokens, estimated cost, subscription allowance and
  selectable 7/30/90-day window) above detailed usage and quota readings.
  Activity Tasks groups queued/running records as current work, failed records as
  Needs attention, and done/canceled records as recently finished, with a
  separately labelled human-review snapshot in Needs attention.
  Preserve backend priorities and wait/pause semantics; no drag-to-complete or
  fabricated approvals. Include unmatched recent direct runs. Live task attempts
  correlate through `run.task_id`; a retry's old `task.run_id` is historical until
  a current run is identified. Search and priority filters are local view state.
  Card inspection uses a native dialog with safe run evidence; keep board scroll
  and keyboard focus on refresh and close, and close private dialogs on logout.
- Activity Review uses `/api/automation.review_run`, the last retained successful
  maintenance snapshot, not the selected historical run or a live approval queue.
  Explain the snapshot timestamp and the external repository review workflow.
  Recheck runs maintenance (which already pulls) using the maintenance task binding;
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
- The WeChat card binds through the console. Render only a `data:image/png`
  source, poll `/api/weixin/login` with a bounded timer until a terminal state,
  require the owner to confirm the displayed scanner identity before enabling,
  and offer an enable/disable toggle only when an owner exists. Explain the
  deployment kill switch and a pinned owner; keep the QR out of storage, URLs
  and logs; stop polling on logout and session expiry. Show the last success time
  and the last fixed failure class from `diagnostics` so a broken runner is
  visible, and render the optional push switch plus test button from
  `state.channels.weixin`; refresh the card when the channel payload arrives
  later than the status payload.
- The subscription-quota panel is read-only. Render magpie's card/window text
  as text nodes, show `text`/`unlimited` windows without a fake percentage,
  keep source errors and stale cards visible, and never put a quota key or URL
  in the page, storage or logs.
- Settings model connections use `/api/model-services` GET/POST and `/api/model-services/test` POST. Keep connection IDs/names/base URLs and task bindings (`assistant`, `report`, `maintenance`) generic; task values are `connection/model`, report overrides retain `overrides.report[action_id]`. API keys are write-only and blank preserves a saved secret. A test may report endpoint reachability and model IDs without claiming inference acceptance; local-network HTTP is valid when the host can reach it.
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
keyboard repository selection, default routes, optional settings, four-item
navigation, Activity/Settings tabs and keyboard roving, hash aliases and back
navigation, board grouping, search/priority filters, dashboard current card,
bounded run history with status filters, attempt drawer and keyboard return,
pause/resume, compact review help and snapshots with safe links, recheck
success/failure, settings deep links, subscription-quota
cards and refresh, usage errors/recovery, both languages, mobile bottom navigation,
launcher placement and overflow,
state-load failure, capability-specific readiness without repository/Codex,
quick-action launcher/dialog submit, field validation, disabled requirements,
close focus and 404 fallback; assistant title, growing composer and prompt
suggestions; structured task cards and chat, run-ID report results and
follow-up, screenshot-first
feedback with no element-selection mode, compact two-field/collapsed-advanced
layout and sticky submit, draggable header and draft-preserving close, denied
capture fallback, current-page capture with element context, uploaded-file
context without elements, image/canvas export, accepted and rejected submissions
with preserved drafts, mobile sheet layout, and session expiry cleanup. The mock
multipart parser exposes the parsed `context` JSON. Set
`FOLIO_SCREENSHOTS=1` to save synthetic screenshots under `/tmp`. Actual deployed
provider authorization and external manual review still need runtime acceptance.
