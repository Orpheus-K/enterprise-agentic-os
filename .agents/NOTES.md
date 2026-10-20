# 接续项目迭代

## 定位当前里程碑

- 进展：2026-10-20，工程 M1、M2 已退出；M3 已实现知识存储/健康（15—16）、授权向量检索（17）、任务清单与基础统计（18）、会话引用/检索/交付复核（19，远端 CI 通过）。M3 整体尚未退出。
- 推进计划：①消费者库层 ✔ ②审计查询/统计 ✔ ③计算隔离部分 ④飞书读取/规范化/身份映射与同步任务存储已实现，运行时同步未接线 ⑤会话检索远端 CI 通过 ⑥SQLite 范围待决策 ⑦PG 备份部分完成 ⑧M3 退出验收待完成。

## 当前切片：飞书 docx 读取器（2026-10-20）

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
- 下一片：接入运行时调度配置与来源健康/权限检查，用纪要/价格表合成夹具贯穿读取、规范化、Embedding 和带围栏发布；补可见的未知任务状态，不能依赖操作者直接改表。SQLite 范围仍待决策。

## 当前切片：会话检索与交付（2026-10-20）

- 实现：R03/R06/R10/R12/R13、A04/A05/A06/A08、T05/T06/T08/T09/T10/T13、V03/V06/V07/V08/V12 的执行与授权边界。配置 retrieval 固定空间、模型/版本、limit，启动验证空间与维度，指纹绑定嵌入配置；未启用保留纯聊天指纹。Worker 在外部意图后 Embed/Search/Ground 并持久化引用，后续工具回合复用且重验，响应附真实来源标题和锚点。契约单点见 [会话检索与交付](../contracts/knowledge.md#会话检索与交付)。
- 守住交付：模型前续租、工具/发送意图在任务锁后用同一事务重验当前身份及全部引用；审批查看、决定、改单和通知另验审批人的知识权限。未知嵌入不自动重放；审计/数据库错误显式失败，不当作无资料。群聊仍由渠道拒绝。
- 证据：最终生产代码 `pnpm check`、`pnpm test:db` 均退出 0（`.tools/m3-retrieval-runtime-final-check.log`、`m3-retrieval-runtime-final-db.log`）；启动/配置/撤权/恢复/审批/工具定向日志 `m3-retrieval-runtime-targeted.log`。后补报价贯穿用例 `go test -tags=integration -count=1 -run '^TestQuoteMessageToolApprovalAmendDelivery/retrieval' -v ./internal/feishu` 通过（`m3-retrieval-quote.log`），覆盖来源进入工具报价、改单后批准交付及撤权拒绝改单。均为合成来源、模型/MCP/发送替身。
- 保留反例：续租锁等待期间撤权的测试先失败（旧代码返回 nil），补锁后重验后通过；日志 `m3-retrieval-renew-before.log`、`m3-retrieval-renew-after.log`。最终全量在该生产修复后执行。
- 清理与远端：测试库/角色残留 0，WSL `17/agentos-m3` 已停止；a72d34a 的 [PR CI 39388783955](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39388783955) 与 [push CI 39388741561](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39388741561) 均 completed/success。工作分支 `codex/m3-conversation-retrieval`，草稿 PR #1。
- 下一片：核实飞书云文档权限语义、版本/游标恢复及撤回信号，完成受控来源连接器与向量发布；不得以应用 Token 可读推导全员 ACL。真实租户/模型联调、中文召回质量、群聊接收范围及 M3 其余退出项仍待独立验收；SQLite 未启动重写。

## SQLite 差距清单（未实现，需决策）

- 范围测量（生产 Go 代码 50 个文件）：经 database.Queries 写原生 PG SQL 的文件 18 个；RLS 上下文（current_setting/set_config）27 行/13 文件；FOR UPDATE 9 行/7 文件；数组 ANY/array_agg 19 行/7 文件；ON CONFLICT 9 行；clock_timestamp/now() 36 行/12 文件；pgx 类型 42 行/12 文件；plpgsql 函数（can_sync、source_ready）及 18 份迁移（约 35 KB，含强制 RLS、触发器、pgvector）。
- 结论：不是换驱动，而是“数据访问层抽象＋迁移双份＋应用层授权等价＋同契约测试参数化”。SQLite 无 RLS，每条查询须显式带租户/安装/成员条件并通过同一套跨租户/越权负例；无 pgvector，向量检索须显式禁用。
- 建议路线（待用户确认）：先抽取窄的存储接口并只迁移任务/审批/审计/身份（M1—M2 范围），知识与向量在 SQLite 版明确禁用并拒绝启动对应能力；同一契约测试在 PG 与 SQLite 双跑后再扩展。预计为多切片工作，不在单次提交内完成，也不宣称 A03 已满足。
## M3 嵌入客户端（库层）

- 实现：model.Embedder（固定 URL/模型/Revision/维度，拒绝维度或条数不符、溢出、超大响应；错误脱敏），共用 post() 已重构自 Client.request。契约见 [知识契约](../contracts/knowledge.md#查询向量来源与配置)。去掉维度检查的变异使测试失败。
- 远端证据：2026-10-20 核实 5b7b41342d74597295c86625840a6db62bf021b9 [CI 39284755597](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39284755597) completed/success，不再处于排队未确认状态。

## M3 嵌入配置切片（2026-10-20）

- 范围：R10/R13、A05/A06/A08、T13/V12 的配置边界。bootstrap.Config 增加可选 embedding，严格解析；启动独立读取 APIKeyEnv 并构造客户端，命名密钥缺失在数据库连接前失败。省略/null 不构造，启动无供应商探测或文本外发。
- 证据：`go test -count=1 ./internal/bootstrap ./internal/model` 通过，覆盖配置、独立凭证、无鉴权服务及实际 Open 启动失败路径；`pnpm check` 退出 0，日志 `.tools/m3-embedding-config-check.log`；`git diff --check` 通过。本片未改变数据库语义，未运行 test:db；新改动尚无远端 CI 证据。
- 边界：未接入业务 Embed 调用；不修改纯聊天运行指纹、不宣称 RAG 可用。部署片段见 [飞书配置](../deploy/feishu.md)，契约为单点规范。
- 下一片：先检查 conversation 的版本、检查点和 delivery_pending 恢复路径；将检索空间/配置纳入运行绑定，持久化引用，模型前与交付前按当前身份取证。验证模型后撤权、恢复后旧引用、跨租户及审计失败；完成这些前不启用检索。随后再做飞书云文档来源与双通道恢复；SQLite 仍待范围决策。

## M3 会话引用检查点（2026-10-20，schema 19）

- 范围：R03/R06/R12、A04/A06/A08、T05/T06/T08、V03/V07。00019 新增 `channel.tasks.knowledge_refs`；SaveKnowledgeReferences 在 model_running 下以有效租约/世代/步骤保存不可变引用集合，并原子留痕；Claim 恢复引用，区分未准备与空结果。契约见 [持久化会话引用](../contracts/knowledge.md#持久化会话引用消费者前置)。
- 本机证据：`go test -tags=integration -count=1 -run TestKnowledgeCheckpoint -v ./internal/feishu` 通过；真实 PG 全量 `pnpm test:db`、`pnpm check` 均退出 0，日志 `.tools/m3-retrieval-checkpoint-targeted.log`、`m3-retrieval-checkpoint-full-db.log`、`m3-retrieval-checkpoint-check.log`。测试覆盖连接池重开、空结果、篡改/重复/无效输入、跨范围及过期执行者拒写、审计回滚、未知意图不自动重放。初次定向编译因测试用例名称缺少 map 下标失败，修正后通过。
- 清理：WSL `17/agentos-m3`（55440）测试库/角色残留均为 0，已停止本次启动的集群。未跑 Windows race（无 gcc）；本片仍无远端 CI 证据。
- 边界：仅新增持久化能力，Worker 尚未调用，无检索正文流入模型或交付。之前的 schema-18 备份演练为历史证据，不自动覆盖 schema 19。
- 下一片：在 bootstrap 固定检索空间、Embedding 配置与运行 Revision；Worker 先提交 model_running 意图，再 Embed/Search/Ground、保存引用并调用模型。恢复/工具后续回合使用原引用重新取证，交付前任何引用失效均阻断整个响应；审批查看和通知须分别验证审批人的知识权限，不能只核对任务发起人。完成真实 PG 撤权/恢复/审计故障测试后再启用会话检索。

## M3 PG 备份恢复演练

- 实现：[backup-restore.sh](../deploy/postgres/backup-restore.sh)；manifest 事实比对、校验和、非空目标拒绝。演练与未覆盖项见 [PG 指南](../deploy/postgres/README.md#备份与恢复)。演练脚本与凭证在 .tools（忽略），日志 m3-backup-drill.log，残留 0，集群已停止。
- 未做：文件/密钥联合备份、在线一致备份、RTO/RPO、SQLite；离线安装包尚未开始。

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
- 当前工作树制品要求 schema 19；已发布迁移保持不变，pgvector 为必需前置。M2 CI 为历史已验收制品证据，不能代替当前 M3 远端验证。
