# 缓存与对话延迟优化实施记录

授权：2026-09-08，用户要求依次处理代码评估中的问题。基线为 `main` 分支 `b70df288a067e70724a28bf246569941ad57ab6b`。

原始评估文档记录改动前的证据和问题；本文件记录实施后的状态，二者不应混作同一版本的行为说明。

## 范围与不变条件

在当前工作区和分支实施，不部署、不重启正在运行的服务、不访问真实模型上游、不截图。测试使用临时数据库及可控制的本地 Mock。保留分组优先级、备用组阶段、并发限制、原始错误和响应提交后不重放的契约。缓存预算只约束缓存占用，不拒绝原本合法的业务请求。

## 分项验收

| 顺序 | 工作项 | 验收重点 | 状态 |
| --- | --- | --- | --- |
| 1 | 首事件与首内容指标 | 多协议分片、元数据先到正文迟到、纯工具输出、旧数据未知值、持久化及 UI、提交语义不变 | 已实现，测试及独立复审通过 |
| 2 | 动态提示词与续接连续性 | 保留内容和指令层级；两轮/多轮输入与续接一致；工具回合、上下文压缩不误续接 | 已实现，测试及独立复审通过 |
| 3 | 会话状态生命周期 | 同配置换模型/账户/地址隔离，并发倒序完成不覆盖新状态，读取 TTL 与运行期清理 | 已实现，测试及独立复审通过 |
| 4 | OAuth 并发与配置快照 | 同账号刷新合并、不同账号并行、取消、失败、落库冲突与手动刷新协调，路由快照不被请求修改 | 已实现，新增回归/race 及独立复审通过 |
| 5 | 工具缓存与审计开销 | 按字节和条目淘汰、同键 miss 合并、按需转换、超大项正常转换但不缓存、审计及会话耗时可见 | 已实现，含自动缓存键补充项，测试及独立复审通过 |
| 6 | 抓包流式处理 | 首段及时到达、转发字节不变、日志有界、异常和关闭只结算一次 | 已实现，测试及独立复审通过 |
| 7 | 客户端池与重试观测 | 空闲回收不打断活跃响应；停机关闭空闲连接；组合回退次数可核对、总预算不放大 | 已实现，测试及独立复审通过 |
| 8 | 集成及独立复审 | 相关 Go 普通/race 测试、前端构建、独立只读复审、变更与验证对应 | 已完成，必需改动及验证均已收口 |

## 方案约束

首内容观测只解析已读取的响应，不为获取指标提前读完整流，也不改变 SSE 的提交时点。旧 `first_byte_ms` 保留兼容，新指标明确标识首内容是否被识别。

Responses 续接需要同时满足请求语义、上游状态与本地状态一致；不能通过删除动态指令或将 developer/system 改成 user 来提高命中。账户身份和配置代次应稳定，不使用每次轮换的 access token 作为缓存身份。

数据库会话状态在客户端结束前可见的顺序继续保留。是否改成异步持久化取决于测量和一致性证据，不把所有写入直接改成后台 goroutine。

## 验证结果

最终代码已通过以下检查，所有实施项的独立复审均已收口：

- `go test ./...`：全部通过。
- 首轮运行 `go test -race ./codex ./converter ./store ./router ./proxy ./upstreamproxy ./test -count=1` 全部通过。凭据修复及补充项完成后，`codex`、`store`、`api`、`proxy`、`test`、`router` 分组重新运行 `-race -count=1`，全部通过；未改变的 `converter` 和 `upstreamproxy` 继续沿用该稳定版本的验证结果。
- `npm.cmd run build`：前端构建通过，包含最新首响应阈值说明。
- `go test -race ./converter -run ToolCache -count=1`。
- `go test -race ./converter ./store ./proxy -run 'ToolCache|CaptureObserver|ClientPool|ResponsesSession|ResponsesDynamicInstructions|ResponsesContinuationPreserves' -count=1`。
- `go test -race ./proxy -run ForwardResponsesDynamicContinuation -count=1`，验证第二轮发出增量且下游读到 EOF 时会话状态已可见。
- `go test -race ./upstreamproxy -count=1`，包含协议回退后再次请求的真实 RoundTrip 计数以及 deadline 不被重置。

中途集成曾遇到尚未写完的 `audit_ms` 迁移，以及指标 hook 编辑过程中的首包样本测试失败；稳定代码统一回归均已通过。早期指标代理写入的 `store/db.go` 出现全 NUL，已保留异常文件副本，从可恢复的 Git 基线还原后重加迁移并通过临时数据库测试。所有改动源码 UTF-8/NUL 检查和 `git diff --check` 通过。

独立 Sol 首轮已通过工具缓存、Responses 续接/状态、抓包、连接池及指标主要边界。凭据范围发现配置编辑被旧快照覆盖、刷新迟到窗口、多代轮换传播、路由与新凭据混配、敏感缓存无界及过期时间重算等 blocker，随后完成精确修复与新增回归；此前通过的测试没有覆盖这些问题，不作为该范围最终完成依据。Luna 补充复审已通过自动缓存键规范化与 DONE-only 首事件统计。后续 Sol 发现用量派生信息未保存、手动导入混合凭据、配置删除后误用旧快照及跨 Provider 恢复旧密钥四组问题，修复后相应边界已通过新的 Sol 核对。Provider 比较规范化、同一 refresh token 的 JSON 包装比较及旧 Provider 值显式修正，也已完成精确修复、API race 与新增回归，并由新的独立 Terra/Luna 分别核对相关边界。当前无未修复的阻断项；非阻断生命周期限制和运行环境未验证项见文末。

## 已落地的实现选择

`CredentialManager` 统一自动刷新、手动刷新和模型测试入口。路由返回独立 `Model` 快照，凭据刷新不再修改共享路由对象。按账户身份及 refresh generation 合并并发刷新；取得刷新所有权后重读数据库，迟到请求发现代次已变时重新选择最新代次。不同账户、不同 refresh generation 可以独立刷新，等待者可取消，leader 取消后存活请求可重试。

刷新成功后，将同账户、同一旧 refresh generation 的模型凭据在一组事务 CAS 中同步；某行已被手动导入替换时，冲突事务整体回滚。CAS 使用存储中的原始 access/refresh/account 字段，因此 auth JSON 与空字段也能准确比较。只在收到 provider 结果时计算一次绝对过期时间；后续模型和失败恢复复制这个时间戳，不重新累加 `ExpiresIn`。成功结果不放入长期内存 token 缓存。落库失败时保留待恢复的整代快照，重试事务成功后清除；恢复信息不能像普通缓存一样随意淘汰，否则可能再次使用已失效的旧 refresh token。

读取或刷新后会核对 provider、有效 BaseURL、endpoint、代理、地址后缀开关及可用账户身份。配置发生不相容变化或被删除时返回凭据冲突；旧请求不会将新凭据发到旧地址。只有没有数据库或输入 ID 为空时才允许临时凭据路径；明确创建入口在预检测时临时清空 ID，保留客户端指定 ID 的正常创建能力。新增 Mock 验证覆盖连续两代轮换、迟到刷新、auth JSON 原始值、过期时间、批量回滚、真正的 PUT/POST 保存，以及八类配置变化下上游收到零次请求。

普通模型 PUT 用已有配置预检测凭据，再将凭据与用量字段合并回用户本次编辑；配置 SQL 原子保留当前凭据。用量 JSON、套餐及邮箱在一条凭据条件 UPDATE 中保存，用量查询期间切换账户会返回 HTTP 409，不会写入旧账户数据。手动导入新 refresh token 时清除继承的旧 access token、过期时间和账户资料，并获取配套凭据；仅更改 access token 而保留旧 refresh token 的不完整组合明确拒绝。跨 Provider 切换要求显式提交目标 Provider 的凭据，不用掩码恢复旧 Provider 密钥。

POST/PUT 在进入凭据分支前统一 Provider 的大小写与空白。比较 refresh generation 时先解析 auth JSON，不能把同一令牌的包装变化误认为新一代；同代手动刷新保留已知 DB ID 与身份，既能共享自动刷新，也能在取得所有权后重读最新代次。纯包装变化路径只合并凭据与用量，不覆盖用户本次配置编辑。新增测试覆盖大小写/空白 Provider、跨 Provider 无出站请求、JSON 伪装的 access-only 变更拒绝、包装令牌的并发取消与迟到刷新，以及不存在模型的 PUT。

数据库中既有的非规范 Provider 值按运行时实际识别方式处理；修正为目标 Provider 属于显式切换，需要提供目标凭据，不从掩码恢复旧密钥。测试同时验证掩码提交会被拒绝且不改库、不请求上游，以及提供新凭据后能够正常保存规范 Provider 和配套凭据。

请求用量新增 `first_event_ms`、`first_content_ms`、`first_content_type`、`max_content_gap_ms`、`content_measured` 和 `content_measurement_complete`，区分协议事件、文本、推理和工具输出。元数据不计入首内容；超出观测预算或非法 JSON 后保留 partial 状态，不将缺失区间之后的内容当成准确的首内容样本。首内容汇总排除未知和 partial，已测得 `0ms` 仍参与平均。分组健康指标对成功 SSE 使用首内容，对成功非 SSE 延续首字节口径，界面统一称“首响应”；HTTP 错误响应延续旧行为，不新增延迟样本。观测不改变响应提交后禁止重放的规则。

新增 `audit_ms`、`session_read_ms`、`wire_attempt_count` 和 `transport_fallback_count`；会话提交日志包含 `session_write_ms`。这些字段用于把本地审计、状态操作和真实网络尝试分开定位，不把逻辑 retry 次数当成实际网络尝试数，也不将不同请求的阶段 P95 相加。

工具缓存按 OpenAI / Anthropic 分别惰性构建，同一协议和内容哈希的并发 miss 共用转换结果。预算为最多 100 个转换结果、32 MiB 保留数据估算、10 分钟 TTL；超大项照常转换但不保留。OpenAI 工具规范化哈希和工具转换审计报告复用缓存，减少暖请求中的重复 JSON 转换和规范化。`GET /api/stats?type=cache` 返回命中、miss、合并、淘汰、超大跳过、估算保留字节和累计转换耗时。预算不等于进程 RSS，正在处理的请求仍可能持有已淘汰数据。

本地 Responses 接口自动生成 `prompt_cache_key` 时，对 `instructions`、`tools` 和 `prompt_cache_options` 进行对象键序及空白规范化，字段名进入命名空间，数组顺序保持不变。使用 `json.Number` 避免大整数和高精度小数经过 `float64` 后合并；不承诺把 `1` 与 `1.0` 等数字词法归成相同值。客户端已有的非空 key 原样保留，当前 user input 不加入 key。自动 key 使用 `kiro-local-v2-` 前缀，首次切换可能导致旧自动 key 对应的缓存关联重新建立。该 helper 同时用于本地原生 Responses 和 Chat fallback；最初评估将其描述为仅 fallback，调用范围在实施取证中已更正。

启用 Responses 续接时，每轮将 Kiro 的单一 `SystemText` 放入 `instructions`；普通未启用续接和 Codex HTTP SSE 路径保持现有请求布局。完整指令每轮重新发送，不把旧动态尾部放进续接历史。依据是 OpenAI 文档明确说明旧 `instructions` 不会随 `previous_response_id` 继承：[Responses API 参考](https://developers.openai.com/api/reference/cli/resources/responses/methods/create)。此选择不能推导为所有兼容上游均能提高 prompt cache 命中率；真实延迟和缓存收益仍须按上游验证。

会话 scope 包括转换布局版本、配置 ID、provider、上游模型、地址、endpoint、凭据身份摘要、reasoning、tier、静态指令与工具。旧布局不会继续复用。`BeginResponsesSession` 分配代次，只有最新发起的请求可发布状态；请求失败不需要等待长时间锁或租约。读取时执行 7 天 TTL 校验，运行中每分钟最多清理 256 个过期行；超过 4 MiB 的输入快照不保存。续接修改只替换 `input` 和 `previous_response_id`，保留未知顶层扩展字段。

抓包响应改为读取旁路，日志按配置预算采集，完整模式也有 16 MiB 上限。EOF、异常和 Close 只结算一次，保留原始响应字节及读取错误，并将采集字节数和截断状态放到结构化元数据。

HTTP client 池按响应体生命周期跟踪活跃请求，按 10 分钟空闲 TTL 和最多 16 个空闲 client 清理；活跃请求暂时可以超过空闲预算，释放时再次清理。停机关闭空闲连接，已取得旧 client 的请求仍可正常完成。新增底层尝试和协议回退观测，不改变现有重试次数、期限、分组与响应提交规则。

`GET /api/app-version` 新增当前可执行文件的 `build` 信息，启动日志使用实际 Git revision 和 Go 构建信息；缺少构建信息时明确显示 unknown，不再打印固定历史版本字符串。

## 运行边界与后续测量

本次保持单进程、现有路由分组和 SQLite 架构。Responses 状态票据使用 DB 实例内原子序列防止删除后旧请求覆盖新状态，适用于当前单进程单 DB 实例；多进程同时写同一数据库不在本次验收范围。

OAuth provider 已完成令牌轮换、但本地尚未提交时进程崩溃的窗口，不能由进程内协调器彻底消除。本次覆盖进程存活期间的失败恢复和事务不产生部分提交，不宣称提供跨进程或跨外部服务的分布式事务。

失败恢复的 `pending` 不是成功结果缓存：恢复提交或确认该代无数据库引用时会清除；若失败后手动删除或替换了凭据，且该恢复路径不再被访问，条目仍可能驻留至进程退出。当前不对尚有恢复价值的轮换凭据使用普通 TTL 淘汰。这是独立复审记录的非阻断生命周期限制。

尚未请求真实模型上游，也未用新源码替换或重启当前后端可执行文件。前端构建已执行；后端功能是否生效应以新版本运行后的 `build` 信息确认。此次不能量化线上提速百分比、真实 prompt cache 命中率或连接复用改善幅度。上线后应固定模型、账号、推理强度和上下文，分别比较冷/热连接下的首内容 P50/P95、内容间隔、真实缓存 token 与本地耗时，再决定数据库并发或更大规模结构重构是否值得投入。
