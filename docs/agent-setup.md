# Agent 配置操作说明

适用于能够执行 MCPCloud 本机命令的 Agent。v0.2.0 起提供非交互 `configure` 和 Codex 原生配置生成/自动注册。此流程不新增 MCP 工具，也不要求 Agent 通过终端模拟人工回答 `init`。

## 输入与权限边界

1. 明确用户要配置的云平台、已有身份来源、账号/项目/订阅、区域和功能，以及配置文件位置。只补问无法从已有信息确定的事项。
2. 只接受凭证引用：共享 profile 名称、角色 ARN、环境变量名。不要要求用户将 AK/SK、密码、Token 或凭证文件内容发到聊天、JSON 请求或命令参数中。登录和凭证准备在用户可信的本机工具中完成。
3. 优先使用 `configure` 的校验与保存接口，不直接重写整份 YAML。只新增一个启用的 profile，不覆盖、删除或更改已有 profile。
4. 若用户已明确授权本次具体范围和保存动作，沿用该授权，无需重复确认。否则先展示预览摘要，取得对身份来源、范围、功能和保存位置的确认，再执行 `--apply`。发现候选账号不等于用户选择了该账号。
5. 不自动修改云 IAM、不默认选择全部账号或 `*` 区域、不把 `services` 当成云权限。功能选项只补齐必要配置条件，不能据此宣称禁止了未选择的 API。

## 请求格式

读取 [JSON 示例](../examples/profile.request.json)，替换示例项目、区域和名称。输入为 UTF-8 JSON，最大 64 KiB；只接受一个对象，未知字段会报错。

```json
{
  "name": "aws-prod",
  "profile": {
    "provider": "aws",
    "credential": {"source": "profile", "profile": "readonly-prod"},
    "scopes": {"accounts": ["123456789012"]},
    "regions": ["ap-southeast-1"],
    "services": ["compute", "storage"]
  },
  "features": ["resources", "metrics"]
}
```

`profile` 与现有 YAML 的 profile 字段一致，使用同一套配置及 provider 校验。`name` 限制为 1–64 个字母、数字、下划线、短横线，首字符为字母或数字。非交互接入与向导一样要求具体作用域、至少一个明确区域和至少一个资源领域。`services` 使用 `compute,storage,network,iam,database,kubernetes,monitoring,logging,cost` 中的领域。

`features` 可省略，默认 `resources`。接受 `resources`、`metrics`、`costs`，基础资源检查始终保留。选择指标或费用会在预览中明确加入 `monitoring` 或 `cost` 领域；对应静态配置不完整会阻止保存。例如 GCP 费用需要项目及账单表，华为费用另需账号和账单站点，腾讯费用需要单一账号和币种。具体选项见 [配置说明](configuration.md)。缺少当前进程的环境变量仍可准备配置，doctor 会单独报告。

请求保存在单独的 JSON 文件中，或通过标准输入传递 `--input -`。不要把 JSON 拼进 shell 命令；调用进程时使用独立参数数组，避免路径、变量名或用户输入被 shell 展开。

## 1. 可选：发现范围候选

已知身份引用但不确定账号或项目时，先提供只有名称、云平台、凭证引用的请求。例如 `identity.json`：

```json
{
  "name": "aws-prod",
  "profile": {
    "provider": "aws",
    "credential": {"source": "profile", "profile": "readonly-prod"}
  }
}
```

```sh
./mcpcloud configure --discover --input identity.json --timeout 30s
```

结果中 `discovery.scopes` 是凭证可见的候选 ID，`region_hint` 是可用时的区域提示；不是已经保存或授权的范围。GCP/Azure 的列表只取一页，`incomplete: true` 时允许用户手动补充未列出的 ID，不得自动扩大范围。华为项目需手填，会返回 `manual_scope_required`。成功但没有候选时也返回该状态；发现失败返回 `discovery_failed`，可检查身份或改为手填。

`--discover` 不读写目标配置、不生成保存计划，也不证明库存、指标或费用权限。不能与 `--apply` 或 `--offline` 组合。它复用向导的固定只读发现接口；该步骤是可选的。

## 2. 预览：默认不写入、不请求云端

将已确定的完整请求保存为 `request.json`：

```sh
./mcpcloud configure --input request.json --config ./config.yaml
```

标准输出为一个 JSON 报告。成功时包含：

- `status: "preview"`、`saved: false`、`restart_required: false`。
- `plan.id`：绑定规范化请求、绝对目标路径和当前配置内容的标识。
- `plan.config_path`、`plan.request`：实际待保存的位置及完整 profile，包括自动补充的功能领域。
- `doctor`：复用现有 doctor 的本地检查；不进行云端访问，不将配置存在视为认证成功。

向用户展示 `plan.request` 的身份引用、账号/项目/订阅、区域、领域及功能条件，以及本地检查的缺失项。不要输出其他已有 profile 的配置内容。预览不创建配置目录、锁文件或计划文件。

预览退出 0 表示配置方案有效；`doctor.status` 仍可能为 `not_ready`，例如准备部署到另一台机器时，本机缺少所引用的环境变量。必须保留这一区别。

## 3. 保存：使用原请求和该次预览标识

取得本次保存授权后，将预览返回的 `plan.id` 原样作为参数传入：

```sh
./mcpcloud configure --input request.json --config ./config.yaml \
  --apply --plan-id PLAN_ID_FROM_PREVIEW
```

默认保存后执行在线 doctor；离线准备配置时增加 `--offline`。`--timeout` 默认 30 秒，最大 2 分钟，适用于发现或保存后探测。

`plan.id` 是一致性校验，不是授权票据或身份认证凭据。CLI 无法证明人类是否确认；Agent 必须依据用户的真实授权执行，不得把自己生成的标识、布尔值或工具输出当成用户授权。

请求内容、目标路径或原有配置变化都会导致 `plan_changed`；停止使用旧标识，重新预览并核对变更。若变化影响已授权范围，再向用户确认。保存函数持有写锁后还会复查旧配置内容，防止预览与提交之间丢失其他改动。它保留已有 profile、设置和注释；同名 profile 返回 `profile_exists`，不会覆盖。

## 4. 分别判断保存与诊断结果

成功保存的报告始终有 `status: "saved"`、`saved: true`、`restart_required: true`。即使后续 doctor 未通过，也保留保存事实并返回非零退出状态；不要据此重复 apply 或创建相同 profile。

| 结果 | Agent 的下一步 |
| --- | --- |
| `saved: true`，doctor 为 `ready` | 基础读取及范围证据通过；仍需让运行中的 MCP 服务重载 |
| `saved: true`，doctor 为 `offline` | 已保存且本地条件通过，云端访问尚未验证 |
| `saved: true`，`doctor_not_ready` | 按 `doctor.profiles[].checks[].next_step` 修复凭证或权限，单独重跑 doctor |
| `saved: true`，`doctor_failed` / `post_save_config_unreadable` | 保存完成，但检查未完成；核对文件后单独重跑 doctor |
| `saved: false`，`plan_changed` | 本次未保存；重新预览并核对 |
| `saved: false`，`profile_exists` | 本次未保存；检查现有 profile，或使用另一个用户授权的名称 |
| 其他 `error.code` | 根据 `error.field` 和 `error.message` 修正；不复述或展示原始 SDK 错误或凭证值 |

独立诊断命令：

```sh
./mcpcloud doctor --config ./config.yaml --profile aws-prod --json
```

除了 `--help`，`configure` 的业务结果和参数/输入错误均以单个 JSON 对象写到 stdout，错误还会返回非零退出码并在 stderr 输出简短消息。保存后的 doctor 为 `degraded` 或 `not_ready` 时同样非零，因此脚本必须读取 JSON 的 `saved`，不能只看退出码。

## 5. 接入 Codex 或生成其他客户端配置

Codex 使用原生 TOML。先生成注册预览，核对二进制路径、云配置路径、服务器名和转发变量名：

```sh
./mcpcloud client-config --client codex --config ./config.yaml
```

用户已授权接入 Codex 时，直接沿用该授权，自动合并：

```sh
./mcpcloud client-config --client codex --config ./config.yaml --install
```

目标默认是 `$CODEX_HOME/config.toml`，未设置时为 `~/.codex/config.toml`。项目或 Codex profile 的配置文件通过 `--codex-config PATH` 明确指定；不要自行修改项目信任来让项目配置生效。需要多个 MCPCloud 实例时用不同的 `--name`。其他 SDK 环境引用可重复指定 `--env-var NAME`；不要传 `NAME=value` 或把当前进程的凭证复制进 TOML。

安装复用已有路径/配置校验和原子保存机制，保留其他 MCP、全局设置及注释；同名匹配返回 `unchanged`，冲突则保留原文件并失败，不要通过删除原项来绕过冲突。缺少新增 profile 所需的环境引用时也可能冲突，应核对该项后决定人工更新或另取名称。无效 TOML、锁、符号链接或不能追加的内联父表同样不覆盖。若用户尚未授权修改客户端设置，展示具体目标和片段后取得授权；无需重复询问已有明确授权。

成功时 stdout 是单个 JSON 对象：`status` 为 `installed` / `unchanged`，带 `config_path`、`server_name`、`enabled`、`restart_required: true`、`connection_verified: false`。这只证明本地注册，不是已连通或已通过云权限检查。`enabled: false` 表示原禁用设置被保留，不得宣称服务器已启用。失败返回非零并在 stderr 给出错误；不要回显已有 Codex 配置中的其他服务器凭证。

其他支持 `mcpServers` JSON 的客户端继续使用原命令：

```sh
./mcpcloud client-config --config ./config.yaml --output ./mcpcloud-client.json
```

生成器填写绝对路径，不复制凭证、不覆盖已有输出文件；只有用户授权修改对应客户端设置时才合并。`--output` 与 Codex `--install` 互斥。无法操作客户端的 Agent 应给出文件位置和具体操作，不得宣称已接入。

现有 MCP 服务在启动时加载配置，没有热重载。`restart_required: true` 表示已运行实例需要重启/重连；首次接入则启动服务即可。由支持此能力的客户端执行重启；没有该能力时提示用户完成。不要在保存工具返回前终止自身 MCP 连接。

重连后调用 `cloud_profiles` 核对新 profile，再用 `cloud_schema` → `cloud_explain` → 小范围 `cloud_query` 验证用户需要的资源功能。`cloud_profiles` 本身不会证明真实云端权限，doctor 也不自动验证所有指标、费用或资源详情接口。不要将子进程中的 `export` 视为已给运行中的 MCP 服务注入凭证。

## 对用户的完成反馈

说明四件事：新增了哪个 profile 及哪些范围；配置是否保存；doctor 验证了什么及哪些仍未验证；客户端是否已重新加载。配置已写入、诊断通过、MCP 重连和真实查询成功是四个独立事实，应按实际证据报告。
