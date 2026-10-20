# Windows 服务注册脚本

关联 R19/R15、A10、V15。脚本需要 PowerShell 7。当前 Go 核心是控制台进程，不能直接用 `sc create` 当作 Windows 服务。脚本使用管理员事先取得的 WinSW v3 全局命令接口；不下载、不打包或认证第三方 wrapper，不把脚本测试称为 SCM/停机恢复验收。

1. 先按[离线安装](../offline/INSTALL.md)验包、迁移、配置并以前台进程检查 readiness。停止前台进程后再准备服务，避免两个执行者共用安装。
2. 从 [WinSW 官方发布](https://github.com/winsw/winsw/releases)取得经安装方审核的 v3 制品，独立确认 SHA-256；调用参数遵循[官方 CLI](https://github.com/winsw/winsw/blob/v3/docs/cli-commands.md)。wrapper 不包含在当前离线包，也不新增运行时自动下载。
3. 将核心、wrapper、配置和服务 XML 的目录限制为管理员可写。授予 `NT AUTHORITY\LocalService` 读取制品/配置、写入指定数据库和日志目录的最小权限；脚本不自动修改 ACL。密钥由安装方注入该账户可读取的受保护密钥源，不将密钥写入命令参数或 XML。服务不继承交互用户的进程环境；确认配置引用的变量在服务上下文真实可用。
4. 运行下例先输出计划，默认不写文件、不注册、不启动服务。路径必须绝对且不能含环境变量展开符 `%`；配置正文不会复制进 XML。

```powershell
.\deploy\windows\Register-Service.ps1 `
  -Name AgentOS `
  -Executable C:\AgentOS\agentos.exe `
  -Configuration C:\AgentOS\channel.json `
  -Wrapper C:\AgentOS\WinSW.exe `
  -WrapperSHA256 <经独立核验的64位小写摘要> `
  -ServiceXML C:\AgentOS\service.xml
```

5. 安装方核对计划后，在管理员 PowerShell 用同一组参数增加 `-Register`。固定 LocalService、手动启动、回环 `127.0.0.1:8080`；只执行 `install <xml> --no-elevate`，不会弹出提权或启动服务。XML 以 CreateNew 写入，已有文件拒绝覆盖；安装失败或结果未知保留 XML，先核对 SCM 和 wrapper 日志，不自动重新注册。
6. 正式启动由安装方执行 `WinSW.exe start C:\AgentOS\service.xml --no-elevate`，核对 `/api/ready`、当前授权及审计后再开放流量。停机/卸载使用同一 wrapper 与 XML 的 `stop`/`uninstall`，保留数据库、配置和未知操作证据；这些真实服务动作本轮未执行。

[配置语义](https://github.com/winsw/winsw/blob/v3/docs/xml-config-file.md)由上游定义。强制终止、系统重启、账户环境、ACL 和在途任务恢复仍须在目标 Windows 安装中验收；本脚本不声称解决这些运行问题。脚本测试：`pwsh -NoProfile -File deploy/windows/Service.Tests.ps1`，所有命令调用均注入替身，不执行测试 `.exe` 或访问 SCM。
