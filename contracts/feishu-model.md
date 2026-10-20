# 接通可信消息与模型

- 对齐：R01/R02/R13/R15，A03/A04/A07/A08，V06/V11/V12 的本切片范围。
- 绑定：管理员配置内部 TenantID＋飞书 AppID＋TenantKey；只接自建应用、用户单聊文本。内部租户不取自请求 Header 或消息正文。
- 认证：长连接只消费官方 SDK 的凭证绑定通道；Webhook 要求 Encrypt Key、Verification Token、原始密文签名、±300 秒窗口。加密 URL challenge 只验证配置，不创建任务。
- 映射：复用 IAM，Provider=`feishu`，Issuer=`feishu:<AppID>:<TenantKey>`，Subject=`open_id`。调用与交付前再次 Resolve；映射变更、成员/租户停用均阻止下一外部动作。
- 接收：持久化任务和审计后才确认；按租户＋应用＋event_id/message_id 去重；相同消息不同内容拒绝。数据库失败不返回成功。
- 隔离：IAM 保持成员可见性；channel 表仅供可信安装 Worker，按租户强制 RLS，不作为面向员工的查询 API。缺少上下文不可见。
- 调用：默认只发当前消息文本，不附凭证、历史群聊或企业文档。显式开启 retrieval 后按 [知识契约](knowledge.md#会话检索与交付) 加载当前获权证据，保存引用并在后续消费重验。固定获准端点、模型及输出预算；纯文本协议禁止重定向、隐式备用模型、流式、工具调用和不完整结果；受控工具协议另见 [MCP 契约](mcp-gateway.md)。
- 交付：以 text 回复原单聊消息，绑定稳定任务 UUID；不生成富文本动作、附件、审批或公章。拒绝群聊，直到接收范围校验完成。
- 追溯：同事务写入状态与审计；任务 ID 与 Trace 链接关联接入、调用与交付。日志仅存获准元数据；查询权限与脱敏见 [运维契约](operations.md)。任务表保存必要输入/成功模型结果，部署前明确保留与清理策略。

顺序：`pending → claimed → model_running → delivery_pending → delivery_running → succeeded`；撤权进入 `failed`，外部结果不明进入 `unknown`。事务内无网络等待；模型结果在 delivery_pending 持久化后才允许准备发送。

租约过期时，claimed 和 delivery_pending 可在相同安装/Revision 下重新认领；后者复用已保存响应并重新授权，不再次调用模型。model_running/delivery_running 过期转 unknown，不能自动重发。租约、世代、步骤版本与迁移规则以 [持久化任务契约](task-runtime.md) 为准。

本切片只扩展现有单聊任务，不包含审批、Skills、工具调用、自动结果核对或 HA 渠道所有权；不承诺 exactly-once。

验收：[渠道测试](../services/core/internal/feishu/adapter_test.go)、[真实数据库链路](../services/core/internal/feishu/chain_integration_test.go)、[模型协议测试](../services/core/internal/model/client_test.go)。这些是协议替身与真实 PG 的证据，不等于客户飞书或模型服务认证。

## SQLite 任务接收基础

`conversation/sqlite.Inbox` 实现既有渠道 `Inbox.Enqueue` 窄接口；以 sqlitedb 打开的文件连接为前提，先取得写事务再解析当前身份，任务和 pending 审计同事务提交。按租户、应用和消息/事件键去重；重复投递不更新原执行版本或正文，身份改绑不能接管旧任务。审计失败回滚任务，不确认接收。

PostgreSQL 和 SQLite 共用 `conversation/contracttest.RunInbox`：覆盖并发投递、实例重建、事件与消息交叉冲突、租户/应用隔离、停用/改绑和审计失败。SQLite 测试使用独立文件连接，迁移从身份 schema 1 升到 2 并保留原数据。

SQLite 基础认领、文本任务状态推进及原响应审批见 [任务契约](task-runtime.md#sqlite-基础文本任务租约)。渠道、Worker、运维审批/核对与查询已接入，启动选择和未支持能力拒绝以 [SQLite 部署](../deploy/sqlite/README.md#选择运行存储)为准。真实飞书租户联调仍待完成。
