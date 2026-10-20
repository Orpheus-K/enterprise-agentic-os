# 绑定身份与租户

- 输入：`ExternalIdentity{TenantID, Provider, Issuer, Subject}`；租户来自可信安装配置，身份来自已验签渠道/OIDC 适配器。
- 区分：结构校验与映射查询不等于认证；本切片不提供登录、Header 身份注入或公开身份 API。
- 输出：`Principal{TenantID, MemberID}`；成员 ID 仅在租户内唯一，不以员工名或外部 Subject 直接授权。
- 解析：每个请求重新 `Resolve`；精确匹配租户＋提供者＋签发方＋Subject；不存在、停用或撤销均拒绝。
- 执行：PostgreSQL 调用 `Repository.Within`；事务内重新检查租户和成员有效性，再运行回调；不得跨请求缓存 Principal 作为认证凭据。
- 隔离：PostgreSQL 通过事务局部变量传入身份；缺少上下文不返回记录；运行账户只读 IAM 表，不能自行修改成员或权限。
- 限定：授权在短事务入口复核；撤权影响后续事务，不宣称撤回已提交动作；禁止在事务中等待网络或人工审批。
- 信任：应用数据库凭证与受信代码可设置上下文；RLS 防漏过滤，不防已控制数据库连接的攻击者伪造上下文。
- 接入：飞书来源认证与固定安装绑定见 [链路契约](feishu-model.md)；任务调用和交付重验映射，不缓存旧 Principal 作为授权。
- 运维：独立令牌映射 `operator` 身份，应用级能力另行授予；配置凭证不自动获得审计权限，见 [运维契约](operations.md)。
- 待建：OIDC、其他 IM 验签、会话生命周期、组织角色与对象 ACL；当前成员有效性不等于任意业务操作权限。

关联：产品 R02；架构 A03/A08、V02/V06；实现位于 `services/core/internal/identity`。

## SQLite 身份基础切片

- `identity/sqlite.Repository.Resolve` 与 PostgreSQL 使用同一行为测试：完整键精确匹配、同成员 ID 跨租户、停用、撤链、改绑与取消；单条连接查询同时检查链接、成员和租户，失败返回零值 Principal。
- `sqlitedb` 使用固定版本 modernc SQLite 与 Goose 独立迁移序列（版本以代码 SchemaVersion 为准）；只接受绝对文件路径，运行打开不创建文件或自动迁移。迁移须停应用后显式调用；所有连接启用外键，等待锁上限 5 秒。写事务先用 BEGIN IMMEDIATE 取得写锁，再读取身份；ResolveIn 不设置任何隐式授权上下文。
- SQLite 无 PostgreSQL 账户权限/RLS。数据库文件及连接属于可信进程；后续业务仓储必须在自有事务中显式限定租户并重验授权，不提供假定有 RLS 的原始 SQL 回调。
- 本片仅提供库与真实文件测试，未开放配置或运行入口；任务、审批、审计及启动能力检查完成前不声称轻量部署可用。首版范围与知识/向量禁用要求以 [A03 的 M3 分片范围](../enterprise-agentic-os-architecture/07-架构决策与研发路线.md) 为准。
