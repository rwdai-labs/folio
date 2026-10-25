# Durable service state and transactions

Inherit `../../AGENTS.md` and `../AGENTS.md`.

| Files | Responsibility |
| --- | --- |
| `store.go`, `storage.go`, `executor.go`, `git.go`, `lifecycle.go` | Layout, remote integration, safe processes and repository lifecycle |
| `automation.go`, `review.go` | Validated automatic edits and owner-reviewed changes |
| `maintenance.go` | Incremental fingerprints, persistent review findings and continuation tasks |
| `tasks.go`, `kinds.go`, `hooks.go` | Durable queue, data-defined actions, signed events |
| `report.go`, `usage.go`, `channels.go`, `models.go` | Reports, usage, notifications and runner catalogs |

- A task is durable intent; a run is one attempt. Preserve single-writer
  checkout access, priority/off-peak policies, pause/hold/cancel/run-now
  behaviour, deduplication, and recovery of interrupted tasks after restart.
- Kinds are configuration mapped to implemented actions. Reject unknown kinds
  both at hook setup and dispatch. A `report` action has no repository writes.
- Automatic organization uses a host-built complete snapshot and the context
  package's structural validator even with an older worker. Checkpoints advance
  only after successful no-change or verified push, acknowledging resulting
  bytes; a failed model/validation/push must not acknowledge input.
  Require local HEAD to match the remote after pull, including no-change runs;
  interrupted unpublished commits need explicit review and reconciliation.
  Review findings persist while the source fingerprint is unchanged. Bounded
  backlogs create maintenance continuation tasks under ordinary pause/window policies.
  A maintenance event received during a running task coalesces into one queued
  follow-up so changes after the pull cannot be lost.
- Verify a hook's signature over timestamp plus exact raw body before consuming
  replay/rate state. Preserve bounded scalar payload projection; expose the
  hook secret only at creation. Bodies cannot select an unauthorized action.
- Before publishing, require the expected clean checkout/remote, pull first,
  validate every edited path and content, stage exactly the approved paths,
  and verify the published head. Preserve rollback and audit evidence on
  failure. Never force-push or bypass a validation gate.
- Keep locks scoped so read-only state/history queries remain responsive during
  model runs. Persist private state with bounded reads/writes and restrictive
  permissions; keep secrets out of responses, diffs and notification ledgers.
- Failed runner audits may store only a fixed diagnostic class such as network,
  authentication, timeout or schema failure; never persist raw stderr or model
  output. Preserve fixed worker diagnostic classes across the service boundary.
- Update this file for changed state/transaction invariants. Persisted schemas,
  task/run statuses or response fields also require a compatibility decision
  and synchronized HTTP/UI/worker instructions, tests and contract docs.

Run `go test -race ./internal/store` from the repository root and the root Go
checks. Focus regression coverage on failed validation/push, concurrent access,
restart recovery, hook replay/rate limits, and secret redaction when affected.
Tests use temporary repositories and fake runners, never live credentials.
