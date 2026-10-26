# Native Weixin text channel

Folio implements Tencent's public iLink HTTPS protocol in Go. No OpenClaw
Gateway, Agent Runtime or Node.js sidecar is used for Weixin. The existing
model runner remains Folio's own configured Codex CLI; Weixin does not add
another model runtime.

## Research and integration boundary

Reference snapshot: Tencent/openclaw-weixin **2.4.10**, commit
[`874637a0dbc24050f72d1385add4fc6170a2d726`](https://github.com/Tencent/openclaw-weixin/tree/874637a0dbc24050f72d1385add4fc6170a2d726).

| Upstream area | What Folio uses |
| --- | --- |
| [`docs/protocol_zh_CN.md`](https://github.com/Tencent/openclaw-weixin/blob/874637a0dbc24050f72d1385add4fc6170a2d726/docs/protocol_zh_CN.md), `src/api/api.ts`, `src/api/types.ts` | Reimplemented HTTP requests, headers, base metadata, message envelopes and return-code handling in Go |
| `src/auth/login-qr.ts`, `src/auth/accounts.ts` | QR states, verification, credentials and scanner identity; Folio owns its private file format |
| `src/monitor/monitor.ts`, `src/storage/sync-buf.ts`, `src/messaging/send.ts` | Long polling, `get_updates_buf`, text sending and reply context tokens; Folio adds its own durable inbox/outbox |
| `src/api/config-cache.ts`, `src/messaging/typing.ts` | Native `getconfig`/`sendtyping` primitive; automatic typing/progress callbacks are deferred |
| `src/cdn/`, `src/media/`, `src/messaging/send-media.ts` | Research only; media is deferred |
| `src/channel.ts`, `src/messaging/process-message.ts`, authorization/pairing and OpenClaw SDK imports | Framework adapters replaced by a small Folio handler; no Agent, Memory, Tool or Session framework imported |

The public backend document describes client behavior, **not a complete server
contract**. In particular it does not guarantee client-ID idempotence, stable
identity across account rebindings, cursor retention, all possible regional
hosts or future protocol compatibility. Real-account acceptance remains an
operator check. The Go client announces `bot_agent: Folio/0.1.0`; the wire
compatibility version is `2.4.10`, with application ID `bot`.

The entrypoint wires:

```text
Weixin → native iLink poller → channel.Message → Folio authorization/handler
       ← native text sender ← saved reply    ← read-only Folio result
```

`internal/channel` contains only a message envelope and the application handler
contract. Identity includes channel, bot account, sender, direct conversation,
message ID and optional transport session ID. A future attachment field reserves
media kinds without accepting paths or downloading anything. The transport
keeps `context_token` private; it never sends it to the model handler.

There was no general conversational Folio endpoint to reuse. The MVP's
`internal/store/inbound.go` therefore supports `/help`, `/status` and ordinary
text via the existing read-only `agent --report-prompt` runner. Each question
uses a fresh ephemeral model call, with no shared chat history or model session
resumption. It supplies no private Context automatically, enables no tools or
maintenance writes, and does not create queue tasks, run-ledger records or
notifications. Channel model usage is not currently included in the dashboard.
The existing report runner has a 180-second timeout here. A connected repository
and configured Codex runner are needed for ordinary text; `/help` and `/status`
can establish communication before model setup.

## Operator setup

Login runs in an interactive private terminal. Never redirect its QR output to
service logs, public files or chat. Use the same OS user and external private
data directory as the service. For a local build:

```sh
go build -o /tmp/folio ./cmd/folio
/tmp/folio weixin login --data-dir /path/to/private-folio-data
```

Scan the terminal QR in Weixin and enter the phone's verification digits when
requested. Login lasts at most five minutes, with at most three QR refreshes.
The command saves the bot token and prints the scanner's opaque user ID locally.
After checking the scanner identity, set `WEIXIN_OWNER_ID` to that exact ID in
the service environment and `WEIXIN_ENABLED=true`, then start Folio normally.
The environment example is in `compose.yaml`; container steps are in
[`../deploy/README.md`](../deploy/README.md#weixin-channel).

The owner is **never** inferred from the first inbound message, nickname,
message text or prompt. Login alone does not enable processing or grant owner
access. Missing owner configuration fails startup when the channel is enabled.
Owner identity is matched exactly, scoped to the saved bot account and a direct
conversation. Other senders receive no reply and cannot reach status, model,
repository, maintenance or tool entrypoints. Reauthorization happens before
processing and before delivering a saved reply, including after an owner change.

Send `/status` first, then a short question after the model runner is ready.
`folio weixin status --data-dir ...` checks only local credential presence and
shows scanner identity; it does not validate the token online. Login/status use
the exclusive channel lock, so stop an enabled service first. Disabling Weixin
leaves its private credentials and pending state available for later restart.

## Storage, delivery and failure behavior

All channel files live in `secrets/weixin/` below the external service data
directory, never in the context repository or image:

| File | Contents |
| --- | --- |
| `credentials.json` | Bot token, account ID, scanner ID and validated API base URL |
| `sync.json` | Schema version, account ID, cursor, pending inbound messages/prepared replies and recent message hashes |
| `channel.lock` | Exclusive process lock, released by OS on process exit |

The directory is `0700`; files are `0600`. Credentials are protected by filesystem
permissions, **not encrypted at rest**; encrypt the private volume/backups if
required. Reads reject symlinks, nonregular or publicly accessible files and
oversized/corrupt JSON. Writes use a private temporary file, file sync, atomic
rename and directory sync. Corruption or an account mismatch stops processing
instead of silently resetting the cursor. Account switching requires a separate
data directory. State paths inside Git worktrees are refused. The service image
is Linux; the channel supports Unix flock
platforms and fails closed on other platforms.

1. Poll with the saved `get_updates_buf`. Persist a successful batch's new
   nonempty cursor **together with** its accepted inbox before calling Folio.
2. Authorize and process pending messages sequentially. Save a prepared reply
   before attempting network delivery. Each retry reuses a deterministic
   `client_id` scoped to channel/account/sender/conversation/message.
3. After successful sending, atomically remove the pending item and add its
   hash to the most recent **4096** message IDs. Duplicate entries in a batch
   and replays inside this window do not invoke Folio again.

Only completed (`message_type: 1`, `message_state: 2`) direct text messages with
a stable message ID, sender and reply context token are accepted. String and
integer message IDs are accepted without floating-point conversion. Bot echoes,
group messages, incomplete messages, mixed text/media and unsupported envelopes
are skipped and acknowledged by the cursor. There is no `sync_buf` fallback.
Texts are bounded to 16 KiB; replies over 4000 Unicode characters or 16 KiB get
a short request to narrow the question, rather than an unbounded send.

HTTP responses are capped at 4 MiB, batches at 100 messages, cursor at 256 KiB
and private JSON state at 8 MiB. Pending work is drained before polling again;
a prolonged delivery failure blocks later messages in this single-account MVP.
Server poll timeout hints are accepted only within 1–60 seconds, with a
five-second client margin. Network/ordinary API failures back off from two to
30 seconds; shutdown cancels polling and model subprocess calls. Business code
`-14` or HTTP 401/403 stops the channel for explicit re-login. Unlike the
reference's one-hour pause, it does not repeatedly try a stale credential.
The Web service can remain available when the channel stops; its generic HTTP
health check does not establish channel health. Service logs contain only fixed
failure classes, never QR content, tokens, cursors, peer IDs or message text.

The first successful poll stages pending work durably, but a crash between a
model result and its saved reply can repeat the read-only model call. A crash
after Weixin accepts a reply but before local acknowledgment can resend it.
The same `client_id` is reused, but backend deduplication is not guaranteed by
the public document. This is bounded replay protection and durable retries,
**not exactly-once delivery**. Very old replays beyond the deduplication window
can also be processed again. Credentials/cursor persistence does not imply a
server token never expires or that the backend retains every offline message.

## Deferred capabilities and acceptance

No picture/file/voice/video download or upload, group routing, external-user
persona, interactive Web QR setup or full channel framework is implemented.
Media can later reuse the documented CDN upload URL, AES-128-ECB/PKCS#7 and
encrypted reference flows behind separate bounds and URL validation. No media
requirement found in the reference requires an OpenClaw runtime. Any future
external-user handler must enforce a context allowlist and a separate capability
boundary in Go; a persona prompt alone cannot grant or restrict authority.

Automated tests use synthetic accounts and fake local HTTP services. They cover
QR verification, protocol headers, error redaction, bounded responses, exact IDs,
cursor/inbox checkpoint ordering, retry after restart, cached-reply revocation,
deduplication, account/session isolation, unauthorized access, cancellation and
private-state checks. They do not establish compatibility with a live account.

For operator acceptance, use a private terminal to log in, test `/status` and a
short model question, restart with the same data volume, then test another
message and verify that an unapproved sender gets no reply. Do not paste real
tokens, scanner IDs, QR images or message contents into reports. No live account
was connected during development.
