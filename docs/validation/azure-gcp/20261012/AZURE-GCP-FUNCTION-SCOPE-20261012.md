# Azure/GCP 原定功能与拓展功能清单

按用户要求，优先完成原定功能，拓展项不作为原定验收的阻塞条件。不创建 500 个节点，不新增规模测试资源，不替换原交付版，不输出凭证。

## 范围依据

1. 原交付源码 eb600d6 的 docs/providers.md、原版实际 cloud_schema：Azure 13 个、GCP 14 个原生读取操作；每云另有 DSL-only metrics/costs 连接器。原有日志仅为资源元数据，IAM 仅为授权元数据，不包含日志正文、完整目录或有效权限计算。
2. AZURE-GCP-RESOURCE-PLAN-20261009.md 和 PREFLIGHT：资源清单、详情、关联、平台监控、真实费用，并明确包含 Kubernetes 集群/节点与健康后端。Node 读取和网络详情虽在本轮新增实现，仍用于完成原计划，不算自动拓展需求。
3. 用户后续明确追加对象级读取及容量验收，并批准测试容器补权和微小验收对象。对象元数据属于已授权追加范围；不是任意正文下载要求。

原版到候选版原生操作数从 27 到 50，增加 23 个实现。新增文件或操作不能单独作为需求拓展的判断依据。完整名字对照见 verification.azure-gcp.scope-catalog.20261012.json。

## 原定功能完成项

| 原定项目 | Azure | GCP |
|---|---|---|
| VM / 磁盘 / 存储 / 网络 / 数据库 / Kubernetes 分类清单 | 对应六类真实数据通过 | 对应六类真实数据通过，完整跟随云端续页；不因空结果页而提前停止 |
| IAM 分类及 DSL | 原生与 DSL 22 条 ID 集合一致 | 原生与 DSL 46 条 ID 集合一致 |
| 日志资源元数据 | Workspace 与 Application Insights 共 2 条 | 全地域查询完整八页，4 条日志资源；新加坡限定结果为空不能代表全项目无日志 |
| 告警规则元数据 | 用户授权创建的禁用 metric alert，原 MCP 直接读取及专用 global 配置分类通过 | 用户授权创建的禁用 AlertPolicy，原 MCP 直接读取及完整八页分类通过 |
| 固定 VM、数据库、集群详情 | VM / PostgreSQL / AKS 身份通过 | VM / Cloud SQL / GKE 身份通过 |
| Resource Graph / Cloud Asset 基础读取及资源 DSL | 固定类型/身份及资源 DSL 通过 | 资源搜索、IAM-policy 搜索与资源 DSL 通过 |
| 代表性原有 metrics 连接器 | VM CPU 与 PostgreSQL CPU 各 60 点 | VM CPU 116、Cloud SQL CPU 59、Redis CPU 232 点，结果分页完整 |
| 真实费用 | 见下节搁置项 | 真实 BigQuery 标准账单 17 行，对独立查询逐条核对通过 |
| 网络详情与关联、后端健康（原计划） | LB/VNet/NAT/NSG、规则与 NIC 后端/子网关联；LB 后端探针指标通过 | VPC/子网/Router/NAT/Firewall/转发规则/区域后端关联；后端 HEALTHY |
| 节点池及实际 Node（原计划） | 节点池及 1 个 Ready Node 通过 | 节点池及 1 个 Ready Node 通过 |
| 代表性存储容量读取 | 云监控非零容量采样、原生 API 对账通过 | 现有两个桶非零容量及原生 API 核对通过 |

原交付 27 个原生操作现已全部取得真实非空结果；两云告警在用户授权创建真实禁用规则后取得正向样本。两云 metrics 与 GCP costs 连接器已验证；Azure costs 保留搁置。部分原操作在原交付版存在缺陷，完成状态指修复候选版及下述专用告警配置，不能据此宣称未修改的原交付版同样全部可用。

本轮使用独立 8121 端口、config.azure-gcp.original-scope-20261012.yaml，沿用 v18 exe；只为 GCP 候选 services 加入原有 logging 分类，订阅/项目权限范围不变。原版和旧候选配置保留。

## 原有预设功能未验证项

| 项目 | 当前缺口 | 后续处理 |
|---|---|---|
| Azure 真实费用 | 当前经典 Sponsorship 不支持所用 Cost Management 接口；用户已要求先不管 | 记录平台限制并搁置，不追加权限或变更订阅 |

2026-10-12 告警正向补验：用户明确授权通过 CLI 创建两云规则。Azure mcpval-aks-cpu-alert-20261012 与 GCP mcpval-gke-cpu-alert-20261012 均禁用、无通知接收方；独立 Owner 创建，原 MCP 身份直接读取及 MCPCloud 分类读取通过。仅验证规则元数据，不验证触发或通知投递。

Azure metric alert 的 ARM 地域为 global，原 8121 配置限定三个具体地域，会正确过滤该规则。新增专用 config.azure-gcp.alerts-20261012.yaml / 8122，仅启用 monitoring 分类；Azure regions=[global]，GCP regions=['*']，云身份和订阅/项目范围不变。告警读取须使用此配置；8121 继续用于原有指标验收。本轮未修改代码或原交付文件，也未新增 IAM 权限。正向证据见 verification.azure-gcp.created-alerts.global-config.20261012.private.json；公开汇总见 verification.azure-gcp.alert-acceptance-summary.20261012.json。

联合复测中 GCP 第七页曾 context deadline exceeded；保留原始失败证据，随后以原游标续读第七、八页成功，合并前六页得到完整八页、唯一目标规则。续读证据 verification.gcp.created-alerts-resume.20261012.private.json；另有此前独立 GCP 完整八页通过证据。未将失败的一次调用算作成功，也未忽略续页。

另有验收证据边界：原 docs/providers.md 提及审计记录确认只读调用，目前未提供完整云端审计事件对账证据。它属于证据核验，不是额外业务功能；固定只读 API 和源码测试不冒充审计日志验证。

最新审计抽样已获取 GCP 原 MCP 身份 90 条 BigQuery 事件、项目级审计配置 0 项，以及 Azure 指定 caller 活动日志 0 条；仍不能证明全部接口调用只读。平台记录覆盖及继承配置边界见 AZURE-GCP-AUDIT-BOUNDARY-20261012.md；无审计配置变更。

GCP审计续验已完整翻页读取业务项目90条及导出项目0条，原MCP身份核对12个审计引用作业全部SELECT，并将独立账单参考Job ID关联至审计记录。项目接收器无排除、日志桶有效；组织继承审计配置因权限被拒绝仍无法核实。BigQuery样本通过，跨服务完整审计证据仍待管理员确认/启用Data Access后重测；未新增配置或权限。详情及完整窗口见审计边界文档续验章节。

“所有指标/所有产品”不是原版明确承诺的全面覆盖，不能把无界覆盖写成必须完成的待办。

## 拓展功能完成项

| 追加/拓展项 | 完成范围 |
|---|---|
| 两云对象级元数据读取（用户明确追加） | 两个对象，各云完整两页、1024 字节；固定主机、归属校验、只读身份，无正文或任意 metadata |
| 实际 Pod 列表（超出最初集群/节点计划） | Azure 14 个、GCP 17 个；身份、状态、Ready 等白名单字段 |
| Pod 分页 | page_size 最大 100，绑定查询上下文游标；Azure 五页完整；GCP 连接失败后原游标续读六页完整；601 个 Pod 模拟测试通过，不是创建真实 601 个 Pod |

资源分类、GCP 项目编号/指标单位、原有费用 SQL/结果分页与 Azure IAM 地域遗漏修复属于原功能修复；不计为新需求。Node、节点池、代表网络详情/关联与 regional 后端健康是原验收计划的实现补齐，不计为额外拓展。

## 拓展/追加功能未完成项

| 项目 | 状态 | 是否阻塞原功能验收 |
|---|---|---|
| GCP 测试桶非零监控容量（用户追加验收） | 对象 1024 字节可读，但原生 Monitoring 仍无该测试桶容量序列；具体原因未定位。旧桶容量通过不能替代测试桶结果 | 该追加项待验，不否定既有容量连接器实测 |
| GCP global BackendService 详情/健康（新增实现） | 代码与单元测试完成，现有项目只有 regional 样本，global 缺真实验收 | 不阻塞原计划 regional 后端验收；不为此新建收费资源 |
| Node 跨页支持（此前提出的可选扩展） | 尚未实现，目前固定 limit=500、有继续值明确失败；没有创建 500 个节点的要求 | 不阻塞现有单节点原计划；暂不实施，需要时使用模拟分页测试和已有小集群 |

日志正文、完整 Entra 目录/有效权限、私网 GKE 端点、任意业务健康、发票 PDF 均未列为本轮已约定拓展任务，不新增为欠交功能。

## 当前执行顺序和证据

原定功能已按既定范围完成实现及代表性真实实测，Azure 费用仍搁置。最新统一证据核对覆盖全部 27 个原生操作、10 组 DSL/指标和 17 组原资源计划代表场景；详见 AZURE-GCP-ORIGINAL-FUNCTIONS-FINAL-20261012.md 及 verification.azure-gcp.original-functions-final.20261012.json。客户端额外使用检查不是原定功能阻塞项；完整跨服务云端只读审计证据继续明确保留为未完成的证据要求。本轮未新增云资源、IAM、审计配置或调用范围。

优先原定功能：本轮已补齐 GCP 原有分类完整分页、IAM 对账、三个原有固定详情、日志全地域正向样本和两云五组代表指标。剩余原定缺项按上述平台/样本/证据条件明确保留，不进行未经要求的大规模扩容。拓展待验项单独跟踪，发布和原版替换不在本轮动作中。

主要本机证据：verification.azure-gcp.original-scope.20261012.private.json、verification.azure-gcp.original-scope-summary.20261012.json、verification.gcp.original-global-products.20261012.private.json、verification.gcp.original-global-products-summary.20261012.json、verification.azure-gcp.original-metrics.20261012.private.json、verification.azure-gcp.original-metrics-summary.20261012.json、verification.azure-gcp.scope-catalog.20261012.json。继承 v18 详情/对象/节点/分页证据、v16 网络关联及 600 点原生对账证据、最新 GCP 独立账单对账证据。原始 IAM、账单、资源证据仅本机保存，不对外发送。
