# 观测与查询运维状态

对齐 R02/R15、A03/A08/A10、V06/V13；仅覆盖单安装、单实例基础观测，不开放员工登录或企业级审计后台。

## 区分接口

知识同步核对的 POST 接口、独立能力及证据要求见 [知识契约](knowledge.md#未知-embedding-的受控核对)，不复用 task.reconcile 授权。

| GET 路径 | 授权 | 输出与语义 |
|---|---|---|
| `/api/health` | 无 | 进程存活，保持原契约 |
| `/api/ready` | 无 | 仅 `{ready: boolean}`；就绪 200，否则 503；不包含租户/任务信息 |
| `/api/ops/status` | `status.read` | 连接状态、Worker 状态、存储检查、模型 `not_probed` |
| `/api/ops/metrics` | `status.read` | OTel 累计调用数、结果分类与耗时直方图；重启归零 |
| `/api/ops/audit?task_id=...&after=0&limit=50` | `audit.read` | 单任务状态、原因码、时间、Trace_ID；默认 50，最多 100 条 |
| `/api/ops/tasks?from=...&to=...&after=<task_id>&limit=50` | `audit.read` | 窗口内任务：ID、当前状态、原因码、技能名/版本、创建/更新时间；按 (created_at, id) 分页，最多 100 条 |
| `/api/ops/stats?from=...&to=...` | `audit.read` | 窗口内创建任务总数、按当前状态计数、这些任务的审批按状态计数 |

- 判定就绪：Worker 运行、当前租户有效且已应用本制品要求的 schema（database.SchemaVersion，与启动检查同源）；长连接须 Connected，断连/重连即不就绪；Webhook Listening 只代表本地可接收，不代表公网配置已验证。
- 限定就绪：不主动调用计费模型；就绪不承诺端到端业务成功。未配置渠道返回 503；停机和数据库故障不复用旧的就绪缓存。
- 认证查询：仅接收独立 Bearer 运维令牌；配置绑定 `operator / agentos:ops:<AppID> / Subject`，复用 IAM Resolve。Cookie、查询令牌、用户/租户 Header 均不构成身份。
- 授权查询：每次重新检查映射、租户、成员及 `iam.operation_grants` 的应用/能力；配置令牌不自动授予权限。运行角色不可写授权表。
- 事务边界：PostgreSQL Operation 在同一数据库事务内解析映射、检查能力并执行领域回调；等待业务行锁后的审批/核对仍保留自己的授权复核。SQLite 已提供同契约的事务内能力检查库，运维 HTTP 处理器可使用同一领域接口，已接入 SQLite 启动配置；不以原始 SQL 回调代替 SQLite 领域范围控制。
- 隔离范围：审计员可查看获准应用在当前租户内的任务；不是“所有成员默认可读”。其他租户/应用与不存在任务均返回空页，不泄露计数。
- 限定字段：不返回 Prompt、响应正文、外部用户标识、端点或凭证；分页 ID 是任务内序号，不暴露跨租户全局序列。未知/重复参数与越界分页拒绝。
- 记录访问：查询与 `channel.audit_reads` 的访问留痕同事务；留痕失败不返回数据。拒绝请求只记观测结果分类，不记录请求中的身份、令牌或参数。
- 关闭缓存：所有运维和就绪响应 `no-store`；令牌无效/身份撤销 401、能力撤销 403、依赖失败 503，错误正文不含内部详情。

## 查询任务清单与基础统计

schema 18 起，对齐 R15/R18 基础统计、V06/V13：

- 窗口：from/to 均为 RFC 3339 时刻，[from, to) 且不超过 92 天；缺失、倒置、空窗口、超宽、未知或重复参数一律 400。stats 不接受 after/limit。
- 范围：仅当前租户与配置应用；不返回 Prompt、响应、渠道标识或成员 ID。外租户/外应用/不存在的 after 返回空页，不报错，避免游标探测。
- 口径：统计只计已落库事实，状态为查询时的当前状态，不是窗口结束时的快照；不含耗时分位、人力或收益估算。知识读取/检索统计尚未开放（运行角色对 knowledge 审计表仅 INSERT）。
- 留痕：每次查询与 `channel.audit_reads` 同事务写入 scope=`tasks`/`stats`、task_id 为空、返回条数；留痕失败 503 不返回数据。单任务读取须带 task_id（约束保证）。
- 授权：复用 `audit.read`，因为清单与统计只汇总审计员已可逐任务读取的数据；撤权 403。

验证：[报表真实 PG 测试](../services/core/internal/operations/report_integration_test.go)。去掉应用过滤、游标范围或留痕的变异均使测试失败。

## SQLite 单任务审计查询

`conversation/sqlite.Inbox.ReadAudit` 已实现库层分页：在同一写事务内复核当前身份与 audit.read，按租户/应用/任务过滤后计算任务内序号，再读取并记录返回条数和当前成员。执行 Revision 不限制历史审计查询；正文与外部身份不进入 AuditPage。分页参数、空页和失败不返回数据的语义与 PostgreSQL 一致。

schema 6 保留访问历史并移除访问留痕对任务的外键；这样不存在或跨范围任务可统一返回空页且记录访问，不通过数据库错误泄露存在性。成员外键保留。两个驱动共用 `conversation/contracttest.RunAudit`，覆盖交错全局事件下的局部分页、跨租户/应用、空页、越界、撤权/改绑、取消和留痕故障回滚。

未知结果人工核对库接口见 [任务契约](task-runtime.md#sqlite-未知结果人工核对)。任务清单与统计库接口见下节；SQLite 运维 HTTP 处理器已接入基础运行入口，能力限制见 [SQLite 部署](../deploy/sqlite/README.md#选择运行存储)。

## SQLite 任务清单与统计

`conversation/sqlite.Inbox.ListTasks/Stats` 复用 ReportWindow、TaskPage、TaskStats 与 audit.read 授权契约（R15/R18、A03/A08/A10、V06/V13）。查询不限制执行 Revision；按租户/应用和创建窗口筛选，游标在相同范围内解析。同一事务写入 scope=tasks/stats 的访问记录后才返回数据，撤权或留痕失败不返回部分结果。

schema 8 保留既有访问记录，允许清单/统计留痕不绑定任务，并增加创建时间分页索引。SQLite 创建时间固定为 UTC 毫秒，窗口上下界均向上取整到存储精度，保持 [from,to)；更新时间取该任务最后一条步骤审计时间。技能尚未移植，技能字段为空；统计只汇总实际任务和审批状态，不推算外部结果。

双驱动共享 `contracttest.RunReports` 覆盖同时间戳分页、未知/跨应用/跨租户游标、窗口边界、审批计数隔离、撤权和留痕故障。此库层能力不代表完整轻量部署已开放。

## 关联执行证据

复用 OTel Go SDK；记录入口、身份复核、模型调用、回复和任务执行。入口 Trace/Span 标识持久化；异步执行建立新 Trace 并链接入口，状态审计和日志关联 task_id/trace_id。旧任务保留空链路标识，不伪造历史证据。

指标仅使用固定 operation/outcome 标签，不以租户、成员、任务或正文作为维度。Span 日志仅导出白名单字段，过滤资源环境属性、错误原文和非批准属性。数据仅写本地结构化日志与受控指标接口；未启用 OTLP 外发、诊断包、用量计费或完整审计 UI。

按 [运维配置](../deploy/operations.md) 开启；使用真实 PG 测试权限与访问留痕，使用 SDK/本地协议替身测试链路；真实客户环境仍需联调。

## 授权任务核对

新增独立 `task.reconcile`，复用配置中的 operator 映射与 Bearer 验证。查询/提交路径、状态分流及证据要求统一见 [任务核对契约](task-runtime.md#核对未知结果)。`audit.read`、`status.read` 均不隐含此权限；未授权前保持 403。

## 审批回调

GET/POST `/api/ops/approval` 复用运维身份和 Bearer 边界；须独立 `task.approve` 且为任务指定审批人，audit/status/reconcile 能力均不隐含审批权。GET 为审阅返回该任务响应正文；POST 不返回正文。完整输入、状态和重复语义见 [审批契约](task-runtime.md#挂起与唤醒审批)。

报价改单入口为 POST /api/ops/quotation；复用指定审批人及 task.approve，要求报价版本与审批版本双重匹配。请求、状态与幂等语义见 [报价契约](../contracts/quotation.md#保存初稿并受控改单)。不接受模型生成的批准指令。

报价初稿下载：GET /api/ops/quotation?task_id=<UUID>，返回当前版本 UTF-8 文本附件；只开放给已有指定审批人，仍需 task.approve 和读取审计，撤权立即拒绝。该路径的 POST 仍为改单重审；没有新增公共下载权限。

通知核对：GET/POST /api/ops/notice 需要 task.reconcile，严格绑定任务和通知 UUID、通知版本及人工证据；见 [通知核对契约](approval-notices.md#核对未知通知)。该入口不产生外部发送，重新排队后仍由 Worker 重验接收权限。

任务核对查询与决定均在取得任务锁后再次检查当前身份映射与 task.reconcile，锁等待期间撤权不得沿用先前授权；真实 PG 回归见 reconcile_lock_integration_test.go。

## 运维存储接口

`operations.Store` 只接收当前授权的任务查询、审批与人工核对领域方法；状态/指标通过 AuthorizeOperation 检查 status.read，不将数据库回调暴露给 HTTP 层。PostgreSQL 与 SQLite 使用同一 HTTP 处理器、Bearer 认证、错误码与 no-store 响应规则。

报价与通知通过独立能力接口提供；SQLite 未实现时相应路由返回 404。知识仍要求显式注入知识仓储。此适配不授予能力、不降低授权；启动入口的选择及未支持配置拒绝以 [SQLite 部署](../deploy/sqlite/README.md#选择运行存储)为准。

真实 SQLite HTTP 测试覆盖任务/统计/审计/核对查询、缺失审批、状态/指标、核对版本冲突、能力撤销 403、映射撤销 401、留痕失败 503 无正文及未支持路由 404。PG 运维和飞书集成回归保留既有审批、报价、通知与核对行为。
