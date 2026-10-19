# 接续项目迭代

## 定位当前里程碑

- 进展：2026-10-19，工程 M1、M2 已退出；M3 已完成：知识存储/健康（15—16）、授权向量检索（17）、引用消费者库层、任务清单与基础统计（18）。整体蓝图未完成。
- 推进计划：①消费者签收 ✔ ②审计查询/统计 ③计算隔离 ④原生来源连接器 ⑤检索接入对话与交付 ⑥SQLite 轻量驱动 ⑦离线安装与备份恢复 ⑧M3 退出验收。

## M3 任务清单与基础统计（schema 18）

- 实现：GET /api/ops/tasks、/api/ops/stats，复用 audit.read；00018 为 audit_reads 增加 scope 且非单任务读取 task_id 为空；新增任务时间窗索引。契约见 [运维契约](../contracts/operations.md#查询任务清单与基础统计)。
- 本机验证：pnpm test:db、pnpm check 退出 0（m3-report-full-db/check.log）；三项变异（去应用过滤、去游标范围、去留痕）均失败（m3-report-mutation.log）；残留 0，集群已停止。迁移测试的应用条数改为由 database.SchemaVersion 推导。
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

## 接续 M3 下一切片

1. 评估 A05 剩余项：中文全文（pg_search/BM25）许可与过滤召回、备份/扩展升级演练；未认证前保持禁用。
2. 原生来源权限同步与游标恢复仍待完成；引用消费者接入对话与交付须先有 Embedding 来源与群聊接收范围证明。

## 保留交付环境

- 私有依赖：.tools/pg-test-admin.url、DPAPI 凭证与现场配置全部忽略，禁止回显；公开源码特征扫描 191 个文件无凭证候选。
- 本机 Go：GO_BINARY=$PWD/.tools/go/bin/go.exe；GOTMPDIR=$PWD/.tools/go-tmp，不改全局设置。Windows 无 gcc，race 由上述 Linux CI 证明。
- 独立 PG：.tools/pgdata-m1，127.0.0.1:55439；测试自行建库建角色并清理。升级先停旧实例、备份并显式迁移，已发布迁移不可重写。
- 当前 M3 制品使用 schema 17；00001—00016 保持不变，pgvector 为必需前置。M2 CI 为历史已验收制品证据，不能代替当前 M3 远端验证。
