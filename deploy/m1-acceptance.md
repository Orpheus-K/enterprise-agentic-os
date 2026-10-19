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
| 真实飞书与模型 | 未运行，未提供配置与注入凭证 | 待填任务 ID、Trace_ID、结果 |

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

## 执行联调

1. 固定版本：记录提交 SHA、Go 制品 SHA256、schema、模型版本与无密钥配置摘要；停止其他同安装执行者。
2. 启动渠道：注入所需环境后执行以下命令；当前配置尚未就绪，不能先行启动真实外发。

   ```powershell
   $env:AGENTOS_CHANNEL_CONFIG = "$PWD/.tools/feishu-live.json"
   pnpm start
   ```

3. 核对就绪：另开终端请求 `http://127.0.0.1:8080/api/ready`；连接成功且数据库可用时应为 200/true。使用获准运维令牌查询 status，模型仍为 not_probed。
4. 验证单聊：获准测试用户发送不含业务数据的唯一测试短句；核对仅一条回复、任务 succeeded、审计顺序与 Trace 链接。ACK 或 ready 不算回复成功。
5. 验证撤权：管理员停用测试身份映射后发送新测试句，不能新增可执行任务或调用模型；恢复映射后再验。运维能力撤销应返回 403，身份撤销应返回 401。
6. 验证隔离：用获准审计账户查询其他租户/应用任务，应为空页且留痕；不返回正文或全局序列。真实回调重复投递仅在有受控重投条件时执行，否则保留为替身证据。
7. 验证故障：仅对专用环境断开渠道或停止联调数据库，ready 应为 503；恢复依赖后核对状态。pending 可继续，running/unknown 只核对结果，不手工改回 pending 或盲目重发。
8. 收束现场：停止测试进程、恢复测试授权、核对所有任务终态与未知结果；保存脱敏证据，不提交原始消息、凭证或客户日志。

每项记录“步骤、预期、实际、任务/Trace 标识、通过/失败/未运行”。网络故障注入不得影响共享数据库或其他应用。

## 判定退出

本地与远端 CI 验证已通过；当前 M1 **未退出**。补齐真实单聊与故障/权限证据后再判定。失败项定位到责任模块；未知结果先核对，不进入 M2 掩盖缺口。当前状态回写 [里程碑笔记](../.agents/NOTES.md)。
