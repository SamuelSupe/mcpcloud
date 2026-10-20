# MCPCloud 安装与使用说明

MCPCloud 为 MCP 客户端提供七云资源、服务详情、监控和费用查询。云凭证在运行 MCPCloud 的主机上配置，不随程序包分发。

## 1. 选择程序包

| 系统 / 架构 | 程序包 | 适用设备 |
| --- | --- | --- |
| Windows amd64 | `mcpcloud_v0.1.1_windows_amd64.zip` | Intel / AMD 64 位 Windows |
| Windows arm64 | `mcpcloud_v0.1.1_windows_arm64.zip` | 高通 Snapdragon 等 ARM64 Windows 设备 |
| Linux amd64 | `mcpcloud_v0.1.1_linux_amd64.tar.gz` | x86_64 Linux 服务器 |
| Linux arm64 | `mcpcloud_v0.1.1_linux_arm64.tar.gz` | aarch64 Linux 服务器 |

Windows 的“设置 → 系统 → 系统信息 → 系统类型”可查看 x64 或 ARM64。amd64 同时适用于 Intel 和 AMD；ARM64 设备使用 arm64 包。下方 Windows 示例按 amd64 展示，ARM64 用户将文件名及解压目录中的 amd64 替换为 arm64。Linux 可用 `uname -m` 查看架构。程序包是可执行文件，无需安装 Go。Linux 程序以 `CGO_ENABLED=0` 构建，但访问云 API 仍需系统 CA 证书、正确时间和网络连接。Windows 请使用仍受支持的 64 位系统。

本次不提供 Docker 镜像、32 位或 macOS 包。架构依据及后续镜像计划见 [平台说明](https://github.com/SamuelSupe/mcpcloud/blob/v0.1.1/docs/release-platforms.md)。

## 2. 下载、校验、解压

从 [v0.1.1 Release](https://github.com/SamuelSupe/mcpcloud/releases/tag/v0.1.1) 下载对应程序包与 `SHA256SUMS`。同时提供独立的 `INSTALL.zh-CN.md`，与本说明内容一致。

Windows PowerShell：

```powershell
Get-FileHash .\mcpcloud_v0.1.1_windows_amd64.zip -Algorithm SHA256
# 将 Hash 与 SHA256SUMS 中同名文件逐字比较，匹配后再解压。
Expand-Archive .\mcpcloud_v0.1.1_windows_amd64.zip -DestinationPath .
Set-Location .\mcpcloud_v0.1.1_windows_amd64
.\mcpcloud.exe version --json
```

Linux（将 amd64 替换为实际下载的架构）：

```sh
grep '  mcpcloud_v0.1.1_linux_amd64.tar.gz$' SHA256SUMS | sha256sum -c -
tar -xzf mcpcloud_v0.1.1_linux_amd64.tar.gz
cd mcpcloud_v0.1.1_linux_amd64
./mcpcloud version --json
```

校验不匹配时重新下载。升级前保留旧程序包与配置，回退时恢复原程序和配置；本程序没有需要迁移的业务数据库。

## 3. 先验证程序可启动

程序包中 `examples/config.empty.yaml` 没有云账号，仅用于验证程序和客户端连接：

```powershell
# Windows
.\mcpcloud.exe validate-config --config .\examples\config.empty.yaml
```

```sh
# Linux
./mcpcloud validate-config --config ./examples/config.empty.yaml
```

成功后会显示 `configuration valid: 0 enabled profile(s)`。空配置没有可查询的云资源；启动成功不代表云身份和权限有效。

## 4. 配置云账号和只读权限

将 `examples/config.example.yaml` 复制为 `config.yaml`。仅保留需要使用的 profile，填写自己的账号 / 项目 / 订阅以及地域、服务范围；示例中的标识均为占位符。云身份使用独立的只读身份，不使用管理员账号运行日常查询。

| 云 | 凭证接入 | 基础查询条件 |
| --- | --- | --- |
| AWS | SDK 共享 profile、环境变量或工作负载身份 | Resource Explorer 视图读取，或单独启用产品直查；详情、CloudWatch 与费用另需对应读取权限。中国区使用匹配的中国区账号、地域。 |
| Azure | 工作负载身份，或 SDK 环境变量 `AZURE_TENANT_ID`、`AZURE_CLIENT_ID`、`AZURE_CLIENT_SECRET` | 订阅 / 资源组的必要读取权限；监控、对象元数据、AKS Node/Pod 查询按需增加对应最小权限。 |
| GCP | ADC / 工作负载身份，或 `GOOGLE_APPLICATION_CREDENTIALS` 指向外部凭证文件 | Cloud Asset 和目标服务读取；费用需已有 BigQuery 账单表、表读取及执行 SELECT 查询作业的权限。 |
| 阿里云 | `ALIBABA_CLOUD_ACCESS_KEY_ID`、`ALIBABA_CLOUD_ACCESS_KEY_SECRET`，临时身份另用安全令牌 | Resource Center 及已选产品的必要读取权限；OSS 深度操作需桶配置、统计读取权限。 |
| 腾讯云 | `TENCENTCLOUD_SECRET_ID`、`TENCENTCLOUD_SECRET_KEY`，临时身份另用 Token | 产品直查无需 Resource Center；费用必须选择与账号匹配的账单源和币种。 |
| 华为云 | `HUAWEICLOUD_SDK_AK`、`HUAWEICLOUD_SDK_SK`，临时身份另用安全令牌 | 明确 Project ID / Region；RMS、CES、产品详情和 BSS 费用按需授予读取权限，并配置账单站点。 |
| 火山引擎 | `VOLCENGINE_ACCESS_KEY_ID`、`VOLCENGINE_SECRET_ACCESS_KEY`，临时身份另用会话令牌 | 配置账号和地域范围，授予资源中心、监控、费用及所选固定详情的读取权限。 |

权限接口参考：[权限清单](https://github.com/SamuelSupe/mcpcloud/blob/v0.1.1/docs/permissions.md)。该清单包含基础接口说明；准确操作目录以运行版本的 `cloud_schema` 为准。云凭证保存在外部环境、凭证文件或云原生身份系统中，不放入程序包、客户端对话或 Git。

腾讯云使用已验证的八类资源直查时，在 profile 的 `options` 中设置：

```yaml
options:
  resource_mode: direct
  billing_source: intl_partner_customer  # 仅适用于国际渠道子客账单
  billing_currency: USD
```

普通账单使用默认 `billing_source: billing`，币种按账号配置；普通账单空返回不能解释为零费用。渠道汇总的币种为配置声明，付款归属需与结算记录核实。GCP 的 `billing_table` 必须替换为实际 `project.dataset.table`；本服务不创建账单导出。

配置完成后，先校验，再用原运行身份做有界探测（会调用云读取接口）：

```sh
./mcpcloud validate-config --config ./config.yaml
./mcpcloud probe-profile --config ./config.yaml --profile YOUR_PROFILE --timeout 30s
```

Windows 将 `./mcpcloud` 换为 `.\mcpcloud.exe`。`validate-config` 通过只表示配置可加载；`probe-profile` 验证有限的库存读取，不代表所有详情、监控和费用接口都已授权。

## 5. 接入 MCP 客户端

### 本机使用：stdio

客户端启动 MCPCloud，并通过标准输入输出连接。先用空配置确认连接，再切换为自己的 `config.yaml`。通用客户端配置示例：

```json
{
  "mcpServers": {
    "mcpcloud": {
      "command": "/absolute/path/mcpcloud",
      "args": ["serve", "--transport", "stdio", "--config", "/absolute/path/config.yaml"]
    }
  }
}
```

Windows 的 `command` 使用完整 exe 路径，JSON 中的反斜杠要写成 `\\`。不同客户端的配置文件入口不同，但程序、参数及凭证环境必须指向同一运行主机。

Codex CLI 可使用：

```sh
codex mcp add mcpcloud -- /absolute/path/mcpcloud serve --transport stdio --config /absolute/path/config.yaml
```

Windows PowerShell：

```powershell
codex mcp add mcpcloud -- 'C:\Tools\mcpcloud\mcpcloud.exe' serve --transport stdio --config 'C:\Tools\mcpcloud\config.yaml'
```

配置云凭证环境后重新启动客户端，使其继承环境。stdio 启动后等待输入是正常行为，不是卡死；程序日志输出到 stderr，不向 stdout 打印额外启动文本。

### 共享服务：HTTP

生成独立访问 Token，不要复用云密钥。以下示例将 Token 只保存在当前进程环境中，不打印值。

```powershell
# Windows PowerShell
$bytes = New-Object byte[] 32
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($bytes)
$rng.Dispose()
$env:MCPCLOUD_AUTH_TOKEN = [Convert]::ToBase64String($bytes)
.\mcpcloud.exe serve --transport http --config .\config.yaml
```

```sh
# Linux；需要 openssl 命令，或由现有密钥管理系统注入。
export MCPCLOUD_AUTH_TOKEN="$(openssl rand -hex 32)"
./mcpcloud serve --transport http --config ./config.yaml
```

默认地址为 `http://127.0.0.1:8080/mcp`。客户端发送 `Authorization: Bearer <访问Token>`。云凭证与这个访问 Token 是两类不同的凭证。Token 应通过受控渠道分发；持有同一 Token 的人访问同一个实例配置范围，当前没有内置的每人账号和用户级云权限隔离。

| 路径 | 用途 |
| --- | --- |
| `/mcp` | Streamable HTTP MCP，要求 Bearer Token 和正确 Host / Origin |
| `/healthz` | 进程健康，不证明云权限可用 |
| `/readyz` | 缓存的有界云库存探测，可能调用云 API，不代表全部业务接口通过 |

集中部署时，服务仍监听回环地址；前置 HTTPS 反向代理，限制内部访问，并在 `server.allowed_hosts` / `server.allowed_origins` 中填写所用的精确主机和来源。代理需保留请求与认证头并支持长请求。配置校验会拒绝直接监听 `0.0.0.0`。不要直接把当前进程暴露到公网。

远程 Codex CLI 接入可使用：

```sh
codex mcp add mcpcloud --url https://YOUR_INTERNAL_HOST/mcp --bearer-token-env-var MCPCLOUD_AUTH_TOKEN
```

本次发布不自动安装共享服务、不切换现有服务，也不提供 Docker 镜像。

## 6. 开始查询

连接成功后先调用 `cloud_help`、`cloud_profiles` 和 `cloud_schema`，了解当前配置与操作目录；新查询可先用 `cloud_explain` 检查计划。

| MCP 工具 | 用途 |
| --- | --- |
| `cloud_help` | 使用指引与查询示例 |
| `cloud_profiles` | 已配置账号及配置就绪情况 |
| `cloud_schema` | 字段、能力和固定只读操作目录 |
| `cloud_explain` | 解释查询，不调用云 API |
| `cloud_query` | 统一资源、权限元数据、监控和费用查询 |
| `cloud_native_read` | 调用已注册的固定详情读取 |

可让客户端执行的查询示例（profile、地域和指标要替换为自己的配置）：

```text
# 按云和服务统计资源
resources | summarize count(*) by provider, service

# 查看一个已配置账号的资源
resources | scope profile in ("YOUR_PROFILE") | fields id, name, service, region, state | limit 20

# 按币种和服务汇总费用，不将不同币种直接相加
costs | scope profile in ("YOUR_PROFILE") | range last 7d | summarize sum(amount) by currency, service

# 腾讯云已验证的 10 秒 CPU 形式；并非所有指标都支持秒级
metrics | scope provider in ("tencent"), profile in ("YOUR_PROFILE"), region in ("YOUR_REGION") | where metric == "QCE/CVM::CpuUsage?InstanceId=YOUR_INSTANCE_ID" | range last 10m | step 10s | fields timestamp, value, unit | limit 100
```

分页要继续使用返回的游标，直到没有继续页；`status=partial/failed` 或截断结果不能当作完整结果。更完整语法见 [DSL 说明](https://github.com/SamuelSupe/mcpcloud/blob/v0.1.1/docs/dsl.md)。

## 7. 常见问题与已知限制

| 现象 | 处理 |
| --- | --- |
| `Exec format error` | 检查操作系统与 amd64 / arm64 是否匹配。 |
| 配置有效但没有 profile | 正在用空配置，或没有加载预期的配置文件。 |
| `not_configured` / 凭证错误 | 检查运行进程继承的环境、外部凭证文件和账号范围。 |
| 403 / permission denied | 按报错操作核对最小权限，不直接扩大为 Owner / Administrator。 |
| HTTP 401 | 客户端访问 Token 缺失、未继承或与服务端不一致。 |
| HTTP 403 | 检查 Host / Origin 与代理配置。 |
| 库存、监控或账单为空 | 核对账号、地域、时间、数据采集及账单可见范围；空不等于零。 |

Azure 当前经典 Sponsorship 验证订阅不支持所用费用接口，真实费用未计为通过。腾讯合作伙伴费用已验证，普通账单和付款归属仍需账号侧核实。AWS 集群内部 Node/Pod/可调度容量未纳入本次已通过范围。OSS 已启用版本控制及生命周期的正向真实样本仍待补验。完整跨服务云端只读审计证据尚未补齐。

2026-10-20 已回收 Azure/GCP 专用测试资源；此前真实验收记录保留，本次发行包验证不重建这些资源，也不表示它们仍在线。历史快照与测试点数不代表当前账号资源量。
