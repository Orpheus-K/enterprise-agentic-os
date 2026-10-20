# 推进持久化任务

对齐 R03/R06、A03/A04/A08、V03/V05/V06/V10/V14。M2 前三切片扩展已有单聊链路；不是完整 Agent 工作流引擎。

## 固定状态与恢复边界

| 状态 | 持久化事实 | 中断后的处理 |
|---|---|---|
| pending | 输入、身份、安装和 Revision | 可认领 |
| claimed | 执行者、租约、世代与步骤版本 | 到期后重新认领，重新授权 |
| model_running | 模型请求意图已提交 | 到期转 unknown；不重复计费尝试 |
| delivery_pending | 模型响应已持久化，发送意图未提交 | 到期后复用响应，重验身份，再进入发送 |
| delivery_running | 发送意图已提交 | 到期转 unknown；保留响应，不盲目补发 |
| waiting_approval | 审批内容、指定审批人、版本与期限；无租约 | 等待认证回调；到期转 expired |
| rejected / expired | 拒绝或过期结果 | 不认领、不自动批准 |
| succeeded / failed / unknown | 终态或待核对结果，租约已清除 | 不再认领；unknown 仅允许受控人工核对 |

恢复扫描每次最多锁定 100 条过期外部意图，使用数据库时间和 SKIP LOCKED；每次 Worker 推进前执行。只有相同租户、应用和 Revision 的任务可被扫描或认领。

## 守住执行权

- 分配：每个 Store 实例生成执行者 UUID；每次认领递增 lease_generation 与 step_version，租约 90 秒。
- 续租：有效步骤推进自动续租；显式 Renew 校验当前世代/版本，不复活过期租约。当前外部请求有 45/15 秒超时，不增加后台心跳协程。
- 拒写：先锁定任务行，再检查数据库当前时间；写入同时匹配安装、Revision、状态、所有者、世代、版本和未过期租约。旧结果返回 ErrLeaseLost，不影响新所有者。
- 原子：状态、版本、响应检查点与审计同事务提交；审计新增世代/步骤版本，唯一索引禁止同一步重复记账。续租不增加业务步骤事件。
- 授权：模型调用前及每次准备发送前重新解析 IAM。恢复不沿用旧授权；恢复后的发送仍使用原任务 UUID 作为稳定操作键。
- 限定：提交意图与外部请求之间仍有崩溃窗口；即使请求实际未发出也保守核对。租约不能撤销已到达外部系统的操作，不承诺 exactly-once。

## 升级与验证

停止旧实例、备份、显式执行 schema 9 迁移，再启动新制品。旧 pending/终态及历史审计保留；无租约的旧 model_running/delivery_running 转 unknown，追加 migration_unowned_attempt 审计。禁止旧制品直接回退或混跑；原始迁移不修改。

真实 PostgreSQL 测试覆盖并发认领、续租/过期拒写、旧世代和旧版本、检查点恢复、撤权、未知外部结果、审计回滚、跨安装/Revision、旧 schema 升级。见 [恢复测试](../services/core/internal/feishu/recovery_integration_test.go) 与 [迁移测试](../services/core/internal/database/migrate_integration_test.go)。使用协议替身验证外部副作用，不连接真实飞书或模型。

后续切片接入 Skills、受控工具及业务审批卡片；当前核对只覆盖已有模型/发送意图，不支持任意状态编辑。

## 核对未知结果

- 授权：沿用配置绑定的运维身份，单独授予 `task.reconcile`；每次查询、提交及重复请求重新检查当前身份和能力。限定租户、应用、Revision；不返回正文、响应或外部身份。
- 检查：`GET /api/ops/recovery?task_id=<UUID>` 返回任务、状态、步骤版本、最后模型/发送意图阶段，并记录读取审计。不存在或跨范围统一 404。
- 提交：`POST /api/ops/recovery`，JSON 仅含 `task_id`、`request_id`、`expected_version`、`decision`、`evidence_id`。三个 ID 为非零 UUID，版本为正数；请求不超过 4096 字节。证据编号指向企业自行保管的核对记录，不上传正文或凭证。
- 分流：`unresolved` 保持 unknown；`not_executed` 将模型意图恢复 pending，将发送意图恢复 delivery_pending；`delivered` 仅将发送意图结为 succeeded。后两种决策必须找到原始阶段；发送恢复还须存在响应检查点。
- 约束：仅 unknown 和匹配版本可推进。状态、步骤事件、操作者、证据编号及核对账本同事务；任何写入失败整体回滚。响应为 `task_id/state/version/stage`。
- 去重：请求编号按租户/应用/任务绑定；同操作者、同请求内容返回原结果，不重复记账；改动内容、过期版本或其他操作者复用编号返回 409。重新授权失败返回 401/403；依赖失败 503；参数错误 400。
- 恢复：提交不调用模型/飞书；Worker 正常认领、递增世代并重验原用户身份。旧执行者仍受版本和租约约束；固定原任务、Revision 与发送操作键。
- 核实：`not_executed` 是获授权操作员的人工证明，须先确认旧执行者已停止，且外部请求未执行、也不会迟到执行。超时或缺少回执不构成证明，应选 unresolved。平台当前不自动验证证据编号，也不查询外部服务；已知模型成功但响应未保存仍保持 unknown，不猜测重算。

验证：真实 PG 的 [核对入口测试](../services/core/internal/operations/reconcile_integration_test.go) 覆盖权限、并发幂等、版本冲突、账本回滚与未知结果；与既有租约恢复测试共同约束外部副作用窗口。

## 挂起与唤醒审批

对齐 R03/R04/R06、A04、T04/T05/T07、V03/V05/V06；仅覆盖 T03 的旧内容批准拒绝部分，不代表改价重算已实现。当前审批对象是已有链路的“原响应发送”，不是完整报价/用章授权。

1. **绑定策略**：可选安装配置 `approval: {member_id: <UUID>, ttl_seconds: 3600}`；必须同时开启 operations，期限为 1—604800 秒。入队固定审批人和期限，关闭配置只影响新任务，不能放行旧任务；原任务 Revision 不变。
2. **持久挂起**：Worker 保存模型响应后、发送意图前，以有效租约/世代/版本创建审批，固定 SHA-256 内容摘要、等待步骤版本、Revision 和数据库有效期；同时写 waiting_approval 与审计并清除租约。失败整体回滚；重启复用响应继续挂起。
3. **受控查看**：`GET /api/ops/approval?task_id=<UUID>`；需配置绑定的当前身份、独立 `task.approve` 能力及匹配指定审批成员。返回审批 UUID、原响应、摘要、Revision、等待版本、期限、状态，读取审计失败不出数据。不要把此响应放入公开日志。
4. **认证回调**：`POST /api/ops/approval`，最多 4096 字节，字段为 `task_id/approval_id/request_id/revision/version/content_hash/decision`；前三项非零 UUID，decision 仅 approve/reject。复用 Bearer 认证，拒绝任意身份头、未知字段和尾随 JSON；审批 UUID 是关联引用，不是授权。
5. **绑定决定**：批准/拒绝须同时匹配审批 UUID、Revision、等待版本和当前响应摘要，且未过期；取得任务行锁后重新核验身份与能力，拒绝锁等待期间撤权。批准将任务置为可认领的 delivery_pending，拒绝置 rejected；决策、恢复检查点和审计同事务。没有 callback 到来时，Worker 每轮最多扫描 100 个到期等待任务并标记 expired，永不自动批准。
6. **处理重复**：相同审批、回调请求 UUID、决定及身份返回原决定，不重复推进；不同请求/决定、过期或版本/内容变化返回 409。重复请求仍重验当前身份与授权；401/403 表示身份/权限失效，404 表示任务或审批不在安装/Revision 范围，400 为无效输入，503 为依赖/写入失败。
7. **恢复交付**：Worker 重新认领递增世代，复用保存响应；提交发送意图前重验原发起人、审批有效期、响应摘要、审批成员/能力及其身份映射。撤权、变更或到期则失败且不发送。回调本身不调用外部系统，租约仍不撤销已发出的请求。

审批决定只批准原版本或拒绝；改价通过独立受控改单契约创建新版本、重新核价和审批，不沿用旧批准。飞书卡片、持久通知及可信回调见 [通知契约](approval-notices.md)，报价见 [报价契约](quotation.md)。暂不提供审批列表 UI、提醒升级或自动重开，不接受未经渠道认证的回调。

真实 PostgreSQL 证据：[审批集成测试](../services/core/internal/feishu/approval_integration_test.go)，覆盖挂起原子性、连接池重开、批准后中断、并发重复回调、身份撤销、租约拒写、篡改与过期。未实测宿主机断电或真实审批卡片。

## 恢复只读工具循环

- 固定意图：schema 10 保存模型单次工具选择、参数、工具版本和操作 UUID；model_running → tool_pending 的检查点与事件同事务。最多四次工具执行，模型不能扩大工具授权范围。
- 分离调用：认领 tool_pending 后先提交 tool_running，再调用网关；单任务顺序执行，禁止并行工具请求。工具不支持或参数错误显式失败。
- 保存结果：网关成功结果与操作状态原子落库；Worker 将工具结果加入有界角色历史，回到 pending 并释放租约，下一轮模型消费历史。
- 恢复中断：tool_pending 可重新认领；过期 tool_running 只按完整意图读取网关已保存结果。结果缺失、started 或未知不再次调用，任务进入 unknown。迟到执行者不能提交检查点。
- 重验授权：工具执行、读取保存结果、下一轮模型和最终交付重验当前身份与工具授权；撤权历史不得送入模型。角色历史不记录凭证，不输出日志。
- 限定核对：现有文本核对入口不能将 tool_running 的未知结果当模型请求重新开始；工具未知结果仅保留待核对，不提供猜测恢复。

证据：[真实数据库工具循环测试](../services/core/internal/feishu/tool_loop_integration_test.go) 覆盖检查点写入故障、连接池重开、结果复用、缺失结果、撤权、预算与旧租约拒写；均使用合成 MCP/模型服务。

审批通知的发送状态以独立 operation_id 进入任务审计，不推进任务步骤；配置、撤权与未知发送结果规则见 [通知契约](approval-notices.md)。

schema 19 增加不可变会话引用检查点；字段、写入阶段、恢复与重验约束见 [知识契约](knowledge.md#持久化会话引用消费者前置)。可选检索在 model_running 意图后调用 Embedding；模型、工具、审批与交付的重验边界见 [会话检索与交付](knowledge.md#会话检索与交付)。

## SQLite 基础文本任务租约

`conversation/sqlite.Inbox` 已提供 Claim、Authorize、Renew、Transition、RecoverExpired 库接口，复用共同 State/ValidTransition。支持基础文本任务与下述审批等待/终态；工具状态尚未接入。

- SQLite 用 BEGIN IMMEDIATE 串行取得写权后再检查数据库时间，不模拟 PostgreSQL 的 SKIP LOCKED 行锁。90 秒租约、所有者/世代/版本/安装过滤、到期拒写以及每轮最多 100 条外部意图转 unknown 与 PG 一致。
- 初始步骤版本为 1，认领递增；状态、响应和审计同事务，步骤审计唯一。迁移 schema 3 重建约束并保留 schema 2 的 pending 任务、正文和历史审计；须先停止旧制品再迁移。
- 消费方在模型与发送前调用 Authorize，恢复重验当前映射；不将认领或续租视为授权。人工核对与单任务审计见下述契约；尚无 SQLite Worker 接线、技能、工具或运维 HTTP；禁止真实渠道启用。
- PG/SQLite 共用 `conversation/contracttest.RunLeases`，验证并发认领、范围隔离、重启重领、过期/旧世代/旧版本拒写、响应复用、撤权、成功终态、未知意图不重放与审计故障回滚。此证据不代表完整轻量运行时验收。

## SQLite 原响应审批

schema 5 保存任务入队时的审批成员和期限，支持 WithApproval、SuspendApproval、InspectApproval、DecideApproval、ExpireApprovals。沿用上文原响应审批契约：挂起清除租约；审批绑定版本、精确正文摘要和数据库期限；读取必须留下访问审计；回调重验当前映射/能力和指定成员；相同请求重复返回原决定，决定响应不含正文。

批准后任务恢复可认领的 delivery_pending；发送意图前再次验证批准、正文、期限和当前审批人映射/能力。拒绝与到期不能认领；到期扫描每次最多 100 条。审批记录、任务状态、步骤审计同事务；任何审计失败回滚。写事务取得锁后执行授权，SQLite 不模拟 PG 的 RLS。

SQLite 的 NotifySubject 配置明确拒绝：审批通知、报价改单、飞书卡片和 HTTP 接线尚未移植；此库层验证不构成真实渠道验收。PG/SQLite 共用 `conversation/contracttest.RunApprovals`，覆盖独立实例并发重复决定、重启与取消策略后保持原审批、拒绝、越版本/摘要拒绝、撤权、内容变更、到期及挂起/读取/决定/到期审计故障；schema 4→5 迁移保留响应、租约、版本和历史事件。

## SQLite 未知结果人工核对

`conversation/sqlite.Inbox.InspectRecovery/Reconcile` 提供与 PostgreSQL 相同的基础文本任务核对库接口（R03/R06、A03/A04/A08、V03/V05/V06）。同一写事务复核当前身份及 task.reconcile 能力，严格限定租户、应用与执行 Revision；查看必须成功写入访问记录才返回状态。

核对必须绑定请求 ID、当前版本和非空证据 ID。unresolved 保留 unknown；not_executed 仅允许模型意图回到 pending，或已有响应的发送意图回到 delivery_pending；delivered 仅允许发送意图转 succeeded。不会从超时推断外部结果。状态、步骤审计与 schema 7 核对账本原子提交；恢复认领后递增租约世代，拒绝旧执行者写入。

同操作员、相同请求和参数重复提交返回首次结果；更换操作员或参数拒绝，重复请求仍复核当前授权。共享 `contracttest.RunReconciliation` 覆盖双连接并发、版本/证据/范围拒绝、撤权、响应复用、错误阶段及账本/步骤审计/访问留痕失败回滚。此接口尚未接入 SQLite HTTP 或 Worker，不包含工具及审批通知的未知结果核对。
