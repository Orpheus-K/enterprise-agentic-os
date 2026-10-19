# 接续项目迭代

## 定位当前里程碑

- 进展：2026-10-19，工程 M1、M2 已退出；M3 首切片已实现知识版本与授权读取，已通过本机全套验证，自动目标继续，整体蓝图未完成。
- 真源：[研发路线](../enterprise-agentic-os-architecture/07-架构决策与研发路线.md)、[M2 验收](../deploy/m2-acceptance.md)、[知识管道契约](../contracts/knowledge.md)。知识契约区分已实现存储与未接入来源/检索。

## 当前 M3 知识切片

- 实现基础：schema 15 新增来源、文档独立内容/ACL 版本、切片与审计；四表 FORCE RLS，运行角色不能配置来源，授权同步人写入，普通成员只读获权证据。
- 固定可见：内容版本必须匹配 ACL ContentRevision；拒绝优先，切片只能收紧；缺 ACL、超期、删除、旧引用与来源停用均不可读。ACL 变化不重写内容；事件重复同摘要幂等，旧版/冲突拒绝。
- 验证进展：补齐跨租户池化及连接重开后，完整 pnpm test:db 与 pnpm check 均退出 0；git diff --check 通过，测试库/角色残留 0，PG 已停止；日志 .tools/m3-knowledge-db.log、m3-knowledge-full-db.log、m3-knowledge-check.log。
- 限定范围：internal/knowledge 为模块入口，尚无外部同步 HTTP/MCP、原生组织权限解析、向量召回或模型引用消费者；不宣称完成 RAG。

## 固定 M2 证据

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

1. 接入受控同步与引用读取入口：复用可信身份、来源绑定及 knowledge.Store，不允许客户端指定同步成员或绕过来源权限。
2. 增加来源同步健康/游标恢复契约，失联、权限不可确认先阻断读取；权限恢复须显式新版本，旧事件不得重开访问。
3. 补齐入口鉴权、跨安装、撤权、乱序与恢复真实 PG 测试，再接授权检索/模型消费者；不宣称原生组织权限及向量能力已实现。

## 保留交付环境

- 私有依赖：.tools/pg-test-admin.url、DPAPI 凭证与现场配置全部忽略，禁止回显；公开源码特征扫描 191 个文件无凭证候选。
- 本机 Go：GO_BINARY=$PWD/.tools/go/bin/go.exe；GOTMPDIR=$PWD/.tools/go-tmp，不改全局设置。Windows 无 gcc，race 由上述 Linux CI 证明。
- 独立 PG：.tools/pgdata-m1，127.0.0.1:55439；测试自行建库建角色并清理。升级先停旧实例、备份并显式迁移，已发布迁移不可重写。
- 当前 M3 制品使用 schema 15；00001—00014 保持不变。M2 CI 为历史已验收制品证据，不能代替当前 M3 远端验证。
