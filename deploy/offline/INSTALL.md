# Windows SQLite 核心离线包

关联 R19、A03/A10、V15。此包用于 Windows/amd64 内部安装验收，不是完整 M3 发布物。安装不运行 npm、Go、Python 或包管理器，不下载依赖；飞书和模型服务仍需配置网络可达的目标，“离线安装”不表示业务无需联网。

## 校验与安装

1. 将整个包复制到新的本地目录，禁止覆盖正在运行的版本。数据库和现场配置放在包目录之外，通过操作系统 ACL 限制访问。
2. 使用 Windows PowerShell 执行 `.\Verify.ps1`，缺文件、额外文件或哈希不符均停止。MANIFEST.json 本身须通过可信交付渠道确认；包内哈希只能发现文件变化，不能替代发布签名。
3. 执行 `.\bin\agentos.exe --version` 与 `.\bin\agentos-migrate.exe --version`，核对 MANIFEST.json 的平台、revision、modified 与 schema。modified=true 是工作树构建证据，不是干净发布版本。
4. 停止所有使用目标 SQLite 文件的进程，备份原数据库，清除 `AGENTOS_MIGRATION_DATABASE_URL`，执行 `.\bin\agentos-migrate.exe --sqlite C:\AgentOS-data\runtime.db`。父目录须预先建立。失败则停止；程序启动不会自动迁移。
5. 按 `docs/deploy/sqlite/README.md` 准备完整渠道配置及身份数据，并设置 `AGENTOS_CHANNEL_CONFIG`。不要把凭证写入包或提交到仓库。未配置身份时不承诺可接收消息。
6. 设置 `AGENTOS_HTTP_ADDR=127.0.0.1:8080` 后运行 `.\bin\agentos.exe`。未提供渠道配置时可验证嵌入页面和 `/api/health`，`/api/ready` 应为 503；配置完整渠道后须检查就绪及实际授权业务链路。

停止进程后可移除该新版本目录；不可删除包外数据库和现场配置。升级涉及迁移时，回滚必须同时使用已验证的数据库备份和对应旧程序，不将旧二进制直接指向新版数据库。SQLite 备份恢复演练另行完成。

## 内容与许可证据

MANIFEST.json 枚举交付文件及 SHA-256。DEPENDENCIES.json 记录二进制实际链接的 Go 模块、模块校验和、Go 运行时和前端生产依赖集合；licenses/ 保留上游许可、NOTICE 和版权原文。前端集合可能大于最终打包代码，不宣称精确到 tree-shaking 后的组成，也不是正式 SPDX/CycloneDX SBOM。

包不包含 PostgreSQL 服务/扩展、Python 运行时/wheel、模型、客户数据、凭证或工具安装器。知识/向量、技能/工具、报价与审批通知不属于 SQLite 首版范围。现有仓库没有项目级 LICENSE；上游许可收集不替代本项目发行许可或对外发布审查。本包仅标记为 internal-validation-only。

安装测试验证独立目录中的二进制、嵌入页面、显式 SQLite 迁移、重复迁移和错误配置拒绝；测试子进程清空 PATH，不调用开发工具。不将此结果称为物理断网认证、真实租户联调或 Windows 服务安装认证。
