# CLI and service composition

Applies to `cmd/`; inherit `../AGENTS.md`.

- `folio/main.go` owns command dispatch, environment/flag defaults, built-in
  plugin wiring, HTTP server lifecycle, health checks, scheduler and dispatcher
  startup. Context commands delegate to `internal/context.Run`.
- `folio/weixin.go` owns Weixin command wiring: operator-only `weixin login|status`,
  terminal QR display, the tri-state `WEIXIN_ENABLED` switch (`unset` = managed
  in the console, `true` = pinned on, `false` = kill switch), the optional
  `WEIXIN_OWNER_ID` pin, and the one process-wide supervisor. That supervisor is
  the single owner of the channel directory: console login and the poller share
  its lock and never reopen it; `weixinConsole` adapts it to the Web contract
  and `weixinPusher` adapts `SendText` to the store's outbound notification
  contract. A push reuses the owner's stored reply context; never invent one. Keep QR payloads and scanner identity out of logs; credentials and
  the owner decision live under the external data directory. See
  `../docs/weixin-channel.md`.
- Keep business rules in the owning `internal/` package. Wire capabilities
  through the plugin/storage interfaces rather than importing plugin internals
  into the maintenance worker.
- Preserve graceful cancellation, HTTP timeouts, embedded timezone data, and
  bounded configuration. Ordinary failures must not print subprocess payloads
  or secrets. The first-start password notice is a deliberate exception:
  generate/announce once, reuse silently, and keep its tests and deployment
  documentation aligned.
- Context CLI `agent --auto` accepts `--maintenance-state` only in automatic
  mode and only within the external runtime. The service owns checkpoint writes;
  CLI workers emit validated structural proposals and optional review findings.
- Startup wires the main-agent runtime from validated private configuration and
  starts a background, non-blocking price-catalog refresh. Agent configuration
  failures must not prevent the service from starting; the Assistant fails
  closed. Changes to flags, environment variables, startup or tool paths require
  corresponding updates to `../deploy/README.md` and configuration examples.
  Synchronize this file whenever these responsibilities or checks change.

Validate with `go test -race ./cmd/folio` from the repository root, then the
root Go checks. `dockerfile_test.go` guards build-context coverage; source or
packaging changes also follow `../deploy/AGENTS.md`. `ui_test.go` guards the
shell contract: every id `web/app.js` wires must exist in `web/index.html`, and
plugins load on both session paths.
