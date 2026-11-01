# Native iLink channel

Inherit `../../../AGENTS.md`, `../../AGENTS.md` and `../AGENTS.md`.

- `client.go` owns bounded HTTPS JSON, protocol headers, QR and Bot API calls.
  Allow only the official iLink host or its subdomains; disable HTTP redirects.
  Never surface response bodies, URLs, credentials or raw network errors.
- `login.go` owns interactive QR/verification state, not application authority.
  CLI display is local interactive output, never a service log. Login and the
  poller share an exclusive process lock; stop the poller before replacing login.
- `session.go` is the only presentation of that state to a browser. It renders
  the QR to a PNG and exposes a `data:image/png;base64,` string through `Map`;
  the raw payload, token and transport error never enter a serialized value.
  Never return a `Snapshot` struct: the Web redactor recurses only into maps and
  would flatten a struct, including the image, into one string.
- `settings.go` owns the owner decision beside the credentials: `credentials.json`,
  `pending_owner_id` from the last scan, and the persisted `enabled` flag, all
  `0600` through the same rejecting reader. Enabling without a confirmed owner
  is invalid state, not a normal combination.
- `supervisor.go` owns the process lifetime. One `Disk` handle serves both login
  and polling, so the flock never conflicts with itself; operator mutations are
  serialized by `control` before `mu`. Confirmation compares the submitted
  identity with the session scanner and the persisted pending value in constant
  time. Deployment `ModeOff` refuses every mutation; `ModeOn` fails startup
  without credentials and a pinned owner.
- `state.go` owns 0700 directories, 0600 regular files, atomic synced writes,
  the account-bound cursor, pending replies and a bounded deduplication window;
  `settings.json` uses the same reader and writer.
  Refuse state paths inside Git worktrees. Corrupt state fails closed; never
  silently reset the cursor.
- `service.go` stages inbox plus cursor before invoking the handler, shows the
  best-effort typing hint while an answer is produced, and stages replies before
  sending with stable client IDs. Only direct completed text messages are
  supported. Authorization belongs to the Folio handler. No media fetches,
  arbitrary endpoints, model sessions or tools belong here.
- `diagnostics.go` maps a handler error to one fixed failure class and keeps the
  last success/failure in `diagnostics.json`. Never store message text, peer
  identity or runner stderr there; the supervisor exposes the same bounded map.
- `contacts.go` remembers the reply context token of a peer that actually
  received a reply, bounded to the newest eight, so `Supervisor.SendText` can
  push to the confirmed owner. A sender that was refused never enters the table.
  A push outside that context fails with `ErrNoContext`; it is not retried.
- Preserve cancellation, bounded retry and fail-closed expired-token behavior.
  Tests use synthetic accounts and local fake HTTP servers, never live login.

See `../../../docs/weixin-channel.md` (from repository root:
`docs/weixin-channel.md`) for protocol evidence, operator flow and delivery limits.
Run `go test -race ./internal/channel/...` and the root Go checks.
