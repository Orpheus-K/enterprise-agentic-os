# 构建与验证离线包

当前支持 Windows/amd64 和 Linux/amd64 SQLite 核心包，关联 R19、A03/A10、V15。构建机使用已锁定的 pnpm/Go 工具链和本地依赖缓存；Windows 验包使用系统 PowerShell，Linux 验包使用 Python 3.10+ 标准库，Go 二进制不需要这些运行时。安装边界见 [Windows](INSTALL.md) 和 [Linux](INSTALL-linux.md)。

```text
pnpm package:offline E:\artifacts\agentos-new
pnpm test:offline E:\artifacts\agentos-new

# Windows PowerShell：显式选择已有 WSL 发行版，实际执行交叉构建的 Linux 二进制
$env:AGENTOS_WSL_DISTRO='Ubuntu'
pnpm package:offline E:\artifacts\agentos-linux linux-amd64
wsl -d Ubuntu -- python3 /mnt/e/path/to/repository/scripts/test-offline-linux.py /mnt/e/artifacts/agentos-linux

# Linux 构建机：原生构建与独立安装验证
pnpm package:offline /absolute/artifacts/agentos-linux linux-amd64
python3 scripts/test-offline-linux.py /absolute/artifacts/agentos-linux
```

输出父目录须已存在、目标目录不得存在。默认目标为宿主 Windows/amd64 或 Linux/amd64；仅支持 x64 构建机。构建显式设置 GOOS/GOARCH 与 CGO_ENABLED=0，写入 dist/<target>，不覆盖普通宿主构建。命令重建前端与两个 Go 二进制，并实际执行 --version，拒绝旧 revision 或不同平台；Windows 交叉打包 Linux 必须提供可执行该产物的 AGENTOS_WSL_DISTRO，不从源码拼造身份。Go 许可源从实际链接模块确定，按嵌入校验和核对本地缓存，禁止为收集许可证隐式下载模块；缺许可原文、替换模块、版本冲突会失败。失败可能留下不完整目录；没有成功日志且未通过对应验包器的目录不得交付。

前端许可集合使用 `pnpm licenses list --prod --json`，保守包含 workspace 生产依赖；原文递归保留 LICENSE/COPYING/NOTICE/COPYRIGHT/PATENTS/AUTHORS。Go 运行时原文来自构建工具链。DEPENDENCIES.json 提供版本、来源相对路径和文本摘要，MANIFEST.json 提供全部交付文件摘要及二进制身份；不包含本机模块缓存绝对路径。

SBOM.cdx.json 使用 CycloneDX 1.6，打包时以固定的上游 JSON Schema 离线验证结构；schema 来源、版本和哈希见 [验证数据](../../scripts/vendor/cyclonedx/README.md)。组件包含 Go 链接模块、运行时、前端生产依赖及二进制 SHA-256；Go h1 是独立属性，不当作归档文件哈希。前端是保守超集，不声明精确 tree-shaking 组成或未知的传递依赖图。SBOM 同样纳入 MANIFEST 文件校验；这不是签名、漏洞扫描或正式发行许可。

测试复制到独立目录，用空 PATH 启动二进制，验证文件变更/缺失/额外文件拒绝、身份、SQLite 显式与重复迁移、备份恢复与覆盖拒绝、目标冲突、嵌入静态资源和缺租户就绪拒绝；Linux 额外验证符号链接拒绝，仅使用本机回环 HTTP。CI 分别在 Windows 和 Linux 上构建及安装。它不替代真实租户联调、配置/密钥联合恢复或完整安装环境矩阵。Windows 服务注册脚本见[独立运维说明](../windows/README.md)，仅通过替身测试，不包含在离线包。真实服务安装、发布签名、本项目发行许可与完整 Python/PG 离线包仍待现场或后续交付验收。
