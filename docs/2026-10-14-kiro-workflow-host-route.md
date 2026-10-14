# Kiro 子代理工作流的父会话路由读取

## 问题与证据

Auto 模式通过 `agent://` 创建工作流时，桥接的 `workflow-source` 补丁直接调用 `t.host.getSession(parentSessionId)`，会触发 `t.host.getSession is not a function`，中断子代理工具启动。

Kiro 的 `buildRuntimeStack()` 向工作流 handler 提供受限的 host facade。它通过闭包暴露 `getSessionModelId` 等专用接口，内部可以访问完整 host，但没有对 handler 暴露 `getSession`。安装包及原版备份的构造链证明了这一区别。现场日志可确认有 `run_workflow` 活动，未记录该错误的完整堆栈；根因依据是实际安装代码中的接口错用。

## 修复契约

在相同 facade 边界新增只读 `getSessionKiroProxyRoute(sessionId)`，通过闭包读取底层 live session 的 `kiroProxyRoute`。创建工作流时改用该窄接口，父路由的职责、执行和工作流身份继续进入原有 sidecar，不向 handler 暴露完整会话对象，不用可选方法调用掩盖接口缺失。

保留原有来源语义：显式 `taskType` 可以独立声明职责；省略时继承可用的父路由；没有可用路由时保留原有空声明。`parentRoute: {}` 仍是 agent 来源标记，不在本次修复中新增强制父路由校验或改变默认职责。

新旧两族固定补丁和结构 profile 同步更新。结构变体须正确改写捕获的局部绑定，保留既有语法、作用域、AST 形状和注入校验。

## 已安装桥接升级

保留上一个已交付结构适配器的精确原始字节，仅用于验证迁移。旧安装先通过完整尾记录、payload、编辑范围及原版摘要校验，再用对应旧适配器重编原版并要求与当前安装逐字节一致；验证通过后才用新适配器生成升级候选。

未知适配器、篡改记录或无法复现的旧安装仍明确拒绝。预检保持只读；安装继续复核当前完整摘要、创建备份并原子替换，缓存只登记新适配器验证过的最终安装。不能只允许旧摘要后信任恢复记录。

## 验证与交付边界

使用临时目录和 Mock host 回放实际补丁及编译产物，验证受限 host 无 `getSession` 时可以创建子代理，父路由完整持久化，显式职责与已有边界保留；验证旧安装升级、新安装重复预检、未知适配器与篡改拒绝。继续核对两族变量改名、格式和组合变体。

本轮只修改仓库并编译调试候选，不修改正在运行的 Kiro 安装或重启其进程。修复经独立复审后与前两批改动一起推送当前 GitHub 分支，提交说明使用中文。

## 2026-10-15 验证记录

- 新结构适配器为 `2179320` 字节，SHA256 为 `ba9faf169eea9bfb998e16c47ea4883237984ae6635c10409583cab6553cefa7`。历史适配器仍为 `1890969` 字节、`3cfca7cb7f4808d82bb54141b203ab4900852afc86b1f808ac43e6a38259445d`，并与上一批 Git 提交的原始字节一致。
- 两族各生成原版、变量改名、引号、格式、无关代码、组合和名称碰撞七种候选。14 个候选的恢复记录、payload 摘要、编辑内容、原版重建摘要及适配器身份逐一核验通过，真实候选的 Auto 路由与传输行为回放全部通过。
- legacy/native 旧适配器迁移、备份、重复安装和缓存收据回归通过；未知适配器、自洽伪造记录、可编译原版但完整旧候选不一致、预检后文件变化的拒绝回归通过。所有安装和缓存写入都在临时目录内完成。
- 12 条独立桥接单测通过。旧测试夹具已补齐 recipe getter 和 Node 模块依赖，未知工作流的断言按现有契约检查内容身份且不分配内置职责。
- 子代理接口专项从 legacy/native 的 original 和 alpha 候选实际提取 getter、workflow handler 及 source helper，四个候选均通过，完整入口 `6/6` 通过且无跳过。受限 Mock host 不提供 `getSession`，验证父路由完整继承与对象不变、显式/省略职责、sidecar 写读及非 Auto 边界。
- 设置两个真实原版 fixture 后，`go test ./proxy -run '^TestKiroStage' -skip '^TestKiroStageStructuralRealVariants$' -count=1 -v` 通过，耗时 `380.133s`。大文件变体使用本轮已生成的 14 个候选另做完整记录核验与行为回放，性能专项单独运行；本轮没有声称仓库全量测试通过。
- native 原版的性能专项通过：首次完整预检 `38.207s`，含备份和收据的临时安装 `257.095ms`，内存与持久缓存预检分别为 `13.108ms`、`14.561ms`。本次测量与其他回归并行运行，不能据此宣称首次适配或整个 IDE 秒开。
- 调试候选为 `kiro-proxy-dev-workflow-session-fix.exe`，`40258175` 字节，SHA256 为 `b8f84739b9abbfd40f5c579d380e72a89ba48a797dee33670440f35007912de6`。构建使用 Go `1.26.4`、`desktop,dev`、Windows amd64、CGO=1；GUI 子系统为 2，保留 `.debug_info` 和 `.debug_line` 调试信息。

真实 Kiro 安装尚未应用本次补丁，真实子代理工具也尚未复测。使用新 dev 程序重新执行一键配置后，才会将已验证的新桥接应用到安装目录。
