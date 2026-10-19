# 观测与查询运维状态

对齐 R02/R15、A03/A08/A10、V06/V13；仅覆盖单安装、单实例基础观测，不开放员工登录或企业级审计后台。

## 区分接口

| GET 路径 | 授权 | 输出与语义 |
|---|---|---|
| `/api/health` | 无 | 进程存活，保持原契约 |
| `/api/ready` | 无 | 仅 `{ready: boolean}`；就绪 200，否则 503；不包含租户/任务信息 |
| `/api/ops/status` | `status.read` | 连接状态、Worker 状态、存储检查、模型 `not_probed` |
| `/api/ops/metrics` | `status.read` | OTel 累计调用数、结果分类与耗时直方图；重启归零 |
| `/api/ops/audit?task_id=...&after=0&limit=50` | `audit.read` | 单任务状态、原因码、时间、Trace_ID；默认 50，最多 100 条 |

- 判定就绪：Worker 运行、当前租户有效且已应用本制品要求的 schema（database.SchemaVersion，与启动检查同源）；长连接须 Connected，断连/重连即不就绪；Webhook Listening 只代表本地可接收，不代表公网配置已验证。
- 限定就绪：不主动调用计费模型；就绪不承诺端到端业务成功。未配置渠道返回 503；停机和数据库故障不复用旧的就绪缓存。
- 认证查询：仅接收独立 Bearer 运维令牌；配置绑定 `operator / agentos:ops:<AppID> / Subject`，复用 IAM Resolve。Cookie、查询令牌、用户/租户 Header 均不构成身份。
- 授权查询：每次重新检查映射、租户、成员及 `iam.operation_grants` 的应用/能力；配置令牌不自动授予权限。运行角色不可写授权表。
- 隔离范围：审计员可查看获准应用在当前租户内的任务；不是“所有成员默认可读”。其他租户/应用与不存在任务均返回空页，不泄露计数。
- 限定字段：不返回 Prompt、响应正文、外部用户标识、端点或凭证；分页 ID 是任务内序号，不暴露跨租户全局序列。未知/重复参数与越界分页拒绝。
- 记录访问：查询与 `channel.audit_reads` 的访问留痕同事务；留痕失败不返回数据。拒绝请求只记观测结果分类，不记录请求中的身份、令牌或参数。
- 关闭缓存：所有运维和就绪响应 `no-store`；令牌无效/身份撤销 401、能力撤销 403、依赖失败 503，错误正文不含内部详情。

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
