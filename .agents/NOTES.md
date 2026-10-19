# 接续项目迭代

## 定位当前里程碑

- 进展：M1 联调与退出验收，日期 2026-10-19；第四切片已完成，M1 尚未退出。
- 对齐：[研发路线](../enterprise-agentic-os-architecture/07-架构决策与研发路线.md) R02/R15、A03/A08/A10；覆盖 V06 权限隔离与 V13 默认脱敏部分，不代表完整诊断体系或整个 M1 完成。
- 限定：自建应用、固定安装租户、单实例、非流式文本；真实飞书/模型尚未联调；就绪仅表示本地接收条件，模型明确 `not_probed`。

## 保留成果与证据

- 建立：`apps/web`、共享 UI/API/config 包、`contracts`、Go 核心、Python 包与部署入口；使用 [根 README](../README.md) 获取命令。
- 固定：pnpm/uv 锁文件、严格 TypeScript、Biome、依赖边界、Go fmt/vet/test、Ruff/mypy/pytest；工具版本以清单为准。
- 保留：首切片浏览器 2 项、共享包反向依赖拒绝证据；本切片无前端改动，未重跑浏览器。
- 新增：pgx 连接池、Goose 显式迁移命令与会话锁；分离迁移/运行角色，拒绝特权运行连接；事务统一提交/回滚，无自动重试。
- 约束：[身份契约](../contracts/identity.md) 精确绑定租户/提供者/签发方/Subject；每请求解析、每事务复核；IAM 强制 RLS，连接池事务局部上下文不串租户。
- 新增：`internal/feishu` 使用官方 SDK v3.12.2；长连接与加密 Webhook 共享绑定/接收；签名、Token、时间窗、租户/应用不匹配均拒绝。
- 保留：任务/审计原子提交、持久化去重、模型与交付前身份重验；网络在事务外。
- 新增：`internal/model` 固定获准端点/模型/输出上限；禁止重定向与隐式回退；原单聊 text 回复绑定任务 UUID。
- 新增：schema 3 保存运维能力、查询留痕与 Trace/Span 标识；IAM/查询记录强制 RLS，已有迁移保持不变。
- 观测：`internal/telemetry` 复用 OTel Go SDK v1.47.0；入口→异步任务 Trace 链接、调用/失败计数与耗时直方图；仅本地白名单输出，不自动外发。
- 就绪：`/api/ready` 只返回布尔状态；Worker、当前租户/schema、连接状态联合判定；断连/重连、存储失败或未配置渠道均不就绪。
- 授权：运维 Bearer 凭证绑定现有 IAM 成员，另授应用级 `audit.read`/`status.read`；每请求重验，不接受身份 Header/Cookie；默认不开查询接口。
- 查询：仅返回任务内分页状态元数据；隔离租户/应用，隐藏正文、身份与全局序列；查询留痕失败不返回结果，见 [运维契约](../contracts/operations.md)。
- 通过：`pnpm check`，含 Go/TS/Python 检查、双 Go 制品、默认未就绪/运维禁用二进制冒烟与 Python 制品。
- 通过：`pnpm test:db`，真实 PostgreSQL 17.11 下 18 个集成测试函数；涵盖旧链路回归、schema 1/2→3 数据保留、审计分页/隔离、映射/成员/租户/能力撤销、禁止自授权限、查询留痕原子性及持久化 Trace 关联。
- 通过：就绪生命周期/并发/探测中断连、运维认证/参数边界、Trace 父子与链接、指标与日志脱敏；前端未改，浏览器未重跑。
- 修正：OTel 默认合并环境资源属性导致脱敏反例失败，已在出口白名单投影；分页测试逐页独立解码，避免复用旧游标；修正后整套通过。
- 证明：缺少数据库测试环境直接失败；测试残留数据库/登录角色均为 0；本次专用数据库已停止。
- 验证：本轮 `pnpm check`、`pnpm test:db`、`pnpm test:e2e`（2 项）通过；专用 PG 已停止。日志与联调清单见 [M1 验收记录](../deploy/m1-acceptance.md)。
- 通过：已创建[公有仓库](https://github.com/Orpheus-K/enterprise-agentic-os)，代码提交 `089cec5` 的[首轮 CI](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39198385216) 全部成功：全量检查、真实 PG、普通/integration 两组 Linux race、浏览器测试。日志 `.tools/m1-ci.log` 保留本地；Windows 缺 gcc 不影响该 Linux 证据。
- 保留：[产品](../enterprise-agentic-os-docs/README.md)、[架构](../enterprise-agentic-os-architecture/README.md)与 [规范](../AGENTS.md) 为设计真源；Git 已提交并推送至公有仓库。

## 明确未决与下一步

- 环境：Windows、Node 22.18、pnpm 10.15、Python 3.13.13、uv 0.11.8；Go 1.27.2 已校验后放入 `.tools/go`，执行时显式设置 `GO_BINARY`。
- 取舍：jsdom 固定兼容 Node 22.18 的 26.1；浏览器测试使用 18473，避开本机已有服务；不终止外部进程或静默换端口。
- 未决：生产环境、候选驱动与业务场景尚未验收；Python 当前只有可打包 CLI 骨架。
- 复现：按 [数据库指南](../deploy/postgres/README.md) 设置独立凭证；本机测试集群位于 `.tools/pgdata-m1`，端口 55439，连接文件 `.tools/pg-test-admin.url` 已忽略，勿输出或提交。
- 启动：本机复跑执行 `.tools/postgresql/pgsql/bin/pg_ctl.exe -D .tools/pgdata-m1 -l .tools/pg-test.log -o '-h 127.0.0.1 -p 55439' -w start`；设置 `GO_BINARY` 与从忽略文件读取的 `AGENTOS_TEST_ADMIN_DATABASE_URL`，再运行 `pnpm test:db`；结束后 `pg_ctl -D .tools/pgdata-m1 -m fast -w stop`。
- 部署：按 [飞书指南](../deploy/feishu.md) 预置 IAM 映射与获准模型配置；`AGENTOS_CHANNEL_CONFIG` 缺省仅启动控制台；凭证由环境注入，不输出或提交。
- 运维：按 [运维配置](../deploy/operations.md) 可选开启独立凭证与能力；令牌轮换需重启，数据库撤权影响后续请求；当前无 OIDC、审计 UI、OTLP 或诊断导出。
- 保留：running/unknown 需人工核对，禁止直接重发；pending 重启续处理，旧配置任务不转用新模型；详见 [链路契约](../contracts/feishu-model.md)。
- 接续：按验收记录注入真实测试租户/模型配置，完成单聊、故障与权限验证后判定 M1。M2 再完善租约、结果核对与自动恢复；组织同步、群聊接收范围与 Python RPC 尚未实现。
