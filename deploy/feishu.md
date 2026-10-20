# 配置 M1 飞书链路

## 准备可信绑定

1. 按 [数据库指南](postgres/README.md) 升级至当前制品要求的 schema，使用独立运行账户。
2. 创建飞书自建应用与机器人，订阅 `im.message.receive_v1`，配置单聊接收与机器人回复权限。只在测试租户验证，不开启无授权知识源或工具。
3. 由受控管理员预置 IAM 租户、成员与身份映射；Provider=`feishu`，Issuer=`feishu:<AppID>:<TenantKey>`，Subject 为该应用的用户 OpenID。运行账户无 IAM 写权；当前没有自动注册用户或组织同步。
4. 复制 [示例配置](feishu.example.json)，替换安装绑定与获准模型端点。配置仅存环境变量名称，凭证由运行环境注入；不得将实际密钥写入文件或日志。

聊天配置授予已映射且启用的成员使用该安装的纯文本模型能力，不替代知识来源 ACL 或业务写入授权。外部模型需先获准承接消息及拟发送的检索资料；内网无 TLS 模型须显式设置 `allow_http=true`，不得隐式降级。无鉴权内网服务可将 `api_key_env` 设为空字符串；其余命名环境变量缺失即拒绝启动。

可选增加顶层 `embedding` 对象，为后续知识消费者准备独立客户端：

```json
{
  "embedding": {
    "endpoint": "https://approved-model.example/v1/embeddings",
    "name": "replace-with-approved-embedding-model",
    "revision": "replace-with-approved-revision",
    "dimensions": 1536,
    "api_key_env": "AGENTOS_EMBEDDING_API_KEY",
    "allow_http": false
  }
}
```

这是需合并到安装配置的片段；维度、模型及版本须按批准空间填写。单独配置 embedding 不启用会话检索。显式启用时另加：

```json
{
  "retrieval": {
    "space": {
      "space_id": "11111111-1111-4111-8111-111111111111",
      "model": "replace-with-approved-embedding-model",
      "model_revision": "replace-with-approved-revision"
    },
    "limit": 5
  }
}
```

空间 ID 必须替换为管理员已预置且启用的当前安装空间；还须通过受控同步入口发布内容、ACL、健康及向量。启动校验数据库空间，不发模型探测请求。开启、关闭或更换 retrieval 会改变执行指纹，升级前排空旧任务或保留原获准配置处理完毕。当前单聊、审批、恢复及来源展示限制见 [会话检索契约](../contracts/knowledge.md#会话检索与交付)。

## 启动入口

- 默认：不设置 `AGENTOS_CHANNEL_CONFIG` 时只启动控制台与存活接口。
- 长连接：设置配置中的 `mode=websocket`，将 `AGENTOS_CHANNEL_CONFIG` 指向该文件，构建后执行 `pnpm start`；需能访问飞书云端，无需公网回调入口。
- Webhook：显式改为 `mode=webhook`，注入 Verification Token 与 Encrypt Key；将 HTTPS 反向代理指向 `/api/channels/feishu/events`。需调整 `AGENTOS_HTTP_ADDR` 供受控代理访问。
- 验证：单聊机器人发送测试文本；检查 task_id 与 `channel.tasks`/`channel.audit` 状态。收到 ACK 仅表示持久化接收，模型回复异步送达。

一个进程绑定一个安装与一种接入模式；不要为同一应用同时配置两种模式。首期运行单实例；未验收长连接集群所有权或 HA。模式切换共用持久化去重，已受理任务不重复创建。

## 核对失败与升级

- 恢复：pending 可继续，claimed/delivery_pending 租约到期后可安全认领；已记录外部意图的 running 到期转 unknown。按 [任务契约](../contracts/task-runtime.md) 核对，禁止手工改回 pending；尚无自动核对或补发命令。
- 区分：应用不重试模型或不明发送结果；锁定 SDK 对拨号失败/明确令牌失效最多尝试两次，不等于应用任务重新执行。
- 固定：修改安装或模型配置会改变指纹；旧 pending 不转用新配置，升级前排空或保持旧获准配置处理完毕。
- 限制：当前 64 KiB 事件、16 KB 文本输入/输出、45 秒模型请求、15 秒飞书 HTTP 请求；这些是实现边界，不是性能承诺。
- 清理：管理员定义任务正文保留期限并受控清理；审计不存正文。当前无自动保留策略或审计查询 UI。
- 运维：公开就绪与受控审计查询见 [运维配置](operations.md)；模型保持显式 `not_probed`，不将配置存在当作服务可用。
- 验证：执行 `pnpm check` 与 `pnpm test:db`；后者使用真实 PG、签名事件样本和本地 HTTP 协议替身，不调用真实飞书或付费模型。

## 追踪上游

- 复用 [飞书官方 Go SDK](https://github.com/larksuite/oapi-sdk-go/tree/v3.12.2) v3.12.2（MIT），处理长连接、解密、签名算法、令牌与回复协议；额外执行时间窗、绑定、去重和权限复核。
- 对照 [飞书事件接收文档](https://open.feishu.cn/document/server-docs/event-subscription-guide/event-subscription-configure-/encrypt-key-encryption-configuration-case)；长连接与 Webhook 的来源认证不同，不虚构长连接 HTTP 签名。
- 实现非流式 chat-completions 文本及单工具调用子集；vLLM/Ollama/MindIE 具体版本及流式、工具能力仍待认证。

当前证据与后续工作见 [里程碑笔记](../.agents/NOTES.md)，业务限制以 [链路契约](../contracts/feishu-model.md) 为准。

## 启用固定技能

配置可选 `skills`，完整字段见 [技能契约](../contracts/skills.md#绑定运行任务)。selected 指定一个精确 Ref，assets 保留所有在途任务需要的版本和绝对文件路径。管理员向对应 tenant/member/app 授予 iam.asset_grants 的 skill 精确版本权限；运行账户不得写授权表。

先在合成文本环境验证再切换默认版本。删除旧文件不等于任务迁移；移除配置也不能让旧技能任务改走无技能模型。配置 tools 后支持有界只读 MCP 工具循环，详见 MCP 契约；未配置网关的工具技能显式失败。

## 配置只读工具循环

安装配置可增加 `tools.specs`（网关 Spec 列表）与 `tools.credentials`（凭证绑定列表），必须同时配置 skills。凭证项使用 `principal: {TenantID: <UUID>, MemberID: <UUID>}`、`endpoint`、`token_env`；只保存环境变量名，不保存密钥。Spec 字段、版本计算及风险约束以 [MCP 契约](../contracts/mcp-gateway.md) 为准。管理员先固定 Spec 摘要，绑定技能依赖并授予精确版本；缺项启动或调用失败，不借用其他用户凭证。

模型必须支持标准单 function tool_call 与 assistant/tool 角色；每轮仅允许一个调用，每任务最多四次。协议测试仅覆盖本机合成服务，真实供应商仍需单独认证。

技能自动匹配：将 `skills.selected` 替换为 `skills.routes: [{name, revision}, ...]`，同时在 assets 保留候选与在途旧版本。两种选择模式不可同时配置；授权、候选固定与失败规则见 [技能路由契约](../contracts/skills.md#渐进路由与恢复)。

审批通知：可在 approval 中设置 notify_subject，为当前安装下映射到指定审批成员的飞书 OpenID。通知使用机器人已有发送权限，发送绑定版本的审批初稿卡片；没有有效身份映射或 task.approve 则取消。升级至 schema 16 及以上后按 [通知契约](../contracts/approval-notices.md) 验证，不向旧任务追溯补发。

审批回调：在飞书应用订阅 card.action.trigger；使用已配置的可信长连接或加密验签 Webhook。审批成员须在当前安装 issuer 下具有有效飞书身份映射及 task.approve。发布与平台现场交互需单独验证；本机合成测试不证明真实送达。
## 云文档来源轮询

在已有渠道配置中可选增加 sources，并配置匹配的 embedding 与 operations。先由管理员预置 IAM、knowledge.sources 的同步成员和 enabled，以及 embedding_spaces；启动检查不会代建或授权。以下为配置片段，UUID 与环境变量值须替换为实际安装信息：

```json
{
  "sources": [{
    "source_id": "11111111-1111-4111-8111-111111111111",
    "documents": ["doxcDocumentID"],
    "owner": {"open_id": "ou_sync", "access_token_env": "FEISHU_SOURCE_USER_TOKEN"},
    "recipients": [{"open_id": "ou_employee", "access_token_env": "FEISHU_EMPLOYEE_USER_TOKEN"}],
    "space": {"space_id": "22222222-2222-4222-8222-222222222222", "model": "approved-embedding", "model_revision": "v1"},
    "poll_seconds": 10
  }]
}
```

静态 Token 必须是各绑定用户自己的 user access token；静态模式不自动刷新，过期使来源不可用，需要轮换环境变量并重启。候选收件人不是授权名单，执行器逐个验证原生有效权限。配置变更先停旧实例；未知调用须经受控核对，不能通过改配置或清表绕过。轮询与证明有效期、类型限制及失败语义以 [来源契约](../contracts/feishu-docs.md) 为准。

OAuth 来源可将单个 owner/recipient 改为 `{"open_id":"ou_sync","use_oauth":true}`，删除该用户的 access_token_env，并在顶层配置 `"oauth":{"active_key_id":"v1","key_envs":{"v1":"FEISHU_OAUTH_KEY_V1"}}`。环境引用值为安全生成的 32 字节密钥的标准 Base64；应用密钥仍来自 app_secret_env。每个 key ID 必须固定对应同一密钥材料，轮换新增 ID 并保留旧钥用于历史密文和备份。缺钥、未完成授权、未知状态或完全过期时启动失败；SQLite 不支持该来源配置。当前生命周期库与来源消费已测试，受控授权管理命令尚待实现，不通过手工造密文或清表配置授权。

同步任务查询：向 `/api/ops/knowledge/sync/inspect` POST `{"source_id":"配置中的来源 UUID","document_id":"配置中的原生文档 ID"}`，使用既有运维 Bearer。运维绑定成员须具有该来源同步权限及 `knowledge.reconcile` 授权。读取返回的 request_revision、generation、config_hash 与输入摘要，结合供应商证据再调用核对入口；pending 或租约过期本身不能作为 not_sent 的证据。接口契约见[同步任务查询](../contracts/knowledge.md#同步任务查询)。

可选 WASM 折扣复核：管理员审查制品后，在顶层设置 `"quotation_wasm_sha256":"b1899480373d1df63263bc8988fd36fe164fa9cec28c76c5dd436d53a9b49ec7"`。只支持该内置模块，未知摘要拒绝启动。省略不启用；开关会改变运行版本，部署前须按原配置处理或保留在途任务及审批，不能靠改数据库版本字段强行接管。详见[沙箱契约](../contracts/sandbox.md#固定折扣规则模块wasm)。
