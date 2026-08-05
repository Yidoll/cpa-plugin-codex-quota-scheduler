# Tasks：解除 Codex 配额调度器对插件分组的依赖

## 1. OpenSpec 与边界确认

- [x] 1.1 确认本 change 只修改 `cpa-plugin-codex-quota-scheduler`。
- [x] 1.2 确认 CLIProxyAPI `codex-groups` 是宿主前置依赖，不在本 change 实现。
- [x] 1.3 确认不扩展 Plugin ABI、不新增 Scheduler group 字段和不新增 provider 委托值。
- [x] 1.4 运行 OpenSpec 严格校验并修复 proposal、design、spec、tasks 的结构问题。

## 2. 历史配置与数据兼容

- [x] 2.1 保持旧 `active_pool`、账号 `group_id` 和 `groups` 数据可反序列化。
- [x] 2.2 确保旧字段只进入兼容持久化/导出路径，不进入有效调度资格计算。
- [x] 2.3 确保旧导出数据仍保留历史分组和 `active_pool` 字段。
- [x] 2.4 确保导入旧数据不会重新启用插件分组调度。
- [x] 2.5 增加历史数据加载、导出、导入和调度无关性测试。

## 3. Scheduler 选择路径

- [x] 3.1 移除 `ActivePool` 和账号 `GroupID` 对生产选择路径的影响。
- [x] 3.2 保持选择逻辑只使用宿主传入的 Codex candidates、不可变 scheduler snapshot 和现有 quota 状态。
- [x] 3.3 验证插件不会从本地缓存补回宿主已过滤掉的 Codex OAuth candidates。
- [x] 3.4 保持 CPA priority、账号资格、可用性、插件优先级和选择策略顺序。
- [x] 3.5 复用现有 `Provider`、`Providers` 和 fallback 语义处理 AI provider。
- [x] 3.6 验证无 Codex candidates、Codex candidates 全部不可用、混合 provider 和固定 Auth 请求。
- [x] 3.7 删除或改写活动池严格失败、活动池绕过和组外 OAuth 相关的旧诊断。
- [x] 3.8 增加 scheduler 热路径无磁盘、Host Auth 回调和网络调用的回归测试。

## 4. Management API

- [x] 4.1 保留旧分组创建、删除、修改、批量归组和 `active_pool` 路由作为拒绝兼容端点，不提供成功语义。
- [x] 4.2 对上述旧写路由返回固定的 `plugin_group_management_removed` 错误，且不改变状态。
- [x] 4.3 保留别名、备注、标签和插件优先级 annotation 能力。
- [x] 4.4 拒绝非分组 annotation 写请求中的分组字段，避免产生新的插件分组数据。
- [x] 4.5 保持详细 status、logs、settings、export、import、refresh 和其他写操作的 Management 鉴权边界。
- [x] 4.6 增加 API 路由注册、旧路由错误、兼容读取和写入隔离测试。

## 5. 无 key 只读 Resource

- [x] 5.1 定义安全的公开只读 status payload 类型。
- [x] 5.2 增加或复用 Resource 数据路径，返回配置、启用状态和非敏感聚合状态。
- [x] 5.3 从公开 payload 中排除邮箱、Auth ID、Auth Index、额度明细、日志、逐账号错误和凭据字段。
- [x] 5.4 确保公开 payload 不包含 Token、Cookie、Authorization、认证路径、原始认证 JSON 和未清洗宿主文本。
- [x] 5.5 修改页面为打开时自动加载公开数据，写操作再要求 Management key。
- [x] 5.6 增加公开 Resource 无 key、敏感字段扫描、详细 Management 鉴权和写操作测试。

## 6. Management UI

- [x] 6.1 移除分组管理、批量归组、查看分组和 `active_pool` 控件。
- [x] 6.2 保留非分组 scheduler settings 和 annotation 编辑能力。
- [x] 6.3 调整 key 输入和加载流程，使初始只读数据加载不依赖 key。
- [x] 6.4 在执行保存、刷新、导入导出、日志读取和详细状态读取时要求 key。
- [x] 6.5 增加页面契约测试，确认不再提交 `active_pool` 或分组写字段。
- [x] 6.6 增加页面安全测试，确认无 key 页面不渲染账号身份、额度明细和日志。

## 7. 文档与验证

- [x] 7.1 更新 README，说明分组由 CLIProxyAPI 宿主管理，插件只消费宿主过滤后的 candidates。
- [x] 7.2 更新配置示例，移除可配置的 `active_pool`，说明历史字段仅用于兼容。
- [x] 7.3 更新隐私和 Management 边界说明。
- [x] 7.4 更新变更日志和升级/回滚说明。
- [x] 7.5 运行 Go 格式检查、单元测试、契约测试和全量测试。
- [x] 7.6 完成用户审核后，再进入实现计划阶段；本 change 在审核前不得实现代码。
