# Design：解除 Codex 配额调度器对插件分组的依赖

## Context

CLIProxyAPI 的 Scheduler 请求已经包含请求 provider、接受的 providers 和候选账号。宿主侧 `codex-groups` 会在调用插件前根据当前启用组过滤 Codex OAuth candidates。插件不需要知道分组名称、分组 ID 或组成员关系，也不需要扩展 ABI。

当前插件则在自身快照中保存 `ActivePool`，并把账号 annotation 中的 `GroupID` 作为调度资格。该边界会让插件分组与宿主分组产生两个来源。新的设计将插件分组降级为历史数据，只保留兼容读取和导出。

当前 Resource 页面只返回静态 shell，页面脚本在 `loadStatus` 时必须向 Management API 发送 CPA Management key。新的设计将只读配置和聚合状态拆成安全的公开 Resource 数据；详细账号数据和写操作仍保留 Management API 保护。

## Architecture

```text
CLIProxyAPI codex-groups.active
        │
        ▼
宿主过滤 Codex OAuth candidates
        │
        ▼
SchedulerPickRequest
  Provider / Providers / Candidates
        │
        ▼
codex-quota-scheduler
  只在传入 Codex candidates 中排序和选择
        │
        ├─ 选中 Codex OAuth Auth ID
        └─ Codex 无可用候选时复用现有 AI provider fallback

Resource 页面 ──无 key──> 安全配置与聚合状态
Management API ──有 key──> 账号明细、额度、日志与所有写操作
```

## Decisions

### 1. 宿主候选是唯一的分组边界

插件只把当前 `SchedulerPickRequest.Candidates` 中 `Provider` 为 `codex` 的候选交给现有选择逻辑。插件不得根据历史 `AccountAnnotation.GroupID`、`GroupAnnotation` 或 legacy `Config.ActivePool` 再做集合交集。

插件仍可使用自己的 quota snapshot、CPA admission、账号资格、插件优先级和选择策略对传入候选进行排序。宿主没有传入的 Codex 账号不得由插件从本地缓存补回候选。

这样可以保证：

- 启用组由宿主决定；
- 插件不会绕过宿主的 Codex 分组过滤；
- Scheduler 热路径只使用请求和不可变内存快照；
- 不需要在 ABI 中传递 group 字段。

### 2. Provider fallback 复用现有能力

插件继续通过 `Provider`、`Providers` 和候选的 `Provider` 字段识别 Codex 请求。非 Codex 请求保持未接管。

当请求包含 Codex，但宿主传入的 Codex candidates 为空，或传入的 Codex candidates 全部不可选时，插件不从历史分组或本地缓存补候选，而是按照当前 `fallback` 配置和宿主已声明能力复用现有 AI provider fallback。已有 `emergency-provider-fill-first` 能力继续按现有兼容逻辑使用；本 change 不新增委托值、不修改 ABI。

当 `fallback` 未启用或请求固定了 Auth ID 时，插件保持明确失败或未接管语义，不改选组外 OAuth 账号。

### 3. 历史分组和 active_pool 只保留兼容层

旧用户数据中的以下字段继续可读：

- `Config.ActivePool`；
- `AnnotationState.Accounts[*].GroupID`；
- `AnnotationState.Groups`；
- 旧导出文件中的对应字段。

兼容层只负责反序列化、持久化和导出，不把这些字段复制到有效调度资格集合。运行时 `ActivePool` 等价于不启用插件分组过滤；历史 group annotation 仍可随其他 annotation 被读取，但不能影响选择、刷新授权范围或 fallback。

新的 Settings UI 不展示或提交 `active_pool`。新的分组管理 UI 不再提供；旧分组写路由可以保留为拒绝兼容端点，并固定返回 `plugin_group_management_removed`，避免静默修改已经不再生效的数据。

非分组 annotation（别名、备注、标签、插件优先级）继续使用现有受保护接口。

### 4. 公开 Resource 只返回安全只读数据

Resource 页面在无 key 时可以读取安全的只读状态。推荐新增一个公开 Resource 数据路径，例如：

```text
GET /v0/resource/plugins/codex-quota-scheduler/status-data
```

该响应只包含：

- 插件 ID和生成时间；
- 调度启用状态；
- 当前选择策略和月度模式；
- 刷新是否活跃及固定状态分类；
- Codex 账号数量等聚合计数；
- roster 生命周期的能力、健康、确认状态和数量字段；
- fallback 能力是否由宿主声明支持；
- 可安全展示的配置值，不包含不必要的 endpoint 或凭据字段。

该响应不得包含：

- 邮箱、Auth ID、Auth Index、ChatGPT Account ID；
- quota window、reset credit、订阅到期等账号明细；
- 日志、最后选择的账号、逐账号错误；
- Token、Cookie、Authorization、认证文件路径、原始认证 JSON；
- 未清洗的宿主状态文本。

页面打开时自动请求该 Resource 数据。需要读取详细账号数据、日志或执行写操作时，页面再要求用户填写 Management key，并请求受保护的 Management API。

### 5. Management API 保持详细数据和写权限边界

现有 Management API 的详细 status、logs、settings、export、import、refresh 和 annotation 能力继续受 CPA Management 鉴权。`GET /settings` 可继续用于认证后的完整设置读取；公开 Resource 只使用专门的安全 payload，不能直接复用完整 `StatusPayload`。

分组管理路由不再提供成功语义；旧路由保留为拒绝兼容端点：

- 创建、删除、重命名分组请求；
- 批量归组请求；
- 修改账号分组请求；
- 切换 `active_pool` 请求。

上述请求均返回固定的 `plugin_group_management_removed`，且不改变状态。

别名、备注、标签和插件优先级的账号 annotation 更新继续保留，但请求中出现分组写字段时应拒绝或返回固定兼容错误。

### 6. 调度快照移除有效的分组字段

新的有效 `SchedulerSnapshot` 不再依赖 `ActivePool` 和账号 `GroupID` 进行选择。历史字段可以暂留在持久化或兼容结构中，但不能进入生产选择路径。

`PickDecision`、日志和 Management 聚合不再产生基于插件活动池的严格失败原因或 `active_pool_bypassed` 诊断。宿主已经过滤的候选数量可以作为普通 candidate/admitted 统计；分组选择结果由宿主负责解释。

### 7. 保持 roster、刷新和缓存覆盖范围

宿主候选过滤只约束本次 Scheduler OAuth 选择。插件既有的 roster 确认、quota refresh、usage feedback 和缓存生命周期不通过历史插件分组缩小或扩大授权范围，继续遵循当前 CPA roster 和插件内部生命周期规则。

如果后续宿主分组变更导致传入候选集合变化，插件在下一次 Scheduler 请求中直接使用新候选；不需要插件侧读取宿主配置或执行迁移。

## Error Handling

- 非 Codex 请求：保持 `provider_not_codex` 和未接管语义。
- Codex 请求但无传入 Codex candidates：按现有 `no_codex_candidates` 与 fallback 语义处理。
- 传入 Codex candidates 全部不可用：按现有 `no_selectable_account`、fallback 和 provider 委托语义处理。
- 旧分组写 API：返回 `plugin_group_management_removed`，不修改内存或磁盘数据。
- 公开 Resource：只返回安全 payload；构造失败时返回不含敏感字段的错误响应。
- Management key 缺失：公开读取不报错；详细读取和写操作继续返回宿主鉴权失败或插件既有 key required 提示。

## Testing Strategy

- 验证历史 `active_pool` 和 GroupID 存在时，选择结果完全由传入 candidates 决定。
- 验证宿主已过滤候选后，插件不会从本地账号缓存补回组外 Codex OAuth。
- 验证 `Provider`、`Providers`、混合 provider、无 Codex candidates 和 provider fallback。
- 验证非 Codex 请求不被插件接管。
- 验证公开 Resource 无 key 可读取安全 payload，并阻断邮箱、Auth ID、额度、日志和凭据字段泄露。
- 验证详细 status、logs、export、import、refresh 和 annotation 写操作仍需要 Management key。
- 验证分组管理控件、`active_pool` 控件和分组写路由不再提供。
- 验证旧状态可读取、可导出且导入不会重新启用插件分组调度。
- 验证 scheduler.pick 没有磁盘、Host Auth 或网络 I/O。

## Compatibility and Rollback

旧状态无需破坏性迁移。插件升级后仍可加载和导出历史分组数据，但这些数据不会影响调度。回滚到旧插件时，旧插件可能重新解释 `active_pool`；因此升级说明应提醒用户旧版本的分组语义与新版本不同。

本 change 不改变 CLIProxyAPI 的 ABI。宿主 `codex-groups` 必须先具备候选过滤能力；在宿主尚未具备该能力时，插件只能看到宿主传入的全量候选，无法提供宿主分组语义。
