# 无补丁 Auto 路由与 activity 观察契约

## 目标与证据

2026-10-22 整体复审核对本机 Kiro 1.1.499 源码后，本文取代原 activity 路由方案，并取代旧设计中要求启动时安装 JS 桥接的条款。

已证实：raw cfd() 原生 metadata 携带 kiro:agent_continuation_id；activity publisher 每 3 秒读取持久化消息，构造 {"payload":[...]} 异步批量上报，模型请求不会等待 flush。历史日志证明批量 envelope，但现存样本没有 workflow，不能据此宣称真实职责命中已验证。

目标是无需修改 Kiro 即可本地接管 raw Auto，保持同一原生 continuation 的模型稳定，提供可配置精确路径规则。activity 只能提供已到达的节点观察，不能证明当前请求首轮身份。首轮没有可靠观察时使用已配置 default 并记录原因；这不等于每节点首轮精准选模。固定等待 3 秒无法解决退避和跨流关联，不采用此类等待。

## 启动与健康状态

- 正常启动不再预检或安装 JS 阶段桥接，显式 KIRO_PROXY_STAGE_BRIDGE=0，保留 CA、代理模式和启动进程验证。
- 不修改用户实际 Kiro 文件，不删除存量补丁；保留历史桥接工具的严格结构校验，撤销部分 hook 匹配。
- bridge bundle、adapter hash 和桥接启用位不再是路由健康的必要条件，不因缺少桥接要求重启；真实 CA/代理模式启动快照不匹配仍应报告。
- 存量合法私有 workflow 头保留兼容解析，代理不主动安装它；私有头和代理 metadata 不得出站。

## activity 流式消费

POST /agents/activity 继续在 forward/capture 两模式本地终结，完整消费并返回空 204，不转发、不创建模型用量、不记录正文或凭据。读取或关闭失败保留明确错误。

支持官方 payload 数组，兼容根级单条事件。逐项白名单为顶层 sessionId、seq、activityTimestamp、messageId、agentMode、subSession、parentAgentId，以及 content.executionId、content._meta.kiro.agentMode、content._meta.kiro.workflow 中 workflowId/workflowName/nodeId/nodePath。

- 单 token 超过 1 MiB 时须在继续构造完整字符串前停止解析，正文剩余部分继续消费丢弃；路径累计长度、段数、深度和批内待提交状态均有界。
- 白名单类型错误或重复白名单键使该事件不可路由；独立非法事件不污染其它合法项。整份 JSON 语法错误或多文档则不提交整批状态。
- 日志只记录固定错误类别，不拼接解析器原始错误或字段值。

## 观察状态与顺序

- 只为合法 sess_ 主流记录状态。带 subSession 或 parentAgentId 的子流不得覆盖主会话，缺少 workflow 的普通事件不得伪造或抹去节点身份。
- 官方同一主流按顶层 seq 幂等和过滤乱序；重复或更旧事件不覆盖较新观察，不跨子流比较顺序，不以到达时间证明关联。
- 最多 512 会话，24 小时无更新过期，重启为空，并发安全；不同 Server 生命周期不共享可路由状态。
- activity executionId 仅为观察信息，不合成为当前请求的同步 execution 锁。与正文 agentMode 不兼容时不得借用观察。
- 晚到 activity 不改变已经锁定的 continuation 模型。新 continuation 可用当时已有且无冲突的观察初选；没有观察则明确 default。

## raw Auto、手动隔离与 continuation

仅接管 Kiro runtime POST /v1/responses 的 model=auto，SDK 保持既有契约；capture 按原观察规则放行模型请求。

1. 读取原生 kiro:agent_continuation_id，旧 kiro:proxy_continuation_id 仅兼容。存在时须为字符串，两个非空值冲突时明确拒绝。手动 raw 使用相同身份解析。
2. 无补丁 raw 的权威锁作用域为 conversationId + agentMode + 原生 continuationId；同作用域首请求决定模型后复用快照，晚到 activity 不重选。该路由锁不改变 Responses 缓存键的既有语义。空原生 continuation 不伪装成稳定锁。
3. 不根据完整历史中任意 function_call_output 推断本轮工具结果。原生 continuation 保持工具循环稳定；旧桥接有明确当前结果计数时保留原语义。
4. activity 合成头不填 definitionId、definitionRevision、taskType、executionId。正文绑定字段以请求为准，不猜测官方未提供的信息。
5. 未命中 activity 也走本地 default，不 passthrough 官方 Auto。策略未启用、配置无效或目标不可用时明确报错，不以其它路由隐藏失败。
6. execution 锁按会话/分支隔离，手动模型清除对应分支全部 Auto 锁，包括 execution 锁，不清其它会话或分支。

原生 continuation 锁与既有 execution 锁在当前进程内保留，手动选模可清除对应作用域，重启后清空。不按闲置时间驱逐，否则长时间等待工具或用户后可能在同一 continuation 内换模。当前协议没有已核实的同步终止通知，因此本轮未加入猜测性回收；长期运行中锁数量会随不同 continuation 累积。这与有容量和 TTL 的 activity 观察存储是两个独立生命周期。

## 路径规则与配置闭环

沿用现有字段，允许两种明确身份：完整三元组（definition_id/definition_revision/node_path 全部非空），或路径规则（definition_id、definition_revision 同时为空且 node_path 非空）。仅缺一个 definition 字段仍属错误，不伪造身份。

三元组精确匹配优先。请求缺少 definition 身份时，以 node_path 二级匹配：唯一配置项生效，多个配置项（包括 binding/override）为歧义，落 default 并记录 node_path_ambiguous。存储校验、去重、节点目录、单项与批量编辑一致支持路径规则；裸 nodeId 和普通上下文仍不可当规则身份。路径规则跨 definition 匹配，界面须说明。

界面在同一路径规则身份由职责切换为独立模型/分组时，替换同身份职责绑定，并在快捷操作、批量操作和手工编辑中明确说明。完整三元组规则仍保留既有 override 优先语义。新增或编辑路径规则若与其它身份的同路径规则冲突，界面须明确提示冲突，不能宣称必然优先或成功匹配；后端继续按路径歧义使用 default。

activity 路径只去掉与 workflowId 相等的首段，余段按 JS encodeURIComponent 编码后用 / 连接。保留真实 repeat 的 iter-N 段；没有定义树不能安全区分合成帧与真实同名节点，不全局删除。普通旧路径相同可兼容，不宣称所有 repeat 旧规则自动兼容。

不按裸节点名猜 18 个职责，不自动改写用户实际数据库的模型/职责/绑定。配置了职责目标不等于已有节点映射。

## 验收

- 批量 envelope、根级兼容、错误类型、重复键、多文档、截断、超长 token、海量路径段、深度和有界批内状态有稳定 Mock；完整消费与内存分配分别验证。
- 同流新旧 seq、重放、子流隔离、容量/TTL、独立 Server 和并发通过相应测试及 race。
- 原生 continuation 首轮 default 后晚到 activity 不换模，新 continuation 可初选；手动清锁/跨会话分支隔离、旧桥接、SDK、capture、私有信息清理通过相关回归。
- 路径规则保存、编辑、单项与批量配置，唯一/歧义/不命中，完整三元组兼容通过前后端测试。
- 启动不安装补丁，缺少桥接不误报；保留严格补丁校验。源码和 Mock 验证与真实 Kiro 运行验收分开记录，缺少现场请求证据时不声称首轮职责命中已验证。

## 2026-10-22 修复验证记录

本轮修复覆盖批量流式解析、读取失败传播、观察状态隔离与排序、原生 continuation 锁、手动清锁、路径规则编辑和存储闭环、无补丁启动与健康状态，并同步 README 和被取代的历史设计。

已执行并通过：

- `converter`、`router`、`store`、`proxy`、`api` 五个包的完整 Go 测试均通过（分包执行）；`go test -race` 对应五个包全部通过。
- 前端 `node --test frontend/test/auto_activity_path_rules_test.mjs test/auto_duty_frontend_test.mjs`，最终 17 项通过；`npm.cmd --prefix frontend run build` 通过。
- JSON 语法合同测试及 15 秒模糊测试：339,871 次执行，无非法 JSON 被接受或部分提交。
- 超长 token 的实际内存测试：4 MiB 与 40 MiB 正文分配均约 5.1 MiB，剩余正文完整消费。
- 变更 Go 文件的 `gofmt` 检查与 `git diff --check` 通过。
- `go build -tags 'desktop dev' -ldflags '-H=windowsgui'` 重建包含最终前端的候选 `kiro-proxy-dev-activity-20261022.exe`，35,439,921 字节，SHA256 为 `C6EB04045E020E75FE0F55707AA308F9F80BBD539B51C75BA9B5952A0EC37BE1`。

独立复审追加发现的两个组合已定点修复并回归：底层 `Read` 同时返回数据和错误、但 JSON 解析提前退出时，仍保留原始 I/O 错误并返回 400；path-only 从职责绑定切到独立模型/分组时，单项、批量和高级编辑均替换同身份绑定，涉及其它身份的同路径冲突会在界面明确提示并阻止新增。读取边界修复后的 `proxy` 全包和 race 均再次通过。

最终复审无阻断项；同时补齐其指出的非阻断展示遗漏：`node_override_path` 成功命中与 `node_override` 一样标记为独立规则，不进入“职责未确认”筛选，目标影响说明也识别该来源。新增回归直接执行组件中的识别和筛选函数，最终前端测试与桌面构建再次通过。

测试期间还纠正了既有夹具和断言：分组首字节超时须符合现有最小 10,000 ms 校验；Anthropic 不支持的 effort 用 `xhigh` 验证；旧 `task_rules` 报错断言匹配其现有独立错误。这些调整未改变对应产品契约。

验证边界：候选未启动、未部署、未重启真实 Kiro，未改实际数据库或客户端文件。真实旧 bundle 测试所需的 `KIRO_STAGE_TEST_BUNDLE` / `KIRO_STAGE_TEST_BUNDLE_130` 未设置，相关用例按既有条件跳过；严格结构拒绝的合成 Mock 用例已通过。现存流量日志未提供 workflow 样本，真实节点职责命中率和每节点首轮精准性不属于已验证结果。

### 同日界面运行复核

用户反馈桌面 dev 界面无法正常打开后，补充检查现有运行实例。`kiro-proxy-dev.exe` 的文件哈希与上述候选一致，进程持有本机 UI 与代理监听端口。浏览器实际打开该实例的仪表盘、模型配置页和 Auto 模型配置弹窗，已观察到页面内容及节点目录，所检查资源返回 200，未捕获到页面脚本异常。未保存或修改实际模型与路由配置。

Windows 桌面宿主起初处于隐藏状态；通过同一路径启动入口触发现有单实例唤醒后，能枚举到桌面窗口并读取仪表盘的可访问性内容，窗口消息循环响应正常。原生截图采集返回 `FrameArrived timed out`，随后元素点击返回 `coordinate input geometry is unavailable`；因此本次没有完成原生窗口的视觉和交互验收，也未证实用户先前打不开的具体根因。没有为未确认的原因改动桌面窗口代码，不将本次唤醒或浏览器可用表述为该问题已根治。

各构建共用 Windows 单实例锁；已有实例运行时，新启动只尝试唤醒原实例，不会加载新程序。后续候选验收须同时核对监听进程、启动时间、实际程序路径及构建身份，不能仅凭磁盘文件已更新认定运行实例已更新。上述记录补充前次“候选未启动”的验证边界，真实 Kiro 节点职责命中仍未验证。

用户随后明确确认界面已经能够打开，并要求停止对此问题的排查；界面打不开不再列为本次待解决项。以上保留当次工具观察与验收范围，不据此新增桌面修复。
