# Folio product definition

Folio is a self-hosted personal assistant, task center, and owner-controlled
digital counterpart. It turns the owner's intent and trusted events into
bounded work, uses private personal context when needed, acts within the owner's
granted capabilities, and returns evidence through the Web console or optional
channels. The current build executes configured runtime Actions; the approved
architecture also supports goals delegated without a predefined Action.

The product is organized around four connected responsibilities:

- **Personal assistant** — Main understands owner intent, performs work within its
  grant, delegates sustained work and returns results. The approved Main is built
  into Folio with the pi SDK, including file/shell tools, workspace scripts,
  trusted skills and Folio custom tools. The current adapter-based Main only has
  its existing control/context tools; SDK execution is not shipped.
- **Task center** — durable tasks, priorities, pauses, off-peak rules, retries,
  continuation work, run history, usage, cost estimates, and owner review make
  work visible and controllable from one place.
- **Digital counterpart** — Folio keeps the owner's preferences, private
  context, configured actions, and delivery channels together without turning
  them into public repository content or giving an agent unrestricted machine
  access.
- **Context and memory foundation** — the private Markdown repository remains
  authoritative personal context. Automatic context organization is Folio's
  first shipped assistant scenario, not the boundary of the product.

## Product loop

```text
owner intent / trusted event
            ↓
       route and policy
            ↓
      durable task or assistant turn
            ↓
 granted execution + private context
            ↓
   result, audit, review, and delivery
```

Folio's control plane owns authentication, routing, task scheduling, bounded
execution, repository transactions, audit evidence, and channel delivery. The
approved Main is the special pi SDK assistant; task harnesses are interchangeable
behind ACP, including Codex, DSH and pi with verified native interfaces or
adapters. A direct model HTTP call is not a target task runner. Integration
payloads, repository code, and model output are untrusted data; they never become
unrestricted instructions or source/deploy access.

## Approved execution architecture (not implemented)

Main embeds pi SDK through Folio's TypeScript Main Host; pi owns its loop,
conversation, streaming and compaction. Main may act directly or invoke the
Folio `subagent` tool. Folio persists the goal Task and manages its subagent,
Run attempts, grants, scheduling, approvals, recovery, artifacts and delivery.
Task agent execution uses ACP to a harness, rather than direct model inference.
Harnesses own their model calls and tool loops. Existing fixed Actions remain
reusable workflows alongside goal tasks.

A task is the desired outcome, a subagent is an independent execution context,
and a run is an attempt. Acceptance, execution, verified fulfillment and result
delivery are separate facts. Missing information or capability should be a
visible blocker that can be resolved on the same task. Main's file/shell and
skill access does not grant unrestricted private-volume or production access.
The [architecture contract](agent-architecture.md) owns the boundaries, migration
and acceptance criteria; this definition does not add current runtime capability.

## Current scope

The current service includes the Web dashboard, assistant chat, an Activity view
with Tasks/Runs/Review tabs, runtime action registry, event hooks and schedules,
context maintenance and read-only reports, owner review snapshots, optional Bark
and native Weixin channels, and a GitHub storage plugin. The dashboard renders a
server-driven quick-action catalog rather than a fixed daily task list; built-in
report and maintenance actions can be extended or shadowed by owner action
definitions. Feedback is a screenshot-first floating window with an explicit
region selector, automatic page context, private screenshot storage and an asynchronous
main-agent intake that files a plan-only task or asks option-based questions; source
self-modification and deployment remain planned. Assistant and report configuration can be used without a connected
repository; context-dependent work has separate requirements. Task acceptance,
execution evidence and run-owned report results remain distinct. See
[Start using Folio](../README.md#start-using-folio) and
[feedback and self-bootstrap](self-bootstrap.md).
Some future architecture described in
the design notes remains explicitly planned or unverified; those notes do not
expand the shipped contract.

For the detailed implementation contract, see the root [README](../README.md)
and the focused documents for [context maintenance](context-maintenance.md),
[event-driven work](event-driven-assistant.md), and [runtime actions and the
main agent](agent-architecture.md).
