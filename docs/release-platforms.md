# 发布平台与架构

核对日期：2026-10-29。本文件只说明 MCPCloud 的平台选择，不表示它与 DataKit 或观测云平台使用同一发行流程。

## 本次程序包

| 平台 | 架构名对应 | 本次提供 |
| --- | --- | --- |
| Windows amd64 | x86_64 / Intel 64 / AMD64 | ZIP，Windows Server 2025 虚拟机启动验证 |
| Windows arm64 | ARM64 / Qualcomm Snapdragon 等 | ZIP，Windows 11 ARM64 虚拟机启动验证 |
| Linux amd64 | x86_64 | TAR.GZ，Ubuntu 24.04 x86_64 虚拟机启动验证 |
| Linux arm64 | aarch64 | TAR.GZ，Ubuntu 24.04 ARM64 虚拟机启动验证 |

v0.1.2 覆盖上述四种 64 位平台，386、ARMv7、macOS 暂未提供。amd64 同时适用于 Intel 和 AMD，不是 AMD 专用版。四种包使用 GitHub 托管的匹配架构虚拟机，不使用 QEMU；不声明覆盖所有服务器型号和系统版本。v0.1.0 的 Windows 本机、Linux WSL / QEMU 历史验证结果仍按原口径保留。

[GitHub 标准运行器](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)提供 `windows-2025`、`windows-11-arm`、`ubuntu-24.04`、`ubuntu-24.04-arm`。公开仓库使用标准运行器免费。本工作流只用这些运行器，不启用 larger runner 或注入云凭证。

## 官方文档与镜像核对

- [观测云快速部署指南](https://docs.guance.com/deployment/Started/)列出 amd64 和 arm64 平台离线包。文档中基础环境包仍有 x86_64 限制，不能据此推断每个组件均支持所有平台。
- [DataKit 离线安装说明](https://docs.guance.com/en/datakit/datakit-offline-install/)列出 Windows amd64、Linux amd64 / arm64 等程序包。本次不照搬其全部历史 32 位平台。
- 只读核对公开 Registry：`pubrepo.jiagouyun.com/datakit/datakit:2.12.0` 的 Docker manifest list 含 `linux/amd64`、`linux/arm64`。官网示例中的 `pubrepo.guance.com` 会使用 `pubrepo.jiagouyun.com` 的匿名 Registry 认证入口；本次只读取清单，没有下载层或推送镜像。`latest` 标签读取为 404，因此使用明确版本标签，不能假定仓库必有 latest。

## 后续 Docker 范围

后续建议制作 Linux amd64 / arm64 多架构镜像，与本次 Linux 程序包一致；Windows 使用原生 exe。本次不构建、不推送任何 MCPCloud 镜像，也不创建镜像仓库。

开始镜像发布前再确定镜像仓库、命名与版本标签、入口代理和凭证挂载方式。当前 HTTP 服务强制回环监听；普通桥接容器仅映射端口不能代替入口设计，需要单独验证网络与认证，不在本次包发布中声明容器部署已经可用。
