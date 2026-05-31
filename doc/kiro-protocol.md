# Kiro API 协议文档

> 基于实际抓包分析，版本 KiroIDE-0.12.224

---

## 一、网络层

| 项目 | 值 |
|------|-----|
| 端点 1 | `https://q.us-east-1.amazonaws.com/generateAssistantResponse` |
| 端点 2 | `https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse` |
| 方法 | `POST` |
| Content-Type | `application/json` |
| 响应格式 | AWS Event Stream（二进制帧，非标准 JSON） |

---

## 二、请求 Headers

```
Authorization:        Bearer <accessToken>
Content-Type:         application/json
User-Agent:           aws-sdk-js/1.0.39 ua/2.1 os/win32#10.0.26200 lang/js
                      md/nodejs#22.22.0 api/codewhispererstreaming#1.0.39
                      m/N KiroIDE-0.12.224-<machineId>
X-Amz-User-Agent:     aws-sdk-js/1.0.39 KiroIDE-0.12.224-<machineId>
X-Amzn-Kiro-Agent-Mode: vibe | spec | intent-classification
Amz-Sdk-Invocation-Id: <uuid>
Amz-Sdk-Request:      attempt=1; max=3
Connection:           close
```

### X-Amzn-Kiro-Agent-Mode 取值

| 值 | 含义 |
|----|------|
| `intent-classification` | 第一个请求，用轻量模型判断用户意图（do/spec/chat） |
| `vibe` | 正式对话请求（Vibe 模式） |
| `spec` | Spec 模式请求 |

### Authorization Token 格式

```
Bearer <base64_token>:<signature>
```

- `accessToken`：Cognito JWT，有效期约 1 小时
- 通过 `prod.us-east-1.auth.desktop.kiro.dev` 用 refreshToken 换取

---

## 三、请求 Body 结构

```json
{
  "conversationState": {
    "chatTriggerType": "MANUAL",
    "conversationId": "<uuid>",
    "agentContinuationId": "<uuid>",
    "agentTaskType": "vibe",

    "currentMessage": {
      "userInputMessage": {
        "content": "<用户消息文本>",
        "modelId": "claude-sonnet-4-5",
        "origin": "AI_EDITOR",
        "userInputMessageContext": {
          "tools": [ /* 工具定义数组，见下 */ ],
          "toolResults": [ /* 工具调用结果，见下 */ ]
        }
      }
    },

    "history": [
      {
        "userInputMessage": {
          "content": "<历史用户消息>",
          "modelId": "claude-sonnet-4-5",
          "origin": "AI_EDITOR"
        }
      },
      {
        "assistantResponseMessage": {
          "content": "<历史助手回复>",
          "toolUses": [ /* 历史工具调用 */ ]
        }
      }
      // ... 交替排列，user 在前 assistant 在后
    ]
  },

  "profileArn": "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK",

  "inferenceConfig": {
    "maxTokens": 8192,
    "temperature": 0.7,
    "topP": 0.9,
    "reasoningConfig": {
      "type": "enabled",
      "budgetTokens": 10000
    }
  }
}
```

### modelId 取值（已知）

| modelId | 说明 |
|---------|------|
| `claude-sonnet-4-5` | 主力模型 |
| `claude-opus-4-5` | 高级模型 |
| `claude-haiku-4-5` | 快速模型 |
| `simple-task` | intent-classification 专用轻量模型 |
| `auto` | 自动选择 |

### agentTaskType 取值

| 值 | 说明 |
|----|------|
| `vibe` | Vibe 模式（默认） |
| `spec` | Spec 模式 |

---

## 四、工具定义格式（tools）

```json
{
  "toolSpecification": {
    "name": "execute_pwsh",
    "description": "工具描述文本...",
    "inputSchema": {
      "json": {
        "type": "object",
        "properties": {
          "command": { "type": "string", "description": "..." }
        },
        "required": ["command"],
        "additionalProperties": false,
        "$schema": "http://json-schema.org/draft-07/schema#"
      }
    }
  }
}
```

与 OpenAI function calling 的对应关系：

| Kiro | OpenAI |
|------|--------|
| `toolSpecification.name` | `function.name` |
| `toolSpecification.description` | `function.description` |
| `toolSpecification.inputSchema.json` | `function.parameters` |

---

## 五、工具调用结果格式（toolResults）

```json
{
  "toolUseId": "<uuid>",
  "content": [
    { "text": "工具执行结果文本" }
  ],
  "status": "success"
}
```

---

## 六、助手消息中的工具调用（toolUses）

```json
{
  "toolUseId": "<uuid>",
  "name": "execute_pwsh",
  "input": {
    "command": "go build ./..."
  }
}
```

---

## 七、响应格式：AWS Event Stream

响应是二进制流，每帧结构：

```
[4字节 总长度][4字节 头部长度][4字节 CRC][头部字节][载荷字节][4字节 消息CRC]
```

头部包含 `:event-type` 字段，标识事件类型。

### 事件类型

| event-type | 含义 | 载荷结构 |
|------------|------|---------|
| `assistantResponseEvent` | 文本内容块 | `{"assistantResponseEvent": {"content": "文本片段"}}` |
| `toolUseEvent` | 工具调用 | `{"toolUseEvent": {"toolUseId": "...", "name": "...", "input": "...", "stop": true}}` |
| `messageMetadataEvent` | Token 用量 | `{"messageMetadataEvent": {"tokenUsage": {...}}}` |
| `meteringEvent` | Credit 用量 | `{"meteringEvent": {"usage": 1.0}}` |
| `contextUsageEvent` | 上下文使用率 | `{"contextUsageEvent": {"contextUsagePercentage": 42}}` |
| `reasoningContentEvent` | Thinking 内容 | `{"reasoningContentEvent": {"text": "..."}}` |
| `exceptionEvent` | 错误 | `{"exceptionEvent": {"exceptionType": "...", "message": "..."}}` |

### assistantResponseEvent 示例

```json
{"assistantResponseEvent": {"content": "好的，我来帮你"}}
```

### toolUseEvent 示例（流式，分多帧）

```json
// 开始帧（有 name，无 stop）
{"toolUseEvent": {"toolUseId": "abc123", "name": "execute_pwsh", "input": ""}}

// 输入片段帧（多个）
{"toolUseEvent": {"toolUseId": "abc123", "input": "{\"command\":"}}
{"toolUseEvent": {"toolUseId": "abc123", "input": "\"go build\"}"}}

// 结束帧（stop=true）
{"toolUseEvent": {"toolUseId": "abc123", "stop": true}}
```

### messageMetadataEvent 示例

```json
{
  "messageMetadataEvent": {
    "tokenUsage": {
      "uncachedInputTokens": 1200,
      "cacheReadInputTokens": 800,
      "cacheWriteInputTokens": 0,
      "outputTokens": 350
    }
  }
}
```

---

## 八、intent-classification 请求特殊说明

第一个请求（`X-Amzn-Kiro-Agent-Mode: intent-classification`）使用 `modelId: simple-task`，
history 中第一条 userInputMessage 包含完整的分类提示词，要求模型返回：

```json
{"chat": 0.0, "do": 0.9, "spec": 0.1}
```

这个请求与正式对话请求**并发发出**，用于决定后续走 vibe 还是 spec 流程。

---

## 九、会话管理

- `conversationId`：整个会话的唯一 ID，同一对话保持不变
- `agentContinuationId`：每次请求生成新的 UUID
- `history`：完整历史消息数组，每次请求都携带全量历史
- 历史消息必须严格交替：user → assistant → user → assistant...

---

## 十、系统提示注入方式

Kiro **没有独立的 system 字段**，系统提示通过以下方式注入：

1. 在 `currentMessage.content` 开头插入特殊标签：
   ```
   <thinking_mode>enabled</thinking_mode>
   <max_thinking_length>200000</max_thinking_length>
   
   Current time: 2026-05-29T07:13:00Z
   
   [实际用户消息]
   ```

2. 或通过 history 第一轮注入（system prompt → user，"Understood" → assistant）
