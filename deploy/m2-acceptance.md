# 验收 M2：持久执行与受控工具

依据 [M2 路线](../enterprise-agentic-os-architecture/07-架构决策与研发路线.md) 与 [验证矩阵](../enterprise-agentic-os-architecture/08-验证矩阵与参考依据.md)。本页归集验收证据，不替代测试结果。2026-10-19：工程 M2 可以退出。验收制品为 8dad1fb00790ce9f2dfdb190c991263680745dbd；本机与远端合成测试通过，范围为下表定义的持久执行、审批、只读工具与内部报价初稿，不等同企业生产就绪。

## 对齐退出条件

| 条件 | 实现与反例证据 | 边界 |
|---|---|---|
| V03 租约与恢复 | [恢复测试](../services/core/internal/feishu/recovery_integration_test.go)：竞争认领、过期重取、世代屏障、旧执行者迟到、安装/Revision 隔离 | 多连接真实 PG；不宣称生产 HA 容量 |
| V04 未知操作 | [核对测试](../services/core/internal/operations/reconcile_integration_test.go)：证据、幂等、撤权、原子审计；[通知核对](../services/core/internal/feishu/notice_reconcile_integration_test.go)：旧发送结果不能完成新轮次 | 证据引用由企业保管；不能自动证明远端未执行 |
| V05 审批恢复 | [审批测试](../services/core/internal/feishu/approval_integration_test.go)、[可信回调](../services/core/internal/feishu/approval_callback_integration_test.go)：绑定版本、重启、重复回调、撤权、拒绝 | 操作身份与渠道身份均须当前获权 |
| V10 MCP 治理 | [工具网关](../services/core/internal/toolgateway/gateway_integration_test.go)：固定目标、凭证隔离、输入/输出契约、撤权、重复意图与未知结果 | 当前只读工具；拒绝未授权写操作及任意代理 |
| 版本恢复基础 | [技能版本](../services/core/internal/feishu/skill_integration_test.go)、[工具检查点](../services/core/internal/feishu/tool_loop_integration_test.go)：旧任务固定 Revision、重开连接、复用已存结果 | 保留旧技能资产；缺包显式失败，不降级聊天 |
| 报价贯穿 | [消息→MCP→核价](../services/core/internal/feishu/quote_chain_integration_test.go)、[卡片→改单→交付](../services/core/internal/feishu/approval_card_integration_test.go)：七五折、改价重算、旧卡片拒绝、V2 单次交付 | 本机模型/渠道替身；内部文本初稿，不含正式 PDF 或印章 |

## 执行验收

1. 固定制品：记录提交 SHA、依赖与 schema 14；保留 00001—00013，使用独立合成数据库，不迁移 M1 联调库。
2. 验证本机：运行 `pnpm check`、`pnpm test:db`；检查上述反例实际通过，清理测试库/角色。日志放忽略目录，不提交凭证。
3. 验证远端：推送已审查制品，运行 [Check](../.github/workflows/check.yml) 的工程检查、真实 PG、普通与 integration race、浏览器测试；记录同一 SHA 的运行 URL 和结论。
4. 判定退出：当前制品证据齐备才放行 M2；失败修复后重跑受影响项，不以历史 M1 CI 或文档覆盖替代。

## 保留后续范围

真实平台应用发布与卡片现场认证另行记录；未授权真实发送时仅合成验证。知识 ACL、SQLite、沙箱、正式文档与联合备份等按 M3 文档继续推进，不借本页声明已实现。M4 的多节点容量与集团组合仍待对应环境认证。

当前本机证据：2026-10-19，pnpm check 与 pnpm test:db 均退出 0，日志 .tools/m2-card-send-check.log、.tools/m2-card-send-full-db.log；数据库/角色残留为 0。当前制品的远端验证见下表。

远端首轮：0296799 的 [Check 39233587067](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39233587067) 工程检查通过，真实 PG 通知并发测试失败（候选快照过时导致 no rows）；race 未执行。修复后须以新提交完整复验，不将本轮记为通过。

## 核对最终制品证据

[Check 39234206040](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39234206040) 对 8dad1fb 完成并返回 success；完整日志保存在 .tools/m2-ci-success.log。

| 命令/检查 | 结果 |
|---|---|
| pnpm check | 通过：Go/TS/Python、构建与制品冒烟 |
| pnpm test:db | 通过：真实 PG 17.11；含审批卡片报价链路、通知竞争、锁等待撤权、技能版本及工具未知结果 |
| go test -race ./... | 通过 |
| go test -race -tags=integration -count=1 ./internal/... | 通过 |
| pnpm test:e2e | 通过：2 项 Chromium 测试 |

核对退出：V03/V04 的租约与未知结果、V05 的版本审批与恢复、V10 当前只读 MCP 安全边界、技能版本基础及报价样例均有上表和所链接测试证据。未认证写工具、SOAP、来源 ACL、群聊、正式 PDF/用章与生产容量保持禁用/后续范围，不用合成测试替代现场认证。

转入 M3：先实现 [知识管道契约](../contracts/knowledge.md) 的内容/ACL 独立版本、不可见发布状态与授权读取，再接来源、检索和隔离能力。此后仅文档记录更新不改变已验收制品。