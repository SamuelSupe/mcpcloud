# 两步初始化、诊断与 MCP 客户端配置

本文描述 v0.2.0 起提供的功能。可直接使用发布包，也可从源码构建：

```sh
go build -o mcpcloud ./cmd/mcpcloud
./mcpcloud init
```

Windows PowerShell 使用 `go build -o mcpcloud.exe ./cmd/mcpcloud` 和 `./mcpcloud.exe init`。默认配置文件为用户主目录下的 `.config/mcpcloud/config.yaml`，可通过 `--config PATH` 指定其他文件。

## 通过 Agent 配置

能执行本机命令的 Agent 可使用 `configure --input request.json --config PATH`，无需模拟向导输入。默认只返回 JSON 预览和离线 doctor；确认具体变更后，通过 `--apply --plan-id ID` 保存。`--discover` 可单独发现候选范围。它与本向导复用配置校验、功能条件检查和保留已有内容的保存逻辑。完整步骤、JSON 示例和诊断处理见 [Agent 操作说明](agent-setup.md)。

## 第一步：连接现有云身份

选择云平台、本地 profile 名称和凭证来源。向导只询问已有共享 profile、角色 ARN、环境变量名等引用；不会询问、复制或保存密钥值，也不会创建云端 IAM 用户、角色或授权。

| 云平台 | 可选择的凭证来源 | 在线范围发现 |
| --- | --- | --- |
| AWS | `default`、`profile`、`env`、`workload` | STS 当前账号；提示 SDK 解析的区域 |
| GCP | `default`、`env`、`workload` | Resource Manager 可见的 ACTIVE 项目，最多一页 |
| Azure | `default`、`env`、`workload` | ARM 可见的 Enabled 订阅，最多一页 |
| 阿里云 | `default`、`env` | STS 当前所属账号 |
| 华为云 | `env` | 手动填写控制台“我的凭证 / API 凭证”中的项目 ID |
| 腾讯云 | `default`、`env` | STS 当前所属账号 |
| 火山引擎 | `default`、`env` | STS 当前所属账号 |

AWS 会读取共享配置文件中的 profile 名称供选择。GCP 本地默认身份使用 ADC，例如已有的 `gcloud auth application-default login`；普通 `gcloud auth login` 不是同一份 ADC。Azure 默认身份可复用已有 `az login`。AWS/GCP/Azure 的 `workload` 仍使用现有 SDK 默认链，不代表强制禁用其他身份来源。

`env` 默认使用各家 SDK 的标准变量名，也可填写自定义变量名。缺少变量时仍可先保存配置，但后续 `doctor` 会报告本地凭证条件不满足。GCP 的变量值应指向外部凭证文件；向导不会把该文件复制进 YAML。

在线发现失败时可以手填范围。GCP/Azure 有后续页时会明确提示列表不完整；不会沿返回链接继续读取，也不会把未列出的范围视为不可用。发现只是候选提示，不证明这些范围内的所有服务权限已具备。

## 第二步：确认范围和功能

明确输入账号、项目或订阅 ID；也可以输入候选编号，例如 `#1`，多个范围用逗号分隔。GCP 还可选择 organization 或单个 folder。随后填写允许的具体区域和资源领域。向导不接受 `*` 作用域或区域，不会默认开放全部账号和区域。全局资源仍遵循现有 [作用域规则](configuration.md)。

默认领域为 `compute,storage,network`，功能为 `resources`。选择 `metrics` 会补上 `monitoring` 领域；选择 `costs` 会补上 `cost` 领域，并按云平台询问必要条件：

- GCP：现有 Billing Export 表 `project.dataset.table`，以及可选的查询项目、数据集位置。费用必须使用项目级 profile，不能以 folder/organization 配置读取整张账单表。
- 华为云：费用所属账号 ID 和 `china` / `intl` 账单站点。
- 腾讯云：账单来源及明确币种；国际伙伴客户账单要求 `USD`。

功能选择用于补齐配置条件，不是新加的 API 开关。`services` 是返回结果过滤，不能代替云端 IAM；详细权限见 [只读权限说明](permissions.md)。未选择费用不会自动查询账单，也不会让费用权限缺失阻断基础资源连接。

保存前展示完整的新 profile，默认回答为取消。确认后以新名称添加到配置中，保留已有 profile、服务器设置、限制及注释。遇到同名 profile、无效配置、配置符号链接、写锁或写入期间检测到文件变化会停止，不覆盖已有内容。配置通过同目录临时文件替换；在 Unix 上新配置文件权限为 `0600`。中断或拒绝保存不会写入 profile。

保存后自动运行 `doctor`，并输出 MCP 客户端配置片段。若诊断失败，profile 仍已保存，命令返回非零状态并提示下一步；修复凭证或权限后重跑 `doctor`，无需再次创建同名 profile。

```sh
./mcpcloud init --config ./config.yaml --timeout 45s
# 不执行任何云端发现或探测，适合离线准备配置
./mcpcloud init --config ./config.yaml --offline
```

`--timeout` 默认 30 秒，最大 2 分钟，分别用于发现和保存后的探测阶段，不计入人工回答时间。

## doctor：区分配置条件与实测证据

```sh
./mcpcloud doctor --config ./config.yaml
./mcpcloud doctor --config ./config.yaml --profile aws-main --timeout 45s
./mcpcloud doctor --config ./config.yaml --all --json
./mcpcloud doctor --config ./config.yaml --offline --json
```

不选 profile 时检查全部启用的 profile；`--profile` 与 `--all` 不能同时使用。无配置、没有启用 profile、未知或停用的名称、非法参数会在探测前失败。

| 检查 | 能证明什么 |
| --- | --- |
| `configuration` | YAML 和 provider 参数通过现有校验 |
| `credentials` | 凭证引用已配置；env 必要变量存在，GCP env 文件可读。`configured_unverified` 不代表凭证有效 |
| `scope_configuration` | 是否声明了明确资源作用域和区域；过宽配置显示 warning |
| `cloud_access` | 所列库存操作成功执行；只代表这次读取，不代表所有接口、区域或当前调用者身份 API 已验证 |
| `scope` | 库存资源行能否为配置范围提供匹配证据 |
| `iam` / `metrics` / `costs` | 静态条件是否齐全；未实测的权限保留 `configured_unverified`，条件不足显示 `not_configured` |
| `details` | 未选择具体资源做详情读取，明确显示 `not_checked` |

在线检查复用 `probe-profile` 的有界库存读取，每个区域最多 10 页、每页至多 100 条，并受总超时限制。不会扫描所有服务，也不会自动运行指标、账单或实例详情查询。报告不包含资源行、SDK 错误原文或凭证值；失败项给出错误代码和下一步操作。更多证据限制见 [probe-profile 说明](configuration.md#runtime-commands)。

退出状态与汇总结果：

- `ready`：所有所选 profile 的基础读取和范围证据通过，退出 0。可选费用或指标尚未验证不会改变该结果。
- `offline`：只检查本地条件，退出 0；不能据此认定云端权限通过。
- `degraded`：读取成功，但范围证据不足，退出非零。空账号、过滤后没有资源、多个范围只看到部分证据等都可能如此。folder 不能从该库存探测获得充分的范围证据时也保持未验证。
- `not_ready`：至少一个 profile 缺少本地必要条件、读取失败或没有探测结果，退出非零。其他满足本地条件的 profile 仍会检查。

`--json` 将已完成的诊断报告输出为 JSON；配置或参数导致检查无法开始时，通过标准错误报告并退出非零。报告中的 `profiles[].checks[]` 包含 `name`、`status`、`message`，以及适用时的 `next_step` 和 `operation`，便于脚本读取。

## 生成 MCP 客户端配置

```sh
./mcpcloud client-config --config ./config.yaml
./mcpcloud client-config --config ./config.yaml --binary /absolute/path/mcpcloud --name cloud-prod --output ./mcpcloud-client.json
```

默认将当前可执行文件和配置文件转换为绝对路径，输出标准 `mcpServers` JSON。带空格的路径保留为独立参数，不经过 shell 拼接。示例：

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

输出前检查可执行文件存在（Unix 上还检查执行位）以及配置文件有效且至少有一个启用 profile。使用 `--output` 只创建新文件，已有文件会报错，避免覆盖客户端原有服务器配置。默认只生成片段；将片段合并到支持 `mcpServers` 格式的客户端配置中即可。Codex 可使用下面的原生生成和显式安装选项。其他客户端的接入方式见 [安装说明](install.zh-CN.md#5-接入-mcp-客户端)。

生成器不复制环境变量值，MCP 子进程继承客户端启动环境。桌面客户端与当前终端的环境可能不同；凭证应由实际启动客户端的环境或 SDK 身份来源提供，并在变更后重启客户端。请使用安装后的可执行文件生成配置；`go run` 的临时可执行路径不能用于长期配置。

### Codex 原生配置与自动接入

```sh
# 仅输出 TOML 预览，不修改 Codex 设置
./mcpcloud client-config --client codex --config ./config.yaml

# 自动合并到 Codex 用户设置
./mcpcloud client-config --client codex --config ./config.yaml --install

# 可选：指定项目配置、服务器名及额外环境变量引用
./mcpcloud client-config --client codex --config ./config.yaml --install \
  --codex-config ./project/.codex/config.toml --name cloud-prod \
  --env-var AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE
```

生成的原生片段示例（GCP 默认身份）：

```toml
[mcp_servers.mcpcloud]
command = "/absolute/path/mcpcloud"
args = ["serve", "--transport", "stdio", "--config", "/absolute/path/config.yaml"]
env_vars = ["GOOGLE_APPLICATION_CREDENTIALS"]
```

`env_vars` 根据启用的 profile 自动汇总标准凭证变量名或 `credential.env` 的自定义引用，包括可选临时令牌；AWS 共享身份带上凭证文件路径变量，默认/工作负载身份还带上常用身份选择变量。其他 SDK 环境变量可用重复的 `--env-var NAME` 补充，不接受 `NAME=value`。只写变量名，不读取或保存变量值。Codex 必须能从自己的启动环境取得这些值；生成/安装成功不表示凭证已注入运行中的桌面应用。

自动安装不依赖 `codex` 命令存在，直接使用原生 TOML 设置。目标依次为显式 `--codex-config`、`$CODEX_HOME/config.toml`、`~/.codex/config.toml`。项目配置须由 Codex 信任后才会加载；选择 Codex 配置 profile 时应显式指定其实际文件。不会自动改变项目信任、工具审批或其他 Codex 设置。[原生 MCP 配置规则](https://learn.chatgpt.com/docs/extend/mcp)、[CODEX_HOME 说明](https://learn.chatgpt.com/docs/config-file/environment-variables)。

安装保留已有服务器、其他设置和注释，使用与 profile 保存相同的写锁、内容复查和原子替换；新写入文件权限为 `0600`。同名且命令、参数、所需变量引用一致时返回 `unchanged`，已有额外变量、超时、工具限制和 `enabled = false` 保留。不同配置的同名项报错，不自动覆盖；可以换 `--name` 或人工核对原项。无效 TOML、符号链接、正在写入的文件以及无法追加的内联父表（如 `mcp_servers = {...}`）会报错并保留原文件；内联父表可改成标准 `[mcp_servers.NAME]` 表后重试。

安装输出 JSON 示例：

```json
{
  "status": "installed",
  "config_path": "/home/user/.codex/config.toml",
  "server_name": "mcpcloud",
  "enabled": true,
  "restart_required": true,
  "connection_verified": false
}
```

`installed` / `unchanged` 仅表示设置已写入/已存在。安装不会自动重启正在工作的 Codex 会话；在 Codex 的 MCP 设置中重启/重新连接，再用 `cloud_profiles` 和小范围查询验证。`enabled: false` 表示原条目仍禁用，需根据用户意愿在 Codex 中启用。生成和安装均不请求云端；云端连接仍由 doctor 和实际查询分别验证。

`--output` 仍用于生成独立文件，不能与 `--install` 同用；`--codex-config` 只在安装时使用。默认 JSON 输出不变。

## 验证范围

自动测试覆盖七云离线向导、明确范围确认、已有配置保留、密钥不进入输出、doctor 状态与退出结果、模拟身份/范围发现，以及发现的取消、分页提示和响应边界。Codex 测试覆盖原生 TOML、凭证引用、已有设置保留、重复接入、冲突/锁/符号链接和安装状态；发布 smoke 从生成的 Codex 配置实际启动 MCP 完成握手。真实云端身份与各项服务权限仍需在目标部署环境执行 `doctor` 和代表性查询验收。
