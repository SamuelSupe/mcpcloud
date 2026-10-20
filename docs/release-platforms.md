# 发布平台与架构

核对日期：2026-10-20。本文件只说明 MCPCloud 的平台选择，不表示它与 DataKit 或观测云平台使用同一发行流程。

## 本次程序包

| 平台 | 架构名对应 | 本次提供 |
| --- | --- | --- |
| Windows amd64 | x86_64 / Intel 64 / AMD64 | ZIP，原生 Windows 启动验证 |
| Linux amd64 | x86_64 | TAR.GZ，WSL Linux 启动验证 |
| Linux arm64 | aarch64 | TAR.GZ，QEMU 用户态仿真启动验证；未替代 ARM 实机验收 |

先覆盖常用 64 位客户端和服务器。Windows ARM64、386、ARMv7、macOS 暂无明确目标环境，本次不追加这些包。Go 编译成功与启动验证分别记录；仿真通过不声明已在所有 ARM 服务器或 Linux 发行版实测。

## 官方文档与镜像核对

- [观测云快速部署指南](https://docs.guance.com/deployment/Started/)列出 amd64 和 arm64 平台离线包。文档中基础环境包仍有 x86_64 限制，不能据此推断每个组件均支持所有平台。
- [DataKit 离线安装说明](https://docs.guance.com/en/datakit/datakit-offline-install/)列出 Windows amd64、Linux amd64 / arm64 等程序包。本次不照搬其全部历史 32 位平台。
- 只读核对公开 Registry：`pubrepo.jiagouyun.com/datakit/datakit:2.12.0` 的 Docker manifest list 含 `linux/amd64`、`linux/arm64`。官网示例中的 `pubrepo.guance.com` 会使用 `pubrepo.jiagouyun.com` 的匿名 Registry 认证入口；本次只读取清单，没有下载层或推送镜像。`latest` 标签读取为 404，因此使用明确版本标签，不能假定仓库必有 latest。

## 后续 Docker 范围

后续建议制作 Linux amd64 / arm64 多架构镜像，与本次 Linux 程序包一致；Windows 使用原生 exe。本次不构建、不推送任何 MCPCloud 镜像，也不创建镜像仓库。

开始镜像发布前再确定镜像仓库、命名与版本标签、入口代理和凭证挂载方式。当前 HTTP 服务强制回环监听；普通桥接容器仅映射端口不能代替入口设计，需要单独验证网络与认证，不在本次包发布中声明容器部署已经可用。
