# 飞书云文档读取契约

关联 R02/R10/R12、A06/A08、T08/T09、V07/V08。当前实现为 `services/core/internal/feishudocs` 读取、规范化与同步执行器，通过可选 sources 配置接入启动和后台轮询。知识存储与消费者边界继续以 [知识契约](knowledge.md) 为准。

## 身份与权限

- 只接受管理员可信绑定的 AppID、OpenID、TenantKey 和 user access token。Reader 固定 AppID；每次操作通过 user_info 核对 OpenID、TenantKey，调用方不得用请求参数自行指定员工绑定。
- user_info 返回应用范围的 OpenID，不直接证明配置中的 AppID。应用绑定属于可信凭证配置的责任；本库拒绝跨 Reader AppID 的凭证。
- 使用原生 permission/member/auth 的 `docx/view` 检查当前 token 对应用户；应用可读和协作者枚举均不能推导员工有效 ACL。CheckAccess 只提供该用户此刻的访问检查，不读取正文，也不保证未来访问。
- 每次请求显式使用 user token，关闭 SDK token cache，不获取应用 token、不自动重试或换身份。生产地址固定为 `https://open.feishu.cn`，拒绝重定向。

MappedReader 将管理员配置的内部 TenantID、飞书 AppID/TenantKey 与凭证绑定。它使用现有渠道键 `provider=feishu`、`issuer=feishu:<app_id>:<tenant_key>`、`subject=<open_id>` 调用 IAM Resolve，禁止请求选择内部 MemberID。凭证跨安装或本地映射不可用时，不发起来源请求。

读取/权限探测前后均查询当前 IAM；成员停用、链接撤销、查询失败或读取期间改绑到另一成员，均返回空结果和错误。IAM 基础设施错误保持可识别，不伪装为“无权限”。返回的 Access 仅记录本次核验的外部身份和内部成员，不是授权 Token；原生同步发布须使用知识模块 PublishSyncForMembers，在任务/文档锁后及完成前复核全部 IAM 成员，并校验来源同步权限、健康世代及 ACL。此层不注册来源、不生成成员允许列表。

## 完整快照

Read 先验身份和权限，再取文档元数据，将所有分页固定到初始 revision。分页结束后检查块树完整性，再重读元数据、身份和权限；版本或标题变化、身份错配、撤权、缺页、坏游标、重复块、孤立块及超限均返回错误和空快照。

返回文档 ID、revision、标题和完整原始块对象，保留未知字段。读取成功不代表块内容已被支持；后续规范化必须显式支持内容类型或拒绝，不能静默丢弃图片、附件、表格或未知块。

限制：单 HTTP 响应 2 MiB，累计原始块 8 MiB，4096 块、32 页，每页请求 500 块；单 HTTP 请求 15 秒，整个操作 30 秒。游标只在本次固定版本读取中使用；失败后丢弃整个快照，重新开始，不发布部分内容。

## 失败与未实现边界

错误仅暴露分类，不包含 token、原文或供应商错误正文；SDK 日志关闭。输入无效、身份/权限拒绝、版本变化、快照超限分别失败，HTTP/协议/凭证过期等不可确定状态归为来源不可用。来源不可用不能解释为删除或全员撤权。凭证默认 JSON 与格式化输出不包含密钥。

尚未完成：完整块类型支持、原生事件游标、事件订阅与主动撤回，以及真实租户联调。OAuth 生命周期库和来源读取已接线；合成 HTTP 与 PG 测试不证明线上权限配置或真实租户联调通过。

## OAuth v3 传输契约

2026-10-21 核对飞书官方当前文档：换取与刷新使用 `https://accounts.feishu.cn/oauth/v3/token`，表单提交 confidential client 的 client_id/client_secret；授权码换取带 HTTPS redirect_uri 和 PKCE code_verifier。v1/v2 token 接口已列为历史版本。本层不注册公开回调路由；OAuth.Begin 持久保存 state 摘要、PKCE verifier 和五分钟会话后返回授权 URL。[获取 Token 官方契约](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/authentication-management/access-token/get-user-access-token-v3)。

有效期采用响应实际秒数，自请求开始计时；明确校验成功 code、Bearer、Token 格式和实际 scope，不将申请 scope 当已授予 scope。允许授权范围缩小；未获 offline_access 时不假设存在刷新凭证。刷新凭证单次使用，成功返回新凭证后旧凭证失效；未知结果不得重放。传输层没有自动重试；换取/刷新先提交意图，并发竞争者通过 CAS 拒绝。[刷新官方契约](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/authentication-management/access-token/refresh-user-access-token-v3)。

撤销调用 `https://accounts.feishu.cn/oauth/v1/revoke`，成功为 HTTP 200 空响应；已失效 Token 也可返回成功。access_token 与 refresh_token 分别撤销，不能推断撤销刷新凭证会关闭所有历史 access_token。本地先保存 revoking 阻断读取，再分别撤销；远端失败保留凭证供安全重试，不报告撤销成功。[撤销官方契约](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/authentication-management/login-state-management/revoke-token)。

所有生产地址固定、拒绝重定向，单请求 15 秒、响应最多 64 KiB。错误区分输入无效、过期、撤销、明确拒绝与结果未知；仅暴露 HTTP/业务码，不返回供应商正文、请求 URL 或凭证。合成测试覆盖表单协议、范围缩小/扩张、有效期、超限/畸形响应、取消、重定向及脱敏。显式 use_oauth 来源使用账本引用；未授权、缺钥或未知状态拒绝启动，不回退到静态 Token。

### 生命周期与读取

Begin 将随机 state 的摘要、PKCE S256 verifier、批准的 scope 和 HTTPS 回调地址加密保存；Complete 恒定时间核对 state，过期拒绝，持久标记 exchanging 后才调用 Token 端点。[官方授权参数](https://open.feishu.cn/document/authentication-management/access-token/obtain-oauth-code)。调用方必须从可信安装和已认证用户绑定构造 OAuth，不能以回调参数选择租户或员工；库不注册匿名管理接口。

换取/刷新成功后先以 verifying 保存新凭证，再通过 user_info 校验绑定用户和租户，最后进入 active；失败不释放 Token。访问凭证过期时才领取一次刷新意图；刷新凭证已过期则明确要求重新授权。外部结果未知或成功后保存失败时保留非 active 状态，不重放授权码/刷新凭证。并发调用只能有一个 CAS 胜者；检查失败不能改用应用凭证。

Credential 是账本引用。Reader 每次开始读取时解析当前凭证，结束前再查同一代仍为 active 且未过期；读取期间撤销或轮换则丢弃结果。CheckReady 仅查本地授权状态，不触发 HTTP；过期 access 但 refresh 仍有效可启动，后续读取再刷新。Syncer 与原有 IAM/内容 ACL 检查保持独立。

Revoke 先持久阻断，再撤销当前 refresh 与 access；成功后清除秘密并保留 revoked 及审计。对 exchanging/refreshing 的未知结果，可能存在未保存的新 Token；即使已知凭证撤销成功，也保留 RemoteUnknown 并明确返回未知，不允许重新授权覆盖。这种情况需供应商侧核对，再使用下述人工命令；本库不会把证据摘要当成自动撤权证明。只做合成服务验证，不接真实租户。

### 加密账本基础

PG schema 23 增加 channel.oauth_credentials 与独立追加审计。记录按内部租户、应用、飞书租户、OpenID 唯一，并固定初始内部成员；每次读取/写入前后重验当前 IAM。成员/租户停用、链接撤销或改绑不允许读取或覆盖旧凭证。runtime 无 DELETE 权限；审计与凭证版本更新在同一事务，审计失败整体回滚。CAS 只接受上一版本，竞争者明确冲突；外部 HTTP 不在数据库事务内。

OAuthKeys 仅接受外部提供的 32 字节 AES-256 密钥和不可变 key ID；配置/数据库应保存引用，密钥材料需由部署侧安全生成和注入，不由账本导出。密文使用 GCM 随机 nonce，并认证格式域、格式版本、key ID、内部租户、应用、飞书租户、用户及账本版本。缺钥、篡改、跨绑定替换均无明文输出；明文上限 64 KiB。保留旧钥可读历史记录，新写使用当前钥，旧钥只能在全部记录迁移及备份保留期处理后退役。加密并不阻止整库回滚；恢复仍须独立核对外部撤权事实和密钥版本。

OAuth.Begin/Complete/CheckReady/Credential/Revoke 已消费账本；Inspect 只返回版本、状态、过期时间与未知标记。schema 24 为追加审计增加 evidence_hash，人工核对与清除未知标记同事务；要求当前版本、已本地 revoked、RemoteUnknown=true 和合法 SHA-256。核对不会恢复原授权，仅允许之后重新授权。旧版本、重复核对、缺证据或审计失败均拒绝。

## 单次同步执行

Syncer.RunOnce 领取固定来源/配置的任务，使用同步者 user token 读取完整文档并规范化。配置摘要包含来源、同步者/候选收件人的绑定身份、Embedding 配置及空间，不包含凭证值。未领取任务返回 worked=false；失败显式返回错误。

首次计算先提交 Embedding 意图，再逐块调用固定 Embedder，保存完整结果后才继续。已保存结果接管时复用；较新调度请求在配置不变时也保留已知检查点，原文不变不重新计费。原文变化才清除已知结果重新计算；pending 未知请求仍不能覆盖或重放。

Embedding 之后重读同步者的完整文档，要求正文版本/规范化结果未变，再逐个用候选收件人的 user token 核对有效权限和当前 IAM。只有成功证明的成员进入 allow；明确拒绝或 IAM 已失效的成员不进入，基础设施/协议错误则整次失败。候选名单不是权限授予，应用可读也不等于员工可读。

证明有效期为最后一轮核验开始起一分钟，不随人数延长；超时不发布。有效租约通过 ReportSyncHealth 在来源行锁下分配健康版本，再使用返回 epoch 构造 ACL，并用 PublishSyncForMembers 原子发布。ACL revision 使用持久调度的 request_revision，内容 revision 保留原生值。

失败时以独立的五秒清理期限尝试报告来源 unavailable，使旧结果不可检索；陈旧租约或已撤销同步身份无权修改健康，清理失败与原错误一并返回，不吞错。健康观察复用已有世代规则；恢复不自动放行旧世代 ACL。一次租约最长五分钟，不自动续租；批次未及时保存的未知调用继续阻断。

真实 PG＋本地合成飞书/Embedding HTTP 链路覆盖：首次发布、同内容复用、新原生版本重算、员工拒绝、Embedding 后同步者撤权、未知请求不重放、已知结果接管复用以及失败阻断旧已发布结果。测试不访问真实租户。

## 启动与轮询

bootstrap.Config.sources 为可选数组，未配置不加载来源凭证、不创建同步器。配置限制最多 32 个不同来源，各有 1—1000 个不重复原生文档 ID、同步者、候选收件人、Embedding 空间及 5—30 秒轮询间隔。必须配置 embedding 与 operations，以保留未知调用核对路径；密钥只通过环境变量引用读取。

Open 在连接数据库前读取来源凭证；随后核对来源启用/同步成员授权和空间模型/版本/维度。它不调飞书或模型、不调度任务。Run 才为每个来源启动串行轮询循环，与聊天 Worker 共用取消生命周期。每轮开始用 PollSync 在数据库分配下一次请求版本，完成态才递增，同配置在途/过期/未知任务保留原版本与围栏；无工作时等待下次轮询，不忙等。

配置摘要包含显式文档清单。不同配置的活跃租约或未知调用拒绝覆盖；已知过期、未领取或完成任务可转换到新配置。更改配置须先停旧实例，当前不支持同一来源的不同配置并行滚动；Token 值轮换不改变身份摘要，但需要重启加载。文档被移除后新配置不会领取其旧摘要任务；旧健康/ACL 最多按原证明期限到期，不声称即时删除或撤回。

单来源失败会记录只含 source_id 的脱敏错误，并在间隔后继续调度；不通过报错文本输出来源正文、凭证或 SQL。失败不会停止其他来源或聊天 Worker。同步吞吐不足时授权证明按时过期并拒绝读取，不延长有效期来维持可用性。聊天 readiness 不等于所有来源健康；可通过[同步任务查询](knowledge.md#同步任务查询)查看指定任务，完整来源健康面板仍待提供。部署字段见 [飞书部署](../deploy/feishu.md#云文档来源轮询)。

## 基础文本规范化

Normalize 接受页面、文本段落、1—9 级标题、无序/有序列表、待办的 text_run 内容及下述矩形表格；输出 knowledge.Content，但不调用存储、不生成 ACL。未知块、未知内联元素或内容字段拒绝整份文档。当前代码、公式、图片、提及、附件和同步引用块均未支持；包含这些内容的文档不能发布。链接及删除线文本也拒绝，避免把缺失链接目标或已删除的表述当作普通断言。

内容按 children 指定的深度优先顺序输出，不依赖分页返回顺序。每个非空源块生成一个切片，ID 使用源 block_id，原文锚点为 `feishu-docx:<document_id>:<revision>:<block_id>`，是版本绑定的不透明定位符，不是可点击链接。连续 text_run 直接拼接，保留内部换行和字词，不把样式边界变成空格；仅清除块首尾空白。页面标题文本也作为源块保留。

无序项加 `•`；有序项保留显式正整数 sequence（上限十亿），`auto` 仅可接续有已知编号的连续同级有序项。缺失 sequence、无前序可推导的 auto 或溢出均拒绝，不猜测历史编号。待办必须有明确 done 布尔值，输出 `[已完成]` 或 `[未完成]`。列表及待办子块的正文包含上级列表文本，避免子项脱离否定条件或父级状态；锚点仍绑定当前子块。上下文计入正文大小上限，超限整份失败；空列表项拒绝，不生成假内容。

限制沿用知识存储：标题 512 UTF-8 字节，单块正文 16000 字节，最多 256 个非空切片；超限失败，不截断正文、不静默少取切片。空文档不生成假正文。规范化前重新检查块树、快照大小及 UTF-8，任何失败只返回空 Content。更复杂内容与大块拆分需后续实现并测试后再扩大支持范围。

## 矩形表格

支持 Table(31) 与 TableCell(32) 的未合并矩形表。行列数必须为正且乘积等于 cells 数，cells 顺序必须与 children 一致；每个 cell 必须是该表的直接子块。要求完整 merge_info 且所有 row_span/col_span 均为 1；缺失、合并或未知布局不猜测，整份文档失败。嵌套表格暂不支持。

整表作为一个切片，ID 与锚点指向表块；单元格及其子块不单独成为检索候选，以保留价格、产品及行列之间的关系。正文使用 JSON 对象，包含 header_row、header_column 和按行排列的字符串二维数组 rows；不臆造表头，空单元格保留为空字符串，换行、引号和竖线仍属于各自单元格。单元格内复用已支持的文本/列表/待办解析，父级列表条件也保留。表格序列化后的正文与外层条件合计不得超过 16000 字节，否则拒绝，不拆出孤立的价格或单元格。

## 协议依据

使用仓库固定的官方 Go SDK `github.com/larksuite/oapi-sdk-go/v3 v3.12.2`：

- [检查应用或用户是否具有文档权限](https://open.feishu.cn/document/server-docs/docs/permission/permission-member/auth)。
- [获取文档基本信息](https://open.feishu.cn/document/server-docs/docs/docs/docx-v1/document/get)。
- [获取文档所有块](https://open.feishu.cn/document/server-docs/docs/docs/docx-v1/document/list)。

测试通过本地 HTTP 服务覆盖协议交互、权限变化、身份变化、固定版本分页、完整性、资源上限、取消、错误脱敏及重定向拒绝；不向真实租户发送请求。

## 来源至会话交付验证

`TestNativeSyncPipeline` 将真实 Syncer 发布的记录接入 conversation.Store/Worker，查询嵌入使用实际 Embedder HTTP，校验来源正文进入模型上下文、引用持久化及来源页脚交付。覆盖已保存向量接管后的正常交付，以及模型返回前再次轮询确认收件人撤权或原文读取失败时，旧引用阻断派生回答、任务终止且不自动重放。该验证不直接改写 ACL 来模拟原生撤权。

原生读取、嵌入端点、聊天输出及发送均使用合成替身，数据库为真实 PostgreSQL；不覆盖真实用户授权流程、线上权限传播时延、模型质量或飞书实际发送。撤权阻断从来源同步确认或既定证明过期后生效，不宣称平台即时获知尚未观察到的原生权限变化。
