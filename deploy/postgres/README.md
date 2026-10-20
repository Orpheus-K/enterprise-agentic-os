# 管理 PostgreSQL 基础

## 分离账户

1. 在专用集群以管理员执行 `psql -v ON_ERROR_STOP=1 -f deploy/postgres/roles.sql`；两个 NOLOGIN 能力角色只创建一次，冲突直接报错。
2. 创建两个独立 LOGIN：迁移登录仅加入 `agentos_migrator`，运行登录仅加入 `agentos_runtime`；使用密码管理器或 psql `\password` 配置凭证，不提交密码。
3. 创建数据库，以 `agentos_migrator` 为 owner；撤销 PUBLIC 的数据库权限，仅向这两个角色授予 CONNECT。
4. 以管理员在该数据库执行 [vector.sql](vector.sql)（见下文“准备向量驱动”）；schema 17 缺少此扩展时迁移失败并停在 schema 16。
5. 设置 `AGENTOS_MIGRATION_DATABASE_URL`，运行 `pnpm db:migrate`，或使用构建产物 `dist/agentos-migrate`（Windows 加 `.exe`）。

迁移显式切换到 `agentos_migrator`，使用 Goose 会话锁、版本表与逐迁移事务；只执行向前迁移，不启动时偷偷建表，也不提供破坏性自动回退。已应用 SQL 不改写，后续变更追加新迁移。当前运行库仅接受 schema 19；00004 新增租约、世代和步骤版本，将旧 running 任务转为 unknown 并追加审计，不重发外部操作。00005 新增受控核对账本与 task.reconcile 能力，不自动授权；00006 新增任务审批策略、审批记录及挂起/拒绝/过期状态；历史任务不追溯附加审批。00007 新增精确版本的技能/工具只读授权表，详见 [技能契约](../../contracts/skills.md)。00008 固定任务技能名称/Revision；历史任务不追溯绑定技能。00009 增加 MCP 操作摘要账本，见 [MCP 契约](../../contracts/mcp-gateway.md)。00010 保存工具意图、历史和成功结果检查点；00011 固定任务路由候选；00012 保存报价版本与改单记录；00013 保存审批通知及独立操作审计；00014 保存通知版本和核对账本；00015 增加知识来源、内容/ACL、切片与审计；00016 增加来源健康世代与停用失效；00017 要求管理员预装 pgvector 0.8.7，增加 Embedding 空间、切片向量集与检索审计，见 [知识契约](../../contracts/knowledge.md)；00018 为运维访问留痕增加 scope（单任务/清单/统计），允许非单任务读取的 task_id 为空，并增加任务时间窗索引，见 [运维契约](../../contracts/operations.md#查询任务清单与基础统计)；00019 增加会话引用检查点，见 [知识契约](../../contracts/knowledge.md#持久化会话引用消费者前置)；00001—00018 保持不变。升级前备份并停止旧版本实例；当前不承诺混合版本滚动升级。

## 使用内部契约

- 建池：`database.Open(ctx, runtimeURL, maxConnections)`；检查角色、所有权和 schema，拒绝超级用户、BYPASSRLS、所有者与迁移成员。
- 授权：按 [身份契约](../../contracts/identity.md) 解析并执行；业务调用不直接使用无身份事务原语。
- 提交：同一回调共享事务；返回错误、panic 或取消均清理；提交错误直接上报，不自动重试。
- 收口：默认控制台仅提供存活检查；配置 [飞书入口](../feishu.md) 后可接收可信事件，不暗设匿名身份或对外开放数据库操作。

## 执行真实测试

设置 `AGENTOS_TEST_ADMIN_DATABASE_URL` 指向**专用测试集群**，先执行角色脚本，再运行 `pnpm test:db`。集群须已安装 pgvector 0.8.7 软件包；管理员需可创建数据库、登录角色及扩展，测试库按部署顺序由管理员安装扩展；测试创建随机命名数据库与角色，在结束时清理，只在造数与清理时使用管理员。

验证：迁移并发/重复/失败回滚、强制 RLS、跨租户/成员读写、连接池复用、撤权、角色越权、事务失败/panic/取消。使用 `integration` 构建标签隔离外部依赖；指定测试命令缺少环境必失败，不 skip。`pnpm check` 不替代此命令，CI 强制执行两者。

本机 schema 17—18 验证使用下文 WSL `agentos-m3` 集群（PostgreSQL 17.11＋pgvector），连接材料仅留忽略目录；本地无 TLS 仅限回环测试，部署时按企业要求配置 TLS。CI 使用同系列 PostgreSQL 服务；未认证其他版本或生产 HA。

## 准备向量驱动

- 固定组合：PostgreSQL 17.11＋pgvector 0.8.7。CI 使用官方 `pgvector/pgvector:0.8.7-pg17-trixie`，摘要固定在 [工作流](../../.github/workflows/check.yml)，不跟随浮动标签。
- 安装扩展：在目标数据库以管理员执行 [vector.sql](vector.sql)；扩展归管理员，迁移/运行账户不获得超级用户权限。已有不同扩展版本显式失败，由管理员备份并另行升级，不自动降级。
- 验证驱动：专用数据库 owner 为 agentos_migrator；安装扩展后执行 `psql -v ON_ERROR_STOP=1 -f deploy/postgres/verify-vector.sql`。使用运行角色、强制 RLS 与精确余弦排序，验证更近的未授权行不挤掉授权结果、计数不泄露、其他租户不可见；探针事务回滚，不改产品表。
- 保留边界：探针仅认证驱动基本行为，不证明业务 ACL、Embedding 质量、ANN 召回、全文融合或生产容量。schema 17 起向量扩展为必需前置；缺失或版本不同时迁移显式失败。
- 分离本机：Windows 旧 PG 保留 M1 历史库；M3 向量验证使用 WSL Ubuntu 的独立 `17/agentos-m3` 集群、回环端口 55440。PGDG 包固定 `postgresql-17=17.11-1.pgdg24.04+2` 与 `postgresql-17-pgvector=0.8.7-1.pgdg24.04+1`；连接材料只保存在忽略目录，不复用历史库。

依据：[pgvector 安装与精确检索](https://github.com/pgvector/pgvector/tree/v0.8.7)、[PostgreSQL Ubuntu 软件源](https://www.postgresql.org/download/linux/ubuntu/)。

## 备份与恢复

对齐 A10、V15，使用 [backup-restore.sh](backup-restore.sh)（bash；需 `psql`、`pg_dump`、`pg_restore`、`sha256sum`）：

1. 停止全部 AgentOS 实例，设置 `AGENTOS_BACKUP_DATABASE_URL`（应用库的超级用户 URL，不写入脚本或日志），运行 `backup-restore.sh backup <目录>`。产物为自定义格式 dump、`.manifest`（schema 版本、pgvector 版本、强制 RLS 表数、表所有者、关键表行数）和 `.sha256`；备份期间数据变化则删除产物并失败。
2. 在目标集群按上文“分离账户”步骤 1—3 准备空库（角色与登录由管理员重建，**不包含在 dump 中**；不执行 vector.sql，dump 自带扩展，须已安装同版本 pgvector 软件包）。
3. 设置 `AGENTOS_RESTORE_DATABASE_URL` 指向该空库，运行 `backup-restore.sh restore <dump>`：先校验校验和，目标非空（含已有扩展）则拒绝，恢复遇错即停，最后重算事实并与 manifest 逐项比对，不一致即失败。
4. 恢复后仍须由部署方核对：外部系统在备份点之后已发生的动作（飞书消息、业务写入）不会随数据库回滚，未知结果任务按 [核对流程](../operations.md) 处理；不得假设已成功的副作用可重放。

演练证据（本机 WSL `17/agentos-m3`，schema 18，含各表族及一条 pgvector 向量的合成数据）：备份→校验和损坏被拒→删除源库→恢复至新空库→manifest 一致→重复恢复至非空库被拒→运行登录恢复后仍无建表权限且无上下文时 RLS 返回 0 行；篡改 manifest 行数的恢复失败。日志 `.tools/m3-backup-drill.log`，残留 0。

未覆盖：文件/原件目录与密钥材料的联合备份（当前无文件驱动与密钥库实现）、在线（不停机）一致备份、跨 PG 大版本恢复、RTO/RPO 数值、SQLite 备份。这些不得写成已验收。
