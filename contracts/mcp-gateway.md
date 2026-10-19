# 调用受控 MCP 工具

对齐 R07/R09/R15、A04/A08、V05/V06/V10。当前完成 Go 网关、Worker 有界只读工具循环与真实协议/数据库验证；不能宣称报价审批闭环完成。

## 固定协议与依赖

- 复用 [官方 Go SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0)，锁定 MCP `2026-07-28` 与 Streamable HTTP；协商结果不一致则停止，不降级旧协议。
- 关闭：SDK 自动多轮请求、重连重试、独立 SSE；不注册 sampling/elicitation 回调，不提供文件根目录，不启动 stdio 子进程。
- 校验：复用 [jsonschema-go v0.4.3](https://github.com/google/jsonschema-go/tree/v0.4.3)，不加载外部引用。依赖锁定见 go.mod/go.sum，直接依赖许可证保留于 services/core/licenses。

## 注册与披露

- 声明 Spec：本地名称、远端名称、固定 Endpoint、描述、负责人、风险、超时和输入/输出 JSON Schema。
- 固定 Revision：SHA-256(json.Marshal(Spec))；更新目标、契约或风险会生成新版本，必须重新授权并更新技能依赖。
- 限制目标：HTTPS，无 URL 用户信息、query 或 fragment；HTTP 仅允许显式启用的字面环回地址测试。拒绝重定向、备用路径与系统代理；超时 1—60 秒。
- 收紧契约：输入/输出根类型必须 object 且 additionalProperties=false；Schema ≤16 KiB，调用参数 ≤16 KiB。拒绝通过参数增加 URL、头部或凭证字段；嵌套字段由批准的 Schema 约束。
- 暴露定义：Definitions 按当前技能和工具授权返回名称、版本、描述与输入契约，不返回目标地址或凭证。当前仅开放 read；write 明确返回受限错误，等待持久执行和审批接入。

## 隔离调用

1. 接收可信 Adapter 已验证的 ExternalIdentity、固定技能/工具 Ref、稳定 operation UUID 和 JSON 参数；这不是接受任意身份头的公开 HTTP 接口。
2. 解析当前 IAM，检查安装租户、技能权限、技能工具依赖及精确工具版本授权，验证参数；失败不调用目标。
3. 按 tenant/member/完整 Endpoint 的管理员委托映射读取环境变量凭证。没有匹配不借用公共 Token；不透传来访 Bearer，不自动进行 OAuth 登录/发现。当前是部署引用型凭证能力，不代表完整 Vault/KMS 或 OAuth scope 认证实现。
4. schema 9 的 tool_operations 先记录 started 和输入摘要。同安装 operation ID 只允许首次启动，重复一律拒绝，不再次发送，也不从摘要编造历史结果。
5. 新建隔离的 MCP 会话；发出 tools/call 前重新核验身份与权限。凭证只在固定目标 HTTP 传输层注入，模型/技能不接触密钥。
6. 仅接受非错误、非交互请求的 structuredContent，限制响应流 ≤256 KiB、结构化结果 ≤64 KiB；按输出契约校验，丢弃原始文本与附件。发现目标凭证原文回显则拒绝，不声称通用 PII 脱敏已完成。
7. 返回前再次授权；先记录 succeeded/rejected/unknown/denied 与输出摘要；schema 10 在成功事务内同时保存结构化结果，再返回结果。审计失败不返回结果，保留 started 供后续核对。未知结果不重试。

## 审计与验证

- 表保存身份/安装/技能/工具版本、操作 UUID、状态、摘要、Trace ID 和时间；schema 10 另外保存成功结果正文用于恢复，属于受保护业务数据，不写日志或诊断包。参数保存在任务意图中，令牌不落库。强制租户 RLS，当前由内部网关读写，不新增公开查询端点。
- Gateway 崩溃后 started 不代表未执行；Result 仅按原身份、安装、技能/工具版本、操作 UUID 和参数摘要读取 succeeded 结果，并重新授权。没有成功结果则 unknown，不重发；当前无工具核对/补偿 API。
- [集成测试](../services/core/internal/toolgateway/gateway_integration_test.go)：真实 PG＋官方 SDK HTTP 服务端，覆盖成功调用、凭证注入与定义隔离、跨租户/安装/权限、撤权、连接丢失、无重试、重定向、凭证回显、并发操作 ID 与审计失败；合成数据，不调用企业真实系统。
- 边界：远端 read 标签由管理员审查保证，不是网关证明其无副作用。当前未实现写批准、REST/SOAP 适配、限流配额或生产 HA。
