# 执行项目约定

- 保持简洁：直接行动；只报告结果、证据与阻塞；不复述任务。
- 读取真源：[产品需求](enterprise-agentic-os-docs/README.md) → [架构决策](enterprise-agentic-os-architecture/07-架构决策与研发路线.md) → 当前代码；按任务选读，不全量加载。
- 对齐变更：关联相关 R/A/T/V 编号；发现冲突先说明，不把候选架构当已支持能力。
- 遵守规则：开发读取 [代码规则](.agents/rules/code.md)；涉及状态、授权或接口时加读 [架构规则](.agents/rules/architecture.md)。
- 选择技能：实现使用 [agentos-develop](.agents/skills/agentos-develop/SKILL.md)；验证使用 [agentos-test](.agents/skills/agentos-test/SKILL.md)；排错升级使用 [agentos-maintain](.agents/skills/agentos-maintain/SKILL.md)。
- 接续迭代：跨步骤开发、测试与维护先读 [项目笔记](.agents/NOTES.md)，按 [笔记规则](.agents/rules/notes.md) 在里程碑、阻塞或交接时更新。
- 限定范围：实现最小完整变更；不夹带重构、不创建客户核心分支、不增加未证实的兼容层。
- 拒绝伪成功：显式失败；不吞异常、不返回假数据、不降级权限；保留已定义的恢复与人工核对。
- 检查事实：核对实际代码与清单；构建命令从 manifest 与 CI 获取，不虚构脚本、服务或测试通过。
- 更新单点：规范只维护一处，其他文件引用；仅更新受影响的需求、决策与验收。
- 完成即停：相关检查通过后结束；未运行项说明原因；不得把文档检查称为产品验收。
