# AnyClaw 架构与功能模块拆分

## 文档目标

这份文档不是按目录树机械罗列，而是基于当前源码里的真实职责，把 AnyClaw 按功能拆成可演进的模块，方便后续做：

- 架构收口
- 包归属梳理
- ownership 划分
- 代码级拆包或拆仓

判断依据主要来自当前几个核心入口：

- `pkg/runtime` 的启动装配链路
- `pkg/gateway` 的对外 API 与状态管理
- `pkg/capability/agents` 的执行内核
- `pkg/capability/tools` / `pkg/capability/skills` / `pkg/extensions/plugin` 的能力注入方式
- `ui/` 与 `cmd/anyclaw-desktop/` 的展示层结构

## 1. 从源码看当前主链路

AnyClaw 现在的主流程可以概括成下面这条链路：

```text
配置/启动
  -> runtime 装配
  -> Gateway / Desktop / Web UI 对外暴露
  -> Session / Task / RuntimePool 接住请求
  -> Agent 执行
  -> LLM + Memory + Context + Tools + Skills + Plugins
  -> 审批 / 事件 / 审计 / 状态落盘
```

其中最关键的装配顺序已经写在 `runtime.Bootstrap` 里，顺序基本是：

1. `config`
2. `security / secrets / audit`
3. `storage / workspace`
4. `qmd`
5. `skills`
6. `tools`
7. `plugins`
8. `llm`
9. `agent`
10. `orchestrator`

这说明当前系统虽然目录很多，但核心其实可以收敛为几类大能力：装配、接入、执行、扩展、存储、治理。

## 2. 推荐的一级功能模块

建议把当前源码按功能收口为 9 个一级模块。

| 模块 | 主要职责 | 当前对应包/目录 |
| --- | --- | --- |
| 1. 客户端与展示层 | Web 控制台、桌面壳、终端 CLI 展示 | `ui/`, `cmd/anyclaw/`, `cmd/anyclaw-desktop/`, `cmd/anyclaw-desktop/frontend/` |
| 2. 运行时装配层 | 配置加载、启动编排、工作区初始化、环境自检 | `pkg/runtime`, `pkg/runtime/bootstrap`, `pkg/config`, `pkg/workspace`, `pkg/input/cli/setup` |
| 3. Agent 内核 | 对话执行、Prompt、上下文管理、记忆、模型调用 | `pkg/capability/agents`, `pkg/runtime/context`, `pkg/state/memory`, `pkg/embedding`, `pkg/capability/models` |
| 4. 工具与执行平台 | 文件/Shell/浏览器/桌面操作、CLI Hub、沙箱、多模态工具 | `pkg/capability/tools`, `pkg/clihub`, `pkg/vision`, `pkg/media`, `pkg/canvas`, `pkg/qmd`, `pkg/isolation`, `pkg/runtime/execution/verification` |
| 5. 编排与任务流 | 多 Agent 编排、任务执行、工作流、计划、定时任务 | `pkg/runtime/orchestrator`, `pkg/runtime/taskrunner`, `pkg/capability/workflows`, `pkg/runtime/execution/schedule`, `pkg/route`, `pkg/runtime/pool.go` |
| 6. 接入网关与会话层 | HTTP/WS API、OpenAI 兼容 API、会话状态、渠道接入、事件流 | `pkg/gateway`, `pkg/gateway/transport`, `pkg/gateway/ingress`, `pkg/gateway/intake`, `pkg/gateway/events`, `pkg/runtime/channelbridge`, `pkg/runtime/sessionbridge`, `pkg/input/channels`, `pkg/gateway/resources/discovery` |
| 7. 扩展生态层 | Skills、Plugins、MCP、Agent Store、市场能力 | `pkg/capability/skills`, `pkg/extensions/plugin`, `pkg/extensions/mcp`, `pkg/extensions/adapters`, `pkg/capability/catalogs`, `skills/`, `extensions/` |
| 8. 应用自动化与设备协同 | 桌面协议、UI 学习、节点协作、客户端配对 | `pkg/runtime/execution/desktop`, `pkg/gateway/resources/nodes`, `pkg/gateway/resources/discovery`, `pkg/gateway/transport/client`, `pkg/input/channels` |
| 9. 平台基础设施与治理 | 安全、密钥、审计、可观测、存储、索引、基础 API | `pkg/gateway/auth/security`, `pkg/state/policy/secrets`, `pkg/state/audit`, `pkg/state/observability`, `pkg/sqlite`, `pkg/vec`, `pkg/index`, `pkg/api`, `pkg/gateway/events` |

## 3. 每个模块应该如何理解

### 3.1 客户端与展示层

这一层只负责“人如何使用 AnyClaw”，不应该承载核心业务规则。

- `ui/` 是当前主 Web 控制台，页面已经按 Chat / Channels / Market / Studio 切分
- `cmd/anyclaw-desktop/` 是桌面壳，本质上是启动并承载 Gateway Dashboard
- `cmd/anyclaw/` 承载终端 CLI 入口与本地命令展示

建议边界：

- 只做展示、交互编排、调用 API
- 不直接持有 Agent 业务逻辑
- 所有核心状态应从 Gateway 或 runtime 派生

### 3.2 运行时装配层

这一层是系统的“总装厂”，`pkg/runtime` 是当前最核心的装配中心。

职责包括：

- 加载配置与 profile
- 解析工作目录
- 初始化 secrets / audit / memory / qmd
- 加载 skills / tools / plugins
- 创建 LLM client、Agent、Orchestrator

建议边界：

- `runtime` 只负责装配，不负责具体业务策略
- `pkg/config` / `pkg/input/cli/setup` / `pkg/workspace` / `pkg/runtime/bootstrap` 都应围绕启动与环境检查服务
- 不把 HTTP、UI、渠道逻辑继续下沉到这里

### 3.3 Agent 内核

这一层是 AnyClaw 的“思考与执行核心”。

核心职责：

- `pkg/capability/agents`: 单 Agent 执行、工具调用循环、上下文预算、偏好学习
- `pkg/runtime/context`: 上下文预算、窗口与运行时上下文辅助
- `pkg/state/memory`: 本地记忆
- `pkg/capability/models`: 模型客户端、failover、多模态与流式调用
- `pkg/embedding`: 嵌入模型能力

建议边界：

- 这一层不关心 HTTP、页面、渠道协议
- 只暴露“输入消息 -> 执行 -> 输出结果”的能力
- 工具、技能、插件通过接口注入，不反向依赖 Gateway

### 3.4 工具与执行平台

这一层是 AnyClaw 从“会聊天”变成“能做事”的关键。

职责包括：

- `pkg/capability/tools`: 内置工具注册表，包含文件、命令、浏览器、桌面、审批、策略等
- `pkg/clihub` / `pkg/extensions/adapters/cli`: CLI-Anything 与本地 CLI 能力接入
- `pkg/vision` / `pkg/media`: 图像、视频、音频处理与理解
- `pkg/canvas`: UI/画布输出
- `pkg/qmd`: 轻量结构化数据服务
- `pkg/isolation`: 隔离与共享边界

建议边界：

- 统一沉淀为“执行平台”
- 所有可调用能力都通过 registry 暴露
- 审批、策略、沙箱都应该在这一层闭环

### 3.5 编排与任务流

这一层负责把“一个请求”变成“可分解、可追踪、可恢复的执行过程”。

职责包括：

- `pkg/runtime/orchestrator`: 多 Agent 分解与协作
- `pkg/runtime/taskrunner` / `pkg/state/task`: 任务执行与任务状态
- `pkg/capability/workflows`: 图工作流、触发器、执行上下文
- `pkg/runtime/execution/schedule`: 定时任务
- `pkg/route`: 请求路由和低 token 路径优化
- `pkg/runtime/taskrunner` / `pkg/runtime/pool.go`: 当前任务执行与 runtime pool 也承担了这层职责

建议边界：

- 任务规划、执行、恢复、调度应统一归属这里
- 不建议长期把任务核心逻辑继续散落在 `pkg/gateway`
- `gateway` 更适合作为入口，不适合作为任务域中心

### 3.6 接入网关与会话层

这一层负责“系统如何接住外部请求并把结果送回去”。

职责包括：

- `pkg/gateway`: HTTP/WS API、状态聚合、审批接口、市场接口、OpenAI 兼容接口
- `pkg/gateway/transport`: WebSocket、回复分发、控制台 UI 与调度 UI 传输层
- `pkg/gateway/ingress` / `pkg/gateway/intake`: 渠道入口、webhook、entry policy 与输入归一化
- `pkg/runtime/channelbridge` / `pkg/input/channels`: 渠道运行时桥接与渠道契约
- `pkg/runtime/sessionbridge` / `pkg/state`: 会话桥接与会话持久化
- `pkg/gateway/resources/discovery`: 远程资源发现

建议边界：

- Gateway 负责协议适配、鉴权、状态聚合
- 会话和渠道模型应从 Gateway 内部逐步抽到独立子域
- 所有外部入口都先落到这一层，再进入任务或 Agent 内核

### 3.7 扩展生态层

这一层负责让 AnyClaw 可持续扩展，而不是靠修改核心代码加能力。

职责包括：

- `pkg/capability/skills`: 技能定义、加载、工具注册
- `pkg/extensions/plugin`: 插件 manifest、加载、签名、市场、MCP bridge、App 插件
- `pkg/extensions/mcp`: MCP client/server/registry
- `pkg/capability/catalogs`: Agent 包安装与市场
- `pkg/extensions/adapters`: 兼容 `extensions/` 目录与内置 adapter 的扩展体系

建议边界：

- Skill / Plugin / MCP / Agent Store 应被视为同一个“扩展平台”的不同形态
- 统一权限、签名、安装、生命周期规则
- 新能力优先走扩展入口，而不是直接塞入 core

### 3.8 应用自动化与设备协同

这一层是 AnyClaw 面向桌面应用、节点设备、绑定关系的能力域。

职责包括：

- `pkg/runtime/execution/desktop`: 桌面执行与验证链路
- `pkg/gateway/resources/nodes`: 节点资源与发现模型
- `pkg/gateway/transport/client`: 远程客户端与配对传输
- `pkg/input/channels`: 外部渠道契约与适配边界

建议边界：

- 这是一条独立业务线，不建议继续散落到 tools / plugin / gateway
- 未来如果要做“桌面执行平台”或“手机节点”能力，这一层会成为独立子系统

### 3.9 平台基础设施与治理

这一层提供通用底座，不应该反向依赖业务域。

职责包括：

- `pkg/gateway/auth/security`, `pkg/state/policy/secrets`, `pkg/state/audit`: 安全、密钥、审计
- `pkg/state/observability`: health / metrics / tracing / pprof
- `pkg/sqlite`, `pkg/vec`, `pkg/index`: 存储、向量、索引
- `pkg/api`: 向量检索 API
- `pkg/gateway/events`: 事件总线

建议边界：

- 只提供通用能力
- 不持有上层产品状态
- 被 runtime、gateway、agent、tools 等复用

## 4. 建议的依赖方向

建议把依赖关系收敛成下面这条单向链路：

```text
客户端与展示层
  -> 接入网关与会话层
  -> 编排与任务流
  -> Agent 内核
  -> 工具与执行平台 / 扩展生态层 / 应用自动化与设备协同
  -> 平台基础设施与治理
```

具体规则：

1. `runtime` 只做装配，不承载具体业务逻辑。
2. `gateway` 只做入口、鉴权、聚合、状态外发，不吞并任务域和会话域。
3. `agent` 不直接依赖 UI、Gateway handler、前端页面。
4. `tools` 不依赖 `gateway`；审批和策略通过 hook / interface 注入。
5. `skills` / `plugins` 依赖工具契约，不依赖具体 HTTP 实现。
6. `memory` / `embedding` / `vec` / `sqlite` 必须处在低层，避免反向引用上层业务。

## 5. 当前源码里最需要收口的重叠区

下面这些地方说明当前代码已经出现“功能上属于一个模块，但代码上分散或重名”的情况，后续重构建议优先处理。

### 5.1 渠道契约、运行时桥接与 Gateway glue

现状：

- `pkg/input/channels` 定义渠道契约和输入侧适配边界
- `pkg/runtime/channelbridge` 负责运行时桥接
- `pkg/gateway/ingress` 与 `pkg/gateway/gateway_channel_*.go` 承载 HTTP/webhook/control-plane glue

建议：

- 保持“契约、运行时、协议入口”三层边界清晰
- 后续新增渠道优先接入 `pkg/input/channels` 和 `pkg/runtime/channelbridge`
- Gateway 只保留协议适配和状态聚合，不继续沉淀渠道业务规则

### 5.2 Agent 执行内核与管理边界

现状：

- `pkg/capability/agents` 是当前真正的执行内核
- Agent profile 管理主要在 `pkg/config` 和 Gateway 控制面完成

建议：

- 避免再引入新的平行 Agent 管理包
- 能迁移就迁移到 `pkg/capability/agents` / `pkg/runtime/orchestrator`

### 5.3 上下文、记忆与检索边界

现状：

- `pkg/runtime/context` 处理短生命周期运行时上下文、窗口和预算
- `pkg/state/memory` 处理长期记忆
- `pkg/embedding` / `pkg/vec` / `pkg/index` 处理检索和索引能力

建议：

- 明确短期上下文、长期记忆、向量检索三个概念边界
- 避免后续再引入平行的 context 包名造成误解

### 5.4 模型访问层

现状：

- `pkg/capability/models` 承载模型客户端、failover、stream、多模态能力
- `pkg/api/openai` 承载 OpenAI 兼容 API 适配

建议：

- 继续把 provider adapter、wrapper、failover、multimodal 能力收口到 `pkg/capability/models`

### 5.5 Session 相关实现分散

现状：

- `pkg/runtime/sessionbridge`
- `pkg/gateway/transport/sessions.go`
- `pkg/gateway/gateway_session_*.go`
- `pkg/state/conversation_sessions.go`

建议：

- 抽成单独的会话子域
- Gateway 只调用 session service，不自己长期持有完整实现

### 5.6 扩展体系有三套概念

现状：

- `pkg/extensions/plugin`
- `pkg/extensions/mcp`
- `pkg/extensions/adapters`
- `pkg/capability/catalogs`

建议：

- 统一为“扩展平台”
- 区分清楚插件、扩展、Agent 包只是不同 artifact，不是三套独立平台

### 5.7 对外 API 面不止一套

现状：

- `pkg/gateway`
- `pkg/api`
- `pkg/gateway/transport/controlui`

建议：

- `gateway` 作为产品主 API
- `api` 保留为专用向量检索服务或独立基础能力
- `controlui` 保持为 Gateway 的 UI 传输子域，不再额外引入平行 Web 包

## 6. 如果要真正动代码，推荐的拆分优先级

### 第一阶段：先收口命名和边界

- 收口 `input/channels`、`runtime/channelbridge`、Gateway channel glue 的边界
- 明确 `pkg/capability/agents` 是 Agent 执行内核
- 理清 `runtime/context`、`state/memory`、`embedding` / `vec` / `index`
- 把模型访问继续统一到 `pkg/capability/models`

### 第二阶段：把领域从 Gateway 中抽出来

- 把 session 管理抽成独立模块
- 把 task/runtimes 从 `pkg/gateway` 抽成任务域服务
- 让 Gateway 回到“协议入口”角色

### 第三阶段：统一扩展平台

- 技能、插件、MCP、Agent Store 统一抽象
- 收敛权限、签名、安装、市场、生命周期

### 第四阶段：把桌面执行链路独立出来

- `apps` + `tools.desktop` + `vision/media` 形成独立执行子系统
- 为后续桌面宠物、桌面自动化、宿主节点能力做准备

## 7. 一句话结论

AnyClaw 当前最合理的拆法，不是按 Go 包逐个拆，而是按下面这 9 个功能模块收口：

1. 客户端与展示层
2. 运行时装配层
3. Agent 内核
4. 工具与执行平台
5. 编排与任务流
6. 接入网关与会话层
7. 扩展生态层
8. 应用自动化与设备协同
9. 平台基础设施与治理

如果后续要继续重构代码，优先处理重名/重叠模块，再把 `gateway` 内部混合的任务、会话、运行时逻辑逐步抽出来，整体架构会清晰很多。
