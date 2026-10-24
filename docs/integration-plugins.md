# Integration plugins

A plugin is any program outside Folio that notices something in the world and
asks Folio to do work. The context-repository watcher, a CI job, and the GitHub
bridge are all plugins. This document is the contract between them and Folio.

```
external system ──▶ plugin ──▶ POST /api/hooks/{id} ──▶ task queue ──▶ run
                     │                                   │
                     └── owns its credentials            └── owns the audit trail
```

## Dependency direction

**A plugin depends on Folio. Folio never depends on a plugin.**

That single rule decides everything else:

| Question | Answer |
| --- | --- |
| Can Folio import, link, or configure a plugin? | No. No shared code, no shared data directory. |
| Does Folio call a plugin? | No. Folio is passive; the plugin is the caller. |
| Can Folio run without any plugin? | Yes. Manual runs, the scheduler, the board, and usage are complete on their own. |
| Who owns GitHub credentials? | The plugin. Folio holds only the git credential for the context repository. |
| Who owns plugin state? | The plugin. Folio stores tasks, runs, and notifications, nothing else. |
| What if the plugin dies? | The queue drains and waits. Nothing in Folio breaks. |

The boundary is HTTP plus a shared secret. The plugin may live in another
repository, another language, another container, or on another machine. Only the
contract in this document is stable.

## What a plugin may do

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

A plugin cannot ask for `triage`; the hook's action decides the kind, and the
owner picks it in Folio. This is deliberate: the dangerous part of an
integration is not the trigger, it is the action. Today the only implemented
action is `maintenance`, so every plugin schedules context maintenance. A new
kind is a Folio capability, in this order:

1. implement the kind in Folio (prompt, output class, guardrails);
2. expose it as a hook action with its own priority default;
3. point a plugin at it — one configuration change, no plugin release.

External writes (`comment`, `label`, `create PR`) stay proposals until the owner
approves them in Folio. A plugin never holds an approval gate.

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
| `plugins/github-bridge` | GitHub issue opened or updated | maintenance | Go, own PAT |

The GitHub bridge is the reference implementation of this contract: separate Go
module, own PAT, own state, no import of Folio internals.

## Adding a plugin

1. Create a hook in Folio with the action and priority you want.
2. Copy the secret once into the plugin's private configuration.
3. Implement: baseline, detect change, `POST` with a stable event id.
4. Verify with `-dry-run` first; check the task board before trusting it.
5. Keep the secret out of the repository, logs, and command lines.
