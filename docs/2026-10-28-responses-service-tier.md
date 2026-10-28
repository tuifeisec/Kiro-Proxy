# Responses `service_tier` 契约

## 目标

模型配置中的 `service_tier=fast` 表示请求希望使用快速档位。当前实现把它应用到 `provider=openai` 的 Responses 路由；上游是否真正支持 `priority` 尚未通过模型目录确认。

## P0 行为

- 只作用于 Responses 请求的顶层 `service_tier`。
- 本地配置为空时不新增字段。
- 本地配置为 `fast` 时，仅当客户端字段缺失、为 `null` 或显式为 `default` 时写入 `priority`。
- 客户端显式提供 `priority`、`flex`、`auto`、空字符串或其他值时保留原值。
- 普通 OpenAI Responses、raw Responses 与本地 `/v1/responses` 入口共用同一字段改写规则；配置成 Chat Completions 的兼容路径不会合成配置项带来的 `priority`，客户端显式 tier 仍按现有 Chat 映射规则处理。
- 前端模型表单在 Codex 或 OpenAI 的 `/responses` 端点显示“快速”选项；OpenAI 的 Chat Completions 配置不显示该选项。
- Codex 专用 Responses 转换仍保留其现有的 `store`、`stream`、历史字段清理等专用行为，不复用到普通 OpenAI 上游。
- Chat fallback 不发送 `service_tier` 改写。

## 能力边界

当前模型目录只保存模型 ID，尚未保存上游 `service_tiers` 能力声明。因此 P0 只实现显式模型配置驱动的字段映射，不把模型名称或 User-Agent 当作能力证据。后续若接入目录能力，应在明确出现 `priority` 时才自动启用；能力未知时保持原请求。

## 续轮与观测

有效 tier 需要与 Responses continuation 的 scope/auto lock 一致，不能在续轮中重新探测后切换。当前用量统计仍记录模型配置值；最终出站 tier 以请求体验证为准，尚未单独保存上游回显值。
