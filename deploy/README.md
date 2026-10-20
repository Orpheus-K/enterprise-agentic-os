# 构建本地制品

- 执行：根目录 `pnpm build`，先构建 Vite，再嵌入 Go；同时构建 Python wheel/sdist。
- 启动：根目录 `pnpm start`；默认仅监听 `127.0.0.1:8080`，通过 `AGENTOS_HTTP_ADDR` 显式配置地址。
- 区分：`/api/health` 仅表示进程存活；默认未启用渠道时 `/api/ready` 返回 503；配置渠道后的数据库就绪与权限边界见[飞书部署](feishu.md)。
- 限定：当前提供开发制品；生产容器、认证与离线安装包待后续切片实现。

## 离线识别制品

`agentos --version` 与 `agentos-migrate --version` 输出一行 JSON，包含组件名、Go 版本、编译操作系统/架构、源码 revision/modified、所需 schema_version 及内置 quotation_wasm_sha256。读取的是编译进二进制的元数据，不读取现场配置、环境凭证、Git 工作区或数据库，不监听端口；迁移器该模式不执行迁移。未知参数拒绝。

`source: null` 表示缺少完整 Git 构建信息；modified=true 表示构建时工作树有修改。两者都不能作为干净发布版本证明。运行身份信息不表示功能已启用、服务就绪或支持该平台的生产部署，也不能代替制品 SHA-256、发布签名、SBOM 与许可证清单。当前仍未提供完整离线安装包；该接口供后续打包与验包使用。
