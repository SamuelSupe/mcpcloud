# Azure/GCP 云端审计证据边界

原交付 docs/providers.md 的验收准备项要求具备云端审计访问，以核实调用只读。本项属于验收证据要求；27 个原生功能的真实读取验收与完整审计证明是两个不同结论。

Azure 官方说明 Activity Log 通常不记录读取操作。因此，在测试资源组及 MCP 应用身份过滤后的 Activity Log 中没有事件，也不能证明每次资源、监控、对象或 Kubernetes 读取均已记录，更不能作为没有写操作的充分证据。[Azure Activity Log](https://learn.microsoft.com/en-us/azure/azure-monitor/platform/activity-log)

GCP 除 BigQuery 外，Data Access 审计默认关闭；是否能读取日志还取决于访问权限、项目及继承配置、服务日志覆盖和保留期限。Monitoring timeSeries.list 属于 DATA_READ。当前只读取已有配置和日志，不自动修改 auditConfigs、日志接收器或 IAM。[Cloud Audit Logs](https://docs.cloud.google.com/logging/docs/audit)、[Monitoring audit logging](https://docs.cloud.google.com/monitoring/audit-logging)

本轮抽样窗口为 UTC [2026-10-10 00:00, 2026-10-13 00:00)，GCP 限定 YOUR_GCP_PROJECT_ID 项目且过滤原 MCP 服务账号；Azure 限定测试资源组且按原 MCP 应用 ID 过滤 caller。每云最多 1000 条，仅作为有限样本；Azure caller 的应用 ID 过滤不声称覆盖所有身份表现形式。项目级 GCP auditConfigs 不替代组织/文件夹继承配置核对。原始身份及日志仅本机保存。

执行脚本 verify-cloud-audit-boundary-20261012.ps1；公开结果 verification.azure-gcp.audit-boundary-summary.20261012.json。fullReadCallAuditReconciled=false 明确表示没有取得全部调用的完整审计对账证据。查询失败、空结果或平台不产生事件均保留实际状态，不冒充通过。

创建身份与 MCP 读取身份仍分离；此前授权的测试对象、权限和禁用告警创建属于准备操作，不能混入“全部只读”声明。BigQuery 只读 SQL 会创建查询作业，也不能仅凭方法名含 Insert/Query 就判为修改业务数据。

本轮实际读取结果：GCP 项目级 auditConfigs 为 0 项；原 MCP 身份的审计样本 90 条，无续页，全部为 BigQuery 作业/查询/结果读取方法；Azure 测试资源组按 MCP 应用 caller 过滤返回 0 条。GCP 样本仅覆盖 BigQuery，不能推广为 Cloud Asset、Monitoring、Storage、Kubernetes 均有审计证明。未开启新审计配置或增加日志权限。

## GCP 完整分页与作业核对续验

用户要求验证剩余审计证据。本轮读取窗口为 UTC [2026-10-10T00:00:00Z, 2026-10-12T07:17:00Z)，对应北京时间结束于10月12日15:17。按原MCP服务账号过滤，使用独立Owner身份检查现有日志；查询作业及结果表元数据仍使用原MCP身份读取。没有修改auditConfigs、IAM、接收器或日志保留设置，也没有重跑全部云功能制造事件。

| 核对项 | 实际结果 | 结论 |
|---|---|---|
| 业务项目YOUR_GCP_PROJECT_ID日志 | 90条唯一事件、完整1页，全部BigQuery，事件状态均无错误 | 当前窗口可见的匹配事件读取完整；并不代表其他服务的全部请求都被平台记录 |
| 导出项目YOUR_BILLING_EXPORT_PROJECT_ID日志 | 完整2页、0条匹配事件；第一页空仍跟随游标 | 未将空页当查询结束；无法提供源表逐次数据访问证明 |
| BigQuery审计statementType | 仅SELECT | 没有在这批事件中发现DML/DDL语句 |
| 审计引用作业与原MCP的jobs.get | 12个不同作业，全部成功读取、DONE、无errorResult、全部SELECT | 已核对作业实际类型，不依据Insert/Query方法名猜测业务写入 |
| 独立账单参考作业关联 | 本机参考证据1个Job ID与本轮审计作业匹配 | 账单独立对账有正向云审计关联；其他11个不冒充逐条MCP调用ID核对 |
| 作业结果目的地 | 12个作业均指向业务项目同一个以下划线开头的匿名结果dataset，去重2张结果表；原身份读取两表元数据均有过期时间，创建至过期约24.39/24.56h | 与BigQuery临时缓存结果机制一致；不是把SELECT作业创建解释为修改业务源表 |
| permissionType | 样本有ADMIN_WRITE，实际permission为bigquery.jobs.create；无DATA_WRITE权限条目 | 作业创建是管理写行为，不能宣称所有事件都是DATA_READ；SELECT及匿名结果需要单独说明 |
| 项目级审计配置 | auditConfigs=0 | 项目自身没有配置非默认Data Access；不能代表有效继承配置为空 |
| 祖先及组织级审计配置 | 项目直接属于组织，无中间folder；组织get-iam-policy被PERMISSION_DENIED拒绝 | 当前检查身份不能核实组织auditConfigs或exemptedMembers；不能确认所有服务为何无日志 |
| 项目接收器与桶 | _Required/_Default均启用，均无exclusions；两桶ACTIVE，保留400/30天 | 在已检查的项目配置中未发现禁用或排除规则；不替代组织级路由配置核对 |

BigQuery样本审计核对通过，完整跨服务审计核对仍未通过：Cloud Asset、Compute详情、Cloud SQL详情、GKE/真实Kubernetes读取、Monitoring及Storage对象读取没有匹配事件。接口实测成功不替代云端审计证据。本轮不以“没有写日志”证明“没有写调用”，也不把组织读取失败解释成组织未配置审计。

下一步需要管理员只提供组织继承auditConfigs/exemptedMembers和相关路由配置的核对结果；若相关服务确未启用Data Access，则需另行批准启用所需服务的ADMIN_READ/DATA_READ审计，再用原MCP身份重测并逐项匹配新事件。Data Access配置会改变项目日志记录范围并可能增加日志量/费用，不属于本次“读取验证”的自动修改范围。既有未生成的事件不能通过本次日志查询补出。

脚本verify-gcp-audit-reconciliation-20261012.ps1有完整云端分页、重复游标/10页边界及100作业边界；失败保留实际状态，不报告完整通过。公开汇总verification.gcp.audit-reconciliation-summary.20261012.json、verification.gcp.audit-policy-summary.20261012.json、verification.gcp.audit-sinks-summary.20261012.json及verification.gcp.audit-result-tables-summary.20261012.json。原始事件、作业SQL/身份及政策检查错误仅在.private文件本地保存，不进入交付包。fullReadCallAuditReconciled仍为false。

依据：[Data Access配置及继承](https://docs.cloud.google.com/logging/docs/audit/configure-data-access)、[Cloud Asset审计读取方法](https://docs.cloud.google.com/asset-inventory/docs/audit-logging)、[Monitoring审计](https://docs.cloud.google.com/monitoring/audit-logging)、[GKE审计](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/audit-logging)、[BigQuery匿名缓存结果](https://docs.cloud.google.com/bigquery/docs/cached-results)。
