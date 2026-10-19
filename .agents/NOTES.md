# 接续项目迭代

## 定位当前里程碑

- 进展：2026-10-19，工程 M1、M2 已退出；M3 第二切片实现受控知识入口与健康世代，已通过本机验证，自动目标继续，整体蓝图未完成。
- 真源：[研发路线](../enterprise-agentic-os-architecture/07-架构决策与研发路线.md)、[M2 验收](../deploy/m2-acceptance.md)、[知识管道契约](../contracts/knowledge.md)。知识契约区分已实现存储与未接入来源/检索。

## 当前 M3 健康与入口切片

- 接入入口：复用 operations Bearer 绑定身份，提供 knowledge content/acl/health/read 四个 POST；严格 JSON、8 MiB 上限、拒绝请求身份与查询参数，读取按当前 ACL，不因同步人绕过。
- 固定健康：schema 16 保存健康版本/epoch/期限；过期、失联与行政停用使原授权失效，恢复须新世代健康＋新版本 ACL，不重写内容。连续有效心跳和重复事件不改变世代。
- 阻断停用：管理员 enabled=true→false 与健康失效同事务；重新启用不放行旧 ACL。旧 schema 15 来源升级后默认缺健康证明，必须重新确认。
- 验证进展：全套 pnpm test:db 与 pnpm check 均退出 0（m3-health-full-db.log、m3-health-check.log）；停用触发器更新后另补跑迁移/健康/HTTP 定向通过（m3-health-final-targeted.log）。git diff --check 通过；测试库/角色残留 0，PG 已停止。
- 远端证据：fc2d6c6（schema 16）的 [CI 39237901826](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39237901826) completed/success，日志 .tools/m3-health-ci-success.log。
- 向量驱动：PG 17.11＋pgvector 0.8.7 探针在 WSL `17/agentos-m3`（55440）通过（m3-vector-probe.log）；CI 改用摘要固定的 pgvector 镜像并执行同一探针。驱动认证不等于检索实现。
- 限定范围：健康信号由获权同步者报告；原生来源游标拉取/丢失检测、组织权限解析与检索/模型接入仍待实现。当前不宣称完成原生同步或 RAG。

## 固定 M2 证据

- 复验审批：2026-10-19，当前 fc2d6c6/schema 16 已具备本次要求，无重复实现。真实 PG 执行 `go test -tags=integration -count=1 -run 'TestApproval' -v ./internal/feishu` 及 `-run 'TestCardApprovalDatabaseBoundary|TestQuoteApprovalCardAmendRestartDelivery'` 均通过；覆盖版本/摘要绑定、并发重复回调、撤权（含锁等待）、挂起原子性、重启与租约恢复、改单重新审批和单次交付。日志 .tools/m2-approval-current-db.log、m2-approval-current-card-db.log；仅合成替身，无外部发送。此项为当前提交定向复验，不替代全量 CI/race。
- 验收制品：8dad1fb00790ce9f2dfdb190c991263680745dbd；[Check 39234206040](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39234206040) completed/success。工程检查、真实 PG、普通/integration race、2 项浏览器测试全部通过，日志 .tools/m2-ci-success.log。
- 保留失败：0296799 的 [首轮 Check](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39233587067) 在通知并发测试失败；任务候选快照过时，无待发通知却报 no rows。修复为锁后空候选返回无任务，其他错误仍失败；本机连续 20 次真实 PG 回归通过，再以新提交完整 CI 验证。
- 修复撤权：任务核对读/写在锁等待后重验当前身份及 task.reconcile；反例先失败再修复，日志 m2-recovery-lock-before/after.log。通知核对复用同一检查。
- 保留本机：m2-card-send-full-db.log、m2-card-send-check.log、m2-notice-race-regression.log、m2-notice-race-go.log 位于 .tools；测试库/角色残留均为 0，PG 已停止。无真实业务数据或外部发送。

## 保留实现边界

- 固定执行：schema 14；租约世代/版本、持久检查点、未知操作证据核对、审批挂起/唤醒、通知发送与核对均落库；外部未知结果不盲重试，不承诺 exactly-once。
- 固定资产：Skills 以 manifest＋SKILL.md 摘要固定，动态摘要路由、授权交集、在途版本并存；缺包拒绝，旧包须保留。示例摘要按 Git blob 验证。
- 限制工具：官方 MCP SDK 固定目标，只读工具、凭证代办、契约校验、审计与有界循环；复用已保存结果，不重放未知外部请求。
- 绑定报价：整数金额核算、七五折审批、改单重算、旧卡片拒绝、差异显示、可信渠道回调和 V2 内部文本初稿单次交付已合成贯通。真实飞书卡片现场认证、正式 PDF 与用章仍待后续验收。
- 保护卡片：获权审批人可见绑定版本全文，plain_text 且关闭转发；超限拒绝而非截断。已发送内容无法因撤权收回，点击与交付重新授权。
- 保留 M1：[现场验收](../deploy/m1-acceptance.md) 与历史 CI 独立留存；M1 联调库 schema 3 未迁移，不与当前制品混跑。

## 接续 M3 下一切片

1. 核对当前提交远端 CI，不借上一 schema 15 制品通过结论；失败按证据修复。
2. 推进授权候选检索与引用消费者：按 A05 核实本机/CI 的 pgvector 能力，建立当前内容/ACL/健康世代下的数据库授权检索，禁用未经认证组合；不以应用内过滤替代出库前授权。
3. 补齐候选标题/正文/计数隔离、版本失配、检索后撤权和交付复核真实 PG 测试；原生来源权限同步与游标恢复继续保留为待完成项，不把人工 HTTP 输入称为自动连接器。

## 保留交付环境

- 私有依赖：.tools/pg-test-admin.url、DPAPI 凭证与现场配置全部忽略，禁止回显；公开源码特征扫描 191 个文件无凭证候选。
- 本机 Go：GO_BINARY=$PWD/.tools/go/bin/go.exe；GOTMPDIR=$PWD/.tools/go-tmp，不改全局设置。Windows 无 gcc，race 由上述 Linux CI 证明。
- 独立 PG：.tools/pgdata-m1，127.0.0.1:55439；测试自行建库建角色并清理。升级先停旧实例、备份并显式迁移，已发布迁移不可重写。
- 当前 M3 制品使用 schema 16；00001—00015 保持不变。M2 CI 为历史已验收制品证据，不能代替当前 M3 远端验证。
