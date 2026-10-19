# 接续项目迭代

## 定位当前里程碑

- 进展：2026-10-19，M1 已退出；M2 已进入退出验收，0296799 远端验收发现通知竞争缺陷，已修复并准备新提交复验。M2 整体未完成，自动目标继续。
- 对齐：R03/R04/R06/R08/R09、A03/A04/A08、V03/V05/V06；[研发路线](../enterprise-agentic-os-architecture/07-架构决策与研发路线.md) 定义最终退出，切片通过不替代完整验收。
- 真源：[任务/审批契约](../contracts/task-runtime.md)、[技能契约](../contracts/skills.md)、[运维契约](../contracts/operations.md)。

## 当前退出验收切片

- 验证修复：真实 PG 并发通知测试连续 20 次通过，pnpm check:go 通过；证据 .tools/m2-notice-race-regression.log、m2-notice-race-go.log。
- 修复远端反例：任务候选快照可早于其他发送者完成通知；取得任务锁后无可发送通知时返回无任务，其余错误仍失败。未放松发送版本、租约或幂等断言。

- 跟踪远端：[Check 39233587067](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39233587067)，精确 SHA 02967992b4558b73d8db17163353d52c0bdf488a；已完成且失败：真实 PG 的 TestApprovalNoticeConcurrentAndRestart 返回 no rows；后续 race 未运行。失败日志 .tools/m2-ci-failed.log。
- 收束现场：锁等待撤权回归与其余核对测试通过；测试库/角色残留 0，PG 已停止。提交中的示例 Skill 摘要已按 Git blob 字节校验。

- 修复验收发现：任务核对读/写在任务锁等待后重验身份与 task.reconcile，复用通知核对检查；真实 PG 反例先复现成功越权，再修复通过。证据 `.tools/m2-recovery-lock-before.log`、`m2-recovery-lock-after.log`、`m2-exit-go.log`。
- 检查公开内容：191 个 Git 可见文件的凭证特征扫描未发现候选；.tools、凭证、数据库和运行日志保持忽略。
- 固定内容：通知驱动改用强类型 ApprovalNotice；发送前在既有任务锁和接收者授权下读取绑定审批，校验摘要；报价加载前一版本用于差异。
- 复用组件：官方 SDK 交互卡片展示纯文本初稿、版本/摘要/有效期与批准/驳回按钮，关闭转发；不截断业务正文，超出本地上限显式失败。点击仍走可信回调与当前授权。
- 验证贯穿：真实 PG＋SDK HTTP 替身完成七五折、卡片发送、改单差异、旧卡片拒绝、连接重开、新卡片批准去重、V2 初稿单次交付；未向真实用户发送。
- 验证通过：最终 `pnpm test:db`、`pnpm check` 均退出 0；`git diff --check` 通过。测试库/角色残留均为 0，PG 已停止。
- 留存证据：`.tools/m2-card-send-full-db.log`、`.tools/m2-card-send-check.log`；[卡片贯穿测试](../services/core/internal/feishu/approval_card_integration_test.go)。首轮定向日志 m2-card-send-db.log 保留测试替身误用普通聊天断言的失败；修正为批准 V2 报价断言后全套通过，不冒称首轮通过。
- 对齐文档：更新通知、报价、任务契约和飞书部署说明；建立 [M2 验收清单](../deploy/m2-acceptance.md)。当前提交远端 CI/race 与平台现场认证未完成，M2 未退出。

## 保留实现与证据

- 继承：可信渠道、IAM/RLS、持久化状态机、租约世代/版本、未知操作核对、审批挂起与认证唤醒；原内容批准、发送前重验，报价修改重算已接入，飞书审批卡片及可信回调已合成验证。
- 技能资产：manifest＋SKILL.md 以部署期望摘要固定版本；多版本 Registry、渐进摘要、工具权限交集；schema 7 精确授权表只读，运行时不能自授。
- 固定任务：schema 8 增加 skill_name/skill_revision，入队授权与落库原子提交；重复消息保留旧版本，更换默认技能不改写旧任务。旧包必须保留。
- 加载上下文：Worker 按任务 Ref 重验并加载指令、契约和样例，使用独立 system/user 角色；模型/发送意图取行锁后重新查技能权限。旧任务未生成响应时缺包/移除配置显式失败，不降级聊天。
- 配置安装：skills.selected＋assets（绝对路径、期望 Revision），启动校验；支持固定技能与授权摘要路由两种配置。工具技能配置网关后执行有界循环；缺少配置显式拒绝。
- 验证通过：`pnpm test:db` 全套、最终技能运行时定向 PG 测试、`pnpm check`（Go/TS/Python、构建、二进制冒烟）、`git diff --check`。覆盖 V1/V2 并存、连接池重开与过期重新认领、消息去重、角色隔离、模型前/发送前撤权、新版本不继承授权。
- 证据日志：`.tools/m2-skill-runtime-db.log`、`m2-skill-runtime-targeted.log`、`m2-skill-runtime-check.log`；[技能运行测试](../services/core/internal/feishu/skill_integration_test.go)。前序证据见 m2-skills-*、m2-approval-*、m2-reconcile-* 忽略日志及对应测试文件。
- 收束环境：合成数据与本机模型替身；测试数据库/角色残留均为 0，专用 PG 已停止。未调用真实飞书/模型，未跑远端 CI/race；Windows 缺 gcc。前端未改变，不重跑浏览器。
- 保留 M1：[退出验收](../deploy/m1-acceptance.md) 与 089cec5 的 [CI/race](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39198385216) 是历史证据，不冒充当前变更通过。

## 接续有限验收清单

1. 按 M2 验收清单审查当前全部变更与公开仓库内容，固定提交制品；不得提交 .tools 凭证、测试库或日志。
2. 推送并验证当前 SHA 的远端 Check（含普通/integration race、真实 PG、浏览器）；失败仅按证据修复，保留运行链接。
3. 按 M2 文档退出条件逐项判定；缺项继续开发。现场平台、M3/M4 未实现范围保持明确，不借当前合成测试声称生产就绪。

## 保留部署边界

- 升级：停止旧实例、备份并显式迁移至 schema 14；00001—00013 保持原样。M1 独立联调库仍为 schema 3，未修改，不混跑版本。
- 临时目录：C 盘空间不足；后续本机 Go 命令设置 `GOTMPDIR=$PWD/.tools/go-tmp`，目录已创建，不改系统全局配置。
- 测试：`GO_BINARY=$PWD/.tools/go/bin/go.exe`；PG `.tools/pgdata-m1`、55439、忽略文件 `.tools/pg-test-admin.url`，禁止回显凭证。
- 限定：当前固定安装单实例，无生产 HA、自动补偿或 exactly-once 承诺；认证审批接口不是未验签的飞书开放回调。
