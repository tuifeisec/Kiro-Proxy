# d:\proxy vs DeepSeek-Reasonix 缓存方案对比分析

> 审批文档：从代码层次对比两个项目在 API 缓存方面的实现细节与设计哲学差异。

---

## 一、d:\proxy 的缓存体系

### 1.1 架构定位

d:\proxy 是 **MITM 代理**，拦截 Kiro IDE 的请求并转发到上游 API。它**不控制请求内容**——Kiro IDE 决定发什么，代理只能做转换和优化。

### 1.2 策略一：Anthropic `cache_control` 断点注入

**文件**：`converter/kiro_to_anthropic.go`

```go
req := AnthropicRequest{
    Model:     model,
    Messages:  messages,
    MaxTokens: 16384,
    Stream:    true,
    // 顶层 cache_control：Anthropic 自动将 breakpoint 滚动到最后一个可缓存 block。
    // 多轮对话时每次只有最后一轮是新增的，之前所有历史自动从缓存读取。
    CacheControl: &CacheControl{Type: "ephemeral"},
}

// 系统提示只放静态部分，并添加 cache_control。
// Kiro 的 system prompt 可能包含 "Here is the last user message:" 动态尾部；
// 若整块放入 system，会导致 Anthropic system/tools 缓存前缀每轮变化。
if staticSystem != "" {
    req.System = []AnthropicBlock{
        {
            Type:         "text",
            Text:         staticSystem,
            CacheControl: &CacheControl{Type: "ephemeral"},
        },
    }
}

// 工具定义 + 最后一个工具添加 cache_control。
// breakpoint 在最后一个工具上，可缓存稳定的 system + tools 前缀。
tools[len(tools)-1].CacheControl = &CacheControl{Type: "ephemeral"}
```

**设计意图**：Anthropic 最多支持 4 个 `cache_control` 断点。d:\proxy 的分配策略：
- 1 个给 system prompt（静态部分）
- 1 个给最后一个 tool（覆盖 system + tools 前缀）
- 1 个给顶层请求（滚动到最后一个可缓存 block）

### 1.3 策略二：System Prompt 动态尾部拆分

**文件**：`converter/cache_optimize.go`

```go
// optimizeSystemForCache 分离 system prompt 的静态/动态部分
// Kiro 的 system prompt 尾部包含 "Here is the last user message: ..."
// 这部分每轮都变，如果放在 system 里会导致 Anthropic 缓存前缀每轮失效。
func optimizeSystemForCache(system string) (staticPart, dynamicPart string) {
    idx := strings.LastIndex(system, lastUserMsgMarker)
    if idx < 0 {
        return system, ""
    }
    staticPart = strings.TrimRight(system[:idx], "\n\r\t ")
    dynamicPart = system[idx:]
    return
}
```

**OpenAI 侧的对应处理**：

```go
func splitOpenAISystemDynamicTail(msgs []OpenAIMessage, hasCurrentUser bool) []OpenAIMessage {
    // 将 system 内容替换为纯静态部分
    msgs[systemIdx].Content = staticPart

    // 把动态部分插入到最后一条 user 消息之前（即历史对话之后、当前消息之前）。
    // 这样 [static system] + [历史对话] 前缀保持稳定，最大化缓存命中。
    dynamicMsg := OpenAIMessage{
        Role:    "system",
        Content: dynamicPart,
    }
}
```

**效果**：`[static system] + [tools] + [历史对话]` 前缀跨轮稳定，只有最后一条用户消息是新增的。

### 1.4 策略三：Responses 续传（`previous_response_id`）

**文件**：`converter/responses_continuation.go`

```go
// 流程：
//   1. 首次请求：正常发送全量 input → 保存 (responseID, inputJSON, itemCount)
//   2. 后续请求：对比前次 input 是否是当前 input 的前缀
//      - 是：设置 previous_response_id，只发送 delta 部分
//      - 否：全量发送（system prompt 变了等情况）
//   3. 上游返回 400 时：回退到全量发送，清理存储

func ApplyResponsesContinuation(body []byte, prevState *ResponsesContinuationState) (*ResponsesContinuationResult, error) {
    // 无前次状态 → 首次请求，全量发送
    if prevState == nil || prevState.ResponseID == "" || prevState.InputJSON == "" {
        result.Body = body
        return result, nil
    }

    // 检查前次 input 是否是当前 input 的严格前缀
    if len(req.Input) <= len(prevInput) {
        result.Body = body
        return result, nil
    }

    // 逐项比对前缀
    for i := range prevInput {
        if !bytes.Equal(prevInput[i], req.Input[i]) {
            result.Body = body  // 前缀不匹配，全量发送
            return result, nil
        }
    }

    // 前缀匹配 → 续传
    delta := trimPreviousResponseOutputs(req.Input[len(prevInput):])
    req.PreviousResponseID = prevState.ResponseID
    req.Input = delta
    // ...
}
```

**续传状态 key 隔离**（`converter/kiro_parse.go`）：

```go
func buildContinuationStateKey(conversationID, agentMode, continuationID string) string {
    // "branch|" + lengthPrefixed(conversationID) + lengthPrefixed(mode) + lengthPrefixed(continuationID)
    // 确保不同会话分支的续传状态互不干扰
}
```

**效果**：请求体从 ~300KB 缩到几 KB，减少网络传输和序列化开销。

### 1.5 策略四：工具定义转换缓存

**文件**：`converter/tool_cache.go`

```go
// ToolCache 缓存工具定义的转换结果。
// Kiro 每次请求都携带完整工具定义（~300KB），但工具定义几乎不变。
// 用内容哈希做 key，避免每次重复转换。
type ToolCache struct {
    mu    sync.RWMutex
    items map[string]*toolCacheEntry
}

const (
    toolCacheTTL      = 10 * time.Minute
    maxToolCacheItems = 100
)

func (c *ToolCache) GetOrConvert(hash string, rawTools []json.RawMessage) ([]OpenAITool, []AnthropicTool) {
    // 缓存命中 → 直接返回
    // 缓存未命中 → 执行转换并缓存
    // 超过 100 条 → LRU 淘汰
}
```

**效果**：纯本地 CPU 优化，避免每轮重复解析 ~300KB 工具 JSON。

### 1.6 策略五：`prompt_cache_key` 注入

**文件**：`proxy/local_compat_responses.go`

```go
// 自动为 Responses 请求补充 prompt_cache_key
if raw := root["prompt_cache_key"]; len(raw) > 0 {
    // 已有则不覆盖
} else {
    root["prompt_cache_key"] = encoded  // 自动生成
}
```

**效果**：让 OpenAI 服务端能按 key 对齐缓存。

---

## 二、DeepSeek-Reasonix 的缓存体系

### 2.1 架构定位

DeepSeek-Reasonix 是**终端客户端**（AI 编码代理），直接向 DeepSeek API 发请求。它完全控制请求的组装方式，因此能从源头设计缓存友好结构。

DeepSeek API 有**服务端自动前缀缓存**：连续两次请求的消息前缀字节级相同 → 重复部分命中 KV-Cache → 按缓存价格计费。

### 2.2 策略一：请求前缀结构固化

**核心原则**：上一轮的完整请求 = 这一轮的缓存前缀。

请求结构严格固定：

```
[system prompt — 启动后永不变]
[tools JSON schema — 按 name 字典序排序，序列化字节稳定]
[历史消息 — 只追加，不修改，不压缩（除非必要）]
[当前轮新消息 — 唯一的新增尾部]
```

**佐证**（`internal/agent/cachehit_e2e_test.go`）：

```go
// mockDeepSeek 模拟服务端前缀缓存：
// 比较本次请求与上次请求的消息列表，找到字节级公共前缀
common := commonPrefixMsgs(m.prevMessages, msgs)
hitChars := charsOf(msgs[:common])   // 命中部分
totalChars := charsOf(msgs)           // 总长度

// 测试断言：第 i 次请求的缓存前缀 == 第 i-1 次请求的完整内容
for i := 1; i < len(mock.reqChars); i++ {
    if mock.hitChars[i] != mock.reqChars[i-1] {
        t.Errorf("PREFIX BROKEN at req %d: cached %d chars but full prior was %d",
            i, mock.hitChars[i], mock.reqChars[i-1])
    }
}
```

### 2.3 策略二：Reasoning Content 原样回传

DeepSeek 的 `reasoning_content`（思维链）被**原样追加到 assistant 消息**，不做任何截断、改写或格式化。

**佐证**（`cachehit_e2e_test.go`）：

```go
const longReasoning = "Let me reason about this carefully. I will weigh the constraints, " +
    "enumerate the candidate approaches, reject the ones that violate a requirement..."

// agent.go 将 reasoning 原样放入 assistant turn
// 下一轮请求时这段文字字节级不变 → 命中缓存
```

测试 `TestReasoningRoundTripCost` 量化了回传 CoT 对缓存率的影响：CoT 越长，每轮新增尾部越大，缓存率上升越慢——但仍然是净正收益。

### 2.4 策略三：PrefixShape 诊断系统

**文件**：`internal/agent/cache_shape.go`

```go
type PrefixShape struct {
    SystemHash        string  // system prompt 的 SHA-256 前 8 字节
    ToolsHash         string  // tools schema（排序后）的 SHA-256
    PrefixHash        string  // 整体前缀的 SHA-256
    LogRewriteVersion int     // 日志改写版本号
    ToolSchemaTokens  int     // 工具 schema 估算 token 数
}

// 每轮请求前快照
func CaptureShape(systemPrompt string, schemas []provider.ToolSchema, rewriteVersion int) PrefixShape

// 两轮之间对比，精确定位缓存击穿原因
func CompareShape(prev, cur PrefixShape, usage *provider.Usage) CacheDiagnostics {
    reasons := []string{}
    if prev.SystemHash != cur.SystemHash { reasons = append(reasons, "system") }
    if prev.ToolsHash != cur.ToolsHash   { reasons = append(reasons, "tools") }
    if prev.LogRewriteVersion != cur.LogRewriteVersion { reasons = append(reasons, "log_rewrite") }
    // ...
}
```

**工具 schema 排序规范化**：

```go
func normalizeToolSchemas(schemas []provider.ToolSchema) []provider.ToolSchema {
    sort.Slice(out, func(i, j int) bool {
        if out[i].Name != out[j].Name { return out[i].Name < out[j].Name }
        if out[i].Description != out[j].Description { return out[i].Description < out[j].Description }
        return string(out[i].Parameters) < string(out[j].Parameters)
    })
    return out
}
```

### 2.5 策略四：压缩卡住守卫

上下文超过窗口阈值时需要压缩（compaction），但压缩会**重写前缀**导致缓存全部失效。

**守卫逻辑**：
- 检测"压缩后大小几乎没变" → 窗口太小，压缩无意义
- 自动暂停压缩，发出通知 `"Automatic context cleanup paused"`
- 前缀恢复只追加模式 → 缓存率回升

**佐证**（`cachehit_e2e_test.go`）：

```go
func TestCacheHitSurvivesTooSmallWindow(t *testing.T) {
    // 900 token 窗口 + 30 轮工具循环
    a, sink := newAgent(t, srv.URL, mock.tools(), 900, 4)

    // 硬指标：崩塌次数 ≤ 2
    if collapses > 2 {
        t.Errorf("compaction cratered the cache %d times; guard should cap at ≤2", collapses)
    }
    // 必须发出暂停通知
    if !paused {
        t.Errorf("expected an auto-compaction-paused notice")
    }
    // 尾部 5 轮平均缓存率 ≥ 85%
    if tail := tailAverage(usageRates(sink.usages), 5); tail < 85 {
        t.Errorf("tail hit rate = %d%%, want ≥85%%", tail)
    }
}
```

### 2.6 策略五：环境摘要一次性注入

启动时生成项目环境信息（文件结构、git 状态等）的固定摘要，注入 system prompt 尾部后**永不再更新**。避免每轮前缀变化。

### 2.7 策略六：PR 级缓存影响管控

**文件**：`REASONIX.md`（项目宪法级约束）

```markdown
Cache-first: the system-prompt prefix (base prompt + tools + memory)
must stay byte-stable across turns so DeepSeek's automatic prefix cache
stays warm. Never modify it mid-session.

## Cache-impact PR metadata

When PR changes touch files under `internal/boot/`, `internal/tool/`,
`internal/provider/`, or other cache-sensitive paths, the PR body MUST include:

Cache-impact: <none|low|medium|high> — <reason>
Cache-guard: <focused guard test/command or existing guard rationale>
```

---

## 三、核心对比

### 3.1 缓存层次对比

| 层次 | d:\proxy | DeepSeek-Reasonix |
|------|----------|-------------------|
| **请求结构控制** | ❌ 无法控制（Kiro 决定） | ✅ 完全控制 |
| **前缀稳定性保证** | 间接（拆分动态尾部） | 直接（架构级设计） |
| **上游缓存断点** | ✅ Anthropic `cache_control` | 不需要（DeepSeek 自动） |
| **传输层去重** | ✅ `previous_response_id` 续传 | 不适用 |
| **本地转换缓存** | ✅ ToolCache（10min TTL） | 不适用 |
| **缓存诊断** | ❌ 无 | ✅ PrefixShape SHA-256 快照 |
| **防击穿机制** | ❌ 无 | ✅ 压缩卡住守卫 |
| **测试覆盖** | 续传 key 隔离 + 断点位置 | 前缀稳定性 E2E + 崩塌上限 + 恢复率 |
| **工程管控** | 无 | PR 级 Cache-impact 标注 |

### 3.2 缓存率数据

| 指标 | d:\proxy | DeepSeek-Reasonix |
|------|----------|-------------------|
| 缓存率统计 | 不统计（由上游决定） | 实时 `hit/(hit+miss)` |
| 典型缓存率 | 取决于上游（Anthropic 通常 60-80%） | 长会话 >99% |
| 缓存率公式 | — | `CacheHitTokens / (CacheHitTokens + CacheMissTokens)` |

### 3.3 设计哲学对比

**d:\proxy**：
> "我是搬运工，请求内容我说了不算。我能做的是：别把缓存搞坏（拆分动态尾部），能省则省（续传减少传输），给上游创造缓存条件（打断点）。"

**DeepSeek-Reasonix**：
> "我是发件人，请求怎么组装完全由我决定。我的目标是：让前缀字节级稳定，让缓存必然命中，让任何破坏缓存的行为在代码审查阶段就被拦截。"

---

## 四、结论：哪个是细节主要方面

**DeepSeek-Reasonix 是缓存细节的主要方面。**

理由：

1. **控制力**：它完全控制请求结构，能从源头保证前缀稳定。d:\proxy 作为代理，天花板是"别搞坏"。

2. **深度**：从请求组装 → 消息结构 → 压缩策略 → 诊断系统 → 测试覆盖 → 开发规范，全链路 6 层保障。d:\proxy 是 5 个独立优化点，没有系统性。

3. **可观测性**：PrefixShape 能精确告诉你"缓存为什么击穿了"。d:\proxy 没有这个能力。

4. **防御性**：压缩守卫 + PR 管控 + E2E 测试硬指标（崩塌 ≤2 次，尾部 ≥85%）。d:\proxy 没有防击穿机制。

5. **结果**：DeepSeek-Reasonix 能做到 >99% 缓存率。d:\proxy 的上游缓存率取决于 Anthropic/OpenAI 的服务端策略，自己无法保证。

**但 d:\proxy 的续传机制有独立价值**：它解决的不是"缓存命中率"问题，而是"网络传输效率"问题（300KB → 几 KB）。这是 DeepSeek-Reasonix 不需要解决的问题（它直接控制请求大小）。

---

## 五、对 d:\proxy 的改进建议（可选）

| 方向 | 可行性 | 预期收益 |
|------|--------|----------|
| 已有：Anthropic `cache_control` 断点 | ✅ 已实现 | 中高（Anthropic 侧缓存） |
| 已有：system prompt 动态尾部拆分 | ✅ 已实现 | 中（避免每轮前缀变化） |
| 已有：Responses 续传 | ✅ 已实现 | 中（减少传输 + OpenAI 侧缓存对齐） |
| 可选：前缀漂移日志 | 未做 | 诊断价值（知道缓存为什么没命中） |
| 可选：Anthropic 缓存命中率统计 | 未做 | 可观测性（从 usage 响应中提取） |
| 不可行：控制 Kiro 请求结构 | ❌ | —（Kiro 是封闭客户端） |

---

## 六、关键源码索引

| 文件 | 项目 | 职责 |
|------|------|------|
| `converter/kiro_to_anthropic.go` | d:\proxy | Anthropic cache_control 断点注入 |
| `converter/cache_optimize.go` | d:\proxy | system prompt 动态尾部拆分 |
| `converter/responses_continuation.go` | d:\proxy | Responses 续传（previous_response_id） |
| `converter/tool_cache.go` | d:\proxy | 工具定义转换缓存 |
| `proxy/local_compat_responses.go` | d:\proxy | prompt_cache_key 注入 |
| `converter/kiro_parse.go` | d:\proxy | 续传状态 key 构建 |
| `internal/agent/cache_shape.go` | DeepSeek-Reasonix | PrefixShape 诊断 |
| `internal/agent/cachehit_e2e_test.go` | DeepSeek-Reasonix | 缓存稳定性 E2E 测试 |
| `REASONIX.md` | DeepSeek-Reasonix | 缓存优先开发规范 |
