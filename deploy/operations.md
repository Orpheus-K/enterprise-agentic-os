# 配置基础观测与审计查询

Windows 注册准备见[服务脚本](windows/README.md)，本轮仅验证脚本，不执行实际注册。

## 生成脱敏诊断包

在受控管理机使用 Node.js 22 执行 `node scripts/diagnostics.mjs <配置绝对路径> <JSONL日志绝对路径> <新的输出JSON绝对路径>`。只生成一个本地 JSON 诊断包，不联网、不读取环境变量/数据库、不修改源文件；输出已存在即失败。Windows 输出目录 ACL 由安装方限制。

诊断包只含固定配置开关和获准 operation/status 的次数；省略配置值、密钥引用、租户/成员/任务/Trace 标识、URL、错误文本和原始日志。未知日志字段/操作不输出，计入 omitted；JSON/UTF-8 损坏、配置超过 1 MiB、日志超过 16 MiB/10 万行/单行 64 KiB 时整体失败。它是显式输入文件的摘要，不证明健康、日志完整性或授权；需要更细排错时在现场受控查阅原始记录，不把原始文件加入此包。测试覆盖嵌套密钥、字段注入、坏输入及失败无输出。

## 启用受控运维接口

1. 按 [PostgreSQL 部署](postgres/README.md) 当前版本完成显式迁移；保留旧任务、正文和审计，不使用历史最低版本启动新制品。
2. 在飞书渠道配置中按需增加下面的 `operations` 对象；省略时不注册 `/api/ops/`，默认不开启查询。

```json
"operations": {
  "token_env": "AGENTOS_OPERATIONS_TOKEN",
  "subject": "installation-auditor"
}
```

3. 通过密钥管理器生成至少 32 字节随机凭证，注入指定环境变量；不将凭证写入配置或浏览器。当前单安装只支持一个运维凭证，轮换需重启；立即撤权使用数据库停用映射/成员/能力。
4. 由受控管理员建立 IAM 映射：当前 TenantID，Provider=`operator`，Issuer=`agentos:ops:<AppID>`，Subject 与配置一致，绑定一个现有成员。
5. 向该成员显式授予 `iam.operation_grants`：TenantID、MemberID、AppID、`audit.read` 和/或 `status.read`，按需分别授权；`enabled=false` 撤销。仅有效映射而无能力仍会拒绝。
6. 通过受控 HTTPS 代理访问，携带 `Authorization: Bearer <注入的凭证>`；仅限可信运维工具，不放在 URL、访问日志、源码或前端存储中。默认仍监听回环地址。

接口、分页、错误码与就绪定义以 [运维契约](../contracts/operations.md) 为准。`/api/health` 不代替 `/api/ready`；数据库不可用时运维身份也无法确认，查询返回 503，不降级为离线放行。

知识审计/统计另向当前运维成员授予 `knowledge.audit.read`，不复用 audit.read；启用知识仓储后使用 POST `/api/ops/knowledge/audit` 和 `/api/ops/knowledge/stats`。输入、窗口和计数口径仅见 [知识审计契约](../contracts/knowledge.md#知识审计读取与统计)。此权限不能读取知识正文或修改来源。

本地 JSON 日志包含 operation、Trace/Span 标识、耗时、状态及获准 task_id；按企业规则限制文件访问与保留期。指标为进程内累计观测，重启清零；不是计费事实或 SLA 证明。不默认联网导出，后续 OTLP 需单独配置目的地与数据边界。

运行 `pnpm check`、`pnpm test:db` 验证构建、权限、撤权、分页、日志白名单、连接状态与持久化关联；CI 另跑 race。实现复用 [OpenTelemetry Go SDK](https://opentelemetry.io/docs/languages/go/)，版本由 go.mod/go.sum 锁定。

## 执行人工核对

1. 停止旧执行者，使用供应商记录核实结果；不能确定就保持 unknown。
2. 由数据库管理员向指定安装的运维成员显式授予 `task.reconcile`；运行账户无授权表写权限。复用上述 `iam.operation_grants` 授权方式，替换 capability，不批量授权。
3. 使用现有本机 Bearer 请求 `GET /api/ops/recovery?task_id=<UUID>`，取得版本与阶段。
4. 保存外部核对记录，生成证据 UUID 和请求 UUID；按 [核对契约](../contracts/task-runtime.md#核对未知结果) 提交 POST。超时后使用同请求内容重发；409 时重新查询，不自动刷新版本重试。
5. 检查核对结果与审计。恢复提交只放行检查点，后续 Worker 重新授权；原身份撤销会阻止外发。

00005 新增核对账本和独立能力，不自动赋权；当前无恢复控制台或外部证据自动查询器。

## 启用审批测试

1. 停止旧制品、备份并迁移到 [当前支持版本](postgres/README.md)；新迁移不自动授予权限。为指定审批成员配置 operator 身份映射，并单独授予当前应用 `task.approve`。
2. 在渠道配置中增加 `"approval": {"member_id": "<审批成员 UUID>", "ttl_seconds": 3600}`；同时配置 operations。当前回调身份由安装配置固定，须映射到此成员。
3. 发送合成文本，按受控任务日志中的 task_id 查询 `/api/ops/approval`，审阅返回的响应、版本与有效期。
4. 根据 [审批契约](../contracts/task-runtime.md#挂起与唤醒审批) 提交 approve/reject；保存 request_id 以便网络中断后按原内容重发。不得自动改版本重试 409，也不得把令牌或审批正文写入日志。
5. 验证任务挂起时无发送，批准后原响应仅发送一次；拒绝、过期或权限撤销不发送。停机恢复沿用持久化策略；移除配置不能绕过旧任务审批。

此运维接口用于认证的人工审批，不是飞书开放回调地址。飞书卡片通知与回调接线见 [渠道部署](feishu.md)，真实租户体验需另行验收。
