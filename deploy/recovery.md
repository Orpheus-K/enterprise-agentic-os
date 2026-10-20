# 联合备份与恢复管理

关联 R19、A10、V15；格式、安全边界和驱动行为以[联合恢复契约](../contracts/recovery.md)为准。`agentos --recovery` 是本地管理员命令，不启动 HTTP、Worker 或任何真实渠道操作。旧数据库单独备份命令保持原含义。

## 准备可信现场输入

1. 从[SQLite](recovery.sqlite.example.json)或[PG](recovery.postgres.example.json)示例建立站点计划，使用绝对路径；设置 `AGENTOS_RECOVERY_CONFIG` 指向该计划。计划、密钥登记表、备份和恢复父目录仅允许受信任管理员写入。
2. 按[密钥登记表示例](recovery.keys.example.json)登记配置中的每个环境变量名、外部 ID、固定版本和 enabled。登记表不接受密钥值字段。实际值由批准的密钥渠道注入同名环境变量，不写入清单、命令参数、终端历史或仓库。备份包装密钥必须是标准 Base64 编码的 32 字节独立随机密钥。
3. 管理入口提取数据库 URL、飞书凭证、模型/Embedding、运维 Token、工具凭证、来源 Token 及全部 OAuth 加密密钥引用，逐项核对当前登记及可用性。PG 同一备份快照还检查持久 OAuth 密文所需 key_id，缺配置映射或密钥引用就失败；不会只检查当前激活密钥而漏掉旧密文的密钥。
4. PG 备份管理员和渠道运行配置必须指向同一主机/端口/库；需要 PG 17 原生客户端和已部署的角色、pgvector 0.8.7。恢复管理员可指向同一集群的管理库，目标库名必须全新。管理凭证引用在计划里，不导出管理密码。
5. 恢复前维护独立当前授权源并冻结权限变更。SQLite 的 authority_sqlite_path 不得位于待恢复包内；PG 使用 authority_url_env。该源必须包含备份之后的撤权事实，不能是旧备份的另一个副本。缺当前事实或缺密钥存储时拒绝恢复；不凭备份自证安全。

环境变量适配器只保证引用核对和不导出明文；密钥登记表与环境值版本的真实性由现场管理员维护。不得在保持版本不变时替换值。禁用版本立即阻止后续检查；包装密钥轮换使用新环境变量绑定，旧版本如需恢复须保留独立绑定。所有引用的当前版本必须匹配备份记录，不能静默升级到 latest。

## 执行命令

PowerShell 示例只含路径，不含密钥：

```powershell
$env:AGENTOS_RECOVERY_CONFIG='C:/AgentOS-secure/recovery.json'
'{"action":"backup","directory":"E:/backups/site-20261021"}' | ./agentos.exe --recovery
'{"action":"restore","directory":"E:/backups/site-20261021","target_directory":"E:/recovery/site-new"}' | ./agentos.exe --recovery
```

SQLite WAL 在线备份在 backup 请求增加 `"online":true`；原库须已配置 WAL。默认使用停写备份。PG 不接受 online 字段为 true，使用导出快照保障数据库内部一致性。

PG 恢复请求增加新的目标数据库名：

```json
{"action":"restore","directory":"/backups/site-20261021","target_directory":"/recovery/site-new","target_database":"agentos_recovered_site"}
```

请求仅从有界 stdin JSON 读取，未知字段、混用参数、已有目标、当前密钥禁用/缺失或版本不符均报错。每次核对都重新读取外部密钥登记表。备份绑定当前 `agentos` 可执行文件的 SHA-256 和驱动/schema；恢复必须使用对应制品，不从备份清单猜测兼容性。保存原离线包供恢复使用。

## 恢复结果与激活边界

备份目录包含数据库制品及加密配置。恢复不依赖原 channel_config 文件仍存在；解密后重新执行配置解析和密钥引用完整性检查。成功输出 `verified:true`、`runtime_start_allowed:false`；PG 另输出 `database_quarantined:true`。

SQLite 成功目标包含 database.sqlite 与 configuration.json；PG 创建隔离数据库并输出配置目录。恢复配置保留原字节，不自动改写 SQLite 路径、数据库环境引用或执行版本。现场重定位、外部写入核对、恢复密钥来源、当前授权复核和权限变更冻结窗口都须在启用运行时前完成。PG 成功库仍保持 connection limit 0 且没有普通角色 CONNECT；只有完成现场复核后才由管理员显式解除隔离。命令不自动做该激活。

输出失败不代表恢复副作用未发生，错误会要求检查目标；禁止盲目重试已有目标。PG 创建结果未知或清理失败时保留隔离并人工核对。部分备份目录没有有效联合封装时不得交付。备份哈希与 GCM 认证不替代介质访问控制，数据库 dump 本身不由本封装加密。

本地合成配置/密钥及真实 PG/SQLite 测试证明此管理流程；没有接真实租户或外部密钥服务，也不声明现场 RTO/RPO 或自动服务恢复。
