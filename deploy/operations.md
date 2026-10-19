# 配置基础观测与审计查询

1. 显式迁移至 schema 3；迁移新增运维能力授权、审计访问记录及链路字段，保留旧任务、正文和审计。
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

本地 JSON 日志包含 operation、Trace/Span 标识、耗时、状态及获准 task_id；按企业规则限制文件访问与保留期。指标为进程内累计观测，重启清零；不是计费事实或 SLA 证明。不默认联网导出，后续 OTLP 需单独配置目的地与数据边界。

运行 `pnpm check`、`pnpm test:db` 验证构建、权限、撤权、分页、日志白名单、连接状态与持久化关联；CI 另跑 race。实现复用 [OpenTelemetry Go SDK](https://opentelemetry.io/docs/languages/go/)，版本由 go.mod/go.sum 锁定。
