# Automatic personal-context organization

Folio's first assistant scenario is maintaining a private personal-context
repository. Markdown remains authoritative; runtime fingerprints and findings
are derived data outside Git. No context content belongs in this public repo.

## What runs automatically

The owner connects a repository and configures a signed maintenance hook for
the external Git watcher, or uses the existing interval/manual trigger.
Folio pulls a clean checkout first. It scans supported canonical Markdown and
pending inbox candidates, compares exact byte fingerprints with the last
successful maintenance, then supplies a bounded batch of changed documents
plus available ancestors and scope-related canonical context to the runner.
The packet is at most 64 KiB. Replacement inputs include full original content
and metadata. A document too large for a complete packet is reported for owner
review; it is never silently truncated or allowed to starve later documents.
Sensitive documents and credential-shaped content are excluded.

Automatic edits are intentionally structural:

- reorganize sections and remove exact duplicate text;
- add extractive summaries copied from the document or its direct children
  with an explicit matching claim type;
- repair local Markdown document links and add bounded navigation to supplied
  canonical documents, with labels taken from supplied text.

The code validator preserves every distinct normalized existing textual line
and exact frontmatter bytes, including dates, provenance, claim type,
sensitivity and hierarchy. This is a conservative text-preservation check,
not a proof of semantic equivalence. Arbitrary paraphrases, unsupported prose,
new canonical files, cross-file moves/deletions and candidate promotion are
rejected. Semantic consolidation and hierarchy metadata changes remain owner
review work. The model cannot run repository scripts, commit or push.

## Success, backlog and failures

Store validates edits again against its own original complete snapshot, checks
for changed inputs, applies at most eight files, stages only those paths, then
commits and verifies the push. Validation and push failures retain existing
rollback behaviour and never advance the maintenance checkpoint.
Local and remote HEAD must match after pull, including no-change runs. If an
interruption leaves an unpublished local commit, maintenance reports a failure
until the owner reviews and reconciles it; those bytes are never checkpointed.

Successful no-change and pushed runs save resulting fingerprints in
`runtime/maintenance-state.json` (0600). Consequently the watcher's event for
Folio's own commit, an unchanged interval tick or another device changing only
unsupported files does not invoke the model again. A changed document is
eligible again; its ancestors can be reconsidered without reloading unrelated
body text into the model.

A batch with remaining changed documents queues another maintenance task.
Continuations use the configured maintenance kind's priority, off-peak window
and pause controls. They retain the existing queue's Codex default. A new
maintenance event arriving after a task has started creates at most one queued
follow-up for that kind/source; queued duplicates coalesce. This prevents a
change arriving after the current pull from being acknowledged and forgotten.
An interrupted/failed task remains visible and can be retried with Run now.
Queue maintenance selects the Codex runner without forcing a model name.
Maintenance and report run audits carry their originating `task_id` from the
first running record, so a task can be associated with its current attempt
before it finishes. Retrying preserves the task ID and creates a new run ID;
older run records without this association remain readable.

The model can also return `needs_review: [{"source": "...", "reason": "..."}]`.
Pending candidates, stale context, broken document links, unavailable parents,
hierarchy cycles and oversized documents produce deterministic findings.
Findings never approve or promote context. Model findings are retained in
`runtime/maintenance-review.json` while the source bytes remain unchanged;
edited sources are reassessed on the next pass. The Web run view shows these
findings alongside diffs and the remaining document count.

New maintenance runs record the storage plugin ID, its validated repository
name and the checkout branch in `repository` before pulling. Remote URLs and
credentials are excluded. This binds later source/commit links to the run's
repository rather than whichever repository is currently selected. Older runs
without this snapshot do not establish a historical repository identity.

`GET /api/automation` returns `review_run` separately from the newest `latest`
run: it is the complete most recent retained successful maintenance record
(`pushed` or `no_changes`) with an explicit `needs_review` field. Reports,
failures and older successes without that field do not replace it. An explicit
empty or null findings list is a known empty snapshot; no qualifying record
means `review_run: null`. This describes findings at the last successful
organization, not a live approval queue or a completed owner decision. The
snapshot is limited to the retained run history and never fabricates an empty
review state when older evidence is unavailable.

Older run records without these fields remain readable. The original
`summary`/`edits` runner response is accepted; new workers additionally return
input fingerprints, backlog and findings. The host's structural check applies
to both formats. Local Python proposal-only commands remain unchanged.

## Verification

Use synthetic repositories and fake model executables, never personal content:

```sh
go test -race ./internal/context ./internal/store ./internal/web ./cmd/folio
go test -race ./...
go vet ./...
go build -o /tmp/folio ./cmd/folio
node --check web/app.js
node --check web/i18n.js
```

The regression cases cover complete long inputs, oversized-file isolation,
batch coverage, ancestors, fact loss, unsupported prose, protected metadata,
candidate promotion, link repairs, stale snapshots, CRLF metadata, secret
exclusion, successful-push checkpoints, restart/no-model behaviour, failed-push
rollback, queue pause and continuation. Container/browser checks and real
provider/deployment acceptance are separate from these local checks.
