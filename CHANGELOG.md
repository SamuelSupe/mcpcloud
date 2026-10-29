# Changelog

## Unreleased

## [v0.2.0](docs/releases/v0.2.0.md) — 2026-10-29

- 新增 `init` 两步向导：复用已有凭证引用，发现范围候选，明确选择作用域、区域和功能，预览后新增 profile 并保留已有设置。
- 新增 `doctor` 文本/JSON 诊断：区分本地凭证条件、库存读取、作用域证据及尚未验证的可选权限；支持离线检查和明确的退出状态。
- 新增 Agent 非交互 `configure` JSON 接口：默认离线预览，可选只读发现；保存绑定请求、目标路径和旧配置内容，复用向导校验、原子保存及 doctor，拒绝过期计划与同名覆盖。
- 新增 `client-config`：生成绝对路径的通用 `mcpServers` JSON，或通过 `--client codex` 生成原生 TOML；`--install` 自动合并 Codex 配置，保留已有 MCP、注释、禁用状态和工具策略，同名匹配可重复执行，冲突不覆盖。
- 配置只保存身份引用；Codex 只转发环境变量名，不复制凭证值。配置保存、本地注册、云端诊断与运行中的 MCP 连接分别报告；新增配置仍需重连生效。
- 增加七云向导、发现、诊断、预览/保存、并发保护和 Codex 接入回归测试；四平台发布 smoke 验证真实程序的初始化、Agent 配置、原生 TOML 合并及 MCP 启动。程序包补充初始化、Agent、配置、工具及 provider 文档和 JSON 请求示例，发布包与独立安装说明的文档链接可直接使用。

## [v0.1.4](docs/releases/v0.1.4.md) — 2026-10-29

- 修复华为 CES 指标发现的末页判断：以 marker 决定续页，不再将全部结果的 Total 与本页 Count 比较。
- 修复 GCP folder/organization-only profile 的固定详情读取：先验证项目真实祖先，再读取详情并写入验证后的作用域字段；需要 `resourcemanager.projects.get`。
- AWS IAM 查询下推 `service:iam`，避免扫描无关资源耗尽预算。
- 阿里云每日账单及腾讯云默认费用分页允许 Total 增减，保留固定上游页大小；动态账单仍不保证快照一致性。
- GCP BigQuery 费用游标绑定查询上下文，拒绝跨查询或旧格式游标；同一查询仍允许调整结果页大小。
- `/readyz` 共享探测脱离首个请求的取消信号，保留独立超时，避免客户端断开污染一分钟缓存。

## [v0.1.3](docs/releases/v0.1.3.md) — 2026-10-29

- 修复 `/readyz` 探测函数报错后重复探测的问题：成功或失败均缓存一分钟，过期后首次请求再刷新。
- 增加失败缓存与过期恢复的回归测试，同步更新安装、配置及发布文档。

## [v0.1.2](docs/releases/v0.1.2.md) — 2026-10-29

- 修复跨云分页、AWS 区域端点、profile 作用域与全局资源处理、费用时间边界、华为 CES / DCS 分页及 OBS 临时凭证。
- 为 `/readyz` 增加认证和 Host / Origin 检查，并限制过滤错误的返回数量。

## [v0.1.1](docs/releases/v0.1.1.md) — 2026-10-20

- 新增 Windows ARM64 程序包及 Windows / Linux 四种平台的原生虚拟机验证。

## [v0.1.0](docs/releases/v0.1.0.md) — 2026-10-20

- 首次提供 Windows amd64、Linux amd64 / arm64 程序包、安装文档、版本信息与发布验证工具。
