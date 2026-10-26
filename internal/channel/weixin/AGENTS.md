# Native iLink channel

Inherit `../../../AGENTS.md`, `../../AGENTS.md` and `../AGENTS.md`.

- `client.go` owns bounded HTTPS JSON, protocol headers, QR and Bot API calls.
  Allow only the official iLink host or its subdomains; disable HTTP redirects.
  Never surface response bodies, URLs, credentials or raw network errors.
- `login.go` owns interactive QR/verification state, not application authority.
  CLI display is local interactive output, never a service log. Login and the
  poller share an exclusive process lock; stop the poller before replacing login.
- `state.go` owns 0700 directories, 0600 regular files, atomic synced writes,
  account-bound cursor, pending replies and a bounded deduplication window.
  Refuse state paths inside Git worktrees. Corrupt state fails closed; never
  silently reset the cursor.
- `service.go` stages inbox plus cursor before invoking the handler and stages
  replies before sending with stable client IDs. Only direct completed text
  messages are supported. Authorization belongs to the Folio handler. No media
  fetches, arbitrary endpoints, model sessions or tools belong here.
- Preserve cancellation, bounded retry and fail-closed expired-token behavior.
  Tests use synthetic accounts and local fake HTTP servers, never live login.

See `../../../docs/weixin-channel.md` (from repository root:
`docs/weixin-channel.md`) for protocol evidence, operator flow and delivery limits.
Run `go test -race ./internal/channel/...` and the root Go checks.
