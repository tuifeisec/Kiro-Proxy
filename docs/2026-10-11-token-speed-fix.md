# tok/s 协议与统计口径修复

本契约取代 2026-10-08 文档的测速公式和历史样本处理；布局、费用和原始 Token 统计保持原语义。用户本次授权本地模拟回归测试，不请求真实模型、不修改真实数据库、不部署或重启。

## 查证与根因

2026-10-11 查阅官方在线说明：

- [NVIDIA AIPerf 最新指标参考](https://docs.nvidia.com/aiperf/reference/ai-perf-metrics-reference.md)：逐请求输出速率是 ITL 的倒数，须与包含首 Token 等待时间的端到端吞吐、并发总吞吐区分。[GenAI-Perf](https://docs.nvidia.com/deeplearning/triton-inference-server/user-guide/docs/perf_analyzer/genai-perf/README.html#metrics) 明确排除首 Token。
- [OpenAI 推理说明](https://developers.openai.com/api/docs/guides/reasoning)：隐藏推理计入 output_tokens；output_tokens_details.reasoning_tokens 可单独读取。
- [Responses 事件](https://developers.openai.com/api/reference/resources/responses/streaming-events)：done 是最终快照；custom_tool_call_input.delta、refusal.delta 是真实增量。
- [Chat 流事件](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events)：usage 在请求 include_usage 后的末尾事件返回，普通事件可以是 null。
- [Anthropic 流式说明](https://platform.claude.com/docs/en/build-with-claude/streaming)：signature_delta 为签名，message_delta 的 usage 为累计值。
- [Gemini 用量](https://ai.google.dev/api/generate-content#UsageMetadata)：candidatesTokenCount 与 thoughtsTokenCount 分开。

代码中可证实的问题：首末分块区间却使用包含隐藏推理或估算的输出用量；完成快照和签名被当成新内容；自定义工具与拒答增量漏识别；一次 Read 内逐事件调用时钟产生假的时间差；首末时间各自截断到毫秒；首事件探测读出的前缀在测速开始后重放，计时偏移。不同上游还可能采用不同吞吐定义。用户仅确认 GPT，未提供网关名称或成对样本，不宣称已与具体上游数值完全对齐。

## 修复行为

新记录独立保存 `speed_output_tokens`、`speed_duration_ns`、`speed_basis`。测速不使用字符估算的 Token，也不修改原有费用和用量字段。

采用逐请求解码速度近似：`tokens_per_second = (speed_output_tokens - 1) × 1e9 / speed_duration_ns`。分子扣除首 Token，分母在首次到末次真实内容增量的高精度时间点之间直接相减。同一次底层 Read 的事件使用同一个到达时间；因此整段缓冲响应没有虚假的测速区间。SSE 分块未必逐 Token，首块可能含多个 Token，故本指标仍是代理观测近似，不等于模型内部计时。

Chat/Responses 从真实 upstream usage 提取输出数，排除 usage 中的 reasoning_tokens，测速区间只包含正文、拒答和工具内容，`speed_basis=visible_output`。原始推理或摘要仍参与首内容延迟，但不加入正文测速区间；若流中有推理而 usage 没有推理细分，则不能证明分子对应，返回用量不支持。没有推理事件且 usage 未提供细分时按其声明的输出数测量，`speed_basis=reported_output`，详情明确“未提供推理细分”，不宣称已经排除隐藏推理，不推测模型名。已知 rejected_prediction_tokens 从测速分子排除；未观测音频输出不参与此文本指标。Gemini 使用 candidatesTokenCount 与非 thought 内容区间；Anthropic 使用 output_tokens 与可见文本、thinking、工具内容区间，`speed_basis=output_with_reasoning`。摘要本身与隐藏格式 Token 无法精确分词，进一步限制仍在详情说明。

Responses 的 done、part/item 完成快照、空工具开始、签名和服务端工具执行状态不更新测速时间。自定义工具输入、函数参数、MCP 参数、拒答、代码增量须识别。若已观察到音频输出而不能将其用量和正文分开，不返回文本速度；不依赖模型名推测。首内容延迟可以记录推理摘要，测速另用对应区间，不混用两种时间。

在上游响应交付给首事件探测前挂载观测器。请求转换与本地 API 路由明确传递本次发送的协议，测速和首事件探测共用该协议；自定义 URL 不用于猜测协议，classifier 与 Responses→Chat 兼容重试使用实际 Chat 协议。探测前缀经过观测后重放时不再重复计时；最终响应复用同一观测器并接入既有延迟回调。失败重试的观测不成为最终样本。

四种流转换器以实际 SSE 空行分隔符为事件边界，不在终态 JSON 行之后提前关闭源流。LF/CRLF 分隔符跨 Read 时仍完整读取最终用量；完整终态后及时结束，不等待 HTTP EOF。EOF 的空字符串不作为分隔符，缺失分隔符不补全为完整测速。正常错误事件、取消和下游写失败沿用原错误路径，保留受控失败。

请求 API 返回 `token_rate_status`，区分有效、旧记录、非流式/未测量、失败、不完整、无可识别输出、用量缺失或不支持、Token 不足、区间不足。列表仍显示两位小数或 —，悬停和详情解释原因，并显示本次测速 Token、区间与口径；不扩大表格或改变字号。

本地 Chat 流请求在未明确指定 include_usage 时补充 `stream_options.include_usage=true`，保留其他 stream_options 和客户端明确的 false。按协议，上游因此可以追加 usage-only 事件，客户端仍接收正常 SSE；明确禁止用量或上游未提供用量时显示缺值原因，不以估算代替。

旧记录缺少独立分子和高精度区间，不反推或重新解释。保留旧测速日汇总，新测速写入 `generation_speed_v2_sum/samples`，现有 API 的 generation_speed_sum/samples 读取新列；避免旧、新口径混合。Token、金额、请求数历史照常保留，新口径平均从本版本运行开始累计。有效单次速度仍取算术平均。

## 验收

本地确定性时钟和模拟流覆盖 Chat、Responses、Anthropic、Gemini、隐藏推理、缺失 usage、同 Read 合并、亚毫秒间隔、快照/签名排除、自定义工具、拒答、探测前缀重放、部分流/失败。临时 SQLite 验证新增字段、旧记录空值、事务汇总、跨日合并和清空。前端构建、Go 编译与独立只读复审通过后交付；真实上游数值仍需对应请求样本核对。

## 实施与验证记录

源码在当前 `main` 工作区完成，未创建分支、工作树或仓库副本，未提交、推送、部署或重启。独立复审发现的已知 rejected Token、自定义 URL 协议、混合音频输出、MCP 参数和终态 SSE 分隔符边界已修正，并加入本地回归；本次实际发送的协议从请求转换和本地 API 路由传至原始响应，首事件探测与测速共用同一协议。

最终本地验证通过：

- `go test ./store -run 'TokenSpeed|RequestUsage|Stats'`：9 个顶层测试、含子用例 21 项，覆盖真实临时旧库迁移、费用和原始 Token 保留、纳秒公式及日汇总隔离。
- `go test ./proxy ./converter ./api -run 'ContentMetric|TokenSpeed|RequestUsage|Stream|LocalCompat|Stats|SSEPrecommit|FirstByte|ForwardCandidateRetries|ForwardCandidatePreserves|KiroClassifierAndRetry|OpenAIResponsesSchemaNormalization|SSEEvent'`：proxy/converter 的 75 个顶层测试、含子用例 169 项通过；api 包完成构建及空筛选运行（`[no tests to run]`），没有执行 API 行为回归用例。测试覆盖四种协议、原始响应探测至持久化链路、classifier、自定义 URL 和协议兼容重试。真实转换器回放覆盖 LF/CRLF 分隔符跨 Read、完整终态后保持连接、缺失分隔符、错误事件和取消；验证及时结束、不依赖 HTTP EOF，并在临时 SQLite 中核对最终用量与速率。全部使用本地 Mock 或临时数据库。
- `npm.cmd --prefix frontend run build`、`go build -tags desktop,dev -ldflags="-H=windowsgui"` 和 `git diff --check` 通过。

新开发版为 `D:\proxy\kiro-proxy-dev-token-speed-fixed-20261011.exe`，大小 35,156,819 字节，SHA256 为 `fbed4982ba982dd22dd002e943e74d3d819dc4027c19f823c89af9e8f6ec6b49`。已在终态 SSE 修复后重新构建并静态核验为 Windows AMD64 GUI 程序，当前前端 dist 的全部 25 个文件逐字节嵌入；未启动或替换正在运行的程序。旧构建和改动前的前端文件保存在任务临时备份中。

终态 SSE 修复后，新的独立只读 Astra/max 复审通过，未发现 blocker。复审独立核对四种实际转换器与原始响应观测链路、历史问题修复、v2 汇总隔离、前端说明、测试日志和候选 EXE；源码与产物均与最终验证一致。合计 84 个顶层测试、190 项含子用例通过。

真实数据库迁移、运行时界面及具体 GPT 网关的成对测速未执行；新口径平均值从新版运行后开始累计。非流式或合并为单次读取的输出缺少可靠解码时间区间，不伪造速率。
