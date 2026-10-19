# 管理 PostgreSQL 基础

## 分离账户

1. 在专用集群以管理员执行 `psql -v ON_ERROR_STOP=1 -f deploy/postgres/roles.sql`；两个 NOLOGIN 能力角色只创建一次，冲突直接报错。
2. 创建两个独立 LOGIN：迁移登录仅加入 `agentos_migrator`，运行登录仅加入 `agentos_runtime`；使用密码管理器或 psql `\password` 配置凭证，不提交密码。
3. 创建数据库，以 `agentos_migrator` 为 owner；撤销 PUBLIC 的数据库权限，仅向这两个角色授予 CONNECT。
4. 设置 `AGENTOS_MIGRATION_DATABASE_URL`，运行 `pnpm db:migrate`，或使用构建产物 `dist/agentos-migrate`（Windows 加 `.exe`）。

迁移显式切换到 `agentos_migrator`，使用 Goose 会话锁、版本表与逐迁移事务；只执行向前迁移，不启动时偷偷建表，也不提供破坏性自动回退。已应用 SQL 不改写，后续变更追加新迁移。当前运行库仅接受 schema 3；00003 新增运维授权、访问留痕和链路字段，00001/00002 保持不变。升级前备份并停止旧版本实例；当前不承诺混合版本滚动升级。

## 使用内部契约

- 建池：`database.Open(ctx, runtimeURL, maxConnections)`；检查角色、所有权和 schema，拒绝超级用户、BYPASSRLS、所有者与迁移成员。
- 授权：按 [身份契约](../../contracts/identity.md) 解析并执行；业务调用不直接使用无身份事务原语。
- 提交：同一回调共享事务；返回错误、panic 或取消均清理；提交错误直接上报，不自动重试。
- 收口：默认控制台仅提供存活检查；配置 [飞书入口](../feishu.md) 后可接收可信事件，不暗设匿名身份或对外开放数据库操作。

## 执行真实测试

设置 `AGENTOS_TEST_ADMIN_DATABASE_URL` 指向**专用测试集群**，先执行角色脚本，再运行 `pnpm test:db`。管理员需可创建数据库和登录角色；测试创建随机命名数据库与角色，在结束时清理，只在造数与清理时使用管理员。

验证：迁移并发/重复/失败回滚、强制 RLS、跨租户/成员读写、连接池复用、撤权、角色越权、事务失败/panic/取消。使用 `integration` 构建标签隔离外部依赖；指定测试命令缺少环境必失败，不 skip。`pnpm check` 不替代此命令，CI 强制执行两者。

本机验证使用项目 `.tools` 下 PostgreSQL 17.11，连接材料仅留忽略目录；本地无 TLS 仅限回环测试，部署时按企业要求配置 TLS。CI 使用同系列 PostgreSQL 服务；未认证其他版本或生产 HA。
