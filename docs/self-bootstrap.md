# Feedback and self-bootstrap

Status: Phase 1–2 implemented; Phase 3 is a design and is not shipped. Snapshot
date: 2026-11-09. This document describes Folio's own improvement loop without
placing source code editing or deployment in the main agent.

## Purpose

Folio should be able to receive an owner's improvement request, reject it when
it is not actionable, and turn an accepted request into durable, auditable work.
The long-term loop is:

```text
annotated screenshot + structured feedback
              ↓
   advisory sufficiency gate
     accepted + optional hint
              ↓
        durable plan task
              ↓
 isolated source workshop (planned)
              ↓
 tests, commit, push, optional CI merge
```

The base platform owns capture, storage, authentication, task durability,
action execution, audit, and delivery. A specific task such as a report or a
Folio improvement is an action definition, not page code. Chat remains a core
assistant surface rather than an action card.

## Phase 1: action catalog

`actions.json` entries may carry optional presentation metadata. The execution
steps remain the authoritative behavior; the metadata only describes how an
owner starts the action.

```json
{
  "id": "example",
  "label": "Example action",
  "description": "What this action does.",
  "requires": ["report"],
  "presentation": {
    "group": "daily",
    "order": 20,
    "submit_label": "Queue action",
    "fields": [
      {"name": "note", "label": "What should it do?", "type": "textarea", "required": true, "max_length": 4000}
    ]
  },
  "steps": [{"id": "run", "type": "agent", "prompt": "{{payload.note}}"}]
}
```

Declared text/textarea fields are owner input and use an explicit bounded
owner-task payload path (up to 32 KiB total), so 4000-rune values reach the
durable task and dispatch snapshot. The generic hook/plugin payload projection
keeps its smaller 512-rune bound; the two paths are separate. Oversized or
credential-shaped configured payloads are rejected rather than silently trimmed.

`GET /api/catalog` returns the configured actions plus the built-in legacy
report and maintenance kinds, with their fields and capability requirements.
The dashboard renders this catalog and posts to
`POST /api/actions/{id}/run`. The page does not name report, maintenance, or any
other concrete task. Custom action labels and descriptions are owner data and
are rendered as text.

`requires` values are capability names: `assistant`, `report`, `maintenance`,
`context`, or `workshop`. Readiness is local configuration, never proof that an
upstream account or inference call works.

## Phase 2: feedback inbox and gate

Feedback is private owner data stored under `data/feedback/`; the public image
never contains context, credentials, or runtime files. The console exposes it as
a screenshot-first movable floating window opened from a fixed launcher. The
owner drags a rectangle around just the part of the current tab that shows the
problem, or captures the whole view, then describes what happened and what they
want; pasted/dropped/selected files are also accepted. The window can be closed
without losing the draft.
The area, title and acceptance text are derived automatically and remain editable
under a collapsed “More” section, so the visible form stays small. A submission
contains:

- a screenshot, captured or uploaded;
- current behavior and expected behavior;
- derived title, area, acceptance criteria and priority;
- optionally, a bounded page/element context captured with the screenshot.

Current-page capture records the view, route, viewport and up to 24 visible
semantic elements (tags, accessible names, stable selectors, section labels and
relative rectangles). A region capture includes only elements intersecting the
selected rectangle; the summary also names the region size. It never records
input values, passwords, raw HTML or hidden elements. Uploaded files carry only
the basic view/route metadata, not invented DOM elements. The context is capped
at 24 KiB, stored only in the private feedback record, and summarized into the
gate/plan prompt; the detail API exposes it to the authenticated owner while list
summaries expose only a count. Screen capture uses the browser display-capture
API when the page is a secure context and the selected region can be mapped to a
tab frame; on an HTTP LAN address, or when the browser shares a window/monitor
instead of the tab, the same selector renders a permission-free local DOM
snapshot cropped to the chosen region and attaches the same metadata. The
screenshot still never leaves the private feedback store.

The screenshot is decoded and re-encoded as PNG to strip metadata, bounded by
dimensions and byte size, and served only through an authenticated route. No
caller-supplied path is accepted. The image itself is capped at 8 MiB; the
multipart envelope gets a separate bounded 1 MiB allowance so a maximal image
plus form fields is still accepted. Required boolean catalog fields must be
explicitly true, not merely present.

The gate has two layers:

1. Deterministic required-field, area, priority, and image checks. Text fields
   only have to be non-empty and within their maximum length; there is no
   minimum length to guess.
2. When a report model binding exists, a strict model readiness check returning
   `ready` plus a fixed reason (`too_broad`, `ambiguous`, `not_actionable`,
   `unsafe_scope`, or `ok`). The prompt tells the model that "this module" or
   "this UI" means the page elements captured inside the owner's screenshot
   region, so pointing at the image is not treated as missing detail.

The model layer is advisory: owner input is authoritative. A not-ready verdict
(`too_broad`, `ambiguous`, `not_actionable`, `unsafe_scope`, `gate_unavailable`)
is stored as `gate_advisory` and shown as a non-blocking hint while the plan
task is still created. Deterministic malformed input (empty or oversized
fields, an invalid image or context) is refused with a 400 response before a
feedback record is written. An accepted
request currently creates a durable **planning** report task: it may describe
likely files, implementation steps, tests, assumptions, and scope risks, but it
must not edit files, run commands, deploy, or ask the owner a clarifying
question. This is deliberately not the source-edit loop.

The deterministic checks and the plan task are implemented in Phase 2. The
source workshop below remains planned.

## Phase 3: isolated source workshop

The main agent must not gain source-edit or deployment tools. A workshop is a
separate trust boundary with:

- its own checkout of the Folio source repository and its own fine-grained
  credential;
- no mount of the private context repository, channel credentials, or model
  keys from the main runtime;
- a bounded input document (feedback JSON plus a copied screenshot) passed as
  data, never as shell arguments;
- a model runner restricted to that checkout;
- mandatory formatting, tests, vet/build, and diff limits;
- a branch and pull request by default; direct main push only as an explicit
  owner setting;
- audit evidence for the plan, changed files, tests, commit, pull request, and
  failure class.

The workshop is not implemented. Until it exists, Folio can capture, reject,
and plan, but it cannot automatically modify its own source or deploy.

## GitHub issues

A local private inbox is the source of truth. GitHub issue creation is an
optional mirror, not a prerequisite: issue image upload is awkward through the
API, and a separate login should not block feedback capture. The existing
GitHub bridge can turn labelled issues into signed events, but its current
payload carries title, labels, state, and URL rather than issue body or image
content, so issue-first feedback would require an additional bounded body fetch
and attachment policy.

## Threat model

- Feedback text, screenshots, issue bodies, and model output are untrusted data.
- Feedback never selects a command, path, credential, or deployment target.
- Model output is a proposal; the workshop must validate it with tests and diff
  limits before publishing.
- Sensitive paths (CI workflows, deploy configuration, dependency manifests,
  authentication/security code) require an explicit owner policy and should
  never be changed silently by a bootstrap run.
- A rejected or failed run leaves the source checkout and the private context
  repository untouched.

## Acceptance for the shipped phases

- The dashboard renders actions from `/api/catalog`; no concrete daily task is
  hard-coded in `index.html` or `app.js`.
- Built-in report and maintenance actions remain usable and keep their existing
  task semantics.
- A screenshot can be dragged as a region (or captured whole), pasted, dropped,
  selected, annotated, and submitted; the stored image is private and
  authenticated.
- Missing or vague feedback is rejected with a fixed reason and no task.
- Accepted feedback creates a durable plan task and preserves the feedback and
  task association.
- Both languages, keyboard operation, mobile layout, logout cleanup, and the
  synthetic browser acceptance check pass.

Phase 3 acceptance additionally requires a separate workshop deployment,
credential isolation, a passing validation suite, and an explicit publish
policy. None of those are claimed here.
