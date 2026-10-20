# Linux/amd64 SQLite 核心离线包

关联 R19、A03/A10、V15。内部验收包，不是完整 M3 发布物。Go 二进制为 CGO_ENABLED=0 的 Linux/amd64 构建；不包含 PG、Python Worker、模型、现场配置或凭证。业务仍需要可达的飞书及模型服务。

1. 将整个包复制到新的本地目录。数据库和配置放在包外并限制访问。不要覆盖运行中的版本。
2. 现场使用 Python 3.10+ 标准库运行 `python3 verify.py`。验包器需要 Python；两个 Go 二进制本身不需要 Python、Node、Go 或包管理器。失败即停止。MANIFEST.json 必须通过可信交付渠道核对，未签名哈希不能防止清单与文件同时被替换。
3. `chmod 700 bin/agentos bin/agentos-migrate`；分别运行 `--version`，核对 MANIFEST.json 中的 revision、modified、平台与 schema。modified=true 不等于干净发布。
4. 停止目标库的所有使用者，备份原数据，清除 `AGENTOS_MIGRATION_DATABASE_URL`；创建数据父目录后执行 `./bin/agentos-migrate --sqlite /absolute/data/runtime.db`。启动不会自动迁移。
5. 按 `docs/deploy/sqlite/README.md` 准备配置与身份，设置 `AGENTOS_CHANNEL_CONFIG`、外部凭证环境变量和 `AGENTOS_HTTP_ADDR=127.0.0.1:8080`，运行 `./bin/agentos`。没有配置有效租户时 `/api/ready` 必须是 503。

升级回滚须配套匹配版本的数据库快照，不能把旧二进制指向新 schema。备份恢复边界见包内 SQLite 部署文档。

SBOM.cdx.json 是 CycloneDX 1.6 组件清单：实际链接 Go 模块、Go 运行时、前端生产依赖保守超集及二进制 SHA-256；未声明精确 tree-shaking 归属或传递依赖关系。DEPENDENCIES.json 与 licenses/ 保存校验和、许可原文位置和哈希。Go h1 保留为模块校验和属性，不冒充压缩包 SHA-256。项目发行许可、签名和正式发行审查不在本包范围；release_status 保持 internal-validation-only。
