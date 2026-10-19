# 配置 M1 飞书链路

## 准备可信绑定

1. 按 [数据库指南](postgres/README.md) 升级至 schema 3，使用独立运行账户。
2. 创建飞书自建应用与机器人，订阅 `im.message.receive_v1`，配置单聊接收与机器人回复权限。只在测试租户验证，不开启无授权知识源或工具。
3. 由受控管理员预置 IAM 租户、成员与身份映射；Provider=`feishu`，Issuer=`feishu:<AppID>:<TenantKey>`，Subject 为该应用的用户 OpenID。运行账户无 IAM 写权；当前没有自动注册用户或组织同步。
4. 复制 [示例配置](feishu.example.json)，替换安装绑定与获准模型端点。配置仅存环境变量名称，凭证由运行环境注入；不得将实际密钥写入文件或日志。

配置授予已映射且启用的成员使用该安装的纯文本模型能力，不授予文档检索或业务写入。外部模型需先获准承接消息数据；内网无 TLS 模型须显式设置 `allow_http=true`，不得隐式降级。无鉴权内网服务可将 `api_key_env` 设为空字符串；其余命名环境变量缺失即拒绝启动。

## 启动入口

- 默认：不设置 `AGENTOS_CHANNEL_CONFIG` 时只启动控制台与存活接口。
- 长连接：设置配置中的 `mode=websocket`，将 `AGENTOS_CHANNEL_CONFIG` 指向该文件，构建后执行 `pnpm start`；需能访问飞书云端，无需公网回调入口。
- Webhook：显式改为 `mode=webhook`，注入 Verification Token 与 Encrypt Key；将 HTTPS 反向代理指向 `/api/channels/feishu/events`。需调整 `AGENTOS_HTTP_ADDR` 供受控代理访问。
- 验证：单聊机器人发送测试文本；检查 task_id 与 `channel.tasks`/`channel.audit` 状态。收到 ACK 仅表示持久化接收，模型回复异步送达。

一个进程绑定一个安装与一种接入模式；不要为同一应用同时配置两种模式。首期运行单实例；未验收长连接集群所有权或 HA。模式切换共用持久化去重，已受理任务不重复创建。

## 核对失败与升级

- 保留：pending 可重启继续；running/unknown 需管理员核对模型/飞书结果，不直接改回 pending。尚无自动核对、重放或补发命令。
- 区分：应用不重试模型或不明发送结果；锁定 SDK 对拨号失败/明确令牌失效最多尝试两次，不等于应用任务重新执行。
- 固定：修改安装或模型配置会改变指纹；旧 pending 不转用新配置，升级前排空或保持旧获准配置处理完毕。
- 限制：当前 64 KiB 事件、16 KB 文本输入/输出、45 秒模型请求、15 秒飞书 HTTP 请求；这些是实现边界，不是性能承诺。
- 清理：管理员定义任务正文保留期限并受控清理；审计不存正文。当前无自动保留策略或审计查询 UI。
- 运维：公开就绪与受控审计查询见 [运维配置](operations.md)；模型保持显式 `not_probed`，不将配置存在当作服务可用。
- 验证：执行 `pnpm check` 与 `pnpm test:db`；后者使用真实 PG、签名事件样本和本地 HTTP 协议替身，不调用真实飞书或付费模型。

## 追踪上游

- 复用 [飞书官方 Go SDK](https://github.com/larksuite/oapi-sdk-go/tree/v3.12.2) v3.12.2（MIT），处理长连接、解密、签名算法、令牌与回复协议；额外执行时间窗、绑定、去重和权限复核。
- 对照 [飞书事件接收文档](https://open.feishu.cn/document/server-docs/event-subscription-guide/event-subscription-configure-/encrypt-key-encryption-configuration-case)；长连接与 Webhook 的来源认证不同，不虚构长连接 HTTP 签名。
- 只实现非流式 chat-completions 文本子集；vLLM/Ollama/MindIE 具体版本及流式、工具能力仍待认证。

当前证据与后续工作见 [里程碑笔记](../.agents/NOTES.md)，业务限制以 [链路契约](../contracts/feishu-model.md) 为准。
