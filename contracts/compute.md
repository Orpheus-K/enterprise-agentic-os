# 无状态文档 Worker

关联 R14、A01/A09；协议单点为 [compute.proto](compute.proto)。当前仅库层与合成 loopback 服务测试，CLI 未启用，无真实对象存储接入、容器隔离或业务任务接线。

`document.extract.v1` 接受 PDF、DOCX 的不可变对象引用，不在 RPC 传输文档。执行绑定租户、任务、步骤、运行版本、租约世代、请求 ID、Trace ID、能力版本、绝对截止时间；同时强制 RPC 截止时间不超过 30 秒。配置 `rpc.SERVER_OPTIONS` 限制消息 32 KiB，处理器亦核对实际大小。

可信 `ObjectAccess` 必须校验短期 grant 的完整执行绑定、准确输入/输出范围、到期与当前撤销状态；每次读取/暂存都重新授权并执行传入的取消检查。不能把任意 URL、文件路径或通用密钥当 grant。读取必须在分配前实施 8 MiB 限额。Worker 再验输入长度/SHA-256，暂存前后核对授权，输出回执必须匹配实际字节摘要、长度和类型。

输出是 canonical JSON 文本块，最多 1 MiB；PDF 最多 100 页、DOCX 归档最多 1024 项/16 MiB 展开。拒绝加密/活动 PDF、外部文档关系、宏/嵌入内容、路径穿越与 XML 实体。DOCX 正文段落/表格按出现顺序提取；没有 OCR、完整页眉页脚/嵌套表格或布局保真承诺。解析检查和截止时间是协作式边界，不能替代进程 CPU/内存隔离。

对象适配器按执行请求和内容摘要实施不可变、幂等暂存；结果对象到期回收由对象存储负责。Worker 不拥有任务、审批或交付状态，也没有重试业务副作用。Go 消费者在推进任务前必须重验当前租约、运行版本、授权与对象摘要；取消后已暂存的对象不能凭存在即视为完成。当前未实现该业务消费者。

错误映射：非法输入/无截止时间为 INVALID_ARGUMENT，撤权为 PERMISSION_DENIED，摘要不符为 DATA_LOSS，繁忙为 RESOURCE_EXHAUSTED，超时/取消分别为 DEADLINE_EXCEEDED/CANCELLED。未知后端失败返回 INTERNAL，不携带原始异常、grant 或文档内容，不返回假结果。

生产 `--serve` 始终拒绝 `isolation_unavailable`；未实现可信隔离提供方，环境标记不构成隔离证明。测试通过注入合成对象适配器注册本机 gRPC，不能视为部署认证。
