# Proposal：解除 Codex 配额调度器对插件分组的依赖

## Why

`codex-quota-scheduler` 当前同时维护插件自有分组、`active_pool` 和账号库存分组逻辑。CLIProxyAPI 已将 Codex 分组管理放在宿主侧，并负责按照当前启用的 Codex 分组过滤 Scheduler 候选。两套分组来源会产生重复配置、语义不一致和额外的 Management 操作。

插件的职责应收敛为：在宿主传入的 Codex 候选中执行额度、可用性、套餐、插件优先级和选择策略排序；当 Codex 候选不可用时，依据请求中的 provider 信息复用现有 AI provider fallback。

当前调度器配置页还要求用户先填写 CPA Management key 才能加载配置和状态。配置页的基础配置、启用状态和非敏感聚合状态不需要写权限，应支持无 key 只读加载；账号身份、额度明细、日志和所有写操作仍必须受 Management key 保护。

## What Changes

- 停止使用插件自有分组和 `active_pool` 参与实际调度。
- 不再提供插件分组创建、删除、修改、批量归组和 `active_pool` 配置的成功 UI/API；旧写路由可保留为只返回兼容错误的拒绝端点。
- 继续兼容读取历史分组 annotation 和 `active_pool` 字段，并在导出数据中保留这些字段；它们不再影响调度。
- 将 CLIProxyAPI 宿主过滤后的 Codex candidates 作为唯一 OAuth 调度输入，不扩展 Scheduler ABI。
- 插件只选择传入的 Codex candidates；当 Codex candidates 不存在或全部不可用时，依据 `Provider`、`Providers` 和现有 fallback 能力处理 AI provider。
- 允许 Resource 页面无 Management key 加载调度器配置、启用状态和非敏感聚合状态。
- 账号邮箱、Auth ID、额度明细、日志、凭据相关信息和所有状态变更继续走受保护的 Management API。
- 保持 scheduler.pick 热路径无磁盘读写、Host Auth 回调、网络请求和插件分组查询。

## Goals

- 让 CLIProxyAPI 的 `codex-groups` 成为 Codex 调度分组的唯一来源。
- 保持插件现有 quota refresh、账号资格、选择策略、CPA priority 和 provider fallback 语义。
- 清理插件侧重复的分组调度边界，同时不破坏历史用户数据的读取和导出。
- 让用户进入调度器配置页即可查看安全的配置和聚合状态。
- 明确公开 Resource 与受保护 Management API 的字段边界。

## Non-Goals

- 不修改 `/Users/yidoll/Workspace/personal/cliproxy` 或实现宿主 `codex-groups`。
- 不扩展 Plugin ABI 或给 `SchedulerAuthCandidate` 增加 group 字段。
- 不在插件中重新读取宿主分组配置。
- 不允许插件依据历史分组 annotation 过滤候选。
- 不在未认证 Resource 中返回账号邮箱、Auth ID、额度明细、日志或凭据。
- 不改变 quota endpoint、quota refresh、套餐过滤或选择策略的既有业务定义。

## Impact

- 插件配置解码、持久化兼容和导入导出需要识别 legacy `active_pool`，但运行时必须忽略其调度语义。
- Scheduler snapshot、选择逻辑、决策诊断和相关测试需要移除活动池过滤依赖。
- Management API/UI 需要移除插件分组管理和 `active_pool` 控件，同时保留非分组 annotation 能力。
- Resource 页面需要增加或复用一个安全的公开只读状态响应。
- 详细账号状态和写操作仍依赖 CLIProxyAPI 的 Management 鉴权。
- CLIProxyAPI 的宿主级 Codex 分组过滤是本 change 的前置依赖，不属于本 change 的实现范围。
