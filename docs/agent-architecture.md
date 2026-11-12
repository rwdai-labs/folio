# Main agent, delegated tasks, and harness execution

Status: approved target architecture, updated 2026-11-12. The pi SDK Main Host,
`subagent` tool, ACP task runners, generic waiting/resume state, and reliable
result delivery described below are **not implemented**. This document replaces
the earlier target of a replaceable single-turn Main adapter with no shell or
filesystem tools. It does not change the behavior of the current binary.

The [product definition](product-definition.md) supplies the shared vocabulary;
[README](../README.md) and [deployment instructions](../deploy/README.md) describe
what operators can use today. Historical research is not an implementation
contract.

## 1. Decisions

1. **Main is special and built on the pi SDK.** Folio embeds
   `@earendil-works/pi-coding-agent` in its own TypeScript Main Host. Pi owns the
   Main agent loop, conversation, streaming and compaction. Main is not selected
   from the interchangeable task-runner list.
2. **Main can work directly.** It has file and shell tools, can write and execute
   scripts in its granted workspace, and can use trusted skills. It is not
   restricted to queue/configuration tools. This does not grant production source,
   deployment or unrestricted private-volume access.
3. **`subagent` is a Folio custom tool registered with Main pi.** Calling it
   records a durable goal and delegates execution to Folio. Folio manages the
   subagent lifecycle; pi does not have to provide a native background-job system.
4. **Task agent execution uses ACP to a harness.** Folio is the ACP client;
   Codex, DSH, pi and other compatible harnesses are ACP agents, using their
   native interface or a separately verified, pinned adapter. A direct model
   HTTP call is not a task runner. Model calls belong inside the harness.
5. **Folio owns the work and authority.** Authentication, routing, task scheduling,
   grants, approvals, cancellation, retries, run evidence, repository transactions,
   artifacts and delivery remain Folio responsibilities. Pi/harness session state
   and Folio task state have distinct owners.
6. **Goals do not require a predefined Action.** Reusable Actions remain useful
   fixed workflows. An open-ended owner delegation can create an agent-executed
   Task without publishing a new Action or adding a task kind for that request.

These decisions are general across research, document work, resource retrieval,
calculations and operations through connected systems. New requests should reuse
capabilities rather than introduce a separate hardcoded product flow each time.

## 2. Current implementation and the failure it explains

The following is shipped code, not the target:

| Area | Current behavior / source |
| --- | --- |
| Main | `internal/store/agent.go` owns a bounded Go tool loop and session history; HTTP and command adapters propose one response at a time. No embedded pi SDK host exists. |
| Runtime selection | An assistant model-service binding constructs the built-in OpenAI-compatible adapter. Otherwise private agent configuration selects HTTP or command mode; the default command path is `/data/agent/runtime`. This is not proof of a pi installation. |
| Main tools | `agent_tools.go` grants owner queue/action/route/policy/channel/context/audit tools. There is no generic `subagent`, shell, file or skill execution surface. |
| Task creation | `tasks.go` resolves a configured Action or legacy kind before creating work. Unknown actions fail closed. Tasks are currently action-bound. |
| Task execution | `actions.go` dispatches maintenance, report or a fixed pipeline. An `agent` pipeline step is a report call, not a delegated tool-capable agent. |
| Model paths | `model_execution.go` makes single tool-free HTTP report calls; CLI report/maintenance paths invoke configured workers. Codex worker flags explicitly disable shell, browser and multi-agent features. These are migration inputs, not ACP runners. |
| Task state | `queued`, `running`, `done`, `failed`, `canceled`; restart changes running work to `failed/interrupted`. Generic input/approval waiting and checkpoint resume do not exist. |
| Recovery | Queue deduplication compares active kind/source/title/payload. There is no durable inbound-message-to-delegation key. Pipeline retry starts from the beginning; step outputs are not recovery checkpoints. |
| Results | Run-owned Markdown reports exist. Generic file artifacts and evidence-based goal completion do not. Successful run status currently suffices to mark a task done. |
| Delivery | `notifyRun` accepts only `pushed` and `failed`; successful `reported` and `completed` runs are filtered out. Explicit pipeline push steps can send text. Weixin media and a reliable proactive delivery outbox are not shipped. |

An owner request can therefore reach Main, yet Main cannot find an execution
path. Its tool surface encourages choosing an existing Action or a read-only
report. Its ordinary turn can end with explanatory text without a task. Missing
an Action is easily confused with missing every way to achieve the goal.

`action.list` compounds that problem: it lists the runtime registry only;
`actions: []` does not mean built-in report/maintenance or every capability is
absent. `GET /api/catalog` merges runtime Actions and legacy kinds, but is an
owner launch catalog, not a complete agent capability directory. Private-context
search is evidence about the owner, not a replacement for external discovery.
A background report does not acquire tools merely because its model is capable.

The design fixes those execution and handoff contracts, rather than only asking
the model to sound more proactive. Acceptance is not a promise of success, and
capability gaps must remain visible.

## 3. Composition and ownership

```text
Web / Weixin / trusted task lifecycle events
                  |
                  v
Folio Go service: identity, routing, tasks, grants, audit, delivery
                  |
          private bidirectional IPC
                  |
                  v
Folio Main Host (TypeScript, pi SDK embedded)
  pi sessions / loop / streaming / compaction / trusted skills
  file + shell + workspace scripts
  Folio SDK custom tools, including subagent
                  |
          custom-tool callback to Go
                  |
                  v
Folio task and subagent manager -> ACP client
                                  |
                   +--------------+-------------+
                   v              v             v
              Codex harness   DSH harness    pi harness
             native ACP or verified versioned ACP adapter
```

The Main Host is a maintained component of Folio, supervised with the service,
not an optional user-installed CLI wrapper. The SDK executes inside Node.js or
Bun, not inside the Go binary. Packaging must pin one supported JS runtime, the
SDK and dependencies, with their own build and smoke checks. A concrete runtime
version, package layout and IPC wire schema are implementation work; this design
does not introduce deployable flags or installation commands.

| Responsibility | Owner |
| --- | --- |
| Owner identity, trust class, event routing | Folio Go |
| Main model interaction, loop, session transcript and compaction | Main pi SDK |
| Main file/shell tools, scripts, skills | Main Host in a Folio-granted execution environment |
| Folio custom-tool registration | Main Host; typed SDK tools or built-in extension |
| Custom-tool business validation and persistent effects | Folio Go service |
| Delegated agent model interaction, planning and tool loop | Selected ACP harness |
| Task/subagent identity, parentage, queue, budgets, approval and recovery | Folio |
| Artifacts, execution evidence, completion decision and delivery | Folio, with Main interpreting results where needed |
| Private-context validation, commit/push and rollback | Existing Go-owned repository transaction boundary |

Pi can also appear as a task harness behind ACP. That is a separate session,
workspace and grant from Main; it does not turn Main into a replaceable task
runner or give a task Main's authority.

## 4. Main Host and custom tools

### 4.1 Session contract

Use the pi SDK's session lifecycle instead of building another Main loop. The
Go/Host boundary must support opening/restoring a session, prompting, subscribing
to bounded events, steering/follow-up input and cancellation. A single JSON
`AgentRuntime.Turn` response is insufficient because pi executes the loop and
calls tools during a prompt.

The SDK owns its canonical transcript, queued input and compaction. Folio stores
the authenticated identity-to-session mapping, run correlation, input/event IDs,
task links and bounded public projections. Do not keep a second Go transcript
that competes with pi's session manager. SDK state remains private; audit stores
bounded execution evidence, not hidden reasoning or provider credentials.

New messages during work are serialized into the appropriate session using
explicit steer/follow-up semantics. A queued message is not already processed.
Background results enter as correlated lifecycle events, not forged owner text.
The Host event bridge must distinguish intermediate SDK turn ends from the
session actually settling. Browser disconnect, explicit cancel and delegated
Task cancellation need separate policies; disconnect must not silently discard
a durable Task.

### 4.2 Folio tools inside pi

Register Folio-specific tools through the SDK custom-tool mechanism or a built-in
Folio extension. Their handlers are thin typed callbacks over private IPC to the
Go service, which validates identity, active grants, arguments, revisions and
persistent operations. MCP is optional for external integrations, not required
for these built-in tools.

The target tool surface includes `subagent`, task inspection/control, context
search, Action management and artifact access. Existing action/route/policy
operations retain host validation and versioning. New tool names or schemas
besides the agreed `subagent` entry point are not frozen wire APIs yet.

A request cannot supply its own owner identity, trust class, parent-task authority
or approval. The Host authenticates its callback channel; Go binds the call to
the actual Main session/run and enforces limits independently of model behavior.
Observers or feedback-scoped operations must not inherit Main's full tool set.
Pi's process is not itself the authorization boundary.

### 4.3 Direct work, scripts and skills

Main may read/write files, run a shell, write and execute scripts, use installed
skills, prepare inputs and perform short interactive work within its grant.
Disabling those tools globally defeats the approved design. Long-running work,
independent contexts or work needing a different harness can be delegated.
Delegation is a choice about execution, not a requirement to launch a subagent
for every message.

Grant a private Main workspace and explicitly selected resources. Isolate it
from the service's full data volume and unrelated secrets using OS/container
controls. A working-directory setting, extension hook or prompt is not a sandbox.
Keep time/output/process/network limits at the environment boundary, including
for tools that bypass Folio custom-tool callbacks.

Distinguish three kinds of code:

- Trusted packaged extensions and approved skill resources are executable
  dependencies; install from controlled roots and pin/review changes.
- Main-authored workspace scripts can be executed within the existing grant.
  Generating a script does not make it a trusted Host extension or a global Action.
- Retrieved documents, downloaded files and the private Context repository are
  untrusted inputs. Do not auto-load their extensions, execute their scripts or
  let them change grants. Context facts and the private compatibility CLI remain
  outside this public repository.

Main's shell capability does not authorize modifying Folio's production source,
CI, credentials or binary. Source work requires the separate isolated
[workshop](self-bootstrap.md) and its publish policy. Preserve repository
validation/rollback rather than providing shell access to the canonical checkout.

### 4.4 Owner-facing behavior

Answer ordinary questions directly. Start clear delegated work without asking
the owner to repeat the instruction. For work that needs durable tracking,
record the goal through `subagent` or launch a suitable existing Action. Only
claim acceptance after Folio returns a persisted receipt with a real Task ID.

Discover capabilities and attempt useful in-scope progress before asking for
missing information. Ask a concrete question only when the missing decision
changes execution. Do not substitute a report for an operational goal without
making the scope change explicit. A missing tool is a recorded blocker, not
proof that every route is impossible or permission to expand grants.

## 5. The subagent tool and Folio manager

`subagent` delegates work; it is not a synchronous wait for another model answer.
Its implementation is a Folio tool, not a dependency on pi-native subagents.

| Handoff input | Meaning |
| --- | --- |
| Goal | Desired outcome, preserving the owner's request |
| Inputs/context references | Relevant selected facts, documents and artifact IDs; no automatic full transcript or secret copy |
| Completion criteria | Observable outcomes and evidence expected |
| Constraints | Resource scope, allowed operations, time/cost/step limits |
| Runner preference | Optional registered harness/profile preference, checked by Folio |

The manager assigns Task, subagent and Run identity; authenticated origin,
parentage and effective grants are host data. It validates and persists the
handoff before returning `{task_id, status}`-equivalent receipt metadata to Main.
The actual wire schema will be versioned during implementation. Capability gaps
can produce durable blocked intent without launching an unsupported executor;
this does not relax the current Action lookup until a new goal-task path exists.

Execution sequence:

1. Main invokes the tool; Folio records the goal and message/delegation key.
2. The tool returns promptly after durable acceptance. Main remains responsive.
3. Folio admits the Task under queue/resource policy and selects an ACP profile.
4. Folio starts/restores the harness session with selected context and grants.
5. The harness performs its own loop; Folio collects updates, approvals, blockers
   and evidence, enforcing lifetime and resource limits.
6. A correlated result event returns to Main for delivery, clarification or
   further work on the same goal.

Task = durable work goal. Subagent = managed independent agent context. Run =
one execution attempt. A Task may have multiple Runs, and a Subagent session may
resume only when that harness/profile supports it. One subagent is sufficient
for ordinary delegated work. Further fan-out requires Folio-managed child tasks,
dependency tracking, bounded depth/concurrency and inherited budgets/grants;
never untracked recursive spawning or privilege inheritance.

Delegated agents do not receive Main's route/policy/Action configuration powers
by default. Do not publish a reusable global Action for each one-off request.

## 6. ACP runner contract

### 6.1 Harness profiles, not model HTTP runners

A runner profile identifies a harness, trusted executable/argv, supported adapter
version, environment and credential references, workspace policy, configured
skills/tools, model preferences where supported, and resource limits. Folio
validates profiles before use; payload/model output cannot select an arbitrary
command, credential path or host mount.

Target profiles include Codex, DSH and pi. Native ACP versus adapter support,
authentication, tools, permissions, cancellation and recovery must be verified
for the pinned versions. Listing a harness name is not compatibility acceptance.
A pi ACP task uses its own harness session, not the embedded Main session.

Task model/agent work goes through ACP. The current direct report HTTP path is a
legacy path to migrate and retire, not a fourth runner type or silent fallback.
The same applies to model-bearing Action steps and feedback planning work.
Deterministic Go transactions and business `http`/script/push steps remain
possible: fetching a business API is not a model inference runner.

Model/provider settings are consumed by pi or the selected task harness, using
negotiated ACP configuration only where supported. Gateway account selection,
cost routing and provider fallback stay at the harness/gateway boundary. Folio
selects execution policy and a harness profile; it does not replace the harness
with a bare model request when that profile fails.

### 6.2 Lifecycle and negotiated capabilities

Folio implements an ACP client over the selected supported transport, initially
supervised subprocess stdio with bounded JSON-RPC framing. It must handle:

- `initialize`: protocol version and client/agent capability negotiation, then
  the supported authentication flow.
- `session/new` and `session/prompt`: create a task-scoped session and submit work.
- Agent `session/update` notifications: normalize text, plan and tool status
  into bounded progress/evidence without treating descriptions as verified acts.
- Agent `session/request_permission`: decide from the effective grant or record
  a durable owner approval request with precise scope and revision.
- `session/cancel`: cancel and reconcile the actual process/tool outcome; a sent
  notification alone is not proof that every side effect stopped.
- `session/load` only if negotiated; otherwise show an explicit restart/new
  attempt, not invented checkpoint recovery.
- Optional client filesystem/terminal methods only when advertised and actually
  bounded by Folio. Unsupported capabilities fail clearly.

The ACP prompt stop reason is execution information, not automatic fulfillment
of the owner's goal. Protocol/session IDs are correlated with Folio IDs and do
not confer authority. Tool permissions and client capabilities are not an OS
sandbox: harness-native I/O may bypass client callbacks, so isolate the actual
process, mounts, network and secret scope too.

## 7. Task lifecycle, results and delivery

Goal tasks preserve the original request, constraints, input revisions,
completion criteria, origin/reply channel, parent links, runner/session
correlation and execution policy. Do not require the final step plan at creation.
Action-backed and agent-backed tasks share the task board and audit model.

Target states extend queued/running/terminal states with durable waiting:
`waiting_input`, `waiting_approval` and `blocked` with fixed reasons such as
`capability_missing`. Keep wait reasons and current questions inspectable. The
exact persisted schema and migration are implementation work; these states are
not valid current API inputs.

Answers update the existing Task's revision and resume eligible work, rather
than opening an unrelated Task. Waiting releases execution resources; process
suspension or session reload depends on negotiated capabilities. Define stale
answer/approval rejection, cancellation of waiting/running work, budget exhaustion,
parent/child reconciliation and crash recovery explicitly.

Persist checkpoints, operation receipts and artifacts separately from bounded
public audit summaries. Use durable origin/delegation keys for acceptance and
operation keys for retryable side effects. Do not promise exactly-once network
effects: reconcile ambiguous outcomes before retrying. New Run IDs must not
invalidate a logical operation's deduplication key.

A result includes accomplished/unaccomplished criteria, verified receipts,
artifact/source references, blockers and resumable state where supported.
Check machine-verifiable criteria in code; use Main to interpret remaining
claims, not an obligatory additional model judge per task. A model stopping,
a successful process exit or an accepted tool call alone is insufficient.

Artifacts belong to exact Tasks/Runs, remain immutable per attempt, and use
ID-based authorized access. Preserve path/symlink/content/size checks and private
storage. Support reports and other file types without assuming that every
channel supports attachments. Retention and cleanup must respect task evidence
and active delivery references.

Lifecycle events feed a durable, deduplicated Main inbox and delivery outbox.
Execution outcome and delivery outcome are distinct. Notify success, failure and
needed owner decisions according to channel policy; retry transient delivery
failures without rerunning the task. For Weixin, expired/missing reply context
remains a visible delivery blocker; text plus an authenticated Folio result
entry can precede optional media support. Never expose private files publicly to
make a result link work.

## 8. Existing Actions, events, APIs and evidence

Runtime `actions.json` v2 remains the shipped fixed-workflow format. Ordered
`context`, `agent`, `script`, `push`, `http` and `task` steps have bounded
outputs/timeouts; scripts use trusted argv, HTTP uses HTTPS allowlists and no
redirects, templates are substitution only, and secrets resolve by reference only
at their owning execution boundary. Validation/versioning/disable/restore and
owner launch-field validation remain in Go. An `agent` step's future ACP execution
requires an explicit schema/config migration, not reinterpretation of old files.

Keep `GET /api/catalog` as the safe owner launch catalog; provide a separate
complete capability view for Main to distinguish installed, configured,
authorized and live-verified operations. Readiness is not credential validation.
Legacy report kinds and maintenance remain compatible during migration; the
built-in maintenance path must retain structural validation, single-writer Git
transactions, rollback and incremental checkpoints under any harness change.

Authenticated console/Weixin events may reach Main. Hook/integration events can
trigger owner-configured work with constrained grants, but cannot choose trust,
runner commands, scripts or Main configuration permissions through payload.
System result events are authenticated/correlated notifications, not owner
instructions. Preserve signed-hook replay/rate limits and input bounds.

Current APIs remain current contracts until code changes:

- Action registry list/validate/publish/disable/version/restore/host operations.
- Owner chat `POST /api/agent/turns` (SSE), session history and runtime config.
- Model-service connections/bindings and read-only catalog tests.
- Tasks, run audits, report results, feedback and channel settings.
- Usage, quotas and cached models.dev list-price estimates.

Do not present proposed runner profiles, SDK Host setup, approvals or artifact
APIs as available endpoints. Schema changes require coordinated store, HTTP,
UI, CLI and deployment instructions with compatibility decisions. Preserve
reported usage; unavailable harness metadata stays unknown. Price estimates are
not bills and exclude unsupported cache/context/subscription adjustments.

## 9. Migration and acceptance

All steps below remain planned. Existing operator settings continue to work
until explicit migration lands; no documentation-only change switches runtimes.

1. **Embed Main pi.** Package/supervise the SDK Host, private IPC, authenticated
   session mapping, streamed events and SDK transcript ownership. Register Folio
   tools; enable workspace file/shell/scripts and trusted skills. Preserve scoped
   feedback and observer restrictions. Replace the Go Main loop rather than
   nesting two competing loops.
2. **Build ACP execution.** Implement client framing, profile validation,
   lifecycle, capability negotiation and process isolation. Verify pinned Codex,
   DSH and pi interfaces/adapters independently. Move report/maintenance and
   model-bearing pipeline execution with their existing output validators;
   retire direct task model HTTP execution after compatibility migration.
3. **Add managed delegation.** Register `subagent`; persist goal tasks, grants,
   parentage, bounded scheduling and result events. Separate interactive Main
   resources from background tasks; keep repository writers serialized. Do not
   reuse a global turn lock that makes Main wait for a whole background run.
4. **Complete reliable operation.** Implement input/approval waiting, cancellation,
   recovery, evidence/artifacts, idempotency/reconciliation and durable delivery.
   Migrate UI/configuration and document actual capabilities per profile.

Acceptance must span different kinds of owner work, not one special request:

- A short direct task can use Main files/shell and trusted skills in its grant;
  the same process cannot reach unrelated credentials or canonical Context code.
- A new goal with available capabilities can be delegated without a matching
  predefined Action, and the receipt refers to persisted work.
- Main remains responsive while a Codex, DSH or pi ACP task runs; all use the
  same Folio lifecycle without a bare model HTTP fallback.
- Tool requests cannot forge identity, expand grants or inherit Main privilege.
  Unsupported ACP capabilities and missing tools produce concrete blockers.
- Owner answers resume the same Task revision; stale approvals are refused.
- Cancel/restart and kill-at-checkpoint tests preserve evidence, avoid duplicate
  acceptance, and reconcile ambiguous external effects before retries.
- Successful results satisfy observable criteria; notification failure is
  retried separately and never re-executes successful work.
- Current maintenance validation/rollback, feedback plan-only scope, hook trust
  rules and old run-owned report reads survive migration.

Use synthetic fixtures and fake ACP agents for protocol/failure tests. Live
harness/provider/channel acceptance is a separate, explicitly reported check;
reading SDK/protocol docs does not prove runtime compatibility.

## References

- [Pi SDK](https://pi.dev/docs/latest/sdk)
- [Pi extensions and custom tools](https://pi.dev/docs/latest/extensions)
- [Pi source](https://github.com/earendil-works/pi)
- [ACP protocol overview](https://agentclientprotocol.com/protocol/overview)
- [Context maintenance contract](context-maintenance.md)
- [Integration/hook contract](integration-plugins.md)
- [Weixin transport and delivery limits](weixin-channel.md)
- [Feedback and isolated workshop](self-bootstrap.md)
- [Quota research and current model configuration](subscription-quota.md)
