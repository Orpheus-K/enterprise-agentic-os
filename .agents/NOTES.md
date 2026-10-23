# 接续项目迭代

## 当前切片：无人值守补齐（2026-10-21）

受测工作树：`E:/agent-project/.tools/sqlite-approval-verify`，分支 `codex/m3-operations-storage`。M1/M2 工程退出证据见 deploy/m1-acceptance.md、deploy/m2-acceptance.md；M3 尚未整体退出，当前差距单点为 [M3 退出差距](../deploy/m3-readiness.md)。当前 PG schema 28、SQLite schema 9（2026-10-22 起任务持久化渠道 Provider，去掉会话核心写死的 "feishu"，见 contracts/identity.md；渠道适配器/来源连接器接口尚未抽取，bootstrap 仍直接装配飞书）。

用户的 15 项按限定范围落实如下。日志目录均为 `E:/agent-project/.tools/`；已关闭的中间过程、旧失败和历史切片保留在 Git 历史及原日志，不作为当前待办。

| 项 | 当前结果与单点 | 主要证据 |
|---|---|---|
| 1 | 当前笔记只保留一个当前切片，删除已过时的离线包/SQLite/PDF/Worker 待办；更新 M3 差距 | 文档核对，不称产品测试 |
| 2 | 指定四个提交 1cf146b/e33b684/7b40ab7/4fe8851 已推送；原 CI 验包超时修复后通过 | 9aa64cc / CI 39482454535 success；后续 CI 见下 |
| 3 | OAuth 换取/刷新/过期/撤销、AES-GCM 保存、未知不重放及本地管理入口；contracts/feishu-docs.md | m3-oauth-admin-check.log、m3-oauth-admin-db.log 退出 0 |
| 4 | SQLite WAL 在线一致备份、完整性/占用/篡改及恢复后审批/撤权；deploy/sqlite/README.md | m3-online-backup-check.log、m3-online-backup-final.log 退出 0 |
| 5 | PG/SQLite 数据库与加密配置联合恢复、固定密钥引用、版本/当前撤权复核；contracts/recovery.md、deploy/recovery.md | m3-recovery-admin-check.log、m3-recovery-admin-db.log、m3-recovery-cli-smoke.log 退出 0；schema 27 用章撤权由最新全量 DB 覆盖 |
| 6 | 文本/JSON WASM 产物限额与扫描库；wazero 指令预算不可由当前公开 API 保证；contracts/sandbox.md | m3-artifact-scan-test.log、m3-artifact-scan-go.log 退出 0；函数 listener 死循环负例 |
| 7 | knowledge.audit.read 独立授权、分页及读取/检索统计；contracts/knowledge.md | m3-knowledge-report-db.log、m3-knowledge-report-go.log 退出 0 |
| 8 | CycloneDX 1.6 Go/前端依赖清单，Windows/Linux amd64 构建与独立验包；deploy/offline/README.md | m3-linux-sbom-package-final.log、m3-linux-sbom-install.log、m3-windows-sbom-package.log、m3-windows-sbom-install.log 均 0；CI 39491570127 success |
| 9 | 完整群接收集合/逐人授权/范围版本证明库，群聊继续关闭；contracts/knowledge.md | m3-audience-go.log、m3-audience-vector-test.log 退出 0 |
| 10 | 中文正式 PDF 库、版本固定/单次预留/发布授权/审计；contracts/quotation.md | m3-pdf-ledger-check.log、m3-pdf-ledger-full-db.log、m3-pdf-ledger-final-targeted.log 退出 0；合成 PDF 经 Poppler 视觉检查 |
| 11 | 无状态 Python 文档 Worker、gRPC 契约、输入/输出/并发/截止时间与撤权检查；未隔离拒绝启用；contracts/compute.md | m3-python-full-check.log、m3-python-rpc-boundaries.log 退出 0，38 个 Python 测试 |
| 12 | 中文合成评测框架；Recall@3=37/60、Filtered Recall@3=0.8、越权 0；pg_search/BM25 仍为候选；contracts/retrieval-evaluation.md | m3-retrieval-evaluation-check.log 退出 0；chinese-retrieval-report.json |
| 13 | 独立用章策略、版本/内容/PDF/证据绑定、持久意图与审计，仅合成替身；contracts/quotation.md | m3-seal-model-check.log、m3-seal-model-full-db.log 退出 0；m3-seal-negatives.log 覆盖撤权与审计回滚 |
| 14 | 官方钉钉/企微支持矩阵；未取得的精确 API 正文明确待核实；contracts/channel-capabilities.md | 仅官方资料研究，无渠道代码/真实租户测试 |
| 15 | Windows 注册脚本预览/替身测试和白名单诊断包已实现；deploy/windows/README.md、deploy/operations.md | 定向脚本/诊断测试及完整 pnpm check 均通过；m3-operations-scripts-final-check.log 退出 0 |

## 当前验证与后续动作

- 第 13 项 74fdbd4 / CI 39503815866、PDF 475d30b / CI 39501749980、中文评测 2001e7f / CI 39502122441 均 success。第 15 项 7ae5690 已推送，其检查见 CI 39504592972；各提交的最终状态以远端 Actions 为准，不将本地结果替代远端结果。
- 第 15 项只执行脚本本身，注册调用为替身；没有调用 SCM、启动服务或真实 wrapper。Windows PowerShell 被本机签名策略阻止，使用 PowerShell 7 测试，未修改执行策略。首轮完整检查仅 tasks.mjs 格式失败，已格式化并重跑，失败日志 m3-operations-scripts-check.log 保留。
- 本轮 15 项限定范围的源码与文档已落实；远端跟进在本次目标会话完成，当前分支检查真源为 https://github.com/Orpheus-K/enterprise-agentic-os/actions 。后续实际联调按 M3 差距清单，未获得真实现场授权前不自行启用。
- 用户指定的 10 月 23 日 GMT+8 04:59 到期重置卡已在额度剩余 1% 时成功兑换；兑换后已用 0%、剩余 2 张。不得重复兑换其他卡。

## 保留环境与边界

- Go/缓存/临时目录使用 E:/agent-project/.tools 内既有工具目录；不要修改全局环境。Windows 本机无 gcc，race 由 Linux CI 验证。
- PG 验证只用 WSL Ubuntu `17/agentos-m3`、端口 55440、PG 17.11/pgvector 0.8.7。最新全量测试后 test_dbs=0、test_roles=0，集群已停止。不得误用历史无 vector 的 Windows 集群或 WSL 17/main。
- 凭证、数据库 URL、DPAPI/现场配置、原始日志均保留在忽略目录，不回显或提交；诊断包仅按白名单汇总。
- 未做真实租户/群聊/印章/客户外发、容器隔离、真实 Windows 服务生命周期或生产激活。PDF/用章/Worker 的真实对象存储与业务接线仍在 M3 差距中；库层合成证据不替代现场验收。
- 离线包仍是有限 SQLite 核心包，非完整 Python/PG 发行物；不签名、不授权正式发行。现有内部样包包含 modified=true 的构建事实，不能作干净正式发布证明。
