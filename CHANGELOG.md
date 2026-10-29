# Changelog

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
