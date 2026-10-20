# 接续项目迭代

## 当前切片：无人值守补齐（2026-10-21）

- 受测目录为 E:/agent-project/.tools/sqlite-approval-verify；4fe8851 及其前三个指定祖先已推送 origin/codex/m3-operations-storage。
- 本轮范围：用户列出的 15 项；逐片实施。第 1 项已修正；第 2 项原四个提交/CI 已闭环；第 3 项 OAuth 合成流程与管理入口完成（见下）；第 4 项 WAL 在线快照和显式 CLI 已实现，库层正反例及恢复后的审批/撤权通过；第 9、14 项库层/文档范围已完成（见下）；第 6 项文本产物扫描库与指令计量评估已完成；第 7 项知识审计独立授权/统计已完成（见下）；第 8 项 SBOM/Windows 与 Linux/amd64 构建验包已完成（见下）；其余联合恢复、PDF、Python Worker、中文评测、用章策略与运维脚本待实施。真实租户/群聊、容器隔离、真实印章及正式签名发行不纳入完成声明。
- 第 4 项证据：pnpm check 退出 0（E:/agent-project/.tools/m3-online-backup-check.log）；补充在线恢复审批/撤权后 go test ./internal/bootstrap ./internal/sqlitedb ./cmd/migrate 退出 0（m3-online-backup-final.log）。首次定向测试因宿主默认临时目录/缓存访问拒绝失败，改用既有 E 盘工具目录后通过；未改变全局环境。无 PG/schema 变更，未重复全量 test:db。
- 第 2 项：CI 39481450456 的 Windows 离线安装测试在首次 PowerShell 验包时触发 20 秒超时（ETIMEDOUT），打包成功；仅为完整验包设置 120 秒上限，二进制冒烟保持 20 秒，不重试、不跳过校验。9aa64cc 的 CI 39482454535 全部 success（含 Windows 离线与 Linux foundation/PG/race/e2e），原四个提交已闭环；本地安装回归 m3-ci-offline-timeout.log 退出 0。后续 CI 结果见下方条目。

- 工程证据：pnpm check:go 退出 0（E:/agent-project/.tools/m3-audience-go.log），无 schema 变更。
- 第 9 项：群聊范围证明库已实现（R01/R12、V07），绑定租户/应用/群/内容摘要/版本，核对完整成员集合、逐人当前授权及检查前后范围版本；失败无证明。移除 Ground 的布尔放行，群聊通道仍关闭。合成正反例通过；真实 PG 的 TestGround 回归通过（E:/agent-project/.tools/m3-audience-vector-test.log）。初次误用无 vector 的 Windows 历史测试集群失败（m3-audience-test.log），已停库并改用既有 WSL 17/agentos-m3；测试库/角色残留 0，专用集群已停止。
- 第 14 项：官方渠道能力矩阵已写入 contracts/channel-capabilities.md；企微原始开发页抓取失败的精确 API 能力明确待核实，基础接入使用腾讯官方指南交叉证明。未写渠道代码、未做真实租户验收。
- 第 6 项：RunArtifact 复用 WASM 隔离约束，限制文本/JSON、最大 1 MiB，验证指针/长度、UTF-8、JSON 解码后内容并强制现场扫描器；失败/取消不返回产物。仅库层，无文件/WASI、无业务交付接线，不宣称完整杀毒。wazero v1.12.0 公开 API 未提供可直接配置的指令预算，函数 listener 的死循环反例仅通知一次，明确不支持指令计量。单点 contracts/sandbox.md；定向沙箱/报价测试与 pnpm check:go 均退出 0（E:/agent-project/.tools/m3-artifact-scan-test.log、m3-artifact-scan-go.log）。无数据库/schema 改动，未重复 PG 套件。
- 第 3 项进行中：官方当前换取/刷新为 v3，撤销为独立 v1/revoke；6f98834 已推送私有传输层与合成 HTTP 测试，pnpm check:go 退出 0（m3-oauth-provider-go.log）。本片新增 AES-256-GCM 绑定加密/密钥轮换及 PG schema 23 密文 CAS/原子审计账本；停用/撤权/改绑不允许获取或覆盖旧凭证，合成数据、真实 PG 的并发 CAS、审计失败回滚与 RLS 负例通过（m3-oauth-ledger-db.log），pnpm check:go 退出 0（m3-oauth-ledger-go.log）。全量 pnpm test:db 退出 1，唯一失败为迁移测试仍假设 channel 10 张表；更新为 12 张且继续要求全部强制 RLS 后，数据库包重跑退出 0（m3-oauth-ledger-migrations.log），全量其余包均通过（m3-oauth-ledger-all-db.log）。测试库/角色残留 0，专用 PG 已停止。日志位于 E:/agent-project/.tools/。
- 第 3 项生命周期：已实现 state/PKCE 会话、换取/刷新前持久意图、未知结果拒绝、本地先撤销、Reader 读取前解析与结束前版本检查，并接显式 use_oauth 来源/启动配置；静态 Token 模式保留，混用拒绝。合成 HTTP＋真实 PG 覆盖重建对象、刷新、密钥轮换、持久撤销、错误身份、并发刷新、读取中撤销及未授权启动拒绝。定向 PG/来源回归退出 0（m3-oauth-lifecycle-db-final.log），pnpm check:go 退出 0（m3-oauth-lifecycle-go.log）。首轮合成撤销夹具缺 Content-Type 导致 SDK 拒绝，修正夹具后通过，失败保留于 m3-oauth-lifecycle-db.log。无新 schema 变化，未重复全量 test:db；PG 残留 0、专用集群已停止。
- 第 3 项已完成无人值守范围：agentos --oauth 从有界 stdin JSON 提供 begin/complete/status/revoke/confirm-provider-revoked，不启动 Worker/HTTP，不在参数/错误中回显授权码；仅管理可信配置中的 use_oauth 用户，依赖本地管理员配置/数据库/密钥权限，并保留当前 IAM。schema 24 的人工核对绑定版本和证据摘要、原子追加审计，只解除 revoked 的未知标记，不恢复旧 Token。真实 PG 管理入口、失败/过期/撤销/并发/未知不重放/轮换/审计失败测试通过；pnpm check 与完整 pnpm test:db 均退出 0（m3-oauth-admin-check.log、m3-oauth-admin-db.log）。构建后二进制实际拒绝混用字段且无授权码回显；PG 残留 0、专用集群已停止。协议/状态/配置单点见 contracts/feishu-docs.md 和 deploy/feishu.md。该片使用 PG schema 24、SQLite 8；真实租户联调仍未执行。
- 第 7 项：schema 25 将知识事件/检索审计读写 RLS 分离，新增 knowledge.audit.read 和只写访问留痕。提供有界时间窗口、稳定游标分页和读取检查/检索/候选计数；独立授权不授予正文或同步能力。真实 PG 与 HTTP 覆盖现有写入/读取、跨租户/应用、边界/游标、能力/身份撤销、请求身份注入与留痕失败无结果。完整 pnpm test:db、pnpm check:go 均退出 0（E:/agent-project/.tools/m3-knowledge-report-db.log、m3-knowledge-report-go.log）；测试库/角色残留 0、PG 已停止。契约单点 contracts/knowledge.md；当前 PG schema 25、SQLite 8。顺带修正知识契约的“原生同步未实现”和运维部署的历史 schema 9 指引，未把文档修订称为产品测试。
- 后续 CI：2e63847、813e982、6f98834、a4a1d51、f03ebd6、2f56845、9eadf4f 均 success，对应 39483392615/39483950043/39485157171/39486243295/39487558321/39488759453/39489992552。下一片处理第 5 项联合备份恢复，再处理文档 Worker/PDF、中文评测、用章策略与运维脚本；总目标未完成。

- 第 8 项：R19、A10、V15；新增 CycloneDX 1.6 SBOM，固定上游 schema 离线验证，包含实际链接 Go 模块、运行时、前端生产依赖保守超集和二进制摘要，不虚构依赖图或将 Go h1 当归档 SHA-256。显式 Windows/Linux amd64、CGO=0 构建，Windows 到 Linux 交叉包在 WSL 实际执行核对身份。两个平台独立安装、迁移/备份/恢复、静态资源与拒绝反例通过；Linux 另测符号链接拒绝。证据 E:/agent-project/.tools/m3-linux-sbom-package-final.log、m3-linux-sbom-install.log、m3-windows-sbom-package.log、m3-windows-sbom-install.log，均退出 0；pnpm check 退出 0（m3-sbom-cross-check-final.log）。初次打包循环变量遮蔽和 Python 行宽检查失败均修复，原失败日志保留。产物 m3-linux-sbom-package-final（130 文件/56 依赖）和 m3-windows-sbom-package（132/58）基于 9eadf4f 工作树、modified=true，仅内部验收；schema 未变，无 SQL 改动，未重复 test:db。Linux 验包器需系统 Python 3.10+，Go 二进制不依赖它。CI 增加 Linux 原生打包与安装；该片远端 CI 39491570127 的 Windows 离线已成功、foundation 仍运行，继续跟进。签名、项目发行许可与完整 PG/Python 包不在完成范围。

- 第 5 项进行中：新增 internal/recovery 认证封装和 SQLite 联合备份恢复库（R19、A10、V15）。AES-GCM 配置密文绑定数据库摘要、制品/schema、配置摘要、当前授权摘要和固定密钥引用；不导出密钥明文。SQLite 核对来源/快照/当前权威源，私有目录恢复后再次核验，成功才发布新目录。真实 DELETE/WAL 文件覆盖备份后与恢复中撤权、密钥撤销、版本/篡改/额外文件/缺权威源/覆盖拒绝，失败不发布。go test -count=1 ./internal/recovery 和 pnpm check:go 退出 0（E:/agent-project/.tools/m3-joint-sqlite-test.log、m3-joint-sqlite-go.log）；无 schema 改动，未运行 PG test:db。契约单点 contracts/recovery.md。下一步实现真实 PG 联合适配与合成数据库测试，再接可信配置/外部密钥引用管理入口；当前只是 SQLite 库层，不将第 5 项或完整联合恢复标为完成。

## 定位当前里程碑

- 进展：2026-10-20，工程 M1、M2 已退出；M3 已实现知识存储/健康（15—16）、授权向量检索（17）、任务清单与基础统计（18）、会话引用/检索/交付复核（19，远端 CI 通过）。M3 整体尚未退出。
- 推进计划：①消费者库层 ✔ ②审计查询/统计 ✔ ③计算隔离部分 ④飞书读取/规范化/身份映射、同步执行器与启动轮询已接线，真实租户联调待完成，已提供授权任务查询 ⑤会话检索远端 CI 通过 ⑥SQLite 首片范围已确认：任务/审批/审计/身份 ⑦PG 备份部分完成 ⑧M3 退出验收待完成。

## 保留切片：SQLite 停写备份恢复（2026-10-21）

- 范围：R03/R06/R19、A03/A04/A10、V03/V05/V15；agentos-migrate 增加 --sqlite-backup 与 --sqlite-restore。默认 DELETE 模式下持有排他锁复制，先验 schema/完整性/外键；生成摘要和时间清单。恢复先验证私有副本，再排他创建新目标，不覆盖、不迁移、不启动 Worker。占用/WAL/错误 schema/外键/篡改/缺失/已有目标明确失败；原 PG 命令不变。单点见 [SQLite 部署](../deploy/sqlite/README.md#停写备份与恢复)。
- 行为：真实 SQLite 备份恢复后，审批等待可继续批准，审批人与发送者撤权继续阻断；成功、模型未知、发送未知和撤权失败任务不重放。定向 SQLite/bootstrap/Worker/CLI 测试退出 0，日志 E:/agent-project/.tools/m3-sqlite-backup-targeted.log；补外键及命令负例由 pnpm check 覆盖。
- 演练：m3-sqlite-backup-drill.log 退出 0；126976 字节样本备份 14.0016 ms、恢复 10.9647 ms。仅本机合成数据，不包含停机、配置/密钥或业务核对，不承诺现场 RTO/RPO。数据库/配置/密钥自动联合备份、WAL/在线备份尚未实现；恢复旧快照前必须核对快照之后的外部效果与当前撤权事实。
- 工程：pnpm check、离线打包和安装测试均退出 0；日志 E:/agent-project/.tools/m3-sqlite-backup-check.log、m3-sqlite-backup-package.log、m3-sqlite-backup-install.log。独立安装测试实际执行备份/恢复与覆盖拒绝，并用恢复文件启动。产物 E:/agent-project/.tools/m3-sqlite-backup-package，基于 7b40ab7 工作树，modified=true，仅内部验收。
- 检查边界：schema 与 PG 运行语义未变，本片未重复全量 PG test:db；未做真实外发，未推送，远端 Windows CI 未运行。M3 未退出。
- 差距清单：[M3 退出差距](../deploy/m3-readiness.md) 已汇总现有证据、真实租户前置和待办。下一步优先补 Windows 远端 CI，再推进 OAuth 生命周期与真实租户联调前置；群聊、PDF/用章、Python 隔离及联合恢复仍分别验收。

## 保留切片：Windows SQLite 核心离线包（2026-10-21）

- 范围：R19、A03/A10、V15；`pnpm package:offline <新绝对目录>` 重建前端和两个 Go 二进制，生成 MANIFEST.json、DEPENDENCIES.json、许可原文及 PowerShell 验包器；拒绝覆盖已有目录、无许可文本、模块替换/校验和不符、旧 revision 和错误平台。构建与安装单点见 [离线包](../deploy/offline/README.md)。
- 制品：E:/agent-project/.tools/m3-offline-package-1；131 个交付文件、41 个 Go 链接模块＋1 项 Go 运行时＋16 个前端生产依赖。前端为保守集合，不宣称精确 SBOM；该验收包基于 e33b684 的工作树构建，modified=true，明确 internal-validation-only。项目未提供 LICENSE；不声明对外发行许可已完成。Python/PG 服务、模型、凭证和客户数据库不打包。
- 验证：打包、`pnpm test:offline E:/agent-project/.tools/m3-offline-package-1`、`pnpm check` 均退出 0。日志 E:/agent-project/.tools/m3-offline-package.log、m3-offline-install-final.log、m3-offline-check-final.log。安装测试复制到独立目录，以空 PATH 启动二进制，覆盖原文件校验、篡改/缺失/额外文件拒绝、身份核对、SQLite 迁移/重复/目标冲突、嵌入资源、缺租户就绪拒绝和坏配置退出；无真实外发，不称为物理断网认证。
- 保留失败：m3-offline-install.log 中测试副本继承 Go 缓存许可文件只读属性导致篡改夹具失败，已仅将测试副本设为可写；m3-offline-check.log 中导入排序已修正。Windows CI 增加 package/test 作业，尚未推送或远端运行。
- 检查边界：没有运行时或 schema 变更，本片未重复全量 test:db；上一片数据库回归证据保留。M3 未退出，完整安装平台矩阵、服务注册、签名和正式 SBOM 仍未完成。
- 下一片：SQLite 停写备份与恢复演练，检查数据库/现场配置/密钥联合恢复边界及失败处理，再汇总 M3 退出差距。

## 保留切片：SQLite 显式运行入口（2026-10-21）

- 目录：`E:/agent-project/.tools/sqlite-approval-verify`，分支 `codex/m3-operations-storage`；主目录只保留入口笔记，不作受测制品。
- 范围：R01/R02/R03/R04/R06/R19、A03/A04/A08/A10、V03/V05/V06/V11/V15；bootstrap 显式选择 SQLite 文件，缺库/未迁移/错误 schema 拒绝。启动前拒绝知识/向量/来源、技能、工具、报价和审批通知配置；共享接收、Worker 与运维处理器，SQLite 不注入通知发送器或知识仓储。运行配置单点见 [SQLite 部署](../deploy/sqlite/README.md#选择运行存储)。
- 行为证据：真实 SQLite 文件、合成加密渠道消息和 HTTP 模型完成原响应审批挂起、重启、运维查看/批准、单次交付；审批人能力撤销与发送者映射撤销均阻断交付。就绪探针核对租户启用/schema，关闭后不可用。日志 E:/agent-project/.tools/m3-sqlite-bootstrap-targeted-final.log；首轮测试夹具 URL 未含 /chat/completions，失败记录保留于 m3-sqlite-bootstrap-targeted.log，已纠正夹具并补启动失败来源断言。
- 工程证据：pnpm check 与全量 pnpm test:db 均退出 0，日志 E:/agent-project/.tools/m3-sqlite-bootstrap-check.log、m3-sqlite-bootstrap-db.log 均保存 EXIT_CODE=0；覆盖 PG 启动/检索/来源及审批、通知、工具、报价与双驱动契约。测试库/角色残留 0，专用 PG 集群已停止。schema 未变（PG 22、SQLite 8）。
- 交付边界：首版仅基础文本任务/原响应审批/身份/审计/核对/统计；真实飞书租户、群聊接收范围、OAuth 生命周期、正式 PDF/用章仍未验收；M3 未退出。Windows 未运行 race；本片尚未推送，远端 CI 未运行。
- 下一片：离线安装制品清单、依赖许可与安装验证，再做 SQLite 备份恢复演练；按实际证据维护 M3 退出差距，不自动声称完整轻量部署交付完成。

## 保留切片：运维存储接口（2026-10-21）

- 目录：`E:/agent-project/.tools/sqlite-approval-verify`，分支 codex/m3-operations-storage。
- 收口范围：本轮完成运维接口验证、提交与工作目录统一；SQLite 启动入口是下一独立切片，不将 M3 标成退出。主目录旧改动已存入 stash ed1383291645d5e392d0649183a290d228ffc8ac；逐文件核对发现 database.go 为 schema 1/缺 immediate 的异常旧版本，未带入受测目录。主目录笔记已改为指向本页的入口。
- 实现：R02/R15、A03/A08/A10、V06/V13；operations.Store 仅暴露查询/审批/核对与授权领域操作，不含 SQL 回调。两驱动新增 AuthorizeOperation，报价/通知为可选接口，SQLite 不支持则 404。规范见 [运维](../contracts/operations.md#运维存储接口)。
- 定向证据：真实 SQLite 上同一 HTTP 处理器的任务/统计/审计/核对、状态/指标、能力撤销、身份撤销、冲突与留痕失败测试通过；未支持路由 404。基础包 go test 通过，无 schema 变更。
- 收口证据：2026-10-21 在本目录重跑 pnpm check 与 go test -tags=integration -count=1 ./internal/operations ./internal/feishu，均退出 0；新日志 E:/agent-project/.tools/m3-operations-storage-close-check.log、m3-operations-storage-close-db.log 保存 EXIT_CODE=0。旧日志只保留历史输出，不倒填退出码。本次真实 SQLite HTTP 测试与 PG 集成回归通过；PG 测试库/角色残留 0，专用集群已停止。无 schema 变更，未重复全量 test:db。
- 下一片：bootstrap 显式选择 SQLite、保持缺库/错误版本拒绝，提前拒绝知识/向量/技能/工具/报价/通知配置，接就绪探针及资源关闭；以真实 SQLite、合成渠道/模型验证启动与原响应审批闭环。当前渠道启动仍仅 PG。
- 远端：cffd0c2 的 CI 39419894264/39419853013 已 success（2026-10-21 复核）；本片远端待确认。

## 保留切片：Worker 存储接口（2026-10-20）

- 目录：`E:/agent-project/.tools/sqlite-approval-verify`，分支 codex/m3-worker-storage。
- 实现：R03/R06、A03/A04/A08、V03/V05/V06；Worker.Store 从 PG 具体类型改为 ExecutionStore，通知、技能执行及知识通过独立能力接口调用，移除 Worker 直接访问 PG 连接。缺能力时明确拒绝相关任务，不实现 SQLite 空成功扩展；规范见 [任务](../contracts/task-runtime.md#worker-存储边界)。
- SQLite 证据：同一 Worker 在真实 SQLite 文件上成功交付，模型/发送未知后不重放，模型后撤权不发送，重建仓储不重复调用；缺能力的技能/工具/报价/知识任务在外部调用前拒绝。定向 go test 通过。
- 工程证据：`pnpm check` 退出 0（`E:/agent-project/.tools/m3-worker-storage-check.log`）。全量 test:db 首次 vet 发现 bootstrap 测试依赖 Worker.Store.Enqueue，已改成独立接收接口并保持原断言；失败日志 m3-worker-storage-db.log 保留，重跑全量 `pnpm test:db` 退出 0（m3-worker-storage-db-final.log），覆盖 PG 检索/审批/工具/报价/通知及全部共享驱动契约。测试库/角色残留 0，PG 已停止。
- 下一片：拆分 operations API 的基础查询/审批/核对与可选报价/通知依赖，再为 bootstrap 配置 SQLite 并提前拒绝知识/向量、技能/工具/报价/通知。当前尚无 SQLite 渠道启动入口。
- 远端：27ffce4 的 CI 39418969725/39418953628 已 success（2026-10-21 复核）；本片远端待确认。

## 保留切片：SQLite 显式迁移入口（2026-10-20）

- 目录：`E:/agent-project/.tools/sqlite-approval-verify`，分支 codex/m3-sqlite-migrate。
- 实现：R19、A03/A10、V15；agentos-migrate 新增 --sqlite 绝对文件路径，拒绝同时设置 PG 迁移 URL，复用独立前向迁移；无参数 PG 路径和离线 --version 保持。说明见 [SQLite 部署](../deploy/sqlite/README.md)。无 schema 变更。
- 验证：cmd/migrate 与 sqlitedb 全包通过；`pnpm check` 退出 0（`E:/agent-project/.tools/m3-sqlite-migrate-check.log`）。构建后二进制实际创建/重复迁移均成功，冲突目标非零退出，临时数据库已删除。覆盖数据保留、未来 schema、坏参数不创建文件与输出失败；未重复 PG test:db，未修改 PG SQL。
- 接线发现：Worker.Store 为 *conversation.Store，直接使用私有 prepareKnowledge/routeSummaries/bindRoute 和 db/tenant/app，另有 NotifyOne/LoadSkill/工具与报价方法；operations 和 bootstrap 也绑定 PG。下一片须按基础执行与可选能力拆分消费接口，并保留 PG 行为回归，再接 SQLite；禁止补空成功方法绕过未支持能力。知识/向量配置必须拒绝后才能开放运行入口。
- 远端：3d6227c 的 CI 39418545746/39418507885 已 success（2026-10-21 复核）；本片远端待确认。

## 保留切片：SQLite 任务清单与统计（2026-10-20）

- 目录：`E:/agent-project/.tools/sqlite-approval-verify`，分支 codex/m3-sqlite-reports。
- 实现：R15/R18、A03/A08/A10、V06/V13；ListTasks/Stats 同事务当前授权、范围筛选和访问留痕；schema 8 保留旧记录并增加 tasks/stats 范围与分页索引。时间边界与更新时间口径见 [运维契约](../contracts/operations.md#sqlite-任务清单与统计)。PG 仅公开复用窗口校验，schema 22 未变。
- 定向证据：共享 PG/SQLite 分页、窗口、范围、审批统计、撤权与留痕失败契约通过（`E:/agent-project/.tools/m3-sqlite-reports-db-final.log`）。初轮夹具以 time.Time == 比较时区、以纳秒比较 PG 微秒参数均已修正；SQLite 独立测试覆盖纳秒边界转换。
- 工程检查：首次 pnpm check 因 database.go 换行格式未过 gofmt 已修复，保留失败日志 m3-sqlite-reports-check.log；最终 `pnpm check` 退出 0（m3-sqlite-reports-check-final.log），包含 schema 升级保留与写锁回归；未重复全量 test:db。测试库/角色残留 0，PG 已停止。
- 下一片：梳理运行入口需要的最小存储接口与不支持能力拒绝，再接 SQLite 基础文本任务/审批/审计/身份路径；不得为了接线伪装已支持技能、工具、通知或知识/向量。
- 远端：facc89e 的 CI 39417806357/39417769893 已 success（2026-10-21 复核）；本片远端待确认。

## 保留切片：SQLite 未知结果人工核对（2026-10-20）

- 目录：`E:/agent-project/.tools/sqlite-approval-verify`，分支 codex/m3-sqlite-reconcile；原目录历史副本不作验证依据。
- 实现：R03/R06、A03/A04/A08、V03/V05/V06；schema 7 增加核对账本，当前操作员授权、证据/版本校验、状态/审计/账本同事务；重复请求返回首次结果，恢复后旧租约拒写。规范见 [任务契约](../contracts/task-runtime.md#sqlite-未知结果人工核对)。PG 仅抽取共享输入验证，schema 22 不变。
- 证据：共享 PG/SQLite 九类场景及既有外部意图恢复回归通过（`E:/agent-project/.tools/m3-sqlite-reconcile-final-db.log`）；`pnpm check` 退出 0（`m3-sqlite-reconcile-check.log`），含 SQLite 迁移/写锁测试。测试库/角色残留 0，PG 已停止；schema 7/immediate 提交前复核正确。未重复全量 test:db。
- 边界：库接口尚无 SQLite HTTP/Worker 接线，不包含工具/审批通知核对；下一片补任务清单/统计，依赖齐全后才开放运行入口，知识/向量仍须启动拒绝。
- 远端：上一片 5e25e1a 的 CI 39416898817/39416862016 已 success（2026-10-21 复核）；本片远端待确认。

## 保留切片：SQLite 单任务审计（2026-10-20）

- 目录：继续在 `E:/agent-project/.tools/sqlite-approval-verify` 的当前分支开发；原目录仅保留历史副本，不作验证依据。
- 实现：R02/R15、A03/A08/A10、V06/V13；同事务当前身份/能力复核、按租户/应用过滤后的任务内分页及访问留痕。schema 6 去除访问记录的任务外键，未知/跨范围任务统一空页并记录访问，保留成员外键与既有记录。规范见 [运维](../contracts/operations.md#sqlite-单任务审计查询)。
- 定向证据：共享 PG/SQLite 审计契约及迁移/写锁测试通过（`E:/agent-project/.tools/m3-sqlite-audit-final-db.log`），覆盖分页不泄露全局序号、返回条数/成员归属、跨范围空页、撤权/改绑、取消和访问留痕失败不返回数据。
- 工程证据：`pnpm check` 退出 0（`E:/agent-project/.tools/m3-sqlite-audit-check.log`）；PG 生产实现与 schema 22 未变，未重复全量 test:db。测试库/角色残留 0，集群停止；隔离目录 schema 6/immediate 保持正确。
- 下一片：SQLite 未知结果人工核对，再补任务清单/统计；通知、技能/工具、报价与运行入口仍按依赖移植，知识/向量启动拒绝要求保留。
- 远端：上一片 49d6acd 的 CI 39416102942/39416059716 已 success（2026-10-21 复核），本片待确认。

## 保留切片：SQLite 原响应审批（2026-10-20）

- 当前本机开发目录：`E:/agent-project/.tools/sqlite-approval-verify`，以该目录当前分支为准。原目录的 sqlitedb/database.go 反复被未知来源覆盖；不再以原目录为受测制品。C 盘临时 worktree 因空间耗尽/写入受限已归档，改动已完整转移到 E 盘隔离目录。

- 实现：R03/R04/R06、A03/A04/A08、V03/V05/V06；schema 5 保存原审批策略，挂起/查看/决定/到期与状态及审计原子提交，发送意图前重验摘要、期限、当前审批人映射与能力。NotifySubject 明确拒绝，尚无通知、报价改单、Worker/HTTP 接线。契约见 [任务](../contracts/task-runtime.md#sqlite-原响应审批)。
- 反例：首轮共享夹具复用 UUID/文本参数导致 PG 42P08，已拆分。sqlitedb/database.go 又出现 schema 1/缺写锁状态，按用户已有“非有意改动、恢复”授权修正为本片 schema 5/immediate；提交前须复核。修正后双驱动初轮审批与迁移通过（`.tools/m3-sqlite-approvals-db-fixed.log`）；更早失败日志保留在 m3-sqlite-approvals-db.log。
- 最终证据：E 盘隔离目录的 `pnpm check` 与全量 `pnpm test:db` 均退出 0；日志位于 `E:/agent-project/.tools/m3-sqlite-approvals-e-check.log`、`m3-sqlite-approvals-e-full-db.log`，包含 11 类 PG/SQLite 审批场景及迁移/写锁回归。测试库与角色残留 0，PG 集群停止；受测目录 schema 5/immediate 保持正确。原目录的 m3-sqlite-approvals-check/full-db.log 为被旧文件覆盖后的失败证据，不计作通过；远端 CI 待确认。
- 下一片：SQLite 未知结果人工核对与审计查询，补齐领域契约后再接运行入口；通知、报价/技能等依赖需逐片移植，知识/向量在 SQLite 下启动拒绝。

## 保留切片：SQLite 业务能力授权（2026-10-20）

- 实现：R02/R15、A03/A08、V06/V13；SQLite schema 4 增加审计、状态、任务核对与审批四种应用级能力；两个驱动提供 RequireCapabilityIn，在领域事务内解析当前映射并检查能力，失败无 Principal。统一 ErrForbidden，PG 旧名称保留同一错误身份供现有调用方使用。
- 接线：PG Operation 改为同事务解析映射、能力检查和领域回调；审批/核对在业务行锁后的复核保持。SQLite 不提供隐式 RLS 或无范围 SQL 回调，仍无运行入口。规范见 [身份](../contracts/identity.md) 与 [运维](../contracts/operations.md)。
- 证据：`pnpm check`、全量 `pnpm test:db` 退出 0（`.tools/m3-sqlite-capabilities-check.log`、`m3-sqlite-capabilities-db.log`），测试库/角色残留 0、集群停止。提交前发现 sqlitedb/database.go 被改回 schema 1 且移除写锁；用户确认非有意改动，已恢复 schema 4 与 _txlock=immediate。恢复后 SQLite 数据库/身份/任务全包通过（`m3-sqlite-capabilities-restored-final.log`），包含双连接事务在读取前取得写锁及回滚释放锁回归。阻塞解除；停止代码编辑后的 `pnpm check` 退出 0（`m3-sqlite-capabilities-restored-check-final.log`）。前一次重验与新增测试导入并发，编译报 time 导入文件缺失，日志保留于 restored-check.log，未计为通过。
- 下一片：SQLite 审批挂起、查看与决定，复用本片事务内授权；绑定版本/摘要/期限并覆盖重启、并发回调及撤权。人工核对、查询与运行入口随后按依赖推进。
- 远端补证：f59d93c CI 39411112694/39411073583、35553ed CI 39411716145/39411708455 已 success；5b8e55e CI 39412485835/39412477837 也已 success。

## 保留切片：SQLite 基础任务租约（2026-10-20）

- 实现：R03/R06、A03/A04/A08、V03/V05/V06；SQLite schema 3 保留旧 pending 任务与审计，增加认领、当前身份复核、续租、基础文本状态推进及未知意图恢复扫描；复用共同状态转换判定。事务写锁后使用数据库时间，步骤审计原子且唯一；没有 Worker/配置入口。单点边界见 [任务契约](../contracts/task-runtime.md#sqlite-基础文本任务租约)。
- 反例：首轮共享测试暴露版本起点差异（PG 初始版本 1、首次认领 2，SQLite 曾从 0 开始），已按 PG 修正。双驱动契约覆盖并发/范围、旧世代/旧版本、检查点复用与撤权、未知结果不重放、状态/恢复审计回滚；另覆盖成功终态与 schema 2→3 原数据保留。
- 证据：定向真实 PG/SQLite 及既有 PG 检查点恢复测试通过（`.tools/m3-sqlite-leases-db.log`），`pnpm check` 退出 0（`m3-sqlite-leases-check.log`）；末补成功终态后双驱动契约通过（`m3-sqlite-leases-final.log`）。未重跑全量 test:db；测试库/角色残留 0，集群停止。上一片 35553ed CI 39411716145/39411708455 已 success（2026-10-21 复核），本片远端待确认。
- 下一片：补齐 SQLite 业务能力授权与审批存储，再接人工核对及查询；所有依赖完成前不开放真实渠道，知识/向量仍须启动拒绝。PG schema 22 未变。

## 保留切片：SQLite 任务接收（2026-10-20）

- 实现：R01/R02/R03/R15、A03/A04/A08、V06/V11/V12；复用渠道既有 Inbox.Enqueue 窄接口，新增 SQLite 文件任务/审计 schema 2。BEGIN IMMEDIATE 后解析当前身份，任务与审计原子提交；不改 PG 生产路径。单点边界见 [渠道契约](../contracts/feishu-model.md#sqlite-任务接收基础)。
- 验证：PG/SQLite 共享接收契约通过（`.tools/m3-sqlite-inbox-db.log`），包括独立 SQLite 连接并发去重、重建实例、执行版本不覆盖、交叉键冲突、跨租户/应用、停用/改绑及审计失败回滚。`pnpm check` 通过（`m3-sqlite-inbox-check.log`）；末补 schema 1→2 保留身份升级测试后，SQLite 全包定向通过（`m3-sqlite-inbox-final.log`）。
- 环境：PG schema 22 未改，未重跑全量 test:db；测试库/角色残留 0，集群停止。上一片 f59d93c 的 CI 39411112694/39411073583 已 success（2026-10-21 复核），本片远端待确认。
- 下一片：实现任务认领、租约世代、状态与审计原子推进的双驱动契约；随后推进审批、恢复与查询。SQLite 仍无运行入口，不得让真实渠道确认接收；知识/向量拒绝检查在运行入口开放前完成。

## 保留切片：SQLite 身份基础（2026-10-20）

- 范围：R02、A03/A08、V02/V06；沿已确认的任务/审批/审计/身份首版范围推进。新增 sqlitedb 文件打开与独立 Goose schema 1、identity/sqlite.Resolve；没有 SQLite 运行入口，不能据此宣称轻量部署可用。契约见 [身份](../contracts/identity.md)。
- 判断：SQLite 没有 PG RLS，未复制 Within 原始 SQL 回调；后续领域仓储须显式拥有授权事务。modernc.org/sqlite v1.60.1（BSD-3-Clause，纯 Go）及匹配 libc 已固定；依赖校验和入库，完整离线许可材料仍待制作。
- 证据：PG/SQLite 共用 Resolve 契约通过，含精确键、同成员 ID 跨租户、停用、撤链、改绑、取消；真实 PG 原有身份/RLS 回归一并通过（`.tools/m3-sqlite-identity-db.log`）。文件测试覆盖重开持久化、连接更换外键、跨租户外键拒绝、缺失/未迁移/未来 schema 拒绝、连接失败不伪装未认证（`m3-sqlite-identity-final.log`）。首轮测试文件名含 Windows 不允许的问号，修正夹具后通过。
- 工程：`pnpm check` 退出 0（`m3-sqlite-identity-check.log`）；最后补充两个数据库负例后定向测试通过。PG schema 22 未改，无运行时调用变动，未重跑全量 test:db；测试数据库/角色残留 0，集群已停止。远端 CI 待确认。
- 下一片：抽取任务领域的窄存储接口，先覆盖任务创建幂等、当前身份、跨租户与状态/审计原子性；审批和恢复随依赖分片。知识/向量禁用启动检查在 SQLite 运行入口开放前完成。

## 保留切片：飞书 docx 读取器（2026-10-20）

- 实现：R02/R10/R12、A06/A08、T08/T09、V07/V08 的来源读取边界；`internal/feishudocs` 显式绑定用户凭证，固定版本分页，前后重验身份与权限，拒绝坏游标、不完整块树和超限。只返回完整原始快照，不发布 ACL；单点契约见 [飞书文档](../contracts/feishu-docs.md)。
- 判断：原生权限检查针对当前 token；应用可读或协作者列表不能直接成为员工 ACL。AppID 为可信配置绑定，userinfo 核对 OpenID/TenantKey。未知块保留原始对象，后续解析须显式支持或拒绝。
- 本机证据：`pnpm check` 退出 0，含读取器合成 HTTP 测试，日志 `.tools/m3-feishu-docs-reader-check.log`。无 SQL/数据库变更，本片未重复运行 test:db；未真实租户联调，Windows 无 gcc，Linux race 待本片远端 CI。
- 后续规范化：已实现页面/段落/标题/列表/待办的 text_run 及未合并矩形表 → knowledge.Content，按源树排序、保留源块 ID、绑定版本锚点；合并/嵌套表、图片/提及/链接/删除线等暂拒绝整份文档，不截断或丢弃后伪成功。限制与未支持类型以契约为准。
- 规范化证据：`pnpm check:go` 退出 0（`.tools/m3-feishu-normalize-check.log`）；补充 256 切片边界、空块与样式片段后，`go test -count=1 ./internal/feishudocs` 通过（`.tools/m3-feishu-normalize-tests.log`）。覆盖分页乱序、中文正文、版本锚点、未知内容、缺块和超限；无 SQL 变更，未重复数据库测试。
- 列表切片：保留显式编号、连续同级 auto 编号和待办完成状态；为子块加入父列表上下文，避免丢失否定/条件。缺编号、缺状态、溢出及上下文超限拒绝。`pnpm check:go` 通过（`.tools/m3-feishu-lists-check.log`），含嵌套语义、编号中断/重启及拒绝部分发布测试；无 SQL 变更，未重复数据库测试。
- 远端与评审：6b3960b CI 39391058510、2e8457a CI 39392608108/39392460431、2cb23d6 CI 39393729434/39393721936、010ca72 CI 39394609097/39394603097 均 success。草稿 [PR #2](https://github.com/Orpheus-K/enterprise-agentic-os/pull/2) 依赖 #1。
- 表格切片：校验行列、cells/children 顺序、明确单位合并跨度；整表为一个 JSON 正文切片，保留表头标记、空单元格、转义字符及父级条件，不单独发布单元格。`pnpm check:go` 通过（`.tools/m3-feishu-tables-check.log`），覆盖价格表关系、缺合并信息/错误几何拒绝、类型错配和序列化后超限；无 SQL 变更，未重复数据库测试。本片远端尚未确认。
- 身份切片：MappedReader 固定安装 TenantID/AppID/TenantKey，使用现有飞书渠道键解析当前 IAM 成员；远端操作前后复核，停用、撤链、改绑及基础设施失败均不返回快照。Access 只是核验记录，不代替发布事务授权。
- 身份证据：`pnpm check:go` 通过（`.tools/m3-feishu-mapping-check.log`）；真实 PG `go test -tags=integration -count=1 -run '^TestMappedReaderWithPostgresIdentity$' -v ./internal/feishudocs` 通过（`.tools/m3-feishu-mapping-db.log`）。初次夹具因重复 employee_id 失败，修正合成员工编号后通过。测试库/角色残留 0，17/agentos-m3 已停止；本片远端待确认。无 SQL/迁移修改，未重跑全量 test:db。
- 身份远端：299467f CI 39395915886/39395910986 均 success。
- 发布事务切片：Store.Publish 将同版本内容、单空间向量、ACL 和审计原子提交；复用现有写入规则，末尾重验同步权限、健康和授权期限。错误回滚并保留旧版本，重试幂等；仍未实现持久同步任务。契约单点见 [知识契约](../contracts/knowledge.md#原子发布已准备的快照)。
- 发布证据：`pnpm check:go`、全量 `pnpm test:db` 均退出 0（`.tools/m3-publication-check.log`、`.tools/m3-publication-full-db.log`）。真实 PG 故障覆盖向量缺项、ACL 冲突、过期、错误世代和末步审计失败；另验重复请求和新版本替换。初次抽取函数的 err 作用域导致编译失败，修正后通过。本片远端待确认。
- 发布远端：f4edd06 CI 39397429247/39397424216 均 success。
- 同步任务切片（schema 20）：新增强制 RLS 的 sync_jobs；持久调度版本/配置摘要、领取世代/Token、过期接管；PublishSync 把三通道发布、任务完成与审计放在同一事务，末尾租约过期或完成审计故障均回滚。相同完成请求可核对未知提交，不接受旧世代/旧配置或不同内容。尚无后台 Worker、原生游标或 Embedding 意图检查点。契约见 [同步围栏](../contracts/knowledge.md#持久同步任务与发布围栏)。
- 同步任务证据：`pnpm check` 通过（`.tools/m3-sync-jobs-check.log`）；定向真实 PG 恢复/并发/围栏用例通过（`m3-sync-jobs-targeted.log`）。全量 `pnpm test:db` 仅知识模块旧 RLS 表数量断言失败（9→实际10，其余模块通过，`m3-sync-jobs-full-db.log`）；按新迁移更新断言后重跑全部知识模块 integration 测试通过（`m3-sync-jobs-knowledge-db.log`），未再次重跑无关模块。测试库/角色残留 0，17/agentos-m3 已停止。新迁移尚待远端 CI；历史 schema-18 备份演练不覆盖 schema 20。
- 同步任务远端：6d3755b PR CI 39399262983 completed/success。
- Embedding 检查点（schema 21）：BeginSyncEmbedding 先保存整批意图与内容/空间；SaveSyncEmbedding 验完整切片集并原子保存向量/审计。未知 pending 不允许过期接管或新请求覆盖；完整结果可接管复用，PublishSync 校验内容/向量一致，ACL 必须重新核验。尚无人工核对入口或后台 Worker，不能视为运行时同步已完成。
- 检查点证据：`pnpm check`、全量 `pnpm test:db` 均退出 0（`.tools/m3-sync-embedding-check.log`、`m3-sync-embedding-full-db.log`）；定向 `TestSync*` 通过（`m3-sync-embedding-targeted.log`）。覆盖未知不重放、迟到拒绝、部分向量拒绝、审计故障回滚、幂等保存及恢复发布。测试库/角色残留 0，17/agentos-m3 已停止。本片远端待确认；备份演练仍未覆盖 schema 21。
- 检查点远端：ed1be1c PR CI 39400702524 completed/success。
- 受控核对（schema 22）：新增 knowledge.reconcile 显式授权和不可变 sync_reconciliations 账本；HTTP `/api/ops/knowledge/sync/reconcile` 支持带证据摘要的 not_sent/completed，后者须完整匹配向量。只处理过期 pending，任务锁后重验 IAM/来源/能力；原子增加世代、写结果/核对/审计，幂等重试仍验权限。当前没有核对 UI 或供应商结果查询。
- 核对证据：`pnpm check`、全量 `pnpm test:db` 通过（`.tools/m3-sync-reconcile-check.log`、`m3-sync-reconcile-full-db.log`）；随后加强账本写入者 RLS 约束，重跑两种 HTTP＋PG 核对用例通过（`m3-sync-reconcile-final.log`）。覆盖活跃租约拒绝、缺能力/证据、错误世代、审计回滚、幂等、旧执行者及能力撤销。测试库/角色残留 0，17/agentos-m3 已停止。本片远端待确认。
- 核对远端：50f910c CI 39402156375/39402147117 均 success。
- 发布身份复核：发现读取前后的 IAM 核验不足以覆盖发布锁等待；新增 PublishSyncForMembers，要求同步者和完整 ACL allow 成员证明，任务/文档锁后及发布后重验当前映射，恢复同步者上下文后写审计。不得将此校验视为原生文档权限证明。
- 复核证据：`pnpm check:go` 与知识模块全部真实 PG integration 测试通过（`.tools/m3-sync-members-check.log`、`m3-sync-members-db.log`）；定向测试覆盖缺员、改绑、撤销、写入中撤销及文档锁等待期间撤销（`m3-sync-members-targeted.log`）。初版测试在持锁连接中观察 pg_stat_activity 超时，改独立观察连接后通过。无 schema 变化，未重跑其他模块数据库测试；测试残留 0，集群已停止。本片远端待确认。
- 复核远端：e8b4915 CI 39403158224/39403152439 均 success。
- 单次执行器：Syncer.RunOnce 已串联持久领取、用户凭证读取、规范化、Embedding 意图/结果、最终原文重读、逐成员有效权限核验、来源健康及 PublishSyncForMembers。配置摘要绑定来源/身份/模型空间；一分钟证明窗、五分钟租约；失败尝试标记来源 unavailable，错误不吞。ReportSyncHealth 在来源锁下分配健康版本，防止文档请求版本混用。
- 检查点复用：较新请求同配置保留完整结果，原文不变不重新嵌入；原文变化才允许清除已知结果并重新写意图，未知 pending 不受影响。已有结果恢复仍重新取当前身份/权限，不能复用 ACL。
- 执行器证据：初版 `pnpm check`、全量 `pnpm test:db` 通过（`.tools/m3-native-sync-check.log`、`m3-native-sync-full-db.log`）；补同内容复用/新版本重算后，`pnpm check:go` 及飞书来源、知识模块全部 PG integration 测试通过（`m3-native-sync-final-check.log`、`m3-native-sync-final-db.log`）。合成原生协议＋真实 Embedder HTTP＋真实 PG 覆盖首次发布、原生拒绝、Embedding 后撤权、未知不重放、已知结果接管和阻断旧证据。测试残留 0，集群已停止；本片远端待确认，未真实租户联调。
- 执行器远端：a5f6419 CI 39404561944/39404556077 均 completed/success。
- 启动轮询切片：R02/R10/R12、A06/A08、T08/T09、V07/V08；可选 sources 固定来源、文档集合、用户凭证环境变量和模型空间。启动先验凭证、IAM/来源及空间，不创建任务或访问供应商；Run 启动受取消控制的轮询。PollSync 在数据库分配请求版本，保留活跃/过期同配置任务与未知 Embedding；文档集合纳入配置摘要，配置变更拒绝覆盖活跃租约或未知调用。部署要求先停止旧配置实例。
- 轮询证据：`pnpm check`、全量 `pnpm test:db` 均退出 0（`.tools/m3-source-bootstrap-check.log`、`m3-source-bootstrap-full-db.log`）；定向配置/启动/轮询 PG 测试通过（`m3-source-bootstrap-targeted.log`），覆盖缺凭证、禁用来源/空间、取消启动无调度、重复轮询及未知结果保护。schema 仍为 22；测试库/角色残留 0，测试集群已停止。本片远端待确认；未真实租户联调，未验证实际供应商持续运行。
- 同步查询切片：R10/R12/R15、A06/A08、T08/T09、V07/V08；POST `/api/ops/knowledge/sync/inspect` 按当前来源同步权限＋knowledge.reconcile 授权返回指定任务的请求/世代/配置、租约有效性、Embedding 状态及输入摘要/空间/版本/切片 ID。不返回正文、向量或租约 Token，不把过期视作未发送证据；无迁移，schema 22。
- 查询证据：`pnpm check:go` 通过（`.tools/m3-sync-inspect-check.log`）；知识模块全部 integration 通过（`m3-sync-inspect-db.log`）。同轮运维测试仅新增禁用来源夹具误用 source_id 列失败，改实际 id 后全部运维 integration 通过（`m3-sync-inspect-ops-final.log`）。覆盖活跃/过期意图、核对后两种状态、缺授权/撤权/禁用来源、不可访问来源/缺任务和输出脱敏。测试库/角色残留 0，测试集群已停止。未变更其他模块或 schema，未重跑全量 test:db；本片远端待确认。
- 贯通验证：T08/T09、V07/V08；扩展 TestNativeSyncPipeline，将真实同步发布接入会话查询嵌入、模型上下文、持久引用及发送。新增模型后原生收件人撤权/读取失败并再次同步，确认派生回答不发送、任务 failed 且不重放；已知向量接管后正常交付亦通过。只改测试/文档，无生产实现或 schema 变动。
- 贯通证据：`go test -tags=integration -count=1 -run '^TestNativeSyncPipeline$' -v ./internal/feishudocs` 全部 7 场景通过（`.tools/m3-source-conversation-targeted.log`）。原生/模型/发送为替身，PG 为真实实例，不代替真实租户联调或权限传播时延验收；本片未重跑无关模块。测试库/角色残留 0，集群已停止。
- 下一片：核查真实租户授权与 Token 更新生命周期及 M3 剩余差距，按实际需求继续开发；当前仅环境变量载入用户 Token，尚无 OAuth 获取/自动刷新。SQLite 首版范围已确认，待实施，M3 不据此退出。

## 保留切片：会话检索与交付（2026-10-20）

- 实现：R03/R06/R10/R12/R13、A04/A05/A06/A08、T05/T06/T08/T09/T10/T13、V03/V06/V07/V08/V12 的执行与授权边界。配置 retrieval 固定空间、模型/版本、limit，启动验证空间与维度，指纹绑定嵌入配置；未启用保留纯聊天指纹。Worker 在外部意图后 Embed/Search/Ground 并持久化引用，后续工具回合复用且重验，响应附真实来源标题和锚点。契约单点见 [会话检索与交付](../contracts/knowledge.md#会话检索与交付)。
- 守住交付：模型前续租、工具/发送意图在任务锁后用同一事务重验当前身份及全部引用；审批查看、决定、改单和通知另验审批人的知识权限。未知嵌入不自动重放；审计/数据库错误显式失败，不当作无资料。群聊仍由渠道拒绝。
- 证据：最终生产代码 `pnpm check`、`pnpm test:db` 均退出 0（`.tools/m3-retrieval-runtime-final-check.log`、`m3-retrieval-runtime-final-db.log`）；启动/配置/撤权/恢复/审批/工具定向日志 `m3-retrieval-runtime-targeted.log`。后补报价贯穿用例 `go test -tags=integration -count=1 -run '^TestQuoteMessageToolApprovalAmendDelivery/retrieval' -v ./internal/feishu` 通过（`m3-retrieval-quote.log`），覆盖来源进入工具报价、改单后批准交付及撤权拒绝改单。均为合成来源、模型/MCP/发送替身。
- 保留反例：续租锁等待期间撤权的测试先失败（旧代码返回 nil），补锁后重验后通过；日志 `m3-retrieval-renew-before.log`、`m3-retrieval-renew-after.log`。最终全量在该生产修复后执行。
- 清理与远端：测试库/角色残留 0，WSL `17/agentos-m3` 已停止；a72d34a 的 [PR CI 39388783955](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39388783955) 与 [push CI 39388741561](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39388741561) 均 completed/success。工作分支 `codex/m3-conversation-retrieval`，草稿 PR #1。
- 下一片：核实飞书云文档权限语义、版本/游标恢复及撤回信号，完成受控来源连接器与向量发布；不得以应用 Token 可读推导全员 ACL。真实租户/模型联调、中文召回质量、群聊接收范围及 M3 其余退出项仍待独立验收；SQLite 未启动重写。

## SQLite 差距清单（未实现，首片范围已确认）

- 范围测量（生产 Go 代码 50 个文件）：经 database.Queries 写原生 PG SQL 的文件 18 个；RLS 上下文（current_setting/set_config）27 行/13 文件；FOR UPDATE 9 行/7 文件；数组 ANY/array_agg 19 行/7 文件；ON CONFLICT 9 行；clock_timestamp/now() 36 行/12 文件；pgx 类型 42 行/12 文件；plpgsql 函数（can_sync、source_ready）及 18 份迁移（约 35 KB，含强制 RLS、触发器、pgvector）。
- 结论：不是换驱动，而是“数据访问层抽象＋迁移双份＋应用层授权等价＋同契约测试参数化”。SQLite 无 RLS，每条查询须显式带租户/安装/成员条件并通过同一套跨租户/越权负例；无 pgvector，向量检索须显式禁用。
- 已确认路线（用户于 2026-10-20 确认）：先抽取窄的存储接口并只迁移任务/审批/审计/身份（M1—M2 范围），知识与向量在 SQLite 版明确禁用并拒绝启动对应能力；同一契约测试在 PG 与 SQLite 双跑后再扩展。预计为多切片工作，不在单次提交内完成，也不宣称 A03 已满足。
## M3 嵌入客户端（库层）

- 实现：model.Embedder（固定 URL/模型/Revision/维度，拒绝维度或条数不符、溢出、超大响应；错误脱敏），共用 post() 已重构自 Client.request。契约见 [知识契约](../contracts/knowledge.md#查询向量来源与配置)。去掉维度检查的变异使测试失败。
- 远端证据：2026-10-20 核实 5b7b41342d74597295c86625840a6db62bf021b9 [CI 39284755597](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39284755597) completed/success，不再处于排队未确认状态。

## M3 嵌入配置切片（2026-10-20）

- 范围：R10/R13、A05/A06/A08、T13/V12 的配置边界。bootstrap.Config 增加可选 embedding，严格解析；启动独立读取 APIKeyEnv 并构造客户端，命名密钥缺失在数据库连接前失败。省略/null 不构造，启动无供应商探测或文本外发。
- 证据：`go test -count=1 ./internal/bootstrap ./internal/model` 通过，覆盖配置、独立凭证、无鉴权服务及实际 Open 启动失败路径；`pnpm check` 退出 0，日志 `.tools/m3-embedding-config-check.log`；`git diff --check` 通过。本片未改变数据库语义，未运行 test:db；新改动尚无远端 CI 证据。
- 边界：未接入业务 Embed 调用；不修改纯聊天运行指纹、不宣称 RAG 可用。部署片段见 [飞书配置](../deploy/feishu.md)，契约为单点规范。
- 下一片：先检查 conversation 的版本、检查点和 delivery_pending 恢复路径；将检索空间/配置纳入运行绑定，持久化引用，模型前与交付前按当前身份取证。验证模型后撤权、恢复后旧引用、跨租户及审计失败；完成这些前不启用检索。随后再做飞书云文档来源与双通道恢复；SQLite 首版范围已确认，待实施。

## M3 会话引用检查点（2026-10-20，schema 19）

- 范围：R03/R06/R12、A04/A06/A08、T05/T06/T08、V03/V07。00019 新增 `channel.tasks.knowledge_refs`；SaveKnowledgeReferences 在 model_running 下以有效租约/世代/步骤保存不可变引用集合，并原子留痕；Claim 恢复引用，区分未准备与空结果。契约见 [持久化会话引用](../contracts/knowledge.md#持久化会话引用消费者前置)。
- 本机证据：`go test -tags=integration -count=1 -run TestKnowledgeCheckpoint -v ./internal/feishu` 通过；真实 PG 全量 `pnpm test:db`、`pnpm check` 均退出 0，日志 `.tools/m3-retrieval-checkpoint-targeted.log`、`m3-retrieval-checkpoint-full-db.log`、`m3-retrieval-checkpoint-check.log`。测试覆盖连接池重开、空结果、篡改/重复/无效输入、跨范围及过期执行者拒写、审计回滚、未知意图不自动重放。初次定向编译因测试用例名称缺少 map 下标失败，修正后通过。
- 清理：WSL `17/agentos-m3`（55440）测试库/角色残留均为 0，已停止本次启动的集群。未跑 Windows race（无 gcc）；本片仍无远端 CI 证据。
- 边界：仅新增持久化能力，Worker 尚未调用，无检索正文流入模型或交付。之前的 schema-18 备份演练为历史证据，不自动覆盖 schema 19。
- 下一片：在 bootstrap 固定检索空间、Embedding 配置与运行 Revision；Worker 先提交 model_running 意图，再 Embed/Search/Ground、保存引用并调用模型。恢复/工具后续回合使用原引用重新取证，交付前任何引用失效均阻断整个响应；审批查看和通知须分别验证审批人的知识权限，不能只核对任务发起人。完成真实 PG 撤权/恢复/审计故障测试后再启用会话检索。

## M3 PG 备份恢复演练

- schema 22 补片（A10/V15）：旧固定 9 表 manifest 遗漏同步检查点，且 echo 内 psql 失败被掩盖。v2 枚举全部业务普通表，记录行数、完整行摘要及逐表 owner/RLS；dump 与 manifest 同校验，事实查询显式失败，备份名随机后缀。v2 明确拒绝旧无版本 manifest。
- 证据：故障注入在旧脚本上复现伪成功，在新脚本上通过，已纳入 CI。WSL 真实 PG schema22 演练通过（`.tools/m3-backup22-drill.log`）：未知输入/核对账本保留、manifest 损坏/非空目标/同数量错误摘要拒绝、运行身份 RLS/DDL 限制保留。测试库/角色清理，集群停止。无 Go/SQL 迁移变更，未重跑产品套件；本片远端待确认。仍要求停写，不能作为在线备份或外部副作用可重放证明。

- 实现：[backup-restore.sh](../deploy/postgres/backup-restore.sh)；manifest 事实比对、校验和、非空目标拒绝。演练与未覆盖项见 [PG 指南](../deploy/postgres/README.md#备份与恢复)。演练脚本与凭证在 .tools（忽略），日志 m3-backup-drill.log，残留 0，集群已停止。
- 本片历史边界：当时未做联合/在线备份与离线包。当前已完成 SQLite 首版、停写备份恢复和 Windows 核心离线包；现状统一见顶部切片与 M3 退出差距。

## M3 WASM 沙箱切片

- 实现：internal/sandbox.Run（wazero v1.12.0）；摘要固定、零 host function/WASI、必填超时与内存上限、每次新运行时。契约见 [沙箱契约](../contracts/sandbox.md)。
- 远端证据：26126c7 [CI 39281615533](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39281615533) success（含 Linux race 与驱动探针）；该次排队约 40 分钟（runner 积压），非失败。
- 本机验证：沙箱单测通过；三项变异（去摘要、去内存上限、去上下文中断）均失败（m3-sandbox-mutation.log）。无需数据库。
- 本机 Go 取依赖需 GOPATH=.tools/gopath（GOMODCACHE 默认在 AppData 不可写）。
- 未实现：规则核算 WASM 模块、容器 Python 路线（本机无容器运行时）、指令计量、产物扫描。

## M3 任务清单与基础统计（schema 18）

- 实现：GET /api/ops/tasks、/api/ops/stats，复用 audit.read；00018 为 audit_reads 增加 scope 且非单任务读取 task_id 为空；新增任务时间窗索引。契约见 [运维契约](../contracts/operations.md#查询任务清单与基础统计)。
- 本机验证：pnpm test:db、pnpm check 退出 0（m3-report-full-db/check.log）；三项变异（去应用过滤、去游标范围、去留痕）均失败（m3-report-mutation.log）；残留 0，集群已停止。迁移测试的应用条数改为由 database.SchemaVersion 推导。
- 远端证据：178be47 就绪修复 [CI 39276496300](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39276496300) success；89572b1（schema 18）[CI 39277691880](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39277691880) success。首次 watch 因 GitHub API 502 中断，非测试失败。
- 未覆盖：知识读取/检索统计（knowledge 审计表运行角色仅 INSERT，需单独授权设计）；耗时分位与人力/收益估算不提供。
- 真源：[研发路线](../enterprise-agentic-os-architecture/07-架构决策与研发路线.md)、[M2 验收](../deploy/m2-acceptance.md)、[知识管道契约](../contracts/knowledge.md)。契约区分已实现存储/检索与未接入来源/Embedding/消费者。

## 当前 M3 向量检索切片

- 固定空间：00017 要求管理员预装 pgvector 0.8.7，缺失则迁移失败停在 16；embedding_spaces 由管理员预置，模型/版本/维度不可原地改。
- 发布向量：PublishVectors 须覆盖当前内容版本全部切片，同事务替换＋审计；新内容版本经切片级联清除旧向量。ACL/健康变化不重算向量。
- 授权检索：Search 与 Read 共享 visibleChunk 谓词，排序前过滤并叠加 RLS；候选仅含 Reference＋距离，取证必须重走 Read。变异去掉谓词后同步身份用例失败，普通读者仍由 RLS 拦截。
- 入口：/api/ops/knowledge/vectors 与 /search，沿用认证与错误码。
- 本机验证：WSL `17/agentos-m3`（55440，需 `pg_ctlcluster --skip-systemctl-redirect` 启动）上 pnpm test:db 与 pnpm check 均退出 0（m3-vector-full-db.log、m3-vector-check.log）；git diff --check 通过；测试库/角色残留 0，验证后集群已停止。
- 远端证据：fc2d6c6 [CI 39237901826](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39237901826) success（schema 16）；b211575 [CI 39266475769](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39266475769) success，含 pgvector 镜像与驱动探针；ab3c7ef（schema 17）[CI 39268242988](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39268242988) success（工程检查、真实 PG、race、驱动探针），日志 .tools/m3-vector-ci.log。
- 限定范围：无 Embedding 调用、ANN、全文融合、中文召回评测、原生来源同步/游标恢复、组织权限解析及模型/交付消费者；不宣称完成 RAG。

## 固定 M2 证据

- 复验审批：2026-10-19，当前 fc2d6c6/schema 16 已具备本次要求，无重复实现。真实 PG 执行 `go test -tags=integration -count=1 -run 'TestApproval' -v ./internal/feishu` 及 `-run 'TestCardApprovalDatabaseBoundary|TestQuoteApprovalCardAmendRestartDelivery'` 均通过；覆盖版本/摘要绑定、并发重复回调、撤权（含锁等待）、挂起原子性、重启与租约恢复、改单重新审批和单次交付。日志 .tools/m2-approval-current-db.log、m2-approval-current-card-db.log；仅合成替身，无外部发送。此项为当前提交定向复验，不替代全量 CI/race。
- 验收制品：8dad1fb00790ce9f2dfdb190c991263680745dbd；[Check 39234206040](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39234206040) completed/success。工程检查、真实 PG、普通/integration race、2 项浏览器测试全部通过，日志 .tools/m2-ci-success.log。
- 保留失败：0296799 的 [首轮 Check](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39233587067) 在通知并发测试失败；任务候选快照过时，无待发通知却报 no rows。修复为锁后空候选返回无任务，其他错误仍失败；本机连续 20 次真实 PG 回归通过，再以新提交完整 CI 验证。
- 修复撤权：任务核对读/写在锁等待后重验当前身份及 task.reconcile；反例先失败再修复，日志 m2-recovery-lock-before/after.log。通知核对复用同一检查。
- 保留本机：m2-card-send-full-db.log、m2-card-send-check.log、m2-notice-race-regression.log、m2-notice-race-go.log 位于 .tools；测试库/角色残留均为 0，PG 已停止。无真实业务数据或外部发送。

## 保留实现边界

- 固定执行：schema 14；租约世代/版本、持久检查点、未知操作证据核对、审批挂起/唤醒、通知发送与核对均落库；外部未知结果不盲重试，不承诺 exactly-once。
- 固定资产：Skills 以 manifest＋SKILL.md 摘要固定，动态摘要路由、授权交集、在途版本并存；缺包拒绝，旧包须保留。示例摘要按 Git blob 验证。
- 限制工具：官方 MCP SDK 固定目标，只读工具、凭证代办、契约校验、审计与有界循环；复用已保存结果，不重放未知外部请求。
- 绑定报价：整数金额核算、七五折审批、改单重算、旧卡片拒绝、差异显示、可信渠道回调和 V2 内部文本初稿单次交付已合成贯通。真实飞书卡片现场认证、正式 PDF 与用章仍待后续验收。
- 保护卡片：获权审批人可见绑定版本全文，plain_text 且关闭转发；超限拒绝而非截断。已发送内容无法因撤权收回，点击与交付重新授权。
- 保留 M1：[现场验收](../deploy/m1-acceptance.md) 与历史 CI 独立留存；M1 联调库 schema 3 未迁移，不与当前制品混跑。

## M3 引用消费者切片（2026-10-19）

- 实现：knowledge.Store.Ground 逐条以当前身份 Read，撤权计入 Dropped；ModelContext 标注资料并拒绝超预算；群聊闸门在读取前失败。合同见 [knowledge 第 6—8 条](../contracts/knowledge.md)。
- 本机验证：WSL `17/agentos-m3` 上定向 TestGround、pnpm test:db、pnpm check 均退出 0（m3-ground-targeted/full-db/check.log）；两项变异（去闸门、吞错为 Dropped）均失败（m3-ground-mutation.log）；`.tools/pg-residue.sh 55440` 测试库/角色残留 0，集群已停止。Windows Go 需 GOCACHE/GOTMPDIR 指向 .tools。
- 未接入：对话路由、Embedding 提供方、飞书群聊交付。当前无运行路径调用检索。
- 远端证据：3313a87 [CI 39275270620](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39275270620) success（工程检查、真实 PG、驱动探针、race、e2e），日志 .tools/m3-ground-ci.log。本切片签收。
- 修复就绪：conversation.Ready 写死 schema 16，而启动要求 17，当前制品 /api/ready 恒 503。改为共享 database.SchemaVersion；补“当前 schema 必须就绪”断言，旧代码下该断言失败（Storage:unavailable），修复后通过。全量 test:db 与 check 退出 0（m3-ready-*.log），残留 0。

## M3 后续认证

当前开发顺序以本页顶部当前切片为准。A05 剩余中文全文（pg_search/BM25）许可、过滤召回与扩展升级仍待认证；schema-18 备份演练不替代后续 schema 联合恢复。群聊在接收范围证明完成前继续禁用。

## 保留交付环境

- 私有依赖：.tools/pg-test-admin.url、DPAPI 凭证与现场配置全部忽略，禁止回显；公开源码特征扫描 191 个文件无凭证候选。
- 本机 Go：GO_BINARY=$PWD/.tools/go/bin/go.exe；GOTMPDIR=$PWD/.tools/go-tmp，不改全局设置。Windows 无 gcc，race 由上述 Linux CI 证明。
- 独立 PG：.tools/pgdata-m1，127.0.0.1:55439；测试自行建库建角色并清理。升级先停旧实例、备份并显式迁移，已发布迁移不可重写。
- 当前工作树制品要求 schema 22；已发布迁移保持不变，pgvector 为必需前置。M2 CI 为历史已验收制品证据，不能代替当前 M3 远端验证。

## M3 WASM 折扣规则模块（2026-10-20）

- 范围：R13/R14、A09、V09；增加纯整数 approval.wat/wasm 与 quotation.ApprovalInSandbox，固定摘要、独立运行时、1 秒/1 页限额；商余数算法避免 MaxMinor×基点溢出。宿主与模块各自拒绝非法输入，结果仅 0/1。WABT 1.0.34 构建；生产 Go 不新增编译器依赖。
- 运行时接线：可选 quotation_wasm_sha256 仅接受内置摘要，纳入 executionRevision；省略保留原指纹。WithQuotationSandbox 固定启用，新报价 SaveQuotation 与改单 AmendQuotation 运行精确折扣复核；与独立整数结果不一致或沙箱失败均阻断，不回退。金额汇总、目录与旧稿验证仍由可信 Go 承担；只声称折扣规则隔离。配置变更不接管旧运行版本任务/审批。
- 运行时证据：`pnpm check`、全量 `pnpm test:db` 均退出 0（`.tools/m3-wasm-runtime-check.log`、`m3-wasm-runtime-db.log`）；3 个 WASM 报价贯通场景通过（`m3-wasm-runtime-targeted.log`），含七五折审批、八折边界、改单重审、检查点失败恢复。单元测试覆盖摘要校验/指纹、取消和结果不一致不回退。schema 22 未变；测试库/角色残留 0，集群停止；本片远端待确认，无真实外发。
- 近期远端：来源贯通 c04faec CI 39406978346/39406973183、备份 c44a543 CI 39407631878/39407623734 均 success。WASM 库层 4e21509 的 CI 尚未确认。
- 下一片：核对 M3 剩余计算输出限额、隔离负例与部署材料；容器 Python、CPU 指令计量及文件产物扫描仍未实现，SQLite 首版范围已确认，待实施。
- 证据：`pnpm check:go` 通过（`.tools/m3-wasm-rule-final.log`）。big.Int 差分覆盖边界/最大金额及 100 个固定随机样本；初版 i32 结果在 raw uint64 带高位导致拒绝，模块出口显式零扩展为 i64 后通过，保留边界测试。无数据库变更，未运行 test:db；远端 CI 待确认。下一步为显式启用与运行版本绑定。

- WASM 输出边界补片：旧 Run 实际接受 9 个结果，负例复现于 `.tools/m3-wasm-output-before.log`。现于实例化前验证 i32/i64 签名，参数/结果各限 8；i32 超宽输入拒绝、输出零扩展，不放开文件或引用读取。含死循环启动段的非法签名先拒绝，避免执行后才发现超限。
- 输出证据：`pnpm check:go` 通过（`.tools/m3-wasm-output-check.log`）；补启动段反例后沙箱与报价全部单元测试通过（`m3-wasm-output-final.log`），夹具另经独立 CompileModule 验证有效。无数据库变化，未重跑 test:db；远端待确认。仍未实现 CPU 指令计量、容器 Python 与文件扫描。

## M3 离线制品身份（2026-10-20）

- A10/V15 前置：两个 Go 二进制增加 --version JSON，报告编译平台、Go 版本、源码 revision/modified、schema 与内置规则摘要；来源缺失明确 null，不伪装干净制品。此路径在加载现场配置/数据库/监听之前退出，未知参数拒绝。
- SQLite 用户决策：首版仅任务、审批、审计、身份；知识/向量启用须启动拒绝。继续采用窄存储抽象、PG/SQLite 同契约测试，不按替换驱动实现、不提前宣称全功能等价。
- 制品身份验证：`pnpm check` 退出 0（`.tools/m3-artifact-identity-check.log`）；构建后冒烟执行两个 --version，配置故意无效仍离线成功，组件元数据一致，额外参数拒绝。缺构建来源单元测试通过。无数据库语义变更，未重跑 test:db；本片远端待确认。
- 下一片：按已确认范围开始 SQLite 身份存储抽象；先检查 identity.Repository 的使用边界和事务授权上下文，再扩展任务/审批/审计，不引入知识/向量降级。
