# folio-github-bridge

A small integration plugin: it watches GitHub issues and asks Folio to schedule
work. It is a separate Go module with its own PAT, its own state file, and no
dependency on Folio's internals. The contract it implements is
[`docs/integration-plugins.md`](../../docs/integration-plugins.md).

```
GitHub ──▶ bridge (poll) ──▶ signed POST ──▶ Folio task queue
```

## Build

```sh
go build -o folio-github-bridge .
```

## Configure

```sh
# In Folio: create a hook (Hooks card) and copy the secret once.
mkdir -p /srv/folio-bridge && chmod 700 /srv/folio-bridge
printf '%s\n' '<hook-secret>' > /srv/folio-bridge/hook-secret && chmod 600 /srv/folio-bridge/hook-secret
printf '%s\n' '<fine-grained-pat>' > /srv/folio-bridge/github-token && chmod 600 /srv/folio-bridge/github-token
```

`bridge.json`:

```json
{
  "folio": {
    "url": "http://127.0.0.1:8080",
    "hook_id": "issues",
    "secret_file": "/srv/folio-bridge/hook-secret"
  },
  "state_file": "/srv/folio-bridge/bridge-state.json",
  "interval_seconds": 300,
  "sources": [
    {
      "type": "github",
      "name": "issues",
      "repos": ["owner/repo"],
      "token_file": "/srv/folio-bridge/github-token",
      "include_prs": false,
      "labels": []
    }
  ]
}
```

The PAT needs only read access to issues on the listed repositories. Prefer a
fine-grained token limited to those repositories.

Environment overrides, for a container deployment: `FOLIO_URL`, `FOLIO_HOOK_ID`,
`FOLIO_HOOK_SECRET`, `GITHUB_TOKEN`, `GITHUB_API_URL` (testing only).

## Run

```sh
./folio-github-bridge -config /srv/folio-bridge/bridge.json -check   # validate
./folio-github-bridge -config /srv/folio-bridge/bridge.json -dry-run -once
./folio-github-bridge -config /srv/folio-bridge/bridge.json          # loop
```

`-once` fits a cron entry or a systemd timer: the process keeps no memory
between runs, only the state file.

The first successful cycle per repository records a **baseline** and sends
nothing. From then on, an opened or edited issue becomes one event, keyed by
repository, issue number, and `updated_at`, so a retry cannot double-queue.

A rate limit puts that repository on hold until the reset time instead of
retrying in a tight loop.

## What it does not do

- It does not read Folio's tasks or runs; the hook response is the only feedback.
- It does not triage or comment on issues. It reports an event; Folio decides
  what work that means. The owner attaches it to any configured task kind —
  `maintenance`, or a `report` kind such as `triage` added to
  `data/task-kinds.json`.
- It does not share credentials with Folio.
