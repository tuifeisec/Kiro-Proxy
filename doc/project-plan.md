# Kiro Proxy 项目规划文档

> 基于 ai-proxy 的缺陷分析 + 实际抓包数据，从零设计

---

## 一、项目目标

构建一个 **Kiro IDE MITM 代理**，核心功能：

1. **拦截** Kiro 发往 AWS 的 AI 请求
2. **转换** 为 OpenAI / Anthropic 格式，转发给自定义后端
3. **转换响应** 回 Kiro 的 AWS Event Stream 格式，无缝返回给 IDE
4. **管理面板**（Web UI）：配置模型、查看流量、统计用量

---

## 二、ai-proxy 的核心缺陷（我们要解决的）

### 缺陷 1：格式转换慢（最严重）

**根因**：每个请求在 `setupInterceptor` 和 `forwardRequest` 中对同一个 body 做 **5 次** `io.ReadAll + json.Unmarshal`。Kiro 请求体可达 400KB+（含完整工具定义），每次反序列化耗时 150~400ms，全部叠加在上游请求开始之前。

**我们的方案**：
- 一次读取，结构化解析，后续全程复用
- 工具定义做 **内容哈希缓存**：同一套工具定义只转换一次，后续命中缓存直接复用
- 转换结果做 **对象池**，避免 GC 压力

### 缺陷 2：无法并发（第二严重）

**根因**：`kiroLimiter` 对同一 `conversationId` 强制串行（`defaultKiroConversationConcurrency = 1`），导致 Kiro 并发发出的 `intent-classification` + `vibe` 两个请求互相阻塞。虽然 classifier 有绕过逻辑，但实现复杂且有竞态窗口（150ms 等待）。

**我们的方案**：
- **不做会话级串行限制**，让请求自然并发
- classifier 请求识别后直接走快速路径，不进入任何队列
- 用 `context` 超时控制，不用信号量阻塞

### 缺陷 3：SQLite 热路径（体感卡顿）

**根因**：单连接 SQLite，每次请求都触发：写日志 + 清理 24h 数据 + 7 次聚合统计查询。状态栏 `/getUsageLimits` 也走全量统计。

**我们的方案**：
- 统计数据用 **内存计数器**（原子操作），定期异步刷盘
- 日志写入用 **批量写入 + 独立 goroutine**，不阻塞请求链路
- `/getUsageLimits` 直接读内存计数器，不查数据库

### 缺陷 4：扩展侧日志 tail 拖慢 VS Code

**根因**：`fs.watch + setInterval(1000ms)` 持续 append 到 OutputChannel，长期运行后 VS Code 扩展宿主越来越重。

**我们的方案**：
- 不做 VS Code 扩展，用独立 Web UI（浏览器）
- 日志通过 WebSocket 实时推送，不轮询文件

---

## 三、整体架构

```
┌─────────────────────────────────────────────────────────┐
│                    Kiro IDE                             │
└──────────────────────────┬──────────────────────────────┘
                           │ HTTPS (经代理)
                           ▼
┌─────────────────────────────────────────────────────────┐
│              MITM Proxy  :18081                         │
│                                                         │
│  ┌─────────────┐    ┌──────────────┐    ┌───────────┐  │
│  │  Interceptor │───▶│  Converter   │───▶│ Forwarder │  │
│  │  (goproxy)  │    │  (一次解析)  │    │ (并发转发) │  │
│  └─────────────┘    └──────────────┘    └─────┬─────┘  │
│                                               │        │
│  ┌─────────────────────────────────────────── │ ─────┐ │
│  │  Stream Converter                          │      │ │
│  │  OpenAI SSE / Anthropic SSE                │      │ │
│  │  → AWS Event Stream 二进制帧               ◀──────┘ │
│  └─────────────────────────────────────────────────────┘
│                           │
│  ┌────────────────────────▼────────────────────────────┐ │
│  │  内存统计 + 异步日志写入                             │ │
│  └─────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
                           │ WebSocket
                           ▼
┌─────────────────────────────────────────────────────────┐
│         Web UI  :18082  (Vue3 + Tailwind)               │
│                                                         │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  │
│  │  仪表盘      │  │  模型配置    │  │  流量抓包    │  │
│  │  (统计/状态) │  │  (渠道管理)  │  │  (实时日志)  │  │
│  └──────────────┘  └──────────────┘  └──────────────┘  │
└─────────────────────────────────────────────────────────┘
```

---

## 四、目录结构规划

```
kiro-proxy/
├── main.go                    # 入口，启动代理 + Web UI
├── config.yaml                # 配置文件
│
├── proxy/                     # MITM 代理核心
│   ├── server.go              # goproxy 初始化、拦截器注册
│   ├── interceptor.go         # 请求拦截、preflight 解析（一次读取）
│   ├── forwarder.go           # 转发逻辑、重试、并发控制
│   ├── ca_install.go          # CA 证书安装 API
│   ├── hub.go                 # WebSocket 广播中心
│   └── logger.go              # 流量日志（内存 + 异步写盘）
│
├── converter/                 # 格式转换层（核心）
│   ├── kiro_parse.go          # Kiro 请求解析（一次解析，结构化）
│   ├── kiro_to_openai.go      # Kiro → OpenAI Chat Completions
│   ├── kiro_to_anthropic.go   # Kiro → Anthropic Messages
│   ├── stream_to_kiro.go      # OpenAI/Anthropic SSE → AWS Event Stream
│   ├── event_frame.go         # AWS Event Stream 二进制帧构建
│   ├── tool_cache.go          # 工具定义转换缓存（哈希 → 结果）
│   └── types.go               # 共享类型定义
│
├── router/                    # 路由层
│   ├── router.go              # 模型映射、渠道选择
│   └── model_map.go           # 内置模型名映射表
│
├── store/                     # 存储层
│   ├── db.go                  # SQLite（仅持久化配置，不在热路径）
│   ├── channel.go             # 渠道 CRUD
│   ├── mapping.go             # 模型映射 CRUD
│   └── stats.go               # 内存统计计数器 + 异步刷盘
│
├── api/                       # Web UI 后端 API
│   ├── server.go              # HTTP 路由注册
│   ├── channels.go            # 渠道管理接口
│   ├── mappings.go            # 模型映射接口
│   ├── stats.go               # 统计接口（读内存）
│   └── logs.go                # 日志接口
│
├── rules/
│   └── config.go              # 配置文件结构
│
├── certs/                     # CA 证书
│   ├── ca.crt
│   └── ca.key
│
├── frontend-inspector/        # 抓包观察前端（保留，用于协议研究）
│   └── src/App.vue
│
├── frontend/                  # 主控制面板前端（新建）
│   └── src/
│       ├── App.vue
│       ├── views/
│       │   ├── Dashboard.vue  # 仪表盘
│       │   ├── Models.vue     # 模型配置
│       │   └── Inspector.vue  # 流量抓包
│       └── components/
│
├── tools/
│   └── genca/main.go          # CA 生成工具
│
└── doc/                       # 文档
    ├── kiro-protocol.md
    ├── format-conversion.md
    ├── project-plan.md        # 本文件
    └── ai-proxy/              # 参考项目
```

---

## 五、转换层设计（解决慢的核心）

### 5.1 一次解析原则

```go
// 请求进入时只做一次 io.ReadAll + json.Unmarshal
type Preflight struct {
    Body           []byte
    Parsed         *KiroRequest   // 结构化，不是 map[string]interface{}
    SourceModel    string
    ConversationID string
    IsClassifier   bool
    ToolsHash      string         // 工具定义的内容哈希，用于缓存命中
}
```

### 5.2 工具定义缓存

Kiro 每次请求都携带完整工具定义（~300KB），但工具定义几乎不变。

```go
// 工具缓存：hash(tools_json) → 转换后的 OpenAI/Anthropic tools
type ToolCache struct {
    mu    sync.RWMutex
    cache map[string]CachedTools  // key = sha256(tools_json)
}

type CachedTools struct {
    OpenAI    []byte    // 序列化好的 JSON
    Anthropic []byte
    CreatedAt time.Time
}
```

**预期效果**：工具定义转换从每次 ~50ms 降到 ~0ms（缓存命中）。

### 5.3 流式转换：零拷贝管道

```
上游 SSE 流
    │
    ▼ (bufio.Reader, 64KB buffer)
SSE 行解析
    │
    ▼ (直接写入 pipe，不缓存全部内容)
AWS Event Stream 帧构建
    │
    ▼
Kiro IDE
```

关键：**不等待完整响应**，每收到一个 SSE 事件立即转换并写入管道，实现真正的流式传输。

### 5.4 并发模型

```
每个请求 → 独立 goroutine
    │
    ├── classifier 请求 → 直接走快速路径（不排队）
    │
    └── vibe 请求 → 正常转发（不串行）

渠道级并发限制（可选）：
    channelLimiter: 每个渠道最多 N 个并发（防止 API 限速）
    默认 N=10，可配置
```

---

## 六、Web UI 设计（参考 Cursor 助手风格）

### 6.1 两个前端

| 前端 | 端口 | 用途 |
|------|------|------|
| `frontend/` | 18082 | **主控制面板**（仪表盘 + 模型配置） |
| `frontend-inspector/` | 18083 | **流量抓包**（协议研究，保留） |

### 6.2 主控制面板页面结构

**仪表盘（Dashboard）** - 参考 Cursor 助手主界面：
```
┌─────────────────────────────────────────────────────┐
│  Kiro Proxy  |  永久免费  |  自定义API              │
├─────────────────────────────────────────────────────┤
│  ┌──────────┐  ┌──────────┐  ┌──────────┐          │
│  │ 对话轮次 │  │ Token消耗│  │ 节省估算 │          │
│  │   538    │  │  54.8M   │  │  $63.68  │          │
│  └──────────┘  └──────────┘  └──────────┘          │
├─────────────────────────────────────────────────────┤
│  服务状态: 运行中  [关闭服务]                        │
│  CA 证书: ✓ 已信任                                  │
│  代理端口: 18081                                     │
├─────────────────────────────────────────────────────┤
│  直连模式 ○  (开启后 Kiro 直连官方)                 │
└─────────────────────────────────────────────────────┘
```

**模型配置（Models）** - 参考 Cursor 助手模型配置页：
```
┌─────────────────────────────────────────────────────┐
│  [OpenAI]  [Anthropic]              [新增模型]      │
├─────────────────────────────────────────────────────┤
│  ┌──────────────────┐  ┌──────────────────┐        │
│  │ claude-sonnet-4-5│  │ claude-opus-4-5  │        │
│  │ OpenAI           │  │ Anthropic        │        │
│  │ HOST: api.xxx    │  │ HOST: api.xxx    │        │
│  │ KEY: sk-****     │  │ KEY: sk-****     │        │
│  │ ✓ 52 t/s 首字4.7s│  │ ✓ 48 t/s 首字5.1s│        │
│  │ [测试][编辑][删除]│  │ [测试][编辑][删除]│        │
│  └──────────────────┘  └──────────────────┘        │
└─────────────────────────────────────────────────────┘
```

**模型编辑表单**：
- 显示名称 / 模型标识（Kiro 看到的 modelId）
- 访问密钥 / 接口地址
- 上下文窗口 / 最大输出 Token
- 接口端点（/v1/chat/completions / /v1/messages）
- 推理强度（低/中/高）
- 额外参数 JSON
- 模型测试（显示 t/s 和首字延迟）

---

## 七、实现优先级

### Phase 1：核心代理（先跑通）
- [ ] 重构 `converter/` 目录，实现一次解析
- [ ] 实现工具定义缓存
- [ ] 实现 Kiro → OpenAI 转换
- [ ] 实现 OpenAI SSE → AWS Event Stream 转换
- [ ] 基础路由（单渠道配置）

### Phase 2：管理面板
- [ ] 新建 `frontend/` 主控制面板
- [ ] 仪表盘页面
- [ ] 模型配置页面（增删改查 + 测试）
- [ ] 内存统计 + API

### Phase 3：完善
- [ ] Anthropic 格式支持
- [ ] 多渠道路由
- [ ] 请求日志持久化
- [ ] CA 证书一键安装

### Phase 4：优化
- [ ] 工具定义缓存命中率监控
- [ ] 并发压测
- [ ] 首字节延迟优化

---

## 八、关键技术决策

| 决策点 | 选择 | 原因 |
|--------|------|------|
| 数据库 | SQLite（仅配置） | 配置数据量小，不在热路径 |
| 统计 | 内存原子计数器 | 避免 SQLite 热路径 |
| 日志 | 内存 ring buffer + 异步写盘 | 不阻塞请求 |
| 流式转换 | io.Pipe + goroutine | 零拷贝，真流式 |
| 工具缓存 | sync.Map + sha256 | 读多写少，无锁读 |
| 前端 | Vue3 + Tailwind v4 | 与现有 inspector 一致 |
| 并发 | 无会话级串行 | 解决 ai-proxy 最大痛点 |

---

## 九、与 ai-proxy 的对比

| 特性 | ai-proxy | 本项目 |
|------|----------|--------|
| 请求体解析次数 | 5次/请求 | 1次/请求 |
| 工具定义转换 | 每次重新转换 | 哈希缓存 |
| 会话并发 | 串行（1个槽位） | 完全并发 |
| 统计查询 | SQLite 全表扫描 | 内存原子计数 |
| 状态栏接口 | 触发全量统计 | 读内存，O(1) |
| 日志写入 | 同步阻塞 | 异步批量 |
| 前端 | VS Code 扩展 Webview | 独立浏览器 Web UI |
| 日志 tail | fs.watch 持续 append | WebSocket 推送 |
