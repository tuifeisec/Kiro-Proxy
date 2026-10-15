# 原生 Responses 请求准备与转换计时

## 目标与范围

补齐 Kiro 原生 Responses 单模型和模型组出口的 `conversion_ms`，并减少请求准备中的重复顶层 JSON 解析和中间编码。只修改本地实现，不调整模型路由、提示词、工具 schema 规则、缓存容量或淘汰策略，不应用 Kiro 桥接、不重启程序、不调用真实模型上游。

## 行为契约

1. `conversion_ms` 记录原生请求准备所花的时间，包括现有 metadata 清理、模型和推理参数设置、工具 schema 兼容处理，以及配置为 Chat Completions 时的协议转换。计时在发送 HTTP 请求前结束，不包含连接、上游等待或响应读取。模型组每次候选实际准备分别累计一次，沿用现有整数毫秒口径；亚毫秒准备仍允许为零。
2. 保持原有请求校验、错误及结算语义。不得把原生路径未执行的审计和续传工作计入 `audit_ms` 或 `session_read_ms`，不得为本次计时增加请求重放、错误兜底或额外统计记录。
3. 请求准备使用请求私有的 `map[string]json.RawMessage` 复用顶层解析结果。必要的嵌套字段解析继续执行；不把当前请求对象保存在跨请求缓存中，不修改输入字节、`Preflight` 的共享正文或模型配置。
4. 对相同输入、相同模型配置和相同路由，最终上游请求正文与改动前逐字节一致。这包含 `prompt_cache_key`、提示词及历史前缀、工具顺序及定义、精确数字、未知字段、推理参数和现有 metadata 边界。调用者显式缓存键保持原值；自动键继续使用原有算法及版本。
5. 保持 Responses、Chat Completions、普通本地兼容入口、抓包和官方透传的现有职责边界。复用内部解析结构不得扩大工具 schema 规范化的应用范围。

## 验证方式

- 在生产实现改动前，用合成输入冻结现有上游正文和错误的固定基线；回归比较两种配置端点的完整输出字节及原始输入不可变性。
- 覆盖显式及自动缓存键、稳定前缀的多轮请求、工具规范化、工具顺序、nested 定义、`strict`、大数及精确小数、未知字段、推理覆盖、metadata 清理和非法输入。
- 使用本地 Mock 上游和临时数据库验证单模型、模型组及准备失败的计时边界、持久化和原有结算。网络等待不得计入转换时间。
- 运行相关缓存、schema、协议、续轮和取消回归；按涉及的共享函数范围补充普通本地兼容入口回归。
- 用同一组合成输入、模型配置、大小、Go 工具链和基准命令比较改动前后的耗时、累计分配与分配次数。`B/op` 为累计分配，不表示存活堆或进程峰值；局部基准不代表真实上游命中率或端到端提速。

## 验证结果

### 实现取舍

原生完整准备链路的顶层解析由 Responses 的四次、Chat 的更多次降为两次。清理与推理参数处理共享第一阶段的请求私有根对象；工具规范化与 Chat 映射共享编码校验后的根对象。必要的编码校验仍按原先顺序保留。普通本地兼容字节入口保留原有中间处理流程。

未采用完全取消中间编码的方案。旧实现使用的 `go-json v0.10.6` 在编码 `json.RawMessage` 时还会校验数值范围、压缩空白并处理 HTML 转义；例如 `1e999` 原先会报错，跳过编码可能让 Chat 转换成功或改变转为字符串的内容。清理改变正文时还必须在覆盖模型和推理参数前保留原有校验，否则可能掩盖随后被覆盖字段中的错误。这些边界由未修改生产代码的隔离 overlay 生成固定基线验证。

计时使用转发器实例私有时钟依赖进行 Mock，生产默认 `time.Now`，不增加用户配置。单模型和模型组成功、候选切换及准备失败共九个子用例验证准备阶段的毫秒数、持久化、结算次数及并发租约。Mock 网络将同一时钟推进一小时，入库的准备耗时仍分别为单次 17 毫秒、两次 34 毫秒，证明网络等待未混入。

固定基线共 29 个用例：生产修改前冻结的 19 项完整输出与错误保留原文；另外 10 项通过 Git HEAD 六个生产文件的测试 overlay 追加，只从旧实现生成期望值。覆盖 `1e999` 拒绝、`1e308` 与精确数字、HTML/U+2028 转义、Chat 丢弃字段仍需校验、清理与推理覆盖的错误顺序等边界。最终 29 项逐字节正文/错误对照、输入不可变性及上述计时用例均通过。

```powershell
go test -mod=readonly ./proxy -run '^(TestPrepareRawResponsesUpstreamBodyMatchesFrozenContract|TestRawPreparationTiming)' -count=1
```

相关 `proxy`、`converter`、`store` 定向回归通过，覆盖原生单模型和模型组、手动选择、metadata、工具 schema、缓存键与前缀、Chat 映射、普通本地兼容、续轮和 usage 持久化。最终两次解析实现还通过以下定向竞态检查：

```powershell
go test -mod=readonly -race ./proxy -run '^(TestRawPreparationTiming|TestPrepareRawResponsesUpstreamBodyMatchesFrozenContract|TestResponsesPromptCacheKey|TestResponsesCacheContract|TestAutoGroupRaw|TestForwardRawResponses)' -count=1
```

最终由未参与写入的 Sol/xhigh 只读复审者核对六个生产文件的 diff、29 项基线及旧新 oracle 内容、计时测试、基准与本文档，未发现阻止交付的问题。测试由主代理运行并提供执行证据，复审者未重复执行测试。交付范围为源码和测试，不包括替换运行程序、部署或重启。

### 既有测试失败

整包执行 `go test -mod=readonly ./proxy ./converter ./store` 时，`converter` 通过，`proxy` 和 `store` 各有一项既有失败。两项均已用 Git HEAD 的六个改动前生产文件覆盖为只读测试 overlay 定向复现，本次不扩大范围修改：

- `TestDutyAutoGroupRecordsActualMemberAfterCapacityDispatch`：`first byte timeout must be between 10000 and 300000 milliseconds`。
- `TestParseAutoRoutingConfigRejectsLegacyAndInvalidInput/task_rules_mode`：`migration error = legacy task_rules Auto configuration is unsupported; stage_fixed is read-only compatibility and duty_fixed v2 is supported`。

未设置真实 Kiro 大文件输入变量的桥接测试仍按原约定跳过，不将其算作本次真实桥接验证。

### 基准结果

Windows amd64、Go 1.26.4、Intel Core i5-13400F，同一基准命令各运行三次，分别取 `ns/op` 和 `B/op` 的中位数。输入构造在 `ResetTimer` 之前，循环只调用本地请求准备函数，不发送 HTTP。

```powershell
go test -mod=readonly ./proxy -run '^$' -bench '^BenchmarkRawResponsesPrepareCompatibility$' -benchmem -count=3 -benchtime=500ms
```

| 端点 | 合成文本大小 | 改前耗时 | 改后耗时 | 耗时降低 | 改前 B/op | 改后 B/op | 累计分配降低 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Responses | 256 KiB | 2.792 ms | 2.104 ms | 24.6% | 4,266,783 | 2,494,770 | 41.5% |
| Chat Completions | 256 KiB | 7.569 ms | 4.024 ms | 46.8% | 10,917,459 | 4,618,100 | 57.7% |
| Responses | 1 MiB | 7.864 ms | 5.974 ms | 24.0% | 18,336,591 | 10,269,587 | 44.0% |
| Chat Completions | 1 MiB | 19.589 ms | 10.162 ms | 48.1% | 44,326,976 | 18,946,243 | 57.3% |

改前原始数据保留在生成固定基线时的会话工具输出；改后完整输出保存于本机临时日志 `%TEMP%\<raw-responses-after-benchmark-local-id>.log`。小样本耗时受调度与 GC 影响，表中只表示此次合成请求的本地准备成本。累计分配降低有助于减少临时对象与 GC 压力，不能换算为进程常驻内存下降、真实上游缓存命中率提高或端到端同等比例提速。
