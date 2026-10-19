# AgentOS

企业级数字员工运行底座。已建立 M1 工程骨架、PostgreSQL/身份基础及可配置飞书单聊→获准文本模型链路；真实租户与模型服务仍需部署验收。

## 准备环境

- 使用 Node 22（至少 22.12）、pnpm 10.15.0、Go 1.27.2、Python 3.13、uv 0.11.8。
- 安装依赖：`pnpm install --frozen-lockfile`；`uv sync --project services/compute --locked`。
- 配置 Go：优先放入 PATH；也可用 `GO_BINARY` 指向明确的 Go 可执行文件。本机隔离工具链位于 `.tools/go/bin/go.exe`，不纳入交付。

## 执行检查

| 命令 | 行为 |
|---|---|
| `pnpm check` | 检查格式/规范、类型、前端测试、依赖边界；构建前端；检查 Go/Python；构建并验证二进制 |
| `pnpm build` | 构建静态 UI、嵌入式 Go 与 Python 分发包 |
| `pnpm start` | 启动已构建 Go 制品，访问 `http://127.0.0.1:8080` |
| `pnpm dev` | 启动 Vite；API 代理到本地 Go 8080 端口 |
| `pnpm check:go` / `pnpm check:python` | 检查对应语言；Go 检查前构建并准备嵌入资源 |
| `pnpm format` | 格式化前端及工程脚本；Go 使用 gofmt，Python 使用 Ruff |
| `pnpm boundaries` | 检查循环依赖、不可解析导入及共享包边界 |
| `pnpm test:e2e` | 在真实 Go 制品上验证 Chromium 页面、主题、移动宽度与键盘交互 |
| `pnpm test:db` | 在专用 PostgreSQL 集群验证迁移、事务、身份隔离及飞书模型链路；缺少环境直接失败 |
| `pnpm db:migrate` | 使用独立迁移账户执行向前迁移 |

PowerShell 使用隔离 Go：`$env:GO_BINARY = "$PWD/.tools/go/bin/go.exe"`，随后执行以上命令。更换机器须自行安装所列工具链；检查脚本不自动下载或寻找替代运行时。浏览器检查前运行 `pnpm exec playwright install chromium` 和 `pnpm build`；Linux CI 使用 `--with-deps` 安装浏览器依赖。

## 定位目录

数据库环境与账户准备见 [PostgreSQL 指南](deploy/postgres/README.md)；`pnpm check` 不启动数据库，数据库变更必须另过 `pnpm test:db`。

飞书入口、安装绑定、模型配置及运行限制见 [接入指南](deploy/feishu.md)；未设置渠道配置时保持控制台模式。

就绪状态、本地观测及受控审计查询见 [运维配置](deploy/operations.md)；运维令牌不代替 IAM 能力授权。

M1 配置清单、联调步骤与退出证据见 [验收记录](deploy/m1-acceptance.md)。

`apps/web` 放页面；`packages/ui` 放共享组件；`packages/api-client` 消费 `contracts`；`packages/config` 放共享配置。`services/core` 管理 Go，`services/compute` 管理 Python，`deploy` 记录制品入口。跨语言使用各自工具链与锁定文件。

读取 [项目规范](AGENTS.md)、[产品文档](enterprise-agentic-os-docs/README.md)、[架构文档](enterprise-agentic-os-architecture/README.md)、[里程碑笔记](.agents/NOTES.md)。
