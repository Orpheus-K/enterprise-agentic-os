# 中文合成检索评测（A05、V12）

框架为 `internal/retrievaleval`；CLI `cmd/retrieval-eval` 只运行离线字符二元组 Jaccard 基线，不调用模型、真实来源、pg_search 或 BM25。语料为 8 篇人工合成文档、6 个查询；相关性标签固定在 [语料](../services/core/internal/retrievaleval/testdata/chinese-synthetic.json)，涵盖中文直接匹配、同义表达、租户、成员 ACL、未发布、来源不健康和无授权证据。

在 services/core 运行：

```sh
go run ./cmd/retrieval-eval -corpus internal/retrievaleval/testdata/chinese-synthetic.json -k 3
```

2026-10-21 合成结果：语料规范 JSON SHA-256 `e2faae0967c9a217237a3b1fa41bce04f3bec3669802e657d3f3b22a7adaf7a6`，后端 `synthetic-character-bigram-v1`。

| 指标 | 结果 | 分母 |
|---|---:|---|
| Macro Recall@3 | 0.616667 | 各查询所有标注相关文档，6 个查询取均值 |
| Macro Filtered Recall@3 | 0.800000 | 当前可见的标注相关文档，5 个非空查询取均值 |
| 过滤后越权命中 | 0 | 返回结果逐个核对租户、成员、发布和健康条件 |
| 无可见相关文档 | 1 个查询 | filtered_recall 为 null，不伪造 0 或 1 |

两个召回指标分母不同，不能把数值差当作过滤带来的质量提升。维修/保养两个同义查询各漏召回一个相关文档，明确暴露词面基线局限。该结果不认证真实模型、生产 PG 检索质量、吞吐量或来源权限时效。

框架接受独立 Retriever 适配器，分别运行未过滤和过滤检索，输出每条查询命中列表与指标；重复/未知 ID、超过 k 的结果及后端错误直接失败。授权过滤应在排序前执行。负面对照故意关闭过滤时越权计数必须上升，防止只看高召回掩盖泄漏；生产检索不调用这个测试用可见性函数，仍遵守知识模块/RLS 的真实授权。

语料和 ACL 以副本传入适配器，相关性答案不提供给被测后端；变异测试验证适配器不能修改评分事实。另有先截断 top-k 再过滤的负面对照：越权数虽为零，过滤召回降至零，证明单看无泄漏不足以判定召回正确。

pg_search/BM25 保持 A05 候选，本次无扩展安装或实现。候选认证仍需：固定版本及许可审查、中文分词/同义词与零命中样本、同一语料和过滤条件下的召回对比、RLS/预过滤负例、扩展升级回退与备份恢复。没有这些证据前不得写入支持矩阵或默认部署。
