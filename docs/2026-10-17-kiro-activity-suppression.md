# Kiro 活动上报停发与会话取消排查

## 行为契约

用户明确要求停止向 Kiro 官方发送活动上报。代理收到 Kiro runtime 域名的 `POST /agents/activity` 时，在转发模式和抓包模式下均在本地结束请求，不访问官方服务、模型中转或出站代理。本行为直接生效，不新增开关。

域名沿用现有 runtime 识别边界：`runtime.kiro.dev` 和 `runtime.*.kiro.dev`；域名不区分大小写，端口不参与域名判断。路径必须为 `/agents/activity`，允许查询参数。其他方法、其他域名及相近路径保持既有处理。

本地响应为 `204 No Content`，正文为空。已核对安装态 Kiro 扩展 1.1.432 的 ActivityLogPublisher：发布器接受全部 2xx，不解析成功响应正文，成功后推进发布游标。这是按用户要求在本地确认并丢弃活动，不表示数据已上传官方。

请求正文流式消费并丢弃，不保存或写入日志详情。2026-10-22 起按[无补丁 Auto 契约](2026-10-20-activity-driven-auto-routing.md)有界解析白名单节点观察，取代原先“不解析”的限制。完整消费并关闭请求体后才返回成功，避免 HTTPS MITM 同一连接的后续请求读到残留正文。流式丢弃不分配与正文大小成比例的缓冲，不新增正文大小限制。读取或关闭失败时保留错误并返回失败，不报告已成功丢弃。只记录不含活动正文及凭证的本地停发摘要。

该路径在抓包分支和活动正文日志之前处理，不创建模型请求统计，不修改模型路由、认证、用量接口和其他请求。Kiro 本地仍可能创建活动和调用发布器；代理负责阻止活动离开本机。未通过该代理的其他客户端不受此行为约束。

本契约取代 README 及历史设计讨论中“活动上报原样放行”的描述；其他透传行为保持原契约。

## 会话取消的已知事实与边界

2026-10-17 的两条失败请求在 Kiro 日志中先出现 `ACP session/cancel`，随后执行中止，代理稍后结算为 `raw Responses body closed before EOF`。用户明确说明当时没有发送消息、停止或切换会话，应按自动中断调查，不能归因为用户操作。

已核对安装态取消处理器会中止当前 turn；界面宿主暴露 `cancelPrompt`，ACP SDK 能发送 `session/cancel`。旧日志没有记录该通知的发送客户端或界面调用来源，目前未证实是哪条自动路径发起取消。不得通过吞掉全部取消通知改变停止、删除会话等正常语义。

活动发布失败由发布器捕获并记录，不向取消处理器抛出。关闭活动上报不是修复会话自动取消的证据，两项必须分别验收。

### 进一步取证

已找到安装态界面入口实际使用的 `packages/kiro-ui-agent-chat/dist/assets/mermaid-GHXKKRXX-CD415fz7.js` 合并脚本。该脚本第 726 行的 `usePrompt` 在 `interrupt` 分支先调用 `cancelPrompt`，再调用 `prompt`；第 730 行的意图选择在执行仍活跃且 live steering 不可用，或发送模式为 `send` 时，可以选择该分支。这是提交消息时隐式取消旧轮次的机制，不要求用户点击停止。

第二次取消后约 15 毫秒，本地会话文件保存了一条新的用户指令；该条有新的 `userMessageTag`，没有宿主自动唤醒会保存的 `syntheticUserMessageReason: agent-initiated-prompt`。此内容与 `cancel → prompt` 的时序共同支持界面提交路径，但不能证明用户当时手动提交，也不能排除预先排队或界面自动重放。第一次取消约 17.86 秒后才有另一条用户消息，证据强度不同，不将两者统一定性。

只读复核从安装态原文抽取意图选择和提交函数，使用内存 RPC/状态 Mock 回放：仅初始化时没有 RPC；运行中提交且不能 steering，或发送模式为 `send` 时，依次产生 `cancelPrompt → prompt`；steering 可用且选择 steering 时，仅产生 `steerPrompt`。未重现完全无提交事件时自动取消。

已检查的 ACP 通知分发、mux 断开清理和项目桥接补丁未发现自动生成这条 `session/cancel` 的证据；这一结论不等于穷举所有客户端调用。现有日志缺少提交来源、取消来源及入站客户端身份，不能据此修改正常取消语义或宣称自动中断已修复。

进一步核对 `pendingComposeSubmit` 的实际写入调用来自 standalone 界面新建云会话后提交草稿，与现场既有本地会话不吻合。`kiroAgent.focusChatInput` 则允许携带 `submit: true`，界面可以在会话加载完成后自动提交；已找到的内建自动提交调用是新会话编辑器动作和创建 hook，不能据此归因现场消息，外部扩展调用此命令的可能性也没有被日志排除。

当次 renderer、exthost 和 Kiro 日志未记录两个时点的提交调用源，无法从现存日志恢复该信息。当前 HTTP 代理不经过 ACP/UI 消息通道；若需要下一次中断的确定来源，须在 Kiro 的取消入口和提交入口采集不含消息正文的调用来源诊断。仅在 HTTP 流结束处加日志不能区分这些调用源。本次未修改安装态或新增此类探针。

## 验证要求

使用本地 Mock 验证 forward/capture 两种模式均返回空 204，官方及模型 Mock 的请求次数不增加，日志没有活动正文或凭证，数据库没有新增模型用量记录。覆盖 runtime 域名、端口、查询参数及方法、域名、路径的负向边界。

使用真实本地 CONNECT/TLS 隧道连续发送多次活动上报和后续普通请求，确认请求体完整消费、同一连接可以继续使用。读取及关闭异常不得返回成功。运行相关 Kiro 手选模型、抓包及 Raw Responses 关闭路径回归，确保模型生成与合法取消语义保持正常。不请求真实模型、官方服务，不修改真实数据库或安装态 Kiro。

现有 `goproxy v1.7.2` 的 MITM 响应对非 WebSocket 请求固定写入 `Connection: close`。普通 HTTP 客户端因此会为后续请求建立新的 CONNECT，这是既有连接策略，本次不改变。原始 TLS 客户端的同隧道连读检查仅验证底层解析器不会读到活动正文残留，不代表承诺普通客户端保持连接；相关验证结果须按实际覆盖说明。

交付时区分源码完成、测试结果、构建产物与运行生效状态；构建候选不等于正在运行的旧程序已经应用变更。

## 本次验证结果与交付状态

2026-10-17 已完成以下本地验证，全部使用 Mock，不访问真实模型或官方服务：

```powershell
go test ./proxy -run '^TestKiroActivity' -count=1 -v -timeout=45s
go test ./proxy -run '^(TestManualRawResponses|TestStageRawRuntime|TestForwardRawResponses(ReadErrorAndEarlyCloseReleaseLease|RequestCanceledReleasesLease)|TestRawResponses(ReadErrorSettlesBeforeLaterCancelAndClose|CloseSettlesBeforeBlockedReadAndLaterCancel|CanceledContextReadCloseFinishOrder))' -count=1 -v -timeout=60s
```

活动测试的 5 个顶层测试全部通过，覆盖域名与路径边界、forward/capture、空 204、正文和凭证不入日志、无模型用量、读取和关闭异常、40 MiB 流式正文，以及同一原始 CONNECT/TLS 隧道内两次活动请求和后续普通请求。相关 Raw Responses、手选模型、抓包及取消回归全部通过。

已构建桌面开发版候选 `D:\proxy\kiro-proxy-dev-activity-disabled.exe`，使用项目既有 `desktop dev` 构建方式：

```powershell
go build -tags 'desktop dev' -ldflags '-H=windowsgui' -o 'D:\proxy\kiro-proxy-dev-activity-disabled.exe' .
```

产物为 42,552,260 字节，SHA256 为 `D982A3787CAB2C074AD958F329E3D16C13907EC0E1D60E009F7811BF56521672`。构建与最终复核的生产源码一致；构建后的修订仅涉及测试和说明文档。

用户已明确选择“暂不重启，只保留新版文件”。本次未替换或重启当前运行的代理，也未修改安装态 Kiro。当前旧进程不能视为已经停发活动。会话取消的自动触发源仍未证实，本次不宣称该问题已修复。
