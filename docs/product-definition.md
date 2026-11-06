# Folio product definition

Folio is a self-hosted personal assistant, task center, and owner-controlled
digital counterpart. It turns the owner's intent and trusted events into
bounded tasks, uses private personal context when needed, executes only the
runtime actions the owner has configured, and returns evidence through the Web
console or optional channels.

The product is organized around four connected responsibilities:

- **Personal assistant** — the main agent is the owner's conversational control
  surface. It can inspect Folio's state and, within its granted tools, change
  runtime configuration such as tasks, routes, policies, channels, and actions.
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
 bounded action + private context
            ↓
   result, audit, review, and delivery
```

Folio's control plane owns authentication, routing, task scheduling, bounded
execution, repository transactions, audit evidence, and channel delivery. A
model runner or main-agent runtime is replaceable infrastructure behind those
boundaries. Integration payloads, repository code, and model output are
untrusted data; they never become unrestricted instructions or source/deploy
access.

## Current scope

The current service includes the Web task and usage dashboard, assistant chat,
runtime action registry, event hooks and schedules, context maintenance and
read-only reports, owner review snapshots, optional Bark and native Weixin
channels, and a GitHub storage plugin. Some future architecture described in
the design notes remains explicitly planned or unverified; those notes do not
expand the shipped contract.

For the detailed implementation contract, see the root [README](../README.md)
and the focused documents for [context maintenance](context-maintenance.md),
[event-driven work](event-driven-assistant.md), and [runtime actions and the
main agent](agent-architecture.md).
