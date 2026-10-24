# Folio 升级为个人助手：可复用的基础调研

状态：调研笔记，不是决策记录。快照日期 2026-10-22。
范围：哪些现成东西可以直接当基础、哪些只能借鉴设计、哪些必须自己写。
内容只含公开信息，不含任何私有上下文。

## 结论先说

1. **不要 fork 或内嵌另一个助手产品。** OpenClaw、Hermes Agent、Khoj、LocalAGI
   都是完整应用，自带状态、记忆和渠道模型。采用它们等于换掉 Folio，而不是升级
   Folio；其中 Khoj 还是 AGPL。
2. **保留 Go 控制面。** 个人助手真正难的部分——鉴权、调度、受限作业执行、运行
   审计、校验闸门、commit/push、容器镜像——Folio 已经做完了。它 `go.mod` 零第
   三方依赖、Web UI 无构建步骤，这两点都是资产，新依赖必须证明自己值得。
3. **把 DSH 当 agent 运行时，而不是当产品。** 镜像里已经 pin 了
   `@deepseek-ai/dsh@0.2.1-rc.2`，现在只用了 headless。同一个安装还提供 SDK
   （stdio JSON-RPC）和 ACP（持久会话、流式更新、权限请求），并自带
   schedule、mcp-client、jobs、subagent、user-approval 插件。这基本就是一个
   agent loop，自己写不划算。
4. **缺的是三层，按顺序加：** 会话状态 → 一个渠道（先 Telegram）→ 带审批的主动
   触发。每一层都是对现有 Folio 机制的薄适配。
5. **对 OpenClaw（MIT）只借鉴设计不搬代码：** 记忆分层、动作敏感记忆、
   "dreaming" 式归并、policy-as-code。渠道连接器架构参考 LocalAGI（MIT，Go）。
   Khoj、basic-memory、Honcho、Open WebUI 因许可证只能当只读参考。

## 1. 对标产品到底是什么形态

| 产品 | 形态 | 它证明的要点 |
| --- | --- | --- |
| OpenAI Dots | 常驻 agent，自带云电脑和浏览器，4000+ 应用连接器，可从 ChatGPT/Slack/Teams/语音进入，每 agent 有权限、规则、审批，后台调研限定只读 | 持久化 + 执行环境 + 权限就是产品本身，聊天框只是其中一个入口 |
| xAI Grok Bot | "AI 同事"，有云电脑，用消息委派任务，适配用户风格，bot 之间能在群里协作，需要时找人确认 | 在聊天渠道里委派，并且在真实工具里把事做完（"100% done"） |
| OpenClaw（MIT） | Gateway + 20 多个渠道 + tools/skills/plugins + Markdown 记忆 + dreaming + 配对 + 沙箱 | 这一类产品可信的开源架构参照 |
| Hermes Agent（MIT） | CLI/TUI + 消息网关（Telegram/Discord/Slack/WhatsApp/Signal/Email）+ cron 带投递 + 记忆提醒 + skills | "cron 结果投递到渠道"和"定期提醒自己落盘"成本极低、价值很高 |

共同点：**目标 → 计划 → 执行 → 学习 → 继续**，加上真实执行环境和权限边界。
Folio 今天是 **定时 → pull → 改 Markdown → 校验 → commit**，是同一个循环的
一个很窄的实例，"执行"这一步被写死成维护文档。

## 2. 现状与差距

2026-10-22 在本仓库核实：Go 1.24、约 1.1 万行 Go、约 4500 行原生 JS/CSS、
`go.mod` 无 `require`、CSP 严格（`default-src 'self'`）、单管理员口令、
GitHub device flow、仓库连接、`codex`/`dsh` headless runner、每次最多 8 个
Markdown 文件改动、带 diff 的运行历史、间隔调度（最小 1 分钟，默认每天）。

| 助手需要的能力 | Folio 现状 | 缺口 |
| --- | --- | --- |
| 与 agent 的持久对话 | 无（只有单次维护作业） | **会话存储 + 流式传输** |
| 在用户所在的地方触达 | 只有 Web | **一个渠道适配器** |
| agent 能用工具，而不只改文件 | 固定维护 prompt，仅写 Markdown | **工具往返**（DSH 提供） |
| 两次对话之间主动干活 | 一个间隔作业 | **触发器/心跳模型** |
| 对高风险动作的审批 | commit 前看 diff | **逐动作审批 UI + 策略** |
| 从交互中学习记忆 | 已有候选审核管线（`inbox/`、`context candidate`） | **对话 → 候选 的捕获** |
| 多设备/通知 | 无 | 渠道投递 |
| 语音、浏览器、4000 个应用 | 无 | 一期不做 |

关键的不对称：**Folio 的记忆通路已经比对标产品更严谨。** 候选暂存、claim 类型、
来源、过期复核、审核闸门，是 OpenClaw 用提示词规则近似的东西。不要为了向量库
把这些换掉。

## 3. 可复用的基础（按优先级）

### 3.1 Agent 运行时：DSH（首选，已装）

在 pinned 安装里核实（`@deepseek-ai/dsh@0.2.1-rc.2`，MIT）：

- `--profile headless`：一次一个任务，输出最终答案，`--json` 事件流，
  `--session-id` 可续。**Folio 现在用的就是这个。**
- `--profile sdk`：stdio 上的 JSON-RPC 2.0，协议定义在 `dsh-sdk-protocol`，
  有 TS 和 Python 客户端。
- `--profile acp`：标准 [Agent Client Protocol](https://agentclientprotocol.com)
  服务端——创建/恢复/关闭持久会话、`session/update` 流式更新、
  `session/request_permission`、挂 MCP server、按会话选模型和推理强度。
  这是聊天产品最干净的接入点。
- 值得挂的插件行：`dsh-schedule`（持久 cron/at/every，投递进会话）、
  `dsh-mcp-client`（外部 MCP 工具）、`dsh-jobs`（长任务）、`dsh-subagent`、
  `dsh-user-approval`（一次性审核、失败即拒）、`dsh-sandbox`、
  `dsh-tool-web`（搜索/抓取，外部内容标记为不可信）。

运维事实：

- **DSH 各包在 npm 的 `latest` 标签是 `0.0.1-rc.1`，我们实际运行的
  `0.2.1-rc.2` 发布在 `next` 下。** Dockerfile 必须继续 pin 精确版本并校验
  `dsh --version`，任何浮动 tag 都是供应链和行为风险。
- 提醒与投递依赖 Web/Session 控制器，所以聊天产品要跑常驻 host，不能只有
  headless 单发。
- 运行时是 agent harness，**不是** OS 边界。Folio README 现在这么写是对的，
  加聊天后要保留这句。

### 3.2 渠道网关

- 建议：**Telegram Bot API + `go-telegram/bot`**（MIT，Go，支持长轮询和带 secret
  token 校验的 webhook，2026-10 仍在维护）。一个 bot、一个 Web 进程，不引入新
  运行时。配对方式：一次性验证码绑定 owner 的数字 user id，陌生发送者默认拒绝。
- 确实需要 WhatsApp 再上 `whatsmeow`（MPL-2.0）；Slack 用 `slack-go`（BSD-2）。
  都是 Go，走同一个适配器接口。
- 适配器模型本身参考：OpenClaw 的 channel 契约（配对、按渠道分会话、白名单）和
  Hermes 的 gateway（单进程多平台，共享 slash 命令）。不要移植代码
  （TS/Python），移植形状：`归一化入站 → 路由到会话 → 流式出站分片`。

### 3.3 记忆

- **保持 `personal-context` 为唯一权威。** 它已有 L0/L1/L2、确定性 packet、
  claim 类型、候选暂存、过期复核、访问遥测。助手的写入路径必须仍是
  `观察 → inbox 候选 → owner 审核 → 转正`，不能是"agent 直接写记忆"。
- 借鉴 OpenClaw 的记忆模型（MIT，`docs/concepts/memory`）：四层
  （用户模型 / 长期记忆 / 按日笔记 / dream diary）、**动作敏感记忆**（记下"什么
  时候可以据此行动"，而不只是事实）、压缩前的自动记忆落盘、按阈值筛过的
  "dreaming" 归并并产出供人审阅的报告。映射到本仓库：`core/` ≈ 用户模型，
  `active/` ≈ 当前，`inbox/` ≈ 候选，`history/` ≈ L2，新增一份归并报告充当
  "dream diary"。
- **现在不要**引入 Mem0 / Letta / Graphiti / Cognee / basic-memory 当记忆库。
  它们都够成熟（Apache-2.0 / Apache-2.0 / Apache-2.0 / Apache-2.0 / AGPL-3.0），
  但每一个都会在已审核的 Markdown 之外制造第二个事实源。等有实测的检索失败再
  说——`PLAN.md` 第 4 阶段本来就写着这个条件。

### 3.4 调度与持久作业

- 一期：沿用 Folio 进程内调度，把"一个维护作业"泛化成"一张触发器表"
  （cron / at / every），选择器语义对齐 DSH，用户心智一致。
- 二期：如果需要跨重启、跨实例，最小可信升级是 `river`（MPL-2.0，Postgres/SQLite，
  Go）；已有 Redis 就用 `asynq`（MIT）。Temporal 对这个产品太重。
- 如果提醒语义由 DSH `dsh-schedule` 承担，就不要在 Go 里再实现一遍：让 DSH 存
  agent 自己排的提醒，Go 的触发器只管那些不能依赖模型调用的维护类作业。

### 3.5 策略与审批

- 借原则不借代码：**默认拒绝、代码里强制、失败即拒**。具体是：陌生渠道发送者 →
  配对码；有副作用的工具调用 → 白名单 + 一次性审批；后台/主动轮次 → 只读工具
  （Dots 的"主动调研"就是这么限的）。
- DSH 已提供 `dsh-user-approval`（一次性、写审计、失败即拒）和 `dsh-sandbox`。
  Folio 要补的是**人的界面**：Web 里的审批卡片、渠道里的内联按钮，以及一条持久
  审计记录。直接复用现有的 run-audit 存储。

### 3.6 Web UI

Folio 的 UI 刻意零依赖：classic script、无构建、严格 CSP、只用 `textContent`。

- `assistant-ui`（MIT，React）和 `CopilotKit`（MIT，React）是成熟的聊天/agent
  前端，Vercel AI SDK（Apache-2.0）是流式协议设计的参考。采用任何一个都意味着
  给一个"Go 二进制直接吐静态文件"的仓库加构建步骤和 React 工具链——这是实打实
  的成本，不是白捡。
- 建议：**一期保持无构建**，只借交互模式：流式气泡、可展开的工具调用卡片、
  内联审批、plan/todo 条、会话切换、每条消息的来源标记。DSH 自己的 Web 客户端
  （MIT/BSD-3-Clause 包）是同一运行时下最接近的视觉参照。
- 如果二期确实需要富输入（附件、文件、canvas），再考虑在独立 `web/` 构建里引入
  `assistant-ui`，Go 服务仍是唯一挂载点。

### 3.7 评测与可观测（后期，成本低）

`promptfoo`（MIT）做 prompt/回归集；`langfuse`（核心 MIT，`ee/` 目录另行授权）
做 trace。做聊天界面都不需要，但信任"主动行为"之前都需要。注意 Folio 已经为
每次运行存了输入摘要、runner、模型、状态和 diff，那已经是一份可用的 trace。

## 4. 建议的目标架构

```text
渠道（Telegram / Web 聊天）
        │  归一化入站消息
        ▼
Folio Go 控制面 ── 鉴权、配对、会话映射、限流
        │                                   │
        │  ── 触发器表（cron/at/every）      │  ── run audit + 审批台账
        ▼                                   ▼
agent 运行时（DSH sdk|acp，常驻）      ── 工具：personal-context CLI/MCP
        │                              └─ 允许的 MCP server（默认只读）
        ▼
personal-context（权威 Markdown + 候选管线）
```

要保住的属性：单进程、单个私有卷、单个管理员凭据、除已鉴权 Web 外不开放入站
端口、模型输出永不进服务日志、每条写入路径都过校验闸门。

## 5. 复用 / 借鉴 / 自研

| 层 | 决定 | 理由 |
| --- | --- | --- |
| agent loop、工具、会话 | **复用 DSH**（sdk/acp，MIT） | 镜像里已 pin，省掉最大一块新代码 |
| 渠道传输 | **复用 `go-telegram/bot`**（MIT） | 原生 Go，webhook 与轮询都支持，接口面小 |
| 渠道语义 | **借鉴** OpenClaw/Hermes 设计 | 配对、按渠道分会话、slash 命令；不搬代码 |
| 记忆存储 | **保留** `personal-context` | 权威、可审计、已有审核闸门 |
| 记忆行为 | **借鉴** OpenClaw 的 memory + dreaming | 用 prompt 和一个定时归并就能实现 |
| 调度 | **扩展** Folio 调度；agent 提醒交给 DSH schedule | 一期避免引入队列依赖 |
| 审批 | **自研**人机界面，复用 DSH 策略 | 策略已有，UI/审计桥没有 |
| Web 聊天 UI | **自研**小规模、无构建、CSP 安全 | React 方案会倒逼工具链变更 |
| Go 连接器框架 | **参考** LocalAGI（MIT，Go） | 同语言，连接器接口经过验证 |
| 向量/图记忆 | **推迟** | 与 Git 权威模型冲突，且没有实测需求 |
| 评测/分析 | **推迟**到二期（`promptfoo`） | 主动自治之前才需要，聊天之前不需要 |

## 6. 分阶段计划

**一期——先能对话（2–4 个内聚改动）。**
会话表（id、渠道、渠道内会话、DSH session id、最后活跃时间）加流式聊天端点；
一个渠道适配器（Telegram，仅 owner 配对）；DSH `acp` 或 `sdk` 常驻；在现有控制台
加聊天区。验收：owner 从手机发消息，得到流式回答，能通过现有 `context build`
工具读权威上下文，且除 `inbox/` 外没有任何写入。

**二期——它能做事，但要审批。**
触发器表（cron/at/every）并投递到渠道；按触发器配工具白名单；审批卡片（Web +
渠道内联按钮）写入 run audit；主动轮次限定只读工具；压缩前记忆落盘只产出候选，
不直接改权威文件。

**三期——更像"你"。**
对候选做阈值化归并（"dreaming"）并产出人审报告；按触发器用 `promptfoo` 做评测；
如果附件/canvas 成为刚需，再在构建步骤后引入 `assistant-ui`；渠道接口撑过
Telegram 之后再接第二个渠道。

## 7. 风险与待定问题

- **许可证：** Khoj、basic-memory、Honcho、Open WebUI 是 AGPL 或带品牌限制，
  只能只读参考。n8n 是 Sustainable-Use，Langfuse 有 `ee/` 目录——可当可选工具，
  不能 vendor 进代码。
- **版本漂移：** DSH 在 `0.2.1-rc.2`，公网 `latest` 落后两个小版本。继续 pin 并
  校验；浮动 tag 是供应链和行为风险。
- **边界：** 带渠道入口和工具执行的助手，风险明显高于夜间维护作业。保持公开/
  私有切分：服务代码公开、上下文私有、凭据两者都不进。
- **范围纪律：** 对标产品卖的是执行环境（浏览器、云电脑、4000 个连接器）。Folio
  可信的优势是**可审计的个人记忆**，不是连接器数量。在这一点上竞争。
- **待 owner 决定：** 先做哪个渠道（Telegram 还是只做 Web）；主动轮次是否允许
  不经审批就写入；DSH 以 `acp`（服务端持会话）还是 `sdk`（客户端持历史）作为
  唯一集成点。

## 8. 参考来源（均于 2026-10-22 获取）

- OpenAI Dots：[发布报道](https://qazinform.com/news/openai-unveils-dots-as-metas-muse-reshapes-ai-assistant-race-9838bf)，
  [架构分析](https://devnavigator.com/2026/10/06/openai-dots-always-on-ai-agents/)
- xAI Grok Bot：[发布](https://blockchain.news/news/xai-launches-grok-bot)
- [OpenClaw](https://github.com/openclaw/openclaw)（MIT）及其
  [memory 文档](https://github.com/openclaw/openclaw/blob/main/docs/concepts/memory.md)
- [Hermes Agent](https://github.com/NousResearch/hermes-agent)（MIT）
- [Khoj](https://github.com/khoj-ai/khoj)（AGPL-3.0）、[LocalAGI](https://github.com/mudler/LocalAGI)（MIT，Go）
- 记忆类：[Letta](https://github.com/letta-ai/letta)、[Mem0](https://github.com/mem0ai/mem0)、
  [Graphiti](https://github.com/getzep/graphiti)、[basic-memory](https://github.com/basicmachines-co/basic-memory)（AGPL-3.0）
- UI：[assistant-ui](https://github.com/assistant-ui/assistant-ui)、[CopilotKit](https://github.com/CopilotKit/CopilotKit)、
  [Vercel AI SDK](https://github.com/vercel/ai)
- 渠道：[`go-telegram/bot`](https://github.com/go-telegram/bot)
- 队列：[river](https://github.com/riverqueue/river)、[asynq](https://github.com/hibiken/asynq)
- 评测：[promptfoo](https://github.com/promptfoo/promptfoo)、[Langfuse](https://github.com/langfuse/langfuse)
- DSH：`@deepseek-ai/dsh@0.2.1-rc.2` 中 `dsh-headless`、`dsh-sdk-app`、
  `dsh-acp-app`、`dsh-schedule`、`dsh-mcp-client`、`dsh-jobs`、
  `dsh-user-approval` 的本地包文档（MIT）
