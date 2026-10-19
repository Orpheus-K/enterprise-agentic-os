# 持久发送审批通知

对齐 R04/R06/R15、A04/A08、V03/V05/V06。当前完成飞书单聊交互审批卡片、持久发送意图、可信回调与未知结果受控核对。平台现场交互能力仍需部署验收。

## 固定收件人与版本

- 配置：在已有 approval 中显式设置 notify_subject（飞书 OpenID，ou_ 前缀）。默认不发送；入队保存该值，配置变化不修改在途任务。收件人的飞书身份须在当前安装 issuer 下映射为指定审批成员。
- 入库：schema 13 增加 approval_recipient 与 approval_notices。审批挂起、改单生成新审批时，同事务创建通知；每个审批 ID 只有一条通知。旧任务不追溯补发。
- 绑定：通知固定租户、任务、审批 ID、审批成员、收件人、issuer、Revision、等待步骤版本与内容摘要。通知不拥有批准权限，不改变审批期限或任务步骤版本。

## 发送与恢复

1. 锁定任务：按当前安装/Revision 选取待发或过期发送记录，使用任务行锁，与审批、改单保持相同锁顺序。
2. 重验范围：只发送仍在等待且未过期的原审批版本；旧审批/已决审批取消。按当前身份映射、成员状态和 task.approve 重验接收者，撤权取消。
3. 提交意图：pending → sending，保存执行者和 30 秒期限，与独立通知审计同事务；网络请求在事务外。缺少通知驱动显式失败，保留 pending。
4. 调用渠道：官方飞书 SDK Create Message，receive_id_type=open_id，以审批 ID 为稳定 uuid；发送 interactive 卡片至已授权审批成员：包含绑定版本的完整初稿、摘要、有效期与批准/驳回按钮；不包含凭证、模型 Prompt 或自动授权链接。
5. 确认结果：成功响应且含 message_id 才记 sent；错误或无法确认记 unknown。提交状态仍检查执行者和期限；已过期结果不能覆盖新状态。
6. 恢复中断：pending 可由其他 Worker 处理；sending 超时进入 unknown，不重发。发送成功但状态/审计提交失败也保留待核对，不依赖渠道有限期幂等保证无限期安全。

## 审计与边界

- 通知事件以 state=approval_notice、reason=发送状态、operation_id=审批/通知 UUID 进入原任务审计；不占任务步骤版本。沿用 audit.read 的受控查询，不公开收件人或业务正文。
- 飞书 SDK 单元测试验证固定接收类型、目标、稳定 UUID、确认响应与单次调用；真实 PG 测试覆盖事务回滚、并发、重启、改版取消、撤权、未知结果与迟到拒写。
- 证据：[通知数据库测试](../services/core/internal/feishu/notice_integration_test.go)、[渠道契约测试](../services/core/internal/feishu/sender_test.go)。均为本机合成测试，未向真实飞书用户发送。
- 通知可能在审批刚结束后到达；它不携带可绕过授权的按钮或凭证，查看/决定仍核验当前版本。未实现撤回已发送消息及自动提醒；已发送正文不能因撤权从客户端收回，点击和后续交付重新授权。

unknown 必须通过下述核对流程处理，不直接改数据库或盲目重试。

## 核对未知通知

- 授权入口：GET/POST /api/ops/notice，复用 Bearer 身份与独立 task.reconcile 能力；取得任务锁后再次检查身份映射和能力。不能以 task.approve 代替运维核对权限。
- 受控读取：GET 仅接受 task_id 与 notice_id，返回 task_id/notice_id/state/version；绑定租户、应用和模型 Revision。读取审计失败不返回结果，不暴露收件人或业务正文。
- 提交证据：POST 只接受 task_id、notice_id、request_id、expected_version、decision、evidence_id，最多 4096 字节。UUID 非零、版本为正，拒绝未知字段、重复查询参数和尾随 JSON。
- 明确决定：unresolved 保持 unknown；delivered 记 sent；not_executed 重新置 pending。只有 unknown 且通知版本匹配可推进。核对不发送消息，Worker 后续重验审批是否仍有效以及接收者权限；旧审批会被取消。
- 原子记账：schema 14 保存证据引用、决定、操作者、请求摘要及结果，通知版本与审计同事务。通知版本独立于任务步骤；每次发送/结束/核对递增，旧发送者不能完成新轮次。
- 幂等重验：同请求与身份返回原结果；改动请求或更换身份复用编号返回 409。重复请求也检查当前权限，撤权立即拒绝。GET 查询当前状态，幂等 POST 返回原决定结果。
- 限定证明：证据编号指向企业保管的核对记录；平台不自动查询飞书或验证证据真实性。not_executed 须确认旧请求未执行且不会再到达，旧发送者已停止；仅超时不构成该证明。版本屏障保护本地状态，不能撤回已发出的网络请求。

[真实 PG 核对测试](../services/core/internal/feishu/notice_reconcile_integration_test.go) 覆盖并发决定去重、证据记账、撤权/跨安装、版本冲突、审计回滚、HTTP 认证解析，以及旧结果与新发送同时存在时的版本拒写。

## 验证卡片回调

- 复用入口：加密 Webhook 沿用签名、时间窗与 Token 验证；长连接注册官方 SDK card.action.trigger 回调。配置绑定同一 Store，不新增匿名审批接口。
- 绑定身份：要求 schema 2.0、应用、组织、操作者组织、OpenID、button 与 im_message 上下文齐备；身份来自可信事件操作者，不来自按钮 value。
- 绑定决定：value 仅承载 task_id、approval_id、revision、version、content_hash、decision；拒绝未知字段及外部指定的非零 request_id。按安装 issuer 和 event_id 派生稳定请求编号，交由既有 DecideApproval 事务校验当前权限、版本、有效期和幂等。
- 限定反馈：成功仅回执“审批决定已记录”，不回传业务正文、Token 或上游错误；失败显式拒绝，不伪装已批准。修改报价继续走受控改单入口，不从未定义的卡片表单猜测内容。
- 留存证据：[回调边界测试](../services/core/internal/feishu/approval_callback_test.go)、[真实数据库测试](../services/core/internal/feishu/approval_callback_integration_test.go)。卡片生成/发送与回调已在合成环境贯通；平台现场交互认证未完成。
## 核对卡片内容

- 复用组件：使用官方 SDK 卡片模型；正文为 plain_text，关闭转发，不将业务文本解析为 Markdown、链接或按钮。
- 固定内容：取任务锁并检查审批接收者后读取同一审批正文，校验摘要；报价通过已有 RenderText 校验并展示，不由模型重写。改单加载前一报价版本，核对链式摘要并展示单价/合计差异（差异单位为分）。
- 明确决定：按钮固定任务、审批、Revision、步骤版本、摘要和决定；二次确认提示当前版本。改单仍使用受控入口，产生新审批与新卡片；旧卡片决定拒绝。
- 限制尺寸：序列化卡片上限 24000 字节；超过则不调用飞书，不截断后保留批准按钮。发送状态保守进入 unknown，经受控证据核对处理，不能假称平台收到。
- 验证闭环：[卡片测试](../services/core/internal/feishu/approval_card_test.go)、[报价卡片数据库贯穿测试](../services/core/internal/feishu/approval_card_integration_test.go) 覆盖 SDK 出站、七五折草稿、改单差异、旧卡片拒绝、重启通知、可信回调去重及对应 V2 初稿单次交付。