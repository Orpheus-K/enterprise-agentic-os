# 验证 M1 联调与退出

对齐 R01/R02/R13/R15、A03/A08/A10/A12，验证 V06/V11/V12/V13/V14/V19 的 M1 子集；不代表完整验证矩阵通过。先固定制品与配置，再跑可信单聊，最后核对异常与隔离。

## 固定本次证据

2026-10-19，Windows amd64、Go 1.27.2、Node 22.18、pnpm 10.15、Python 3.13.13、PostgreSQL 17.11；首轮远端验证提交 `089cec5c96ed9171f1e991d1ce518b28ae20bea0`。

| 检查 | 结果 | 本地证据（忽略文件） |
|---|---|---|
| `pnpm check` | 通过，含构建与二进制冒烟 | `.tools/m1-check.log` |
| `pnpm test:db` | 通过，真实 PG 迁移、事务、身份、链路与运维隔离；测试集群已停止 | `.tools/m1-db.log` |
| `pnpm test:e2e` | 通过，2 项 Chromium 测试 | `.tools/m1-e2e.log` |
| `CGO_ENABLED=1 go test -race ./...` | 阻塞，Windows 找不到 gcc | `.tools/m1-race.log` |
| 远端 Check / Linux race | 全部通过：全量检查、真实 PG、普通及 integration race、浏览器测试 | [首轮 CI](https://github.com/Orpheus-K/enterprise-agentic-os/actions/runs/39198385216) |
| 真实飞书与模型 | 通过：新组织应用 0.1.1、官方长连接、gemini-3.1-flash-lite 合成单聊回复 M1_E2E_OK；唯一任务 succeeded | `.tools/m1-live-ready.json`、`.tools/m1-live-audit.json`、`.tools/m1-feishu-e2e.png` |

本地链路测试使用真实数据库和协议替身；不能作为真实飞书或模型可用证据。远端工作流见 [Check](../.github/workflows/check.yml)，当前仅 push / pull_request 触发。首轮运行成功；仅验收文档更新使用 `[skip ci]`，实现与工作流保持已验证提交内容。CI 提示 Action 的 Node 20 运行时弃用及 ubuntu-latest 镜像迁移，作为后续依赖维护项，不代表本轮测试失败。

## 准备真实配置

按 [飞书指南](feishu.md) 与 [运维指南](operations.md) 配置，不复制新的配置规范。将 [示例](feishu.example.json) 复制到已忽略的 `.tools/feishu-live.json`，替换以下信息；只提供文件路径，密钥由本机环境注入。

| 提供方 | 必需配置 | 核对条件 |
|---|---|---|
| 测试租户管理员 | Tenant UUID、AppID、TenantKey、测试用户 OpenID、MemberID | 自建机器人、单聊事件订阅与回复授权；IAM 精确映射且启用 |
| 数据库管理员 | 独立运行连接、显式迁移至 schema 3 | 运行账户无迁移/IAM 写权；使用专用联调库 |
| 模型负责人 | 获准端点、模型名、max_tokens、API Key 环境引用 | 非流式文本 chat-completions；允许处理合成测试文本；HTTP 必须显式批准 |
| 渠道负责人 | `AGENTOS_FEISHU_APP_SECRET` | 默认 websocket，可访问飞书服务；该模式不要求 Webhook Token/Encrypt Key |
| Webhook 模式负责人 | Verification Token、Encrypt Key、HTTPS 回调 | 仅选择该模式时提供；不与长连接同时接收 |
| 运维负责人 | operations 对象、独立随机令牌、operator 映射 | 按应用分别授予 status.read / audit.read；无授权令牌不能查询 |

默认环境引用为 `AGENTOS_RUNTIME_DATABASE_URL`、`AGENTOS_MODEL_API_KEY`，以配置实际引用名为准。批准的无鉴权内网模型才可显式留空 api_key_env；不要在终端输出环境值。

2026-10-19 本机配置已完成：`.tools/feishu-live.json` 保存新应用精确绑定与获准模型，凭证使用 Windows DPAPI 文件并注入运行环境；独立数据库 schema=3，IAM 由管理员预置。TenantKey/OpenID 来自专用官方 SDK 连接接收的指定合成探测，核对应用、头部租户与发送者租户一致；不自动注册用户。双制品哈希见 `.tools/m1-live-artifacts.json`。

## 执行联调

现场进展（2026-10-19）：用户切换账号后，新组织内创建测试应用，发布 0.1.1，仅所有者可用；两项消息权限经确认开通，外部共享关闭。官方 SDK 长连接与 im.message.receive_v1 已配置发布。正式制品已使用独立联调库完成合成文本单聊；测试结束后停止进程与 PG。

1. 固定版本：记录提交 SHA、Go 制品 SHA256、schema、模型版本与无密钥配置摘要；停止其他同安装执行者。
2. 启动渠道：注入所需环境后执行以下命令；仅启动获准的独立测试安装。

   ```powershell
   $env:AGENTOS_CHANNEL_CONFIG = "$PWD/.tools/feishu-live.json"
   pnpm start
   ```

3. 核对就绪：另开终端请求 `http://127.0.0.1:8080/api/ready`；连接成功且数据库可用时应为 200/true。使用获准运维令牌查询 status，模型仍为 not_probed。
4. 验证单聊：获准测试用户发送不含业务数据的唯一测试短句；核对仅一条回复、任务 succeeded、审计顺序与 Trace 链接。ACK 或 ready 不算回复成功。
5. 验证撤权：管理员停用测试身份映射后发送新测试句，不能新增可执行任务或调用模型；恢复映射后再验。运维能力撤销应返回 403，身份撤销应返回 401。
6. 验证隔离：用获准审计账户查询其他租户/应用任务，应为空页且留痕；不返回正文或全局序列。真实回调重复投递仅在有受控重投条件时执行，否则保留为替身证据。
7. 验证故障：仅对专用环境断开渠道或停止联调数据库，渠道断连时 ready 应为 503；数据库错误导致当前 Worker/主进程失败退出时端口不可达，也不得报告就绪。恢复数据库后须显式重启当前运行时，再核对状态。pending 可继续，running/unknown 只核对结果，不手工改回 pending 或盲目重发。
8. 收束现场：停止测试进程、恢复测试授权、核对所有任务终态与未知结果；保存脱敏证据，不提交原始消息、凭证或客户日志。

每项记录“步骤、预期、实际、任务/Trace 标识、通过/失败/未运行”。网络故障注入不得影响共享数据库或其他应用。

## 判定退出

2026-10-19：M1 **可以退出**。本地检查、远端 CI/race 与下述真实合成文本现场验收共同覆盖接入去重、身份隔离、事务失败和基础可观测。范围限定自建应用、固定安装、单实例、非流式文本；数据库故障恢复依赖显式重启，不承诺 HA 或在途任务自动核对。真实重复回调未执行受控重投，去重沿用真实 PG＋协议替身证据。本轮不启动 M2，状态回写 [里程碑笔记](../.agents/NOTES.md)。

## 核查飞书 21001

2026-10-19：Chrome 核实并完成单用户版本发布，权限仍为已开通；IP 白名单无条目，服务端日志无查询结果。发布后令牌请求仍返回平台策略拒绝 21001。应用属于豆包账号，[官方 CLI 仓库同类反馈](https://github.com/larksuite/cli/issues/2815) 提及同账号类型与令牌路径；账号级策略为有证据的推断，具体根因需平台确认。未找到自助解除入口，未发送客服消息。发布截图保存在忽略文件 .tools/feishu-published.png；这是旧账号应用的历史失败证据；新企业组织应用已完成长连接与单聊，不代表旧应用策略被解除。

## 核对本次真实链路

2026-10-19，新账号下企业测试应用令牌返回 code=0。未扩展通讯录或企业信息权限；企业信息查询因缺少 tenant:tenant:readonly 被拒绝后，改由已获准单聊事件核对测试身份。

| 场景 | 预期与实际 | 证据 |
|---|---|---|
| 长连接就绪 | connected、worker=true、storage=available，ready=true | `.tools/m1-live-ready.json` |
| 合成单聊 | 输入要求只回复 M1_E2E_OK，飞书页面收到一次对应回复；数据库 succeeded=1 | `.tools/m1-feishu-e2e.png`、`.tools/m1-live-runtime.log` |
| 受控审计 | 四步状态完整、无消息正文；查询访问留痕 1 条 | `.tools/m1-live-audit.json` |
| 查询撤权 | 无令牌 401；撤销 status.read 后 403；授权已恢复 | `.tools/m1-live-access-probe.json` |
| 结束检查 | pending/running/unknown=0；两个测试进程和独立 PG 均停止 | 本次受控 SQL 查询与 pg_ctl stop |

关联任务 abfc5774-b784-405c-a723-4f79cad4e03b；入口 Trace 2ef81c9abb9a67cfbb33a47711dfe98d，执行 Trace 599c8d3f864addd19649033e04fed17f。模型状态接口仍为 not_probed；端到端证据来自真实成功回复。原始身份、凭证和截图仅保存在 Git 忽略目录。

## 完成剩余退出验收

2026-10-19，实际 Go 制品 SHA256 与既有记录一致：`D46145D74C8EE64CBA2D2B7931F533C22EE7A1DDF33BE360B7945B725FA52EAA`。仅使用合成文本、本机独立库与仅所有者可用的测试应用；产品代码未变，未重跑已有 CI/race。

| 场景与步骤 | 实际结果与判定 | 本地证据 |
|---|---|---|
| 停用消息身份映射，发送唯一测试消息 | 真实 ingress.websocket 返回错误；任务数不增、模型调用 0；恢复映射后新消息成功。通过 | `m1-message-revocation.json`、`m1-revoke-metrics-before.json` / `after.json`、`m1-revocation-restored.log` |
| 创建同租户不同应用、不同租户同应用的合成终态任务，经实际审计 HTTP 接口查询 | 两项均为 200/空页，与不存在任务一致；本安装正例返回 4 步审计；三项空查询各留痕 row_count=0。通过 | `m1-installation-isolation.json`、`m1-isolation-cleanup.log` |
| 仅为测试进程设置本地 CONNECT 代理，关闭飞书 TLS 隧道后恢复 | ready=503，transport=reconnecting/disconnected；遵循 SDK 退避后自动 connected/ready=200，恢复消息成功，无手工重发。通过 | `m1-channel-fault.json`、`m1-channel-recovery-first-window.json`、`m1-channel-recovered.json` |
| 独立 PG 使用 pg_ctl -m fast 停服，再启动 PG 与运行时 | Worker 报连接错误，主进程退出、ready 端口不可达；不是实测 HTTP 503。重启后 ready=true，原 3 条任务 ID/状态/响应一致，新消息成功。通过失败关闭与人工恢复验收 | `m1-database-fault.json`、`m1-before-db-fault.log` / `m1-after-db-restart.log`、`m1-database-restored.json` |
| 核对终态、授权及清理 | 4 条真实合成任务均 succeeded、各 4 步审计；撤权消息任务 0，在途/unknown 0；授权恢复，隔离夹具删除，9 条审计访问留痕保留；所有测试进程、PG 与端口监听已停止。通过 | `m1-exit-tasks-audits.json`、`m1-exit-cleanup-checks.log`、`m1-exit-recovery.png` |

上述文件均位于 Git 忽略目录 `.tools/`。隔离夹具是受控 SQL 写入的合成终态记录，只用于真实查询边界测试，不声称由其他真实飞书安装执行。故障代理仅透传 TLS，不解密、不改证书、不影响 Chrome 或其他进程。

| 恢复消息 | 成功任务 | 入口 Trace |
|---|---|---|
| M1_IDENTITY_RESTORED | 9e380dfe-cd93-4415-9e52-122afbaea808 | c0fe40cb1058fd24d9b70f9f9b8b6dec |
| M1_CHANNEL_RECOVERED | d3a19c74-c1ca-4924-bf6b-b85ce76044ed | 914b9bbf91236b3d7f5943bb86e13591 |
| M1_DATABASE_RECOVERED | a90b90a4-a800-4d27-a67a-887d5ee964dd | cd1e5c4918690c047e2becd01c3a1b4e |

限定：未执行断电、强制数据库崩溃、在途写结果未知时自动恢复或真实重复回调重投。强制中断命令被自动审批拦截，实际采用正常停服；没有将其标为崩溃恢复。上述能力不属于本次 M1 通过结论。
