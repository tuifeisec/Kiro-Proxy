# Kiro ↔ OpenAI 格式转换规范

---

## 一、OpenAI → Kiro 请求转换

### 输入（OpenAI Chat Completions）

```json
POST /v1/chat/completions
{
  "model": "claude-sonnet-4-5",
  "messages": [
    {"role": "system", "content": "你是一个助手"},
    {"role": "user",   "content": "你好"},
    {"role": "assistant", "content": "你好！"},
    {"role": "user",   "content": "帮我写代码"}
  ],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "execute_cmd",
        "description": "执行命令",
        "parameters": {
          "type": "object",
          "properties": {
            "command": {"type": "string"}
          },
          "required": ["command"]
        }
      }
    }
  ],
  "stream": true,
  "max_tokens": 4096,
  "temperature": 0.7
}
```

### 转换规则

```
1. model 映射
   OpenAI model → Kiro modelId
   "gpt-4"      → "claude-sonnet-4-5"
   "gpt-4o"     → "claude-sonnet-4-5"
   直接传 claude 模型名则原样使用

2. system message 处理
   system message → history 第一轮
   history[0] = { userInputMessage: { content: system_content } }
   history[1] = { assistantResponseMessage: { content: "Understood. I will follow these instructions." } }

3. messages 转 history + currentMessage
   - 最后一条 user message → currentMessage.userInputMessage.content
   - 其余消息 → history 数组（交替 user/assistant）
   - tool role → userInputMessageContext.toolResults（合并到下一条 user 消息）

4. tools 转换
   OpenAI function → Kiro toolSpecification
   function.name        → toolSpecification.name
   function.description → toolSpecification.description
   function.parameters  → toolSpecification.inputSchema.json

5. tool_calls（assistant 消息中）→ assistantResponseMessage.toolUses
   tool_call.id            → toolUseId
   tool_call.function.name → name
   JSON.parse(arguments)   → input

6. tool role 消息 → toolResults
   tool_call_id → toolUseId
   content      → content[0].text
   status       → "success"

7. 固定字段
   origin          = "AI_EDITOR"
   chatTriggerType = "MANUAL"
   agentTaskType   = "vibe"
   conversationId  = 从 user 字段提取，或新生成 UUID
```

### 输出（Kiro 请求体）

```json
{
  "conversationState": {
    "chatTriggerType": "MANUAL",
    "conversationId": "<uuid>",
    "agentContinuationId": "<uuid>",
    "agentTaskType": "vibe",
    "currentMessage": {
      "userInputMessage": {
        "content": "帮我写代码",
        "modelId": "claude-sonnet-4-5",
        "origin": "AI_EDITOR",
        "userInputMessageContext": {
          "tools": [
            {
              "toolSpecification": {
                "name": "execute_cmd",
                "description": "执行命令",
                "inputSchema": {
                  "json": {
                    "type": "object",
                    "properties": {"command": {"type": "string"}},
                    "required": ["command"]
                  }
                }
              }
            }
          ]
        }
      }
    },
    "history": [
      {"userInputMessage": {"content": "你是一个助手", "modelId": "claude-sonnet-4-5", "origin": "AI_EDITOR"}},
      {"assistantResponseMessage": {"content": "Understood. I will follow these instructions.", "toolUses": []}},
      {"userInputMessage": {"content": "你好", "modelId": "claude-sonnet-4-5", "origin": "AI_EDITOR"}},
      {"assistantResponseMessage": {"content": "你好！", "toolUses": []}}
    ]
  },
  "inferenceConfig": {
    "maxTokens": 4096,
    "temperature": 0.7
  }
}
```

---

## 二、Kiro 响应 → OpenAI 响应转换

### Kiro 响应（AWS Event Stream 二进制帧序列）

```
帧1: assistantResponseEvent → {"content": "好的，"}
帧2: assistantResponseEvent → {"content": "我来帮你"}
帧3: toolUseEvent           → {"toolUseId": "t1", "name": "execute_cmd", "input": ""}
帧4: toolUseEvent           → {"toolUseId": "t1", "input": "{\"command\":\"ls\"}"}
帧5: toolUseEvent           → {"toolUseId": "t1", "stop": true}
帧6: messageMetadataEvent   → {"tokenUsage": {"outputTokens": 50, ...}}
```

### 转换为 OpenAI SSE 流（stream: true）

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","model":"claude-sonnet-4-5","choices":[{"index":0,"delta":{"role":"assistant","content":"好的，"},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","model":"claude-sonnet-4-5","choices":[{"index":0,"delta":{"content":"我来帮你"},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","model":"claude-sonnet-4-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"execute_cmd","arguments":""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","model":"claude-sonnet-4-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","model":"claude-sonnet-4-5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}

data: [DONE]
```

### 转换规则

```
Kiro event-type          → OpenAI SSE delta

assistantResponseEvent
  .content               → delta.content（文本追加）

toolUseEvent（开始帧，有 name）
  .toolUseId             → tool_calls[i].id
  .name                  → tool_calls[i].function.name
                           tool_calls[i].type = "function"
                           tool_calls[i].index = i（按出现顺序）

toolUseEvent（输入帧，有 input string）
  .input                 → tool_calls[i].function.arguments（追加）

toolUseEvent（结束帧，stop=true）
  → 触发 finish_reason = "tool_calls"

messageMetadataEvent
  .tokenUsage.uncachedInputTokens + cacheReadInputTokens
                         → usage.prompt_tokens
  .tokenUsage.outputTokens
                         → usage.completion_tokens

reasoningContentEvent
  .text                  → delta.reasoning_content（扩展字段）
                           或包裹在 <thinking>...</thinking> 标签中
```

### 转换为 OpenAI 非流式响应（stream: false）

```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "created": 1748491200,
  "model": "claude-sonnet-4-5",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "好的，我来帮你",
        "tool_calls": [
          {
            "id": "t1",
            "type": "function",
            "function": {
              "name": "execute_cmd",
              "arguments": "{\"command\":\"ls\"}"
            }
          }
        ]
      },
      "finish_reason": "tool_calls"
    }
  ],
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 50,
    "total_tokens": 150
  }
}
```

---

## 三、Anthropic Claude → Kiro 请求转换

### 输入（Anthropic Messages API）

```json
POST /v1/messages
{
  "model": "claude-sonnet-4-5",
  "max_tokens": 4096,
  "system": "你是一个助手",
  "messages": [
    {"role": "user", "content": "你好"},
    {"role": "assistant", "content": "你好！"},
    {"role": "user", "content": [
      {"type": "text", "text": "帮我写代码"},
      {"type": "tool_result", "tool_use_id": "t1", "content": "命令输出结果"}
    ]}
  ],
  "tools": [...]
}
```

### 转换规则（与 OpenAI 的差异）

```
1. system 字段直接读取（不在 messages 里）
2. content 可以是数组，需要提取各 type 的内容：
   - type=text       → 文本内容
   - type=image      → KiroImage（base64）
   - type=tool_use   → assistantResponseMessage.toolUses
   - type=tool_result → toolResults
3. thinking 字段：
   {"type": "enabled", "budget_tokens": 10000}
   → inferenceConfig.reasoningConfig = {"type": "enabled", "budgetTokens": 10000}
```

---

## 四、关键差异对照表

| 维度 | OpenAI | Anthropic | Kiro |
|------|--------|-----------|------|
| 系统提示 | `messages[role=system]` | `system` 字段 | history 第一轮 or content 前缀 |
| 工具定义位置 | 顶层 `tools` | 顶层 `tools` | `userInputMessageContext.tools` |
| 工具调用 | `tool_calls` 数组 | `content[type=tool_use]` | `toolUses` 数组 |
| 工具结果 | `role=tool` 消息 | `content[type=tool_result]` | `userInputMessageContext.toolResults` |
| 流式协议 | SSE text/event-stream | SSE text/event-stream | AWS Event Stream 二进制 |
| 响应事件 | `data: {...}` | `data: {...}` | 二进制帧 + JSON payload |
| 模型字段位置 | 顶层 `model` | 顶层 `model` | `userInputMessage.modelId` |
| 历史消息 | `messages` 数组 | `messages` 数组 | `history` + `currentMessage` 分离 |
| 会话 ID | 无（无状态） | 无（无状态） | `conversationId`（有状态） |
