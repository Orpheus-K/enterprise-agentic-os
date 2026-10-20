# 准备 SQLite 轻量数据库

对齐 R19、A03/A10、V15。当前提供显式迁移命令与领域仓储；渠道 Worker、运维 HTTP 和启动配置尚未接入 SQLite，不能据此启用真实渠道。

## 执行离线迁移

停止访问目标文件的全部 AgentOS 进程；升级前保留数据库备份。父目录须已存在，使用操作系统权限限制数据库文件及目录访问；SQLite 没有 PostgreSQL 的数据库角色或 RLS。

使用构建产物（Windows 添加 `.exe`）：

```text
agentos-migrate --sqlite <绝对数据库文件路径>
```

路径含空格时使用当前 Shell 的引号。只接受绝对文件路径，不接受内存数据库或原始 DSN。迁移可创建新文件；已有文件执行前向升级，重复执行保留业务数据，未来 schema 拒绝。迁移不会自动初始化租户、身份映射或能力授权。

执行前清除 `AGENTOS_MIGRATION_DATABASE_URL`；同时指定 SQLite 与 PostgreSQL 目标会在打开数据库前拒绝。无参数仍使用该环境变量执行原 PostgreSQL 迁移。`--version` 不读取连接配置、不打开数据库。SQLite 成功输出 `SQLite schema <版本> ready` 仅说明迁移完成，不能代表服务就绪。

失败时命令返回非零；不要启动服务，先检查错误与目标文件。运行时仓储仅打开已经迁移的现存数据库，不自动建库或升级。SQLite schema 与 PostgreSQL 独立，版本真源为 `internal/sqlitedb.SchemaVersion`。

## 能力边界

任务、原响应审批、人工核对、审计查询与身份库的契约分别见 [任务](../../contracts/task-runtime.md)、[运维](../../contracts/operations.md)、[身份](../../contracts/identity.md)。知识/向量、技能、工具、报价及审批通知尚未支持；运行入口开放前必须实现明确的配置拒绝。完整 SQLite 备份恢复命令与离线安装包仍待完成。
