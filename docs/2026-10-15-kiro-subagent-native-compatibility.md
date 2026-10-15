# Kiro 子代理原生调用与上游兼容

## 日志结论

本次用户确认的官方调用记录截至 2026-10-15 02:34:42。三次 `run_workflow` 调用同时包含有效的 `workflowPath` 和 `workflowPrompt`，原生工具返回 `Exactly one of \`workflowPath\` or \`workflowPrompt\` must be provided, not both.`。这是原生参数校验失败，不能归为 HTTP 转发失败。后续只传工作流路径与输入的调用获得 `running` 调度确认；调度确认不等于整个子任务已经完成。

抓包 `000193` 中同一个 `toolUseId` 的 134 个输入片段可以拼接为一个完整 JSON 调用，字段是 `workflowPath`、`inputs`、`runLabel`，没有 `workflowPrompt`。相邻 planner 会话与该调用缺少直接 ID 关联，不能凭时间相邻认定为同一条完整成功链。其他记录还包含 `execute_pwsh` 中断和 PowerShell 无法识别 `head`；它们属于工具执行问题。数据库中的 `raw Responses body closed before EOF` 发生在另一条较早请求，不能当作这三次参数失败的原因。本批未定位到 `getSession` 错误条目。

原生 handler 以非 `null` 且非空串判断是否提供路径或提示词；空白字符串仍算已提供。两项只能有一项有效。提示词分支不允许非空 `inputs`，允许省略或空对象。代理继续使用 Kiro 原生工具名、字段、调度、父子会话和工具结果，不修改模型输出的参数、不自动重放工作流。

## 本轮修复契约

原生 Responses 转发与传统 Kiro 请求转换应复用相同的、已经存在的工具 schema 兼容规则，并按实际配置的上游端点选择规则：

- OpenAI Responses 出口只对 `run_workflow` 中已确认的 Unicode 属性正则做等价字符范围改写。
- OpenAI Chat 出口复用已有的空 `not` 分支简化与已观察的 `todo_list` 引用修复；保留引用指向和其他 schema 约束。
- Responses 转 Chat 时，函数参数 schema 使用原始 JSON 保留数值精度；保留 `strict` 的原始值、描述和其他函数定义字段。不得新增或强制启用 `strict`。
- 保持工具数组顺序、名称、字段默认值和必填性。schema 规范化不得修改原始输入或共享缓存。
- 只在选定自定义 OpenAI 上游的出口生效；抓包模式和官方透传保持原生定义。请求历史中的工具调用参数与结果不作改写。

schema 规则在 `prepareRawResponsesUpstreamBody` 按最终端点选定，随后才转换 Chat 请求；转换时自动生成的缓存键以实际规范化后的工具定义为依据，已有调用者缓存键保持原值。共享的 `responsesToolsToChat` 仅负责字段和数字保真，不为其他兼容入口额外应用 schema 规则。平铺 Responses 函数工具的 `type` 留在工具包装层，其他定义字段移动到 Chat 的 `function` 对象；已有嵌套 `function` 按其对象保留。保留未知定义字段不构成上游接受这些字段的保证。

不在本轮加入新的 XOR 组合约束。现有工具说明已表达互斥规则，上游对新增组合的支持尚未得到验证；因此本轮不声称能消除模型生成非法参数的问题。

## 与 host 接口问题的关系

仓库已提交的 `getSessionKiroProxyRoute` 修复负责 Auto 模式下 `agent://` 的父路由继承。当前安装扩展仍含 `t.host.getSession`，没有窄 getter；仅凭新源码或新 EXE 不能宣称运行中的 Kiro 已加载修复。本轮不修改安装目录或重启 Kiro。

## 验收

使用本地 Mock 上游验证原生 Responses 与 Chat 两条实际出口、单模型与模型组、失败参数及后续合法调用的续轮保留。覆盖 schema 正则、空分支、引用、大数、`strict`、工具顺序与原始输入不可变。保留 Kiro 原生错误，验证抓包与官方透传边界。真实 Kiro 与用户上游需另行实测，不能以 Mock 通过代替现场验证。

## 本轮验证结果

- converter schema、缓存与审计定向回归通过，JSON 测试日志记录 20 个通过事件（含子用例）。
- 既有 raw 转发、续轮、失败终态、协议适配与手动路由边界定向回归通过，记录 102 个通过事件（含子用例）。
- 新增工作流 Mock 回放通过，覆盖 Responses/Chat × 单模型/模型组四条实际 HTTP 出口。原始双参数调用、Kiro 错误、后续合法调用、运行确认与子工具结果均保留；这只是转发回放，未运行真实 Kiro 调度。
- 新增边界测试覆盖 `strict` 缺省/真/假/空值、大数、扩展字段、重复工具名与顺序、nested 定义、共享转换器和 capture 不额外规范化、尾随 JSON 不被吞掉。首次运行因 nested 测试夹具没有有效 input 而失败，补充合成输入后通过；生产代码没有因此改变。
- Windows amd64 dev 候选 `kiro-proxy-dev-subagent-compat.exe` 构建通过，使用 `desktop,dev`、CGO、GUI 子系统，保留调试节。大小为 40,306,493 字节，SHA256 为 `7fe76ec539595bcb2934869ade9cd800156da2f78e56effeb35c3c9678564ede`，25 个当前前端资源均已核对嵌入。

原始测试日志、构建身份与源文件摘要位于本机 TEMP 的 `kiro-subagent-compat-136acb2aa3d940a0b3d04e6e3171bfd5` 目录。该次验证没有运行候选 EXE、修改真实 Kiro 或调用真实上游。
