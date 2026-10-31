# 订阅额度管理：magpie 的可借鉴点与 Folio 的落地路径

状态：Phase 1（只读额度面板）已实现；网关路由、配额失败延后、预算和
账号池仍是设计，未实现。快照日期 2026-10-29。
范围：只含公开信息；不记录任何私有订阅、账号、凭据或主机细节。
magpie 迭代很快，本文只对应当日 `main` 分支（核对到 `c3462d5`）与文档；
后续阶段实现前要重新核对。

## 实现状态

- **已实现**：`internal/quota` 读 magpie 的 `GET /v1/magpie/quotas?kept=1`，
  归一化卡片；`GET /api/quotas` 和 `POST /api/quotas/refresh` 由会话保护，
  刷新按钮 30 秒限流；Web 只读面板显示窗口、重置、读数时间、错误和 stale。
  配置是 `data/quotas.json`，key 文件相对 `/data`，0600。
- **未实现**：runner 走 magpie 网关（A1）、`rate_limited` 后的任务延后
  （A3）、预算、通知去重、账号池。第 6、7 节中这些部分仍是设计。
- 面板**不**改变任务调度；它只是把 magpie 已有的读数显示出来。因此本文
  开头“只加四件事”现在只完成了第一件。

## 结论先说

1. magpie 真正解决的不是“多模型协议转换”，而是把**订阅账号、额度窗口、
   路由和每客户端限额**做成了一个可观测的控制面。Folio 最该借的是这个
   模型，不是它的网关代码。
2. **不要复刻 magpie 的 vendor 额度抓取和账号池。** 它维护着几十个 agent、
   十几家订阅、5 小时/周窗口、credits、每日签到和远程共享；这是一条持续
   维护的护城河。Folio 自己写会很快过时，而且会把订阅凭据管理引到公开
   仓库的边界上。
3. 最省力的路径：把 magpie 放在 Folio 旁边，作为**可选的额度数据源**，
   需要时再作为**模型网关**。Folio 计划加四件事：额度面板、配额失败归因与
   延后、按任务类型选模型/路由组、通知；目前只落地了第一件（只读面板）。
   这样 Folio 仍是 Go 控制面、零第三方依赖，订阅凭据不进 Folio。
4. 如果不想引入 magpie：先做**只读额度卡 + 配额失败延后**；账户池
   （切换 `CODEX_HOME`/`DSH_HOME`、自己 failover）是后一阶段，且只在确实
   需要“Folio 自己挑账号”时做。
5. 额度数字必须带 `readAt`/`error`/`stale`；未知就是未知。不要用
   “100 − 已用 token / 估算配额”造一个假精确的百分比。这和 Folio
   现有 usage 的“不估算”规则是同一条原则。

## 1. magpie 做对了什么

magpie 的 README 把它概括为“每个 agent 的模型，一个地方”：一个本地网关
把 agent 的请求送到 API key、订阅账号或本地模型；额度用完时换下一个账号，
路由组再换下一个模型。和 Folio 相关的部分主要是下面几块。

### 1.1 额度卡：把异构供应商压成一个形状

`internal/provider/quotas.go` 的 `Quota` 和 `QuotaSpan` 是很好的抽象。
把两者归一化后，大致是：

```text
Quota
  provider, name, kind(subscription|plan|balance), plan, user
  windows[]: name, used, remaining, resetsAt, display,
             unlimited, amount, limit, unit, balance
  balance, resets, readAt, error
  lastServedAt
```

（`stale` 是缓存/展示层的状态，不是 magpie 的结构体字段。）

关键不是字段多，而是三件事：

- **百分比、计数、余额、纯文本**都能表达。Claude 给百分比，Codex 给窗口，
  预付费 key 给余额，插件可能只给一句“还剩 3 次”。credits/重置次数是
  vendor 特例，能用 `balance`/`resets`/`display` 表达就不另造字段。
- 每个窗口有 `resetsAt`（以及可选的 `span`），所以能回答“还有多久重置、什么时候
  可以再跑”，而不只是“现在用了多少”。
- `readAt` / `error` / `stale` 让读数的新旧和失败可见；读不到就说读不到。

对应到 Folio：额度面板不应该只有“剩余 42%”一个数，而应该是一张卡带窗口、
重置时间、读数时间和错误状态。

### 1.2 账户池：身份和凭据分开

magpie 把“账号”作为一等对象：同一订阅可以有多个账号，每个账号有
启用/停用、排序、每窗口的 usage cap、自己的名字（alias）。它从 agent 自己的
登录文件里发现账号，但不把凭据复制到自己的配置里；它按 agent 的方式读取/
刷新，路由时只说“用哪个账号”。

这个分离很关键：**Folio 可以知道“有哪几个订阅账号、各自还剩多少”，
但不需要拥有它们的 token。** 账号身份、额度、排序是配置/状态；凭据是
另一层，放在私有卷或外部 agent 的登录目录里。

### 1.3 路由：不是“选一个最强的”，而是“按额度花”

magpie 的 routing group 有多种策略：

| 模式 | 行为 | 适合 |
| --- | --- | --- |
| `smart` | 在低于 90% 的订阅里，选额度**最早重置**的；90%/98% 分档 | 交互式、不想浪费即将过期的额度 |
| `pace` | 每周剩余比例 ÷ 距离重置小时数，选最高的 | 批处理、想均匀用完一周 |
| `order` | 第一个用到 100% 或失败，再下一个 | 明确的优先级顺序 |
| `rotate` | 每轮下一个 | 分摊请求 |
| `usage` | 最少使用的先 | 简单的均衡 |

还有几个细节值得抄思路，不必抄代码：

- 90%/98% 分档：90% 以上排到后面，98% 以上再往后；不是二元的“有/没有”。
- 429 之后按 reset 时间 resting；没有 reset 就按 vendor 的 `Retry-After`，
  再没有就短退避；恢复读数后可以提前回来。
- 会话粘性：同一对话尽量留在同一个账号上，保护 vendor 的 prompt cache。
  Folio 的维护/报告是一次性调用，不需要这条。
- 安全过滤拒绝不算账号故障，不应 rest 账号。

### 1.4 限额：一个 key 可以有多扇窗

magpie 的 gateway key 可以带最多五个独立限额：5 小时（N 小时的特例）、
天、周、月、每 N 天；token 和/或美元成本；**所有窗口都还有余量才放行**；
预留按窗口扣；429 会说明最后重置的是哪一扇窗。这正好对应订阅本身的
“5 小时 + 周”两扇窗，也适合“给 Folio 一个专用 key，别让它吃满整个订阅”。

Folio 今天没有“给某类任务限额”的概念；如果走 magpie 网关，这一层可以
直接复用，不必在 Go 里重写。

### 1.5 读数的节奏：不阻塞路由，但也不假装新鲜

magpie 的原则是：

- 路由用**上次读到的值**，不为读额度而等；只有启动后第一次最多等 3 秒。
- 读数超过一分钟就在后台再读；失败后按退避重试。
- 手动刷新有 30 秒/卡片的限流，避免把 vendor 打爆。
- Claude 只读当前登录账号的 `/usage`；Codex 通过 vendor 读；读不到就
  保留上次值并标 `readAt`/`error`。

Folio 应该沿用同样的节奏：**额度是调度输入，不是请求路径上的阻塞依赖**。
读不到额度时，任务照常可以跑（或者按保守策略延后），但要明确显示“未知”。

### 1.6 可观测：token、成本、账号归因

magpie 的 Usage 页面按账号、按上游 key、按会话、按天统计 token 和成本；
缓存读/写、reasoning、调用次数都分开；没有价格的调用不按 0 计，而是
单独标“未定价”。它还记录每次调用实际由哪个账号回答，而不是从今天的
登录状态推断。

Folio 已经有很好的 token 账本（runner 自己报告的 uncached/cached/output，
未报告就留空），缺的是**账号/订阅维度**和**成本**。前者是额度管理的基础，
后者可以等价格表。

### 1.7 为什么 Folio 不能照搬

magpie 是网关：它看得到每个请求的响应头、usage、账号和重试；它也能改写
agent 的配置、接管登录。Folio 只看到子进程的 stdout/stderr，而且刻意不
拥有模型凭据。这个不对称决定了：

- magpie 能在请求级做 failover；Folio 只能在**任务级**做重试/延后。
- magpie 能拿到每个请求的账号；Folio 默认只能拿到 runner/model。
- magpie 能维护几十家 vendor 的额度读取；Folio 不应该背这个维护成本。

所以正确的借鉴是**额度模型和调度策略**，不是把网关搬进 Folio。

## 2. Folio 今天的位置

| 能力 | magpie | Folio 今天 | 影响 |
| --- | --- | --- | --- |
| 订阅账号 | 多账号/多订阅，身份与凭据分离 | 一个 `CODEX_HOME`、一个 `DSH_HOME`；从宿主导入配置 | 不能按账号选 |
| 额度读数 | 卡片 + 窗口 + 重置 + 历史 | 无 | 只能等 429 才知道 |
| 额度路由 | smart/pace/order/… + failover | 无；队列维护任务固定 Codex（手动可切 DSH） | 一个订阅耗尽就失败 |
| 失败恢复 | rest 到 reset，换账号/换模型 | `rate_limited` 归类后任务直接 failed | 夜间任务白跑，第二天再来 |
| 成本 | 按账号/key/会话，含未定价提示 | 只有 token，没有价格表 | 不知道花了多少 |
| 每任务限额 | gateway key 多窗口限额 | 无 | 一个任务可能吃满订阅 |
| 归因 | 每次调用记录账号 | 只有 runner/model | 不知道哪个订阅被消耗 |

具体到代码，有三个关键事实：

1. `internal/store/tasks.go` 的队列维护任务调用
   `RunAutomation("task", "", "codex")`：**排队的维护任务固定用 Codex**。
   `internal/store/kinds.go` 只允许 `report` 类型声明 runner/model，
   `maintenance` 类型明确禁止 runner/model。所以“按任务选订阅”今天连
   配置入口都没有。
2. `internal/context/worker.go` 的 `CodexFailureClass` 把 429 归为
   `rate_limited`（401/403 归为 `authentication`），
   `internal/store/tasks.go` 随后把任务标为 failed；没有解析 reset 时间，
   也没有自动重排。
3. 现有 usage 账本按 run 记录 runner/model 和 token；`GET /api/usage`
   聚合天/模型，但没有账号字段，也没有成本。额度管理需要在这个账本上
   加一层**读数**，而不是另造一套 token 统计。

这意味着路径 A 的一期可以**不改任务模型**：把 Codex 的默认 provider 指向
magpie，所有排队维护任务自然走网关；只有 `report` kind 能按 kind 选
model/group。要按维护任务的 kind 选订阅，才需要扩展 `task-kinds` 和
dispatch，单独排期。

## 3. 借什么，不借什么

| 项目 | 决定 | 理由 |
| --- | --- | --- |
| 额度卡（窗口/重置/余额/文本/新鲜度） | **借** | 异构供应商的最小公共形状；直接决定面板和调度 |
| 账号身份与凭据分离 | **借** | Folio 只存引用和状态，不碰 token |
| `smart` / `pace` / `order` | **借思路** | 批处理优先 `pace`，紧急任务 `smart`/`order` |
| 90%/98% 分档、rest 到 reset | **借** | 比二元“有/没有”稳，能解释为什么延后 |
| 多窗口限额 | **借** | 订阅就是 5 小时 + 周；可映射到任务类预算 |
| `readAt`/`error`/`stale`/未定价 | **借** | 和 Folio 的“不估算”规则一致 |
| 账号归因 | **借** | 至少要知道哪个订阅被消耗 |
| 协议转换网关（OpenAI/Anthropic/Gemini） | **不借** | Folio 不是 agent 网关，没有这个需求 |
| 60+ agent 配置改写 | **不借** | 维护成本极高，和 Folio 的边界无关 |
| 隐私 masking、图片/视频/搜索、Library、Profiles | **不借** | 不同产品面 |
| 自动切换 agent 登录 | **不借** | 改外部 agent 的凭据状态，风险高于收益 |
| 各家 vendor 的额度抓取/签到/暖机 | **不借**（至少一期） | 应该由 magpie 或操作者提供，Folio 不背 |
| 远程 magpie / 多机同步 | **以后再说** | 除非有多机需求；先用本地/私网 |

magpie 是 MIT；即便如此，建议只做外部集成，不 vendor 它的代码——它的价值在
持续维护的 vendor 适配和路由细节，不在可复制的一段逻辑。

## 4. 两条落地路径

### 路径 A（推荐）：magpie 做额度源，必要时做网关

```text
Folio task dispatcher
  ├─ runner: codex / dsh ──► magpie gateway（可选）──► 订阅账号 / API key
  └─ quota client ────────► GET /v1/magpie/quotas（专用 key）
```

职责划分：

| 层 | 负责 |
| --- | --- |
| Folio | 任务意图、优先级、off-peak、选哪个 kind/route、run 审计、任务延后、通知 |
| magpie | provider/账号、额度读取、账号选择、failover、gateway key 限额、成本账本 |
| 两者共享 | model/group id、额度快照、reset 时间 |

这样 Folio 不需要知道“怎么读 Claude 的 /usage”“ChatGPT 的 5 小时窗口
怎么算”；它只需要一个稳定的 JSON 形状和 reset 时间。magpie 的
`GET /v1/magpie/quotas` 返回 `{"object":"list","data":[...]}`；`?kept=1`
只读缓存、不问 vendor；`POST /v1/magpie/quotas/refresh` 有 30 秒/卡片限流。
这些都可以用一个很小的 stdlib HTTP client 接。

**建议的引入顺序（从低风险开始）：**

- **A0：只做额度源，不路由（已实现）。** magpie 跑在 NAS/私网，Folio 用专用 key
  读卡片，面板显示所有订阅；runner 仍直连原来的 Codex/DSH。先验证
  “额度数据可信、足够新、不会泄露”。前提是 magpie 必须能看到这些订阅的
  登录/账号（它自己 host 上的登录，或通过 remote magpie）；Folio 私有卷里
  的凭据不会自动出现在 magpie。某些读数可能不完整或偏旧（例如 Claude 只读
  当前登录账号），卡片会标 `readAt`/`error`。
- **A1：拿一个非关键 report kind 试网关。** 先用一个临时/受限 key，把该
  kind 的 model 指向 magpie 的 `group/...` 或 `provider/model`，确认 usage
  仍能被 Folio 的 Codex/DSH 解析、工具调用/流式行为不变，再考虑维护任务。
- **A2：正式给 Folio 一个专用 gateway key。** 用 magpie 的多窗口限额给
  Folio 设 token/成本上限；429 由 Folio 归类为“额度等待”。
- **A3：配额失败延后。** 基于 magpie 卡片或 429 里的 reset 时间，把
  P2/P3 任务重排到 reset 之后；P0/P1 通知并明确失败/降级。

**runner 怎么接到 magpie（A1 之后）：**

- **Codex**：在 `/data/codex/config.toml` 里把 `model_provider` 指向一个
  `model_providers.magpie` 块，字段用 `base_url`、`wire_api`（Codex 走
  Responses 就写 `responses`）、`env_key`、`requires_openai_auth = false`。
  `env_key` 指向 `/data/runtime/worker.env` 里的 magpie key；Folio 的
  `CodexConnectionArgs` 已经只转发这几个字段和选中的环境变量，不会把
  内联 token 带进命令行。
- **DSH**：在 profile patch 的 `llm-pi-ai` 里声明一个 magpie provider
  （base URL + key 环境变量），任务用 `--provider magpie`；key 放
  `/data/dsh/.env` 或 DSH 的凭据文件，权限 0600。
- 两条路都要先确认：magpie 的响应仍带 usage，Codex/DSH 的流式与工具调用
  不变。确认之前不要把维护任务切过去。

路径 A 的代价：多一个外部服务、多一个网络依赖、网关可能改变 runner 的
请求形状。收益：不用自己维护 vendor 额度读取和账号池；如果 runner 走网关，
magpie 的 Usage/Routing 页面还可以直接复用。

### 路径 B（自包含）：Folio 自己管账号池

只有在“不能/不想跑 magpie”时才考虑。最小形状：

```json
{
  "version": 1,
  "accounts": [
    {
      "id": "codex-a",
      "provider": "codex",
      "runner": "codex",
      "home": "/data/accounts/codex-a",
      "label": "Codex A",
      "enabled": true,
      "order": 1,
      "caps": {"5h": 90, "week": 90}
    }
  ]
}
```

需要新增的能力：

- 每账号一个 `CODEX_HOME`/`DSH_HOME`（Folio 现在全局注入一个）；
- 每账号的凭据导入/刷新（不能让 token 进公开仓库或聊天）；
- 一个 quota adapter（`magpie quota --json` 命令、HTTP、或操作者手填）；
- 账号选择 + 失败分类 + rest/failover；
- run 记录里的 account 字段。

这条路的工作量和风险都明显更高：凭据复制、profile 漂移、token 刷新竞争、
vendor 条款/封号风险。除非路径 A 被否决，不建议先做。

## 5. 数据与接口草案（路径 A 的最小版本）

### 5.1 配置

`data/quotas.json`（0600，私有卷）：

```json
{
  "version": 1,
  "source": {
    "kind": "magpie",
    "url": "http://magpie.internal:3425",
    "key_file": "secrets/magpie-quota-key",
    "timeout_seconds": 5,
    "max_age_seconds": 60
  }
}
```

- `url` 只允许 http/https，不能带 userinfo/query/fragment/path；远程用
  HTTPS 或私网/VPN，不能公网暴露。
- `key_file` 必须配置，相对 `/data`，必须是普通文件（不是 symlink），
  0600；永不返回浏览器、永不进日志。
- source 未配置时，面板显示“未配置”，任务照常运行，不把额度当成硬依赖。

### 5.2 归一化类型

```text
QuotaCard
  provider, provider_label, account, alias, kind, plan
  windows[]: name, used, remaining, resets_at, display,
             unlimited, text, amount, limit, unit, balance
  balance, read_at, error, stale
```

从 magpie 的 `data[]` 映射：

- `provider`、`name`（作为 `provider_label`）、`kind`、`plan`、`user`、
  `alias`、`windows`、`balance`、`readAt`、`error` 直接对应；
- `windows[].used`/`remaining` 是百分比，`display` 是 vendor 原文，
  `resetsAt` 是 RFC3339，`text` 表示没有数字读数；
- 只保留 bounded 字段，不持久化 magpie 的完整响应。

### 5.3 API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/quotas` | 会话保护；返回 normalized cards + source 状态；优先内存缓存，过期才读 magpie 的 `?kept=1` |
| `POST` | `/api/quotas/refresh` | 会话 + CSRF；30 秒限流；429 带 `Retry-After` 和 `retry_after_seconds` |
| `GET` | `/api/quotas/history?days=30` | 以后；先直接转发 magpie 的 history，不自己建历史 |

`GET` 的 source 故障仍是 200 + 固定 `error` code（有缓存时 `stale=true`）；
只有刷新限流是 429。错误码用固定 class，不把 vendor 原文回给浏览器：

- `quota_invalid`（`data/quotas.json` 不合法）
- `quota_key_unavailable`（key 文件缺失/不可读）
- `quota_unauthorized`（key 无效/被限制）
- `quota_unavailable`（不可达/超时）
- `quota_invalid_response`（响应形状/大小不合法）
- `quota_rate_limited`（magpie 返回 429）
- `quota_refresh_limited`（Folio 本地 30 秒限流）

### 5.4 run 记录（未实现）

如果以后把 runner 接到 magpie，最小增量是：

```json
{
  "runner": "codex",
  "model": "group/cheap",
  "quota_source": "magpie",
  "quota_read_at": "2026-10-29T02:00:00Z"
}
```

不要默认写 account：如果走 magpie 的透明 failover，真正的账号在 magpie
的账本里；Folio 不要从“当前登录”推断。若确实要 per-run 归因，见 §6.6。

## 6. 调度与流程

### 6.1 派发前

派发前预检是**可选**的：Folio 只有知道候选账号/路由组成员时才能判断。
如果任务是 `group/...` 而 Folio 不知道组里有哪些账号，不要猜；直接靠
失败后的 429 + reset 更稳。

- 有额度源且知道候选账号时：读取（或读缓存）卡片；若所有候选账号都已被
  某一扇窗挡住，且任务可延后（P2/P3），设 `not_before = 最早 reset + 抖动`，
  reason `quota_wait`，发一次通知。
- P0/P1：不静默延后。按策略选下一个候选/降级模型；没有候选就明确失败，
  通知 owner。
- 额度源不可用：不要阻塞任务；按“未知”处理，记录 `quota_unknown`，
  任务照跑（或按保守策略延后，取决于 owner 配置）。

### 6.2 失败后

- runner 返回 `rate_limited` 时，先读 `?kept=1` 的缓存（不问 vendor），
  找达到 cap 且 `resetsAt` 在未来的窗口：
  - 找到：任务回到 `queued`，`not_before = resetsAt + 60s`，
    reason `quota_wait`；`attempts` 有上限（例如 5 次），避免永久循环；
  - 缓存里没有可用 reset：再做一次受 30 秒/卡片限流的刷新；仍没有就
    保持今天的 `failed`，reason 仍是 `runner_rate_limited`。
- 如果走 magpie 网关，429 的 body 可能带 reset；解析时只接受 bounded
  的时间/枚举，不存原始 stderr。
- 不要把“额度等待”算成内容失败；它不应该推进 maintenance checkpoint，
  也不应该触发“任务失败”的告警语气。

### 6.3 预算

两条路，二选一，不要同时做：

- **magpie key 限额**（路径 A 推荐）：给 Folio 一个专用 key，设
  `5h`/`week` 的 token 或成本上限；magpie 在请求前拒绝并给 429。
  Folio 只需把 429 归因为额度等待。如果要按任务类分预算，就给每类配
  一个 key（以及对应的 runner 配置）；一期先用一个 key 做服务级上限，
  不要为此把 runner 配置复杂化。
- **Folio 自己的 token 预算**（不依赖 magpie）：从现有 run ledger 聚合
  每窗口 token，达到 cap 就延后。成本预算需要价格表，先不做；token
  预算只能近似“订阅额度”，要明确标注为 Folio 自己的预算，不是 vendor
  配额。

### 6.4 通知

复用现有 Bark，不新增渠道。值得发的四类：

1. 低额度：某窗口 ≥ 90%，且队列里有 P2/P3 任务；
2. 重置前有浪费：窗口在 N 小时内重置，但使用率低于阈值（magpie 的
   “reminder before a window renews with much of it unused”思路）；
3. 任务因额度延后：任务 id、原因、预计恢复时间；
4. failover/降级（如果做路径 B）：从哪个账号换到哪个，避免静默换模型。

通知去重：按 `account+window+resetsAt` 记录已发，重置后清掉；不要把
账号邮箱写进通知正文，用 alias。

### 6.5 每周复盘

- 如果 runner 走网关：用 magpie 的 Usage 页面看按账号/按 key 的 token 与
  成本；否则只能用 Folio 的 run ledger 看 token，账号/成本仍是未知。
- 对照 Folio 的 run ledger，回答“哪类任务吃了哪个订阅的周额度”；
- 把 P3/批处理尽量安排在 reset 前、且用 `pace` 策略，避免周初把周额度
  打满；
- 如果某个订阅长期用不完，考虑把它设成 `smart` 的首选，或降低其他订阅
  的优先级。

### 6.6 归因取舍

- **透明 failover（magpie 选账号）**：Folio 只能记 runner/model/route；
  真正的账号在 magpie 的 usage ledger。想按 run 归因，需要给每次 run 带
  一个 session id，再去 magpie 查；或者接受“按天/按 key”而不是“按 run”。
- **严格 per-run 账号**：Folio 必须 pin 账号（`X-Magpie-Account`）并自己
  做 failover，或者走路径 B。前者会失去 magpie 的自动换账号，后者工作量
  更大。
- 一期建议：**不承诺 per-run 账号**；面板按账号展示，run 记录只记
  model/route。这个取舍要在 UI 上写清楚，不要让用户以为“run 里没账号 =
  没花钱”。

## 7. 分阶段计划

| 阶段 | 内容 | 验收 | 预估 |
| --- | --- | --- | --- |
| 0 | 部署 magpie（额度源模式），配专用 key；如果准备让 runner 走网关，先用一个小请求验证 Codex/DSH 的 usage 仍可解析 | 额度 API 能返回卡片；若走网关，一次 report run 后 Folio run 记录仍有 reported usage | 0.5–1 天 |
| 1 | `internal/quota` client + `/api/quotas` + Web 卡片 + 测试 | **已实现**：卡片显示窗口/重置/读数时间/错误；未配置时降级；无凭据泄露 | 1–2 天 |
| 2 | `rate_limited` → 额度等待/延后 + 通知 + 去重 + 测试 | 模拟 429 + reset：P2 任务回到 queued，`not_before` 正确；P0 不静默延后；attempts 有上限 | 1–2 天 |
| 3 | 预算（优先 magpie key 限额；或 Folio token 预算） | 超预算任务被拒绝/延后；reset 后恢复；账本可解释 | 2–3 天 |
| 4 | （仅路径 B）账号池、per-account home、failover | 两个 Codex profile 可切换；失败换账号；run 记录 account | 1–2 周 |

测试策略：

- quota client 用 `httptest`：正常卡片、stale、401、超时、超大 body、
  非法 URL、重复卡片；
- Web API：未登录 401、跨站/CSRF 现状、错误码不泄露 vendor 原文；
- 调度：假 runner 返回 `rate_limited`，假卡片给 reset；断言任务状态、
  `not_before`、通知次数、checkpoint 不推进；
- 不引入真实 vendor 调用；fixture 全部合成。

## 8. 安全与运维边界

- **专用 key**：开启 quota 可见性（`magpie gateway-key quotas <id> on`），
  并按模型/账号收窄。注意它是**网关 key**，不是只读的 quota token：仍可发
  模型请求，所以只交给 Folio 的 quota client，不要给 runner、浏览器或其他
  进程。不要用管理 key，也不要用 runner 的同一个 key。
- **网络**：magpie 只绑 loopback 或私网；Folio 与它同私网/VPN；远程用
  HTTPS；不把 3425 暴露公网。
- **凭据**：key 文件 0600，永不返回浏览器、永不进 run 记录/日志/通知；
  `data/quotas.json` 只存路径，不存 key 值。
- **账号隐私**：邮箱/登录名只给已登录的 owner 会话；通知用 alias；
  公开仓库和测试 fixture 用 `a@example.com` 这类合成值。
- **职责**：额度只影响调度，不影响 trust/approval。低额度不能成为
  “换到不可信 provider”的理由；信任规则仍然优先。
- **降级**：magpie 挂了，Folio 的任务不应全部失败；额度未知时按配置
  选择“照跑/保守延后”，并把状态显示出来。

## 9. 待 owner 决定

1. magpie 是否常驻（NAS/私网），以及 Folio 是否接受它作为可选依赖？
2. 先只做额度可见，还是同时把 runner 接到 magpie 网关？
3. 需要管哪些订阅、各几个账号？是否有 5 小时 + 周两扇窗之外的额度
   （credits、月付、预付费余额）？
4. 是否要求 per-run 账号归因？如果要求，透明 failover 就不够，需要
   pin 账号或走路径 B。
5. 预算用 token 还是钱？如果是钱，价格表和币种由谁维护？
6. P0/P1 额度不足时：失败、降级模型，还是排队等 reset？

## 10. 参考

- magpie：[README](https://github.com/yetone/magpie)、
  [reference](https://github.com/yetone/magpie/blob/main/docs/reference.md)
- magpie 源码：
  [`internal/provider/quotas.go`](https://github.com/yetone/magpie/blob/main/internal/provider/quotas.go)、
  [`internal/provider/subscription_usage.go`](https://github.com/yetone/magpie/blob/main/internal/provider/subscription_usage.go)、
  [`internal/provider/routing.go`](https://github.com/yetone/magpie/blob/main/internal/provider/routing.go)、
  [`internal/gateway/gateway.go`](https://github.com/yetone/magpie/blob/main/internal/gateway/gateway.go)
- Folio：[event-driven assistant](event-driven-assistant.md) 的
  priority/ledger 设计、[context maintenance](context-maintenance.md)、
  [`internal/quota`](../internal/quota/quota.go)、
  [`internal/store/tasks.go`](../internal/store/tasks.go)、
  [`internal/context/worker.go`](../internal/context/worker.go)、
  [`internal/dshconfig/config.go`](../internal/dshconfig/config.go)
