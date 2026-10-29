# Changelog

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
