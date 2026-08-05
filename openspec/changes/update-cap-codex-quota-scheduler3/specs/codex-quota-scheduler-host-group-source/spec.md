# Codex 配额调度器宿主分组来源规格

## ADDED Requirements

### Requirement: 插件必须使用宿主传入的 Codex candidates 作为唯一调度输入

插件 MUST（必须）只在当前 `SchedulerPickRequest.Candidates` 中选择 `Provider` 为 `codex` 的候选。插件 MUST NOT（不得）使用历史插件分组、`active_pool`、本地缓存账号或其他插件自有集合补回宿主未传入的 Codex candidate。

#### Scenario: 宿主已经按启用组过滤候选

- **WHEN** 宿主调用插件，并且候选列表只包含当前启用 Codex 分组的 OAuth 账号
- **THEN** 插件只在这些传入候选中执行额度和选择策略排序
- **AND** 插件不读取或比较历史插件分组字段

#### Scenario: 历史插件分组包含宿主组外账号

- **WHEN** 旧状态中的插件分组 annotation 包含一个宿主没有传入的 Codex Auth ID
- **THEN** 插件不得选择该 Auth ID
- **AND** 插件不得从本地账号缓存将该 Auth ID补回候选

#### Scenario: 宿主传入空 Codex candidates

- **WHEN** 请求包含 Codex provider，但宿主传入的 Codex candidates 为空
- **THEN** 插件将请求视为没有 Codex 候选
- **AND** 插件按照既有 fallback 配置和 provider fallback 能力处理请求

### Requirement: 插件不得使用历史 active_pool 参与生产调度

插件 MUST（必须）继续兼容读取历史 `active_pool` 字段，但该字段 MUST NOT（不得）影响 scheduler snapshot、候选资格、排序、fallback 或重试。

#### Scenario: 旧状态包含严格 active_pool

- **WHEN** 插件加载的旧状态包含 `active_pool=group:legacy`
- **THEN** 插件将该字段作为历史兼容数据保留
- **AND** 当前调度结果只由宿主传入的 candidates 和现有 quota 状态决定

#### Scenario: 历史 active_pool 与宿主候选集合冲突

- **WHEN** 宿主传入的 Codex candidates 与历史 `active_pool` 成员不一致
- **THEN** 插件不得根据历史 `active_pool` 删除或补充候选
- **AND** 插件不得产生插件侧严格活动池失败

### Requirement: 插件必须保留现有 Codex 选择层次

在宿主候选集合内，插件 MUST（必须）继续遵循既有 CPA priority admission、套餐资格、可用性、插件优先级、选择策略和 Auth ID 稳定排序语义。

#### Scenario: 宿主传入多个可用 Codex candidates

- **WHEN** 传入候选中存在多个满足既有资格的 Codex OAuth 账号
- **THEN** 插件按照现有选择层次选择一个 Auth ID
- **AND** 历史插件分组不改变排序结果

#### Scenario: 传入候选中包含非 Codex provider

- **WHEN** 请求候选同时包含 Codex 和其他 provider
- **THEN** 插件只将 Codex candidates 交给 Codex quota selection
- **AND** 插件不得选择非 Codex Auth ID作为 Codex OAuth结果

### Requirement: Provider fallback 必须依据请求 provider 信息处理

插件 MUST（必须）依据 `SchedulerPickRequest.Provider`、`Providers` 和候选的 `Provider` 字段识别 Codex 请求，并复用现有 AI provider fallback。插件 MUST NOT（不得）通过历史分组数据选择组外 OAuth 账号。

#### Scenario: Codex OAuth candidates 全部不可用

- **WHEN** 请求包含 Codex，宿主传入的 Codex candidates 存在但全部因额度、认证、熔断或其他既有资格原因不可选
- **THEN** 在 fallback 开启且宿主能力允许时，插件复用现有 AI provider fallback
- **AND** fallback 不得把插件历史分组之外的 OAuth 账号作为替代结果

#### Scenario: fallback 未启用

- **WHEN** Codex candidates 全部不可用且 fallback 未启用
- **THEN** 插件保留既有明确失败语义
- **AND** 插件不得返回普通或应急 provider 委托

#### Scenario: 非 Codex 请求

- **WHEN** `Provider` 和 `Providers` 均不包含 `codex`
- **THEN** 插件返回未接管或既有 `provider_not_codex` 结果
- **AND** 不触发 Codex quota selection 或 provider fallback

### Requirement: 历史分组数据必须兼容读取和导出

插件 MUST（必须）接受旧用户数据中的账号 `group_id`、`groups` 和 `active_pool` 字段，并在兼容导出中保留这些字段。插件 MUST NOT（不得）在新 UI 或新写操作中继续提供插件分组管理。

#### Scenario: 读取旧用户数据

- **WHEN** 用户数据包含历史插件分组和 `active_pool`
- **THEN** 插件成功加载其他有效配置和 annotation
- **AND** 不因为历史分组数据导致启动失败或改变生产调度

#### Scenario: 导出旧用户数据

- **WHEN** 用户请求导出插件配置
- **THEN** 导出结果保留历史分组和 `active_pool` 字段以支持兼容迁移
- **AND** 导出结果不新增任何凭据、Token、Cookie 或 Authorization 字段

#### Scenario: 新设置提交包含 active_pool

- **WHEN** 新版设置 UI 或客户端提交包含 `active_pool` 的设置更新
- **THEN** 插件不得将其作为新的有效调度配置保存
- **AND** 返回兼容错误或忽略该已废弃字段的行为必须固定且可测试

### Requirement: 插件不得再提供插件分组管理能力

插件 MUST（必须）停止提供创建、删除、修改、批量归组和切换 `active_pool` 的成功管理操作。旧路由可以作为拒绝兼容端点保留。非分组 annotation 的别名、备注、标签和插件优先级管理可以继续保留。

#### Scenario: 请求插件分组写操作

- **WHEN** 客户端请求插件分组创建、删除、修改、批量归组或 active_pool 切换
- **THEN** 插件返回固定的 `plugin_group_management_removed` 错误
- **AND** 内存状态和磁盘状态均不发生变化

#### Scenario: 更新非分组 annotation

- **WHEN** 客户端只更新别名、备注、标签或插件优先级
- **THEN** 插件继续通过受保护 Management API 保存更新
- **AND** 不创建或修改插件分组关系

### Requirement: Resource 页面必须支持无 key 读取安全配置和聚合状态

插件 MUST（必须）提供无需 CPA Management key 的 Resource 只读响应，内容限于调度器配置、启用状态和非敏感聚合状态。公开响应 MUST NOT（不得）包含账号身份、额度明细、日志或凭据字段。

#### Scenario: 无 key 打开调度器配置页

- **WHEN** 用户直接打开插件 Resource 页面且没有填写 CPA Management key
- **THEN** 页面自动加载调度器配置、启用状态和非敏感聚合状态
- **AND** 页面不因缺少 key 显示加载失败

#### Scenario: 公开状态字段

- **WHEN** Resource 返回安全只读状态
- **THEN** 响应可以包含选择策略、调度启用状态、刷新状态、roster 健康状态和聚合计数
- **AND** 响应不得包含邮箱、Auth ID、Auth Index、额度 window、reset credit、日志或逐账号错误

#### Scenario: 公开状态中的敏感内容

- **WHEN** 内部状态或宿主文本包含 Token、Cookie、Authorization、认证路径、原始认证 JSON 或未清洗错误文本
- **THEN** Resource 响应必须排除或清洗这些内容
- **AND** 不能通过查询参数触发详细状态、写操作或刷新操作

### Requirement: 详细数据和状态变更必须继续受 Management key 保护

详细账号状态、额度、日志、导入导出、刷新和所有配置或 annotation 写操作 MUST（必须）继续通过 CPA Management API 鉴权。

#### Scenario: 无 key 请求详细状态

- **WHEN** 客户端没有有效 Management key 请求详细账号状态、额度或日志
- **THEN** 请求被宿主 Management 鉴权拒绝
- **AND** 插件不通过 Resource 路由返回等价详细数据

#### Scenario: 无 key 保存配置

- **WHEN** 用户尝试保存调度设置但没有提供 Management key
- **THEN** 保存失败并保持原配置不变
- **AND** 页面显示需要 Management key 的明确提示

#### Scenario: 有 key 执行写操作

- **WHEN** 客户端提供有效 Management key 并执行受支持的配置或非分组 annotation 写操作
- **THEN** 插件按现有原子持久化和快照发布语义处理
- **AND** 不允许借写操作重新启用插件分组调度

### Requirement: Scheduler 热路径不得增加外部 I/O

插件在 `scheduler.pick` 热路径中 MUST（必须）只使用请求数据和已经发布的不可变内存快照，不得读取磁盘、调用 Host Auth、查询宿主分组 API 或发起网络请求。

#### Scenario: 正常 Codex 调度

- **WHEN** 宿主调用插件选择 Codex candidate
- **THEN** 插件只读取当前请求和内存快照完成选择
- **AND** 选择期间不执行磁盘、Host Auth 或网络 I/O

#### Scenario: 宿主分组发生变化

- **WHEN** 宿主切换启用 Codex 分组并随后传入新的 candidates
- **THEN** 插件直接使用新的请求候选集合
- **AND** 插件不主动读取或同步宿主分组配置

#### Scenario: 旧插件分组数据存在

- **WHEN** 内存或磁盘中存在历史插件分组数据
- **THEN** scheduler.pick 不读取这些数据作为调度过滤条件
- **AND** 调度延迟和 I/O 约束不因历史数据改变
