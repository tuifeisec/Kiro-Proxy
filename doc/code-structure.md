# 代码结构规范

## 原则

每个文件只做一件事，文件名即职责说明。
单文件不超过 300 行，超过就拆。

---

## converter/ — 格式转换层

| 文件 | 职责 | 依赖 |
|------|------|------|
| `types.go` | 所有结构体定义（Kiro/OpenAI/Anthropic） | 无 |
| `kiro_parse.go` | Kiro 请求体 → ParsedConversation（一次解析） | types |
| `tool_cache.go` | 工具定义转换缓存（hash → OpenAI/Anthropic tools） | types |
| `tool_convert.go` | 工具定义格式转换逻辑（从 tool_cache 拆出） | types |
| `kiro_to_openai.go` | ParsedConversation → OpenAI 请求体 | types, tool_cache |
| `kiro_to_anthropic.go` | ParsedConversation → Anthropic 请求体 | types, tool_cache |
| `stream_openai.go` | OpenAI SSE → AWS Event Stream 帧（流式转换器） | event_frame |
| `stream_anthropic.go` | Anthropic SSE → AWS Event Stream 帧（流式转换器） | event_frame |
| `event_frame.go` | AWS Event Stream 二进制帧构建 | 无 |
| `image.go` | 图片格式处理（media type、data URI） | types |

## proxy/ — MITM 代理层

| 文件 | 职责 | 依赖 |
|------|------|------|
| `server.go` | goproxy 初始化、CA 加载、服务启动 | cert, hub |
| `interceptor.go` | 请求拦截、preflight 一次解析、路由决策 | converter, router |
| `forwarder.go` | 转发请求到上游、重试逻辑 | converter, store |
| `stream_pipe.go` | 流式响应管道（上游SSE → Kiro Event Stream） | converter |
| `ca_install.go` | CA 证书安装 HTTP API | 无 |
| `hub.go` | WebSocket 广播中心 | 无 |
| `logger.go` | 流量日志（内存 ring buffer + 异步写盘 + WS推送） | hub |

## router/ — 路由层

| 文件 | 职责 |
|------|------|
| `router.go` | 模型映射查找、渠道选择、内存路由表 |
| `model_map.go` | 内置模型名映射（context window 推荐值等） |

## store/ — 存储层

| 文件 | 职责 |
|------|------|
| `db.go` | SQLite 初始化、迁移 |
| `channel.go` | 渠道 CRUD |
| `mapping.go` | 模型映射 CRUD |
| `setting.go` | 键值设置 CRUD |
| `stats.go` | 内存原子计数器 + 定期异步刷盘（不在热路径） |

## api/ — Web UI 后端

| 文件 | 职责 |
|------|------|
| `server.go` | HTTP 路由注册、中间件 |
| `channels.go` | 渠道增删改查接口 |
| `mappings.go` | 模型映射接口 |
| `stats.go` | 统计数据接口（读内存计数器） |
| `logs.go` | 日志查询接口 |
| `test_model.go` | 模型连通性测试接口（测速） |

## rules/ — 配置

| 文件 | 职责 |
|------|------|
| `config.go` | config.yaml 结构体 + 加载 |

---

## 命名约定

- 结构体：`PascalCase`
- 函数：`PascalCase`（导出）/ `camelCase`（内部）
- 文件：`snake_case`，动词_名词格式（如 `kiro_parse.go`、`stream_openai.go`）
- 测试文件：`xxx_test.go`，与被测文件同目录

## 错误处理约定

- 转换层函数返回 `(result, error)`，不 panic
- 代理层记录错误日志后返回 502，不中断服务
- 流式转换器内部错误写入 pipe 的 error，由调用方处理
