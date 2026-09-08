# Kiro Proxy 缓存、对话延迟与结构评估

评估日期：2026-09-08。当前代码：`b70df288a067e70724a28bf246569941ad57ab6b`，分支 `main`。本次检查代码、调用关系、现有离线测试和历史请求统计，未修改业务代码、部署或请求真实模型上游。

**判断：现有架构适合单机 Kiro 代理，值得做局部改进。优先解决首字指标口径、凭据并发和会话缓存连续性，再根据测量优化本地开销。暂时没有证据支持引入 Redis、拆微服务或整体替换框架。**

## 证据范围与实际耗时

参考项目均核对了具体源码，固定到以下提交，避免后续主分支变化影响结论：

- [new-api：71c1fd7](https://github.com/QuantumNous/new-api/tree/71c1fd7caad738db4d13aabbf28eeadb293d0cfe)。主要检查渠道缓存、缓存封装、HTTP 客户端及流式处理。
- [sub2api：772a038](https://github.com/Wei-Shaw/sub2api/tree/772a0382f079676983c06f24b0d41e09139a8462)。主要检查账号调度、提示词缓存键、OAuth 刷新、连接池和后台缓存写入。

从现有 SQLite 以只读模式提取最新 1000 条请求的结构化指标，范围为北京时间 2026-09-02 23:28 至 2026-09-08 17:10。没有读取对话正文或凭据。下表耗时和比例仅基于各组成功请求；P95 使用排序后的经验分位数。

| 历史请求类型 | 成功样本数 | 记录的首字 P50 / P95 | 转换 P50 / P95 | 上游等待 P50 / P95 | 连接复用比例 | 缓存读取 Token / 输入 Token |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| OpenAI → Kiro | 742 | 4.984 / 22.309 秒 | 4 / 7 毫秒 | 3.720 / 21.231 秒 | 85.7% | 73.3% |
| Codex → Kiro | 141 | 3.531 / 7.530 秒 | 3 / 6 毫秒 | 2.619 / 5.071 秒 | 46.1% | 90.8% |
| Anthropic → Kiro | 33 | 3.005 / 4.547 秒 | 1 / 2 毫秒 | 2.636 / 4.069 秒 | 97.0% | 99.3% |

这些数字说明已有上游缓存读取，并支持优先排查上游等待与长尾延迟。它们不是不同服务的公平速度比较：模型、上下文、推理强度、渠道和请求时间均未控制；Token 比例也不是请求命中率。

有两项重要限制：

1. 当前运行的 `D:\proxy\kiro-proxy.exe` 构建信息为 `vcs.revision=69867347ea248337cdd67c76b5e54abd9b4513fb`、`vcs.modified=true`，与当前 HEAD 不能精确对应。不能据此断言最新代码已经具备上述性能，也不能仅凭提交号断言二进制缺少哪些改动。
2. `conversion_ms` 不涵盖全部本地工作。续传查询、前缀审计等还在这个计时区间之外；各阶段的 P95 不能相加成总 P95。

## 已有机制应当保留

- [router/router.go:50](D:/proxy/router/router.go:50) 已从数据库加载内存路由表，普通模型路由并非每次读取数据库。
- [upstreamproxy/client.go:32](D:/proxy/upstreamproxy/client.go:32) 已实现连接复用、HTTP/2 与协议回退；不是每次对话都重新创建主转发客户端。
- [converter/tool_cache.go:23](D:/proxy/converter/tool_cache.go:23) 已有工具转换缓存；请求转换也有静态提示词拆分、缓存键和 Anthropic 缓存控制。
- [proxy/model_dispatcher.go](D:/proxy/proxy/model_dispatcher.go) 已有会话亲和、并发限制、健康状态和分阶段回退；[proxy/model_latency_tracker.go:55](D:/proxy/proxy/model_latency_tracker.go:55) 已有延迟统计与降级。
- [proxy/logger.go:97](D:/proxy/proxy/logger.go:97) 已有日志队列；[frontend/src/composables/useStats.js:4](D:/proxy/frontend/src/composables/useStats.js:4) 已共享状态与轮询。不应把“增加异步日志”或“避免前端重复轮询”笼统列为尚未实现的优化。

## 优先处理的发现

优先级说明：P1 是优先处理的正确性或明确体验问题；P2 是局部优化及特定场景风险。历史样本不足以证明下列每项都已经在用户运行环境发生。

**1. P2，性能诊断的首要前置工作：把协议首事件与真正的首内容分开统计。**

[proxy/stream_probe.go:234](D:/proxy/proxy/stream_probe.go:234) 接受 `response.created`、`response.in_progress`、`response.queued` 作为可提交事件；Anthropic 的 `message_start` 也会被接受。分组转发据此记录首字时间并更新延迟状态，见 [proxy/forwarder.go:630](D:/proxy/proxy/forwarder.go:630) 和 [proxy/forwarder.go:689](D:/proxy/proxy/forwarder.go:689)。

触发例子：上游 100 毫秒返回 created，20 秒后才返回文本。协议握手很快，用户仍等了 20 秒。当前分组指标无法表达这一区别，也可能把这样的候选评为快速。本次未测量历史样本的 metadata 到正文间隔，尚不能认定这种误判已在运行环境发生。

建议保留当前流式提交语义，另记 `first_event_ms`、`first_content_ms`，分别记录首段文本、推理或工具调用等事件类型；同时增加相邻内容片段最大间隔。是否允许切换上游，应继续由“是否已提交响应”的契约决定，不能因为首内容慢而重放已经输出的响应。以延迟 Mock SSE 验证元数据先到、正文迟到、纯工具回复和提交后失败四种情况。

**2. P1：集中管理 OAuth 凭据，合并同账号的并发刷新。**

[router/router.go:494](D:/proxy/router/router.go:494) 返回路由表中的模型指针；[proxy/forwarder.go:1277](D:/proxy/proxy/forwarder.go:1277) 在请求中调用 `EnsureAccessToken` 并直接修改该对象。[codex/client.go:74](D:/proxy/codex/client.go:74) 的过期判断与刷新之间没有按账号互斥或并发合并；[proxy/forwarder.go:1289](D:/proxy/proxy/forwarder.go:1289) 还忽略了刷新结果写入数据库的错误。

多个请求同时进入刷新窗口时，存在共享字段读写竞争和重复刷新；手动刷新路径持有独立模型对象，还需要与自动刷新协调。发生刷新令牌轮换时，过时结果持久化尤其需要防护。现有 race 测试通过不代表这个未覆盖场景没有问题。

建议将路由配置作为不可变快照，由独立 `CredentialManager` 按账号管理凭据：等待可取消、锁内二次检查、只执行一次刷新、条件更新凭据版本，持久化失败应显式报告。单机无需为此引入 Redis。可借鉴 [sub2api OAuthRefreshAPI](https://github.com/Wei-Shaw/sub2api/blob/772a0382f079676983c06f24b0d41e09139a8462/backend/internal/service/oauth_refresh_api.go#L159) 的进程内互斥、重读与二次检查。

验收应使用可注入的刷新接口：同账号并发请求只触发一次刷新，不同账号互不阻塞；覆盖等待取消、刷新失败、落库失败、手动与自动刷新竞争。

**3. P1，仅抓包模式：读取完整响应后才交付，会损失流式体验。**

[proxy/interceptor.go:353](D:/proxy/proxy/interceptor.go:353) 注册的抓包响应钩子在 `IsCaptureMode()` 时执行 `io.ReadAll(resp.Body)`，恢复 body 后才返回。对长响应，客户端必须等完整上游流结束才能开始读取；内存也会随完整响应增长。此结论不适用于普通 forward 模式。

建议改成随客户端读取而采集的有界响应包装器，结束或关闭时生成日志，并保留截断标记与读取错误。验收用分两段返回的 Mock：首段必须在第二段产生前到达下游，日志上限不影响转发内容。

**4. P2：动态 system 尾部仍会阻断 Responses 的严格前缀续接。**

[converter/kiro_to_responses.go:293](D:/proxy/converter/kiro_to_responses.go:293) 把动态尾部放在当前 user 前。若下一轮 Kiro history 没有回放该代理生成的 developer 项，两轮结构为：

```text
第一轮：static + history + dynamic_1 + user_1
第二轮：static + history + user_1 + assistant_1 + dynamic_2 + user_2
```

当显式启用 `EnableContinuation=true` 时，旧输入在 dynamic_1 位置就不再是新输入的前缀，[converter/responses_continuation.go:106](D:/proxy/converter/responses_continuation.go:106) 因而选择全量发送。续接配置默认关闭；未启用时不存在这项续接收益损失。动态尾部仍会影响最近一轮后缀的 append-only 形状，但静态提示词和已有历史仍可能命中上游缓存，不能把它描述为“缓存全部失效”。

建议先补带真实结构特征的两轮和多轮 Mock，同时检查完整输入前缀、续接结果和消息语义，再决定是否重建代理补充的历史项目。不要直接删除动态内容或把 developer/system 指令改成 user 来换取命中率。现有静态前缀测试尚未完整覆盖这条路径。

**5. P2：续接状态需要配置版本、并发更新规则和运行期过期处理。**

[proxy/forwarder.go:1342](D:/proxy/proxy/forwarder.go:1342) 的状态键包含会话、模型配置 ID 和推理强度。在启用续接的情况下，同一个配置 ID 换账户、provider、上游 `ModelID`、BaseURL 或 endpoint 时，旧状态仍可能被读取；不同配置 ID 本身已有隔离，不应误报为所有模型之间共享状态。

[store/responses_session.go:29](D:/proxy/store/responses_session.go:29) 使用无条件 upsert。两个相同状态键的请求重叠时，后完成的旧请求可能覆盖先完成的新请求。严格前缀校验能防止部分错误复用，但不能替代并发更新契约。[store/responses_session.go:43](D:/proxy/store/responses_session.go:43) 的读取也未检查过期；七天清理仅由启动迁移调用。

建议引入稳定的路由/账户身份指纹或配置代次，明确同一分支的请求序号及条件更新；不要使用每次刷新的 access token 作为身份。运行时分批清理过期状态，读取时拒绝本地已过期状态。以相同配置换上游、并发倒序完成、长时间不重启为 Mock 场景。`previous_response_id` 的使用本身不等于上游 KV 缓存命中。

**6. P2：缓存容量按字节管理，同键冷启动避免重复转换。**

[converter/tool_cache.go:23](D:/proxy/converter/tool_cache.go:23) 限制了 100 条和 TTL，但条目大小不固定。[converter/tool_cache.go:38](D:/proxy/converter/tool_cache.go:38) 的 miss 会同时构造 OpenAI、Anthropic 版本，多个相同 hash 的并发 miss 也可能重复计算。

建议增加总字节预算、按需构造 provider 版本、按 key 合并并发 miss。超大但合法的工具定义可以不入缓存，继续正常转换，不应把缓存预算自动变成新的业务拒绝规则。记录 hits、misses、evictions、bytes 和加载耗时。当前历史转换数据较低，因此这项主要服务于大 schema、并发冷启动和长期运行，不承诺明显降低单轮对话秒级等待。

另有一个较低优先级的兼容性项：[proxy/local_compat_responses.go:87](D:/proxy/proxy/local_compat_responses.go:87) 的自动缓存键直接使用 `RawMessage` 字节。它用于本地 `/v1/responses` 转 Chat Completions 的 fallback，且客户端未提供有效 key；仅当上游支持该字段时，键漂移才影响其缓存关联。语义相同的 JSON 对象若键序或空白不同，可能产生不同 key，不应把这项问题泛化为主 Kiro 转发链路。可规范化对象编码，保留数组顺序、数值精度和客户端显式 key。参考 [sub2api 的缓存键规范化](https://github.com/Wei-Shaw/sub2api/blob/772a0382f079676983c06f24b0d41e09139a8462/backend/internal/service/openai_compat_prompt_cache_key.go#L173)。

**7. P2：把前缀审计和持久化等待纳入耗时分析。**

[proxy/forwarder.go:891](D:/proxy/proxy/forwarder.go:891) 每个候选都会运行前缀审计；[proxy/forwarder.go:1475](D:/proxy/proxy/forwarder.go:1475) 再次解析/哈希完整输入。即使实际续接只发送增量，审计仍可能处理完整历史。这发生在 `conversion_ms` 计时结束之后。

建议转换时复用已计算的摘要，或提供可控的审计采样；先补 `audit_ms` 和会话读写耗时再决定投入。诊断与普通转发应有明确的成本边界。

[store/db.go:27](D:/proxy/store/db.go:27) 使用单 SQLite 连接，读写会共享排队；[proxy/forwarder_stream.go:157](D:/proxy/proxy/forwarder_stream.go:157) 在 EOF 等待观察任务完成，观察任务包括统计和会话保存。这样的顺序有保证下一轮能读到会话状态的价值。应保留必要状态提交顺序，只考虑把可延后的遥测写入移到有界队列，规定队列满、写失败和停机排空行为。不能直接把所有写入都改成 fire-and-forget，或只把 SQLite 最大连接数调大。

**8. P2，先定位：Codex 连接复用与客户端池生命周期。**

历史 Codex 成功请求的复用比例约 46.1%，TLS 耗时 P95 约 2.879 秒。这值得按相同账号、地址、代理和请求间隔分组调查；还不足以判定连接池有缺陷。

当前转发器已经按代理复用客户端。[proxy/forwarder.go:161](D:/proxy/proxy/forwarder.go:161) 的客户端 map 缺少容量/空闲淘汰；[proxy/server.go:297](D:/proxy/proxy/server.go:297) 关闭服务时未统一回收该池。主要影响频繁更换代理和长期运行场景，不应据此宣称每轮都在重建连接。

可参考 [sub2api HTTP 上游池](https://github.com/Wei-Shaw/sub2api/blob/772a0382f079676983c06f24b0d41e09139a8462/backend/internal/repository/http_upstream.go#L129) 的 lastUsed、inFlight 与空闲淘汰；参考 [new-api HTTP 客户端](https://github.com/QuantumNous/new-api/blob/71c1fd7caad738db4d13aabbf28eeadb293d0cfe/service/http_client.go#L79) 的传输配置与缓存键。增加客户端上限前应测量实际池大小；不要无证据提高连接数量。

## 两个参考项目中值得借鉴的部分

| 主题 | 对照源码得到的启发 | 本项目建议 |
| --- | --- | --- |
| 路由缓存 | new-api 在重载时构建 group/model/channel 索引 | 保留现有内存路由；模型数量明显增长时，再将名称、ID、分组候选索引预编译进快照 |
| 会话与调度 | sub2api 区分 previous response 归属、session sticky 和负载分配，并记录命中及切换指标 | 完善已有会话亲和的观测、配置代次及失效规则，兼顾缓存连续性与等待上限 |
| 缓存抽象 | new-api 的 HybridCache 支持 Redis 或内存后端选择，并非所有请求都串行经过 L1/L2 | 先建立明确的缓存边界和失效契约，不为单机桌面代理增加分布式依赖 |
| OAuth | sub2api 集中处理按账号刷新与竞争 | 提取可 Mock 的凭据管理组件，优先解决本地并发正确性 |
| 后台写入 | sub2api 对缓存写入设置有界工作池，并区分关键与非关键任务 | 借鉴队列背压和停机契约；不要直接照搬其计费缓存策略 |

上述对应源码：[new-api 渠道缓存](https://github.com/QuantumNous/new-api/blob/71c1fd7caad738db4d13aabbf28eeadb293d0cfe/model/channel_cache.go#L20)、[HybridCache](https://github.com/QuantumNous/new-api/blob/71c1fd7caad738db4d13aabbf28eeadb293d0cfe/pkg/cachex/hybrid_cache.go#L20)、[sub2api 调度顺序](https://github.com/Wei-Shaw/sub2api/blob/772a0382f079676983c06f24b0d41e09139a8462/backend/internal/service/openai_account_scheduler.go#L390)、[后台缓存写入](https://github.com/Wei-Shaw/sub2api/blob/772a0382f079676983c06f24b0d41e09139a8462/backend/internal/service/billing_cache_service.go#L191)。

## 代码结构与实施顺序

主要结构问题是 Kiro 转发和本地兼容接口分别编排较长的调度流程，且配置、凭据、协议转换、状态保存和统计互相穿插。`forwarder.go` 约 1683 行、`local_compat.go` 约 1210 行、`model_dispatcher.go` 约 1017 行；行数本身不是缺陷，重复编排及共享可变状态才是重构依据。

建议保持现有目录与单进程结构，逐步形成四个明确边界：

1. `RouteSnapshot`：不可变配置和路由索引；运行时健康、负载和凭据不写回配置对象。
2. `CredentialManager` / `SessionStore`：分别负责凭据刷新、会话隔离、版本和过期；接口可 Mock。
3. 共享上游执行流程：统一候选尝试、预算、并发租约、提交状态、取消和失败归类；Kiro 与本地兼容接口保留各自协议边界。
4. 指标与诊断：统一测量口径，普通转发只承担必要的诊断成本。

重试编排还值得补充一组组合验证：[proxy/forwarder.go:89](D:/proxy/proxy/forwarder.go:89) 的 HTTP/2 外层重放、内部网络重试以及协议兼容重放分别维护局部逻辑；底层 transport 也提供协议回退。代码已有总时间预算，分组候选还会关闭内部常规重试，不能据此认定存在无限重试。建议让这些路径共享请求级尝试上下文，分别观测逻辑重试和实际网络尝试，验证组合错误下的次数及累计等待上限，再决定是否收拢实现。

推进顺序：先统一构建身份与首内容观测，修复 OAuth 并发和抓包阻塞；然后补续接边界测试、修复动态尾部及状态版本；最后依据当前版本的测量决定缓存容量、审计开销、数据库和连接池优化。分组优先级、备用组阶段、并发上限及已提交响应不重试的契约必须保留。

速度验收应固定模型、账号、推理强度和上下文规模，区分冷/热连接、冷/热提示词缓存、短/长历史和不同并发数。至少对比首事件、首内容 P50/P95、片段间隔、总耗时、上游尝试数、续接命中、真实 cached tokens 与本地内存。Mock 验证确定性；真实速度对照另行安排，不能用转换器 benchmark 代替。

## 本次验证与尚未验证的内容

- 已有 `go test ./converter ./proxy ./store ./test` 通过，普通测试返回 Go 测试缓存结果。
- 同范围 `go test -race ./converter ./proxy ./store ./test` 通过。尚未覆盖本文提出的所有新场景，尤其是 OAuth 并发刷新和相同续接键倒序完成。
- 主代理另核对了统计并发刷新/清空、数据库写失败保留增量、Hub 并发关闭和 ReasoningTrace 读写测试，均通过。
- 现有 Responses 转换器 benchmark：文本约 22.4 微秒/次、69 KB/次、47 次分配；工具约 40.3 微秒/次、71 KB/次、58 次分配。仅为该测试负载的本地基线。
- 未对当前二进制执行真实上游压测，未量化上述修改的加速百分比；未对两个参考项目做完整安全审计或全仓性能比较。

本次交付为评估和优化顺序。业务代码、配置、凭据和运行服务均未修改。
