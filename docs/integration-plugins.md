# Integration plugins

Folio has two kinds of plugin, and the difference is the dependency direction.

| Class | Lives in | Direction | May do |
| --- | --- | --- | --- |
| **Built-in capability** | `plugins/`, compiled into the binary | Folio → plugin (host mounts it) | own the storage remote, serve backend routes, ship a frontend |
| **External event** | its own repository or module | plugin → Folio (hook API) | notice a change and ask for work |

The GitHub integration is both: `plugins/github` is the built-in capability
plugin (remote, credential, device sign-in, repository picker), and
`plugins/github-bridge` is an external event plugin that reports issues.

Bidirectional communication is a separate service boundary. The native
[Weixin channel](weixin-channel.md) receives identity/text and sends replies
through `internal/channel`; its Folio handler enforces owner authorization.
It uses neither the external signed-hook contract nor the outbound-only
notification contract, and does not import an external Agent runtime.



```
external system ──▶ plugin ──▶ POST /api/hooks/{id} ──▶ task queue ──▶ run
                     │                                   │
                     └── owns its credentials            └── owns the audit trail
```

## Dependency direction

**An external plugin depends on Folio. Folio never depends on an external
plugin.** Built-in plugins are the opposite by construction: they are compiled
in, they implement an interface Folio defines (`internal/storage`), and Folio
calls them. That is why they can own the remote while an external plugin cannot.

That single rule decides everything else:

| Question | Answer |
| --- | --- |
| Can Folio import, link, or configure a plugin? | No. No shared code, no shared data directory. |
| Does Folio call a plugin? | No. Folio is passive; the plugin is the caller. |
| Can Folio run without any plugin? | Yes. Manual runs, the scheduler, the board, and usage are complete on their own. |
| Who owns code-host credentials? | The storage plugin. Folio never sees the token. |
| Who owns plugin state? | The plugin. Folio stores tasks, runs, and notifications, nothing else. |
| What if the plugin dies? | The queue drains and waits. Nothing in Folio breaks. |

The boundary is HTTP plus a shared secret. The plugin may live in another
repository, another language, another container, or on another machine. Only the
contract in this document is stable.

## The storage boundary (built-in plugins)

`internal/storage.Remote` is the one interface a capability plugin implements:

```go
Resolve(input)       // owner input -> canonical repository
Validate(remote)     // is this remote mine?
Sync(...)            // clone or fast-forward the checkout
Head(...)            // remote branch tip
Publish(...)         // push and verify
Status(...)          // auth mode, never a secret
```

Folio owns everything on the local side: the hardened git process boundary
(`storage.Executor`, which the plugin may call but not bypass), the clean-check
gate, the commit, the rollback, and the audit record. A plugin owns the remote
side and the credential. Swapping GitHub for S3 means implementing six methods,
not editing the maintenance worker.

### The browser host surface

A built-in plugin's frontend is served from its embedded `Assets()` at
`/plugins/{id}/`, and the shell loads `/plugins/{id}/plugin.js` after a session
exists — on boot with a restored cookie and again after a fresh login. A plugin
may use only this surface:

| Surface | Meaning |
| --- | --- |
| `window.folio.api(path, {method, body})` | authenticated request with the CSRF header |
| `window.folio.connectRepository(owner, name)` | the host's one connect transaction; resolves `{ok, reason}` |
| `window.folio.refreshState()` | re-read host state after a plugin action |
| `window.folio.refreshTasks()`, `formatTime`, `t` | task board and formatting helpers |
| `folio:session` event | `{authenticated: true}` on boot and login, `{authenticated: false}` on logout or expiry |
| `folio:languagechange` event | the owner switched the interface language |

`folio:session` with `authenticated: false` obliges a plugin to drop its
signed-in UI. The GitHub mounts live inside the shell Settings page, which also serves first-time
setup. Changing the visible page does not end the session or remove the mount.
The plugin mounts into markup the shell reserves and owns its own
ids there. Built-in capability plugins are compiled in, so the shell carries no
second implementation of their surface: the searchable repository picker lives
in the GitHub plugin, and the host keeps only the connect transaction and the
refresh.

**What a plugin may do

1. `POST /api/hooks/{hook_id}` with a signed body. That is the whole write API.
2. Read nothing. A plugin does not query Folio for tasks or runs; the hook
   response tells it that the work is durable (`task_id`) or was a duplicate.

## What a plugin must not do

- Store or proxy Folio's git credential for the context repository.
- Assume a task ran, succeeded, or will run soon. Deferral is Folio's decision.
- Replay history on first run. Record a baseline and report changes from then on.
- Invent a task kind. The hook's action is fixed by the owner in Folio; the body
  only carries data.

## Event contract (v1)

```
POST /api/hooks/{hook_id}
X-Folio-Timestamp: <unix seconds>
X-Folio-Event-Id:  <1..128 of A-Za-z0-9_.->
X-Folio-Signature: <hex HMAC-SHA256(secret, "<timestamp>.<raw body>")>
Content-Type: application/json
```

- Body is a JSON object, at most 16 KB, at most 20 calls per hook per hour.
- Clock skew is ±5 minutes.
- The event id is the idempotency key. Re-sending the same id returns `200`
  with `duplicate: true` and creates no second task.
- `202` means the task is durable. It does not mean the task started.
- Queued maintenance events with the same kind/source coalesce. An event
  arriving while maintenance is running leaves one queued follow-up so a
  change after the current pull is not lost. Unchanged document fingerprints
  prevent that follow-up from making an unnecessary model call.
- `4xx` is permanent: fix the plugin. `429`/`5xx` are transient: retry later.
- The secret is shown once, at hook creation, and is never returned again.

Accepted fields in the body:

| Field | Use |
| --- | --- |
| `title` | Optional task title, at most 120 characters. Falls back to the hook name. |
| anything else | Stored as a bounded flat payload on the task, shown on the board. |

Payload projection: top-level keys only, `^[a-z][a-z0-9_]{0,31}$`, at most 16
keys, scalar values, at most 512 characters per string. Nested objects, arrays,
oversized values, and credential-shaped keys (`token`, `secret`, `password`,
`credential`, `apikey`, `private_key`) are dropped. The body is data: Folio
never executes it, interpolates it into a shell, or writes it into the checkout.

## Task kind belongs to Folio

A plugin cannot ask for `triage` by writing it in the request body; the hook's
action decides the kind, and the owner picks it in Folio from the configured
kinds. This is deliberate: the dangerous part of an integration is not the
trigger, it is the action.

Kinds are data now. `data/task-kinds.json` defines `maintenance` (pull, commit,
push) and any `report` kind the owner adds (`triage`, `summarize`, ...). Wiring a
plugin to a new kind is:

1. add the kind to `data/task-kinds.json` (prompt, priority, optional runner);
2. create or update the hook and choose that kind in the Web UI;
3. the plugin keeps sending the same signed event — no plugin release, no Folio
   release, no Go change.

A genuinely new capability (something that writes, comments, or opens a PR) is
still code in Folio, in the same order: implement it, expose it as an action,
then point a plugin at it. External writes stay proposals until the owner
approves them in Folio; a plugin never holds an approval gate.

## Lifecycle

- The plugin chooses its own cadence (poll, cron, `--once`, systemd timer).
- First successful cycle records a baseline; changes after that emit events.
- Transport failures leave the event pending for the next cycle. Permanent
  rejections are logged and dropped, never retried forever.
- A plugin may be started or stopped at any time. Missed events are simply not
  reported; there is no backfill protocol in v1.

## Current plugins

| Plugin | Trigger | Kind | Form |
| --- | --- | --- | --- |
| `scripts/folio-watch-git.sh` | context remote HEAD changed | maintenance | shell |
| `scripts/folio-hook.sh` | any external script or CI job | maintenance | shell |
| `plugins/github-bridge` | GitHub issue opened or updated | any configured kind | Go, own PAT |
| `plugins/github` | storage, sign-in, repository picker | n/a (capability) | Go + JS, built in |

The GitHub bridge is the reference implementation of this contract: separate Go
module, own PAT, own state, no import of Folio internals.

## Adding a plugin

1. Create a hook in Folio with the action and priority you want.
2. Copy the secret once into the plugin's private configuration.
3. Implement: baseline, detect change, `POST` with a stable event id.
4. Verify with `-dry-run` first; check the task board before trusting it.
5. Keep the secret out of the repository, logs, and command lines.
