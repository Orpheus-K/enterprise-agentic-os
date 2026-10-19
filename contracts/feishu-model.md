# 接通可信消息与模型

- 对齐：R01/R02/R13/R15，A03/A04/A07/A08，V06/V11/V12 的本切片范围。
- 绑定：管理员配置内部 TenantID＋飞书 AppID＋TenantKey；只接自建应用、用户单聊文本。内部租户不取自请求 Header 或消息正文。
- 认证：长连接只消费官方 SDK 的凭证绑定通道；Webhook 要求 Encrypt Key、Verification Token、原始密文签名、±300 秒窗口。加密 URL challenge 只验证配置，不创建任务。
- 映射：复用 IAM，Provider=`feishu`，Issuer=`feishu:<AppID>:<TenantKey>`，Subject=`open_id`。调用与交付前再次 Resolve；映射变更、成员/租户停用均阻止下一外部动作。
- 接收：持久化任务和审计后才确认；按租户＋应用＋event_id/message_id 去重；相同消息不同内容拒绝。数据库失败不返回成功。
- 隔离：IAM 保持成员可见性；channel 表仅供可信安装 Worker，按租户强制 RLS，不作为面向员工的查询 API。缺少上下文不可见。
- 调用：只发当前消息文本，不附凭证、历史群聊或企业文档。固定获准端点、模型及输出预算；禁止重定向、隐式备用模型、流式、工具调用和不完整结果。
- 交付：以 text 回复原单聊消息，绑定稳定任务 UUID；不生成富文本动作、附件、审批或公章。拒绝群聊，直到接收范围校验完成。
- 追溯：同事务写入状态与审计；任务 ID 与 Trace 链接关联接入、调用与交付。日志仅存获准元数据；查询权限与脱敏见 [运维契约](operations.md)。任务表保存必要输入/成功模型结果，部署前明确保留与清理策略。

顺序：`pending → model_running → delivery_running → succeeded`；撤权进入 `failed`；外部错误保守进入 `unknown`。事务内无网络等待。

中断后的 running、unknown 不自动重领或重发，需核对外部结果；pending 可在重启后继续。配置指纹绑定安装与模型，配置变化不将旧任务转给新端点。M2 再完善租约、核对及恢复工具；当前不承诺崩溃自动恢复或 exactly-once。

验收：[渠道测试](../services/core/internal/feishu/adapter_test.go)、[真实数据库链路](../services/core/internal/feishu/chain_integration_test.go)、[模型协议测试](../services/core/internal/model/client_test.go)。这些是协议替身与真实 PG 的证据，不等于客户飞书或模型服务认证。
