# 构建与验证离线包

当前切片为 Windows/amd64 SQLite 核心包，关联 R19、A03/A10、V15。构建机使用已锁定的 pnpm/Go 工具链和本地依赖缓存；目标机只需系统 PowerShell 和包中二进制。包内容和安装边界见 [INSTALL](INSTALL.md)。

```text
pnpm package:offline E:\artifacts\agentos-new
pnpm test:offline E:\artifacts\agentos-new
```

输出父目录须已存在、目标目录不得存在。命令重新构建前端与两个 Go 二进制，拒绝旧 revision 或不同平台的二进制。Go 许可源从实际链接模块确定，按嵌入校验和核对本地缓存，禁止为收集许可证隐式下载模块；缺许可原文、替换模块、版本冲突会失败。失败可能留下不完整目录；没有成功日志且未通过 Verify.ps1 的目录不得交付。

前端许可集合使用 `pnpm licenses list --prod --json`，保守包含 workspace 生产依赖；原文递归保留 LICENSE/COPYING/NOTICE/COPYRIGHT/PATENTS/AUTHORS。Go 运行时原文来自构建工具链。DEPENDENCIES.json 提供版本、来源相对路径和文本摘要，MANIFEST.json 提供全部交付文件摘要及二进制身份；不包含本机模块缓存绝对路径。

测试复制到独立目录，用空 PATH 启动二进制，验证文件变更/缺失/额外文件拒绝、身份、SQLite 显式与重复迁移、备份恢复与覆盖拒绝、目标冲突、嵌入静态资源和缺租户就绪拒绝；仅使用本机回环 HTTP。它不替代真实租户联调、配置/密钥联合恢复或完整安装环境矩阵。Linux、服务注册、发布签名、正式 SBOM、本项目发行许可与完整 Python/PG 离线包仍待后续交付切片。
