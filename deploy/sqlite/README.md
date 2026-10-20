# 准备 SQLite 轻量数据库

对齐 R01/R02/R03/R04/R06/R19、A03/A04/A08/A10、V03/V05/V06/V11/V15。SQLite 已接入显式启动配置、共享渠道 Worker 和运维 HTTP；首版仅支持基础文本任务、原响应审批、身份、审计、人工核对与统计。真实飞书租户联调仍待完成。

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

任务、原响应审批、人工核对、审计查询与身份库的契约分别见 [任务](../../contracts/task-runtime.md)、[运维](../../contracts/operations.md)、[身份](../../contracts/identity.md)。知识/向量、技能、工具、报价及审批通知尚未支持。完整 SQLite 备份恢复命令与离线安装包仍待完成。

## 选择运行存储

在既有[渠道配置](../feishu.example.json)中设置 `storage_driver: "sqlite"` 和 `sqlite_path`（绝对文件路径），删除 `database_url_env`；其他渠道认证、模型与运维配置不变。省略 `storage_driver` 或显式设置 `"postgres"` 保持 PostgreSQL 路径；未知驱动、双目标配置及 PostgreSQL 配置携带 SQLite 路径均拒绝。

例如渠道配置的存储字段为：

```json
{
  "storage_driver": "sqlite",
  "sqlite_path": "E:/agentos/runtime.db"
}
```

以上仅为字段示例，不是完整配置。将完整配置文件路径放入 `AGENTOS_CHANNEL_CONFIG` 后运行 `agentos`。启动仅打开现存且 schema 匹配的文件；缺库、空库、旧版或未来 schema 均失败，不自动迁移。管理员须事先按身份契约配置租户、成员、渠道映射、运维身份映射及应用能力授权；迁移成功不代表已配置身份。

SQLite 配置中出现 `embedding`、`retrieval`、非空 `sources`、`skills`、`tools`、非空 `quotation_wasm_sha256` 或 `approval.notify_subject` 时，在读取凭证或打开数据库之前拒绝。原响应审批可用，但必须配置运维入口，由有权操作员查看和决定；不发送审批通知。知识、报价和通知运维路由不提供。

就绪探针实时核对当前租户启用状态和 SQLite schema，并结合渠道连接及 Worker 状态；数据库关闭后不可就绪。进程正常关闭和启动失败均释放已打开的数据库资源。运行验证使用真实 SQLite 文件、合成加密渠道事件、模拟模型和发送器，覆盖审批前后重启、单次交付与撤权阻断；不代表真实租户现场验收或完整 M3 退出。
