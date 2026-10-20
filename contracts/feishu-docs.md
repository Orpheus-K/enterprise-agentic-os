# 飞书云文档读取契约

关联 R02/R10/R12、A06/A08、T08/T09、V07/V08。当前实现为 `services/core/internal/feishudocs` 读取与基础文本规范化库层，尚未接入配置、来源同步、ACL 发布或向量发布。知识存储与消费者边界继续以 [知识契约](knowledge.md) 为准。

## 身份与权限

- 只接受管理员可信绑定的 AppID、OpenID、TenantKey 和 user access token。Reader 固定 AppID；每次操作通过 user_info 核对 OpenID、TenantKey，调用方不得用请求参数自行指定员工绑定。
- user_info 返回应用范围的 OpenID，不直接证明配置中的 AppID。应用绑定属于可信凭证配置的责任；本库拒绝跨 Reader AppID 的凭证。
- 使用原生 permission/member/auth 的 `docx/view` 检查当前 token 对应用户；应用可读和协作者枚举均不能推导员工有效 ACL。CheckAccess 只提供该用户此刻的访问检查，不读取正文，也不保证未来访问。
- 每次请求显式使用 user token，关闭 SDK token cache，不获取应用 token、不自动重试或换身份。生产地址固定为 `https://open.feishu.cn`，拒绝重定向。

## 完整快照

Read 先验身份和权限，再取文档元数据，将所有分页固定到初始 revision。分页结束后检查块树完整性，再重读元数据、身份和权限；版本或标题变化、身份错配、撤权、缺页、坏游标、重复块、孤立块及超限均返回错误和空快照。

返回文档 ID、revision、标题和完整原始块对象，保留未知字段。读取成功不代表块内容已被支持；后续规范化必须显式支持内容类型或拒绝，不能静默丢弃图片、附件、表格或未知块。

限制：单 HTTP 响应 2 MiB，累计原始块 8 MiB，4096 块、32 页，每页请求 500 块；单 HTTP 请求 15 秒，整个操作 30 秒。游标只在本次固定版本读取中使用；失败后丢弃整个快照，重新开始，不发布部分内容。

## 失败与未实现边界

错误仅暴露分类，不包含 token、原文或供应商错误正文；SDK 日志关闭。输入无效、身份/权限拒绝、版本变化、快照超限分别失败，HTTP/协议/凭证过期等不可确定状态归为来源不可用。来源不可用不能解释为删除或全员撤权。凭证默认 JSON 与格式化输出不包含密钥。

尚未实现：OAuth 获取/刷新、内部 IAM 映射、完整块类型支持、持久游标与同步恢复、事件订阅与撤回处理、ACL/健康状态原子发布、Embedding/向量入库以及真实租户联调。本切片的合成 HTTP 测试不证明上述能力或飞书线上权限配置。

## 基础文本规范化

Normalize 仅接受页面、文本段落、1—9 级标题的 text_run 内容；输出 knowledge.Content，但不调用存储、不生成 ACL。未知块、未知内联元素或内容字段拒绝整份文档。当前列表、代码、公式、待办、表格、图片、提及、附件和同步引用块均未支持；包含这些内容的文档不能发布。链接及删除线文本也拒绝，避免把缺失链接目标或已删除的表述当作普通断言。

内容按 children 指定的深度优先顺序输出，不依赖分页返回顺序。每个非空源块生成一个切片，ID 使用源 block_id，原文锚点为 `feishu-docx:<document_id>:<revision>:<block_id>`，是版本绑定的不透明定位符，不是可点击链接。连续 text_run 直接拼接，保留内部换行和字词，不把样式边界变成空格；仅清除块首尾空白。页面标题文本也作为源块保留。

限制沿用知识存储：标题 512 UTF-8 字节，单块正文 16000 字节，最多 256 个非空切片；超限失败，不截断正文、不静默少取切片。空文档不生成假正文。规范化前重新检查块树、快照大小及 UTF-8，任何失败只返回空 Content。更复杂内容与大块拆分需后续实现并测试后再扩大支持范围。

## 协议依据

使用仓库固定的官方 Go SDK `github.com/larksuite/oapi-sdk-go/v3 v3.12.2`：

- [检查应用或用户是否具有文档权限](https://open.feishu.cn/document/server-docs/docs/permission/permission-member/auth)。
- [获取文档基本信息](https://open.feishu.cn/document/server-docs/docs/docs/docx-v1/document/get)。
- [获取文档所有块](https://open.feishu.cn/document/server-docs/docs/docs/docx-v1/document/list)。

测试通过本地 HTTP 服务覆盖协议交互、权限变化、身份变化、固定版本分页、完整性、资源上限、取消、错误脱敏及重定向拒绝；不向真实租户发送请求。
