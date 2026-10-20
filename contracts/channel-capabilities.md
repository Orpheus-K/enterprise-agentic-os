# 钉钉与企微渠道能力核实

核实日期：2026-10-21（GMT+8）。关联 R01/R02/R08/R12、A06/A08、V07/V08。仅核对官方文档；未创建应用、访问租户、安装 SDK 或实现渠道。表中“文档支持”不是 AgentOS 已支持或现场验收通过。

## 功能矩阵

| 能力 | 钉钉企业内部应用机器人 | 企业微信 API 模式智能机器人 | AgentOS 接入约束 |
|---|---|---|---|
| 无公网入站接收 | 文档支持 Stream 主动连接；仍需公网出站 [D1] | 文档支持 WebSocket 主动连接，Bot ID/Secret 鉴权 [W1][W2] | 与完全断网部署分开；部署前核对出站目的地 |
| URL 回调 | 文档支持 HTTP/Webhook [D1] | 文档支持 URL、Token、Encoding-AESKey 配置 [W2] | 实现后必须验证来源、解密、防重放及应用绑定 |
| 单聊与群聊消息 | 单聊可接收；群聊仅接收 @机器人消息 [D2] | 官方接入指南列出直接对话和群内 @机器人 [W1] | 不推断可读取全群历史；群聊仍默认关闭 |
| 回复和主动发送 | 通过 Webhook/OpenAPI；不能通过 Stream 通道回复 IM [D3] | 指南说明通过同一 WebSocket 回复；列出异步推送 [W1] | 独立建模发送意图、未知结果和核对；不能推断 exactly-once |
| 流式状态 | 卡片更新可实现打字机效果 [D4] | stream 使用相同 ID 全量替换并结束 [W1] | 卡片更新不是增量文本；长审批须持久化，不依赖流式窗口 |
| 交互卡片/按钮 | 文档支持；高级卡片创建时选择 STREAM 回调，普通版 Stream 回调不支持 [D4] | 本次未取得企微卡片 API 正文，待核实 | 不由钉钉能力推断企微支持；回调必须绑定审批版本和当前身份 |
| 身份与业务授权 | 应用凭证可调用获准 OpenAPI [D3]；不等于员工知识权限 | Bot 凭证与可见范围有文档 [W2]；不等于员工知识权限 | 实施前分别验证租户/应用/成员映射、撤权和内容 ACL |
| 完整接收者集合证明 | 本次材料没有证明群成员完整集合及交付时一致性 | 同左 | 群 ID、@列表、机器人可见范围均不能替代接收者授权证明 |
| 历史上下文/文件 | 回调可含文件 downloadCode [D2]；全量历史读取待核实 | 本次未核实历史/附件 API 的权限与限额 | 不宣称任意历史读取；文件下载、版本及 ACL 单独认证 |
| 宜搭/文档授权读写 | 宜搭授权范围、版本与撤销传播待 API 专项核实 | 文档能力不可由机器人收发能力推出 | 本次不承诺 R01 中完整业务资产集成 |
| 重连、重投与限流 | Stream 是传输能力；本次不声明重投次数、顺序或发送幂等承诺 | 官方指南有心跳/重连描述，API 精确错误与重投契约待核实 | 本地 inbox 去重、租约和 outbox 仍是必须实现的事实链 |

## 版本和证据边界

- 钉钉对象是企业内部应用机器人；不混同仅以 Webhook 推送的群自定义机器人。D4 明确普通版与高级版的回调差异，实施时须固定卡片 API 版本。
- 企微对象是 API 模式智能机器人；不把自建应用、传统群机器人、微信客服能力合并。W1/W2 是腾讯官方产品接入指南，证明其列出的接入能力；其中产品自己的超时/重连策略不作为企微通用 SLA。
- 企微原始开发文档 [智能机器人长连接](https://developer.work.weixin.qq.com/document/path/101463)、[群机器人](https://developer.work.weixin.qq.com/document/path/91770)、[应用消息](https://developer.work.weixin.qq.com/document/path/90236) 本次抓取失败。未使用第三方镜像补齐或猜测正文；卡片、精确限额、回执、群成员证明等保留“待核实”。
- 所有真实租户验证均未运行。后续准入至少覆盖：跨租户/应用拒绝、重复事件只建一次任务、断连恢复、未知发送不重放、卡片旧版本拒绝、成员撤权和群成员变化。没有完整范围证明继续拒绝群聊。

## 官方来源

- D1：[钉钉 Stream 概述](https://opensource.dingtalk.com/developerpedia/docs/learn/stream/overview/)；用于传输方向、出站依赖与回调类型。
- D2：[钉钉机器人接收消息](https://open-dingtalk.github.io/developerpedia/docs/learn/bot/appbot/receive/)；用于单聊、群内 @ 与文件下载码边界。
- D3：[钉钉机器人回复/发送](https://open-dingtalk.github.io/developerpedia/docs/learn/bot/appbot/reply/)；用于 Webhook/OpenAPI 发送方式。
- D4：[钉钉互动卡片概述](https://open-dingtalk.github.io/developerpedia/docs/learn/card/intro/)；用于卡片更新及版本/回调方式差异。
- W1：[腾讯官方企业微信智能机器人接入指南](https://intl.cloud.tencent.com/zh/document/product/1316/84454)，页面更新 2026-09-30；用于长连接、单群聊及流式语义。
- W2：[腾讯官方企微助理接入](https://cloud.tencent.com/document/product/1831/134441)，页面更新 2026-07-20；用于长连接/URL 模式、凭证及可见范围。
