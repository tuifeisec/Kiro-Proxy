# Typesafe 接入 Kiro 意图分类

## 目标与边界

用户确认先替换 Kiro 独立 classifier，识别 `chat / do / spec`，本地 Auto 继续使用既有任务字段与能力规则。该接入不新增 Auto 语义选模，不改变手动主请求或兼容 API 的模型路由。只使用明确的意图分类信号：`X-Amzn-Kiro-Agent-Mode: intent-classification`，或当前/历史提示词同时包含现有解析器使用的两个 classifier 标记。`session-title` 请求明确排除；单独的 `simple-task` / `intent-classification` 模型 ID 不足以触发 Typesafe，仍沿用既有处理。因此不会把会话标题或所有 `auto` 主请求交给 Typesafe。

默认关闭。仅在 forward 模式且识别为 Kiro classifier 时生效；capture 继续放行官方。启用后 Typesafe 优先于原 `classifier_model_id`（包括 `__disabled__`），关闭后恢复原设置。GPT-8.8 彩蛋 classifier 保持原快捷行为。真正调用 Typesafe 前仍执行现有授权检查；无授权不发送分类请求。

本轮仅交付源码、Mock 验证和未部署候选，不填写真实 Key、不修改运行数据库、不调用收费接口、不切换代理模式、不替换或重启运行程序、不登记发布指纹。

## 官方接口与输入

依据 [官方 API](https://docs.typesafe.ai/api)、[模型说明](https://docs.typesafe.ai/models) 和 [Choice](https://docs.typesafe.ai/primitives/choice)，调用 `POST https://api.typesafe.ai/v1/systemone`，使用 Bearer Key，默认固定 `jev-1.13.0`。

请求使用一个 `choice` 问题 `intent`，三个选项固定为 `chat / do / spec`。`chat` 表示咨询、解释与对话；`do` 表示请求执行、修改代码或文件等动作；`spec` 表示明确要求规格、需求或设计文档以及规格工作流。问题要求参考 Kiro 自带分类上下文中的类别定义，判断用户意图，而不是把包装中的“输出 JSON”指令当作执行任务。

`state` 使用结构对象保留当前内容、按角色排列的文本历史和单独的 classifier 系统上下文；系统历史不重复传入。任务字段只作元数据，不直接决定类别。传入当前及历史工具的文本结果、工具名与文本参数；不发送图片 bytes、音视频、认证信息、账户标识或完整原始请求。图片仅记录是否存在，无法判断图像内容。

现有解析器没有可核实的独立原始意图字段，也没有足够的真实 classifier 样本证明包装的稳定分隔符。因此不使用正则剥离当前内容、不截断上下文、不宣称准确提取原始用户意图。官方文本容量限制由上游执行，超长请求保留错误；中文语义质量另需真实样本验收。

## 响应、失败与统计

校验 `answers.intent.type=choice`、三类概率完整且均在 `[0,1]`、概率和接近 1、choice 属于三类且为最大概率项、confidence 在 `[0,1]`、实际 model 非空以及完整非负 Token usage。允许浮点概率和的误差，不重写概率、不使用置信度阈值改变意图。

Kiro Event Stream 返回原概率 JSON、实际模型 ID、Token 用量和本地估算 Credits；`confidence` 只记结构化日志。记录独立统计模型身份 `Typesafe classifier / <实际模型>`，提供方为 `typesafe`，调度理由为 `classifier:typesafe`，原请求模型另存 `requested_model`。不会写主会话模型记忆或 Auto 锁。

官方当前输入价为 `$0.042 / 百万 Token`，输出 Token 免费。本地费用为 `input_tokens × input_price_per_million / 1,000,000`，Credits 为费用 USD ÷ 0.04。复用既有明细、汇总和计量链；内部保留精度，界面 Credits 显示两位小数。`typesafe` 价格默认明确为输入 0.042、输出与缓存 0，防止零输出价被普通模型默认价格替换。

HTTP 401/422/429/529 等、超时、取消或非法响应均明确失败，不返回固定 chat、不跳回旧模型、不自动重试分类请求。日志保留状态和错误种类；上游错误原响应保存在客户端错误对象中但不直接写日志，避免回显输入或 Key。若失败响应仍含可验证的实际 usage，按该 usage 记账；没有 usage 的失败记录零已知用量并标记 `usage_available=false`，不代表上游一定免费。

新增日志仅包含提供方、请求/实际模型、类别、概率、confidence、Token、费用、Credits、耗时、是否取得 usage 和错误类别，不新增用户正文或 Authorization。

## 配置与 Key 处理

设置键 `typesafe_classifier_config` 存放完整 JSON 对象，字段如下：

| 字段 | 默认/含义 |
| --- | --- |
| `enabled` | `false`，启用 Typesafe classifier |
| `base_url` | `https://api.typesafe.ai`，追加固定 `/v1/systemone` |
| `api_key` | 空，仅后端保存；启用时必填 |
| `model` | `jev-1.13.0`，可编辑 |
| `timeout_seconds` | 10，范围 1–120 秒 |
| `input_price_per_million` | 0.042 USD，必须为有限正数 |
| `proxy_url` | 空表示直连，可显式填写现有支持的出站代理 |

不自动继承主模型或放行流量的代理地址。基础地址不得带凭据、query 或 fragment；只允许 HTTP/HTTPS，代理地址按项目既有协议规则校验。0/空默认字段会规范化为默认值；无效字段或未知字段报错。

GET `/api/settings` 对 Key 始终只返回 `****`（有 Key）或空（无 Key）。PUT 回传 `****` 保留原 Key，新的非空 Key 更新，空串清除；启用但无 Key 时拒绝。全部配置验证先于任一设置写入，验证失败不产生部分写入。配置和开关保存在同一设置值，避免启用与凭据分散写入。

首页高级设置使用一个 Classifier 下拉框，同时包含 Typesafe 和原模型选项。Typesafe 选项显示配置模型（默认 `jev-1.13.0`），“保存设置”和“设置”按钮在同一行；前者负责切换分类方式，后者在选择 Typesafe 后打开参数弹窗。弹窗配置 Key、模型、基础地址、超时、输入价格和出站代理，不再提供第二个启用开关，参数保存保持读取时的已保存 `enabled`。取消不写入，有未保存修改时确认放弃，保存期间禁止编辑和关闭。每次打开重新读取配置，保存后重新读取遮罩值，关闭后销毁表单，页面不长期保留新输入的明文 Key。统一选择、状态同步和多键事务契约见 [Classifier 统一模型选择](2026-10-06-classifier-selection.md)。

## 验收

使用临时数据库、可注入 HTTP 客户端与本地 HTTP Mock，不调用真实 Typesafe。覆盖概率映射和置信度分离、实际模型与 usage、输出免费及明细/汇总/计量一致、启用覆盖原 disabled、关闭恢复、capture/非 classifier/主 Auto/手动主请求/会话标题不调用、明确 header 与历史 classifier 标记识别、彩蛋保持、授权拒绝不调用、取消/超时、上游 HTTP 错误与非法响应、Key 遮罩及保留/更新/清除、无效配置不部分写入。

完成后执行相关 Go 测试、必要 race 检查、前端 build、候选 build、独立只读复审；真实 API、中文准确率、真实 Kiro UI 和部署另行验收。

## 源码验证与候选产物

- `go test ./typesafe`、`go test ./api`、`go test ./proxy ./store` 通过。HTTP Mock 覆盖真实请求形状、三类概率原样映射、实际模型与 usage、置信度分离、开关与原 disabled 的优先级、授权拒绝、标题排除、Auto 路由与模型记忆保持、HTTP 错误、非法答案、取消和超时。
- 概率为 `null` 的回归使用 `chat=null / do=1 / spec=0 / choice=do`；如果错误地把 null 转为 0，概率和仍为 1，测试就会失败。非法分类答案仍含完整有效 usage 时保留记账；无法取得 usage 时明细记录 `usage_unavailable`，日志标记 `usage_available=false`。
- `go test -race ./typesafe ./proxy -run 'Test.*(Typesafe|Classify|NewClientStopsRedirects|TaskAuto|AutoClassifier|ConversationModel)' -count=1 -timeout 90s` 通过（typesafe 7.521 秒、proxy 12.317 秒）。
- `npm --prefix frontend run build` 和 `git diff --check` 通过。没有截图；未实际操作 Kiro 或管理界面。代理边界 Mock 直接调用 `tryTypesafeClassifier`，没有覆盖完整 goproxy `ServeHTTP` 链路。

本轮最初的桌面候选位于 `bin/kiro-proxy-2.1.9-typesafe-classifier.exe`，构建使用 `-tags desktop`、嵌入该轮前端和版本 `2.1.9`。大小为 `16,482,816` 字节，SHA256 为 `AAE985FD0576E308A6EF02397C35B07B4A2F9304B3111031E6EB5D7EA0FE118D`。该候选包含前一轮 Auto/Credits 改动及 Typesafe 接入；先前的 Auto 候选保持原文件，不包含 Typesafe。用户随后要求将设置移入弹窗，最新布局的候选和 Mock 验证记录见 [Auto 与 Typesafe 设置入口](2026-10-06-settings-ui-placement.md)，旧 Typesafe 候选不包含这次布局调整。

首次编译的是无 desktop 标签的控制台候选，已备份到 TEMP，再以相同桌面构建方式重建上述最终候选。测试阶段的编译错误、合成缓存计费期望与既有合同不符，以及超时 Mock 未读取 POST 请求体造成的挂起，均已修正并复验通过；未据此改动生产缓存计费或超时策略。

`gpt_6_astra_high` 独立只读复审通过，无 blocker；逐项核对请求边界、授权、Key 处理、协议映射、计量、Auto 隔离和候选哈希，确认 `windows/amd64` 与 desktop 标签。审查者采纳已报告的测试结果，没有重新运行测试。本轮没有启动候选、登记指纹、部署或修改实际代理设置。

非阻塞验证边界：Mock 未覆盖完整 goproxy 链路；图片 bytes 与工具非文本字段过滤已核对源码，但缺少专门的 Mock 回归断言。真实 API 连通性、中文分类质量、真实 Kiro 交互和用户数据库升级仍未验证；填写 Key 与启用应在部署后通过管理界面完成。
