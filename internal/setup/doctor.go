package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/probe"
	"mcpcloud/internal/provider"
)

type ReportRunner interface {
	Run(context.Context, []string) (probe.Report, error)
}

type Check struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	NextStep  string `json:"next_step,omitempty"`
	Operation string `json:"operation,omitempty"`
}

type ProfileDiagnosis struct {
	Profile  string         `json:"profile"`
	Provider model.Provider `json:"provider"`
	Status   string         `json:"status"`
	Checks   []Check        `json:"checks"`
}

type DoctorReport struct {
	Status   string             `json:"status"`
	Offline  bool               `json:"offline"`
	Profiles []ProfileDiagnosis `json:"profiles"`
}

// Diagnose separates local prerequisites from evidence of a cloud read. A
// successful inventory probe cannot establish permissions for metrics or costs.
func Diagnose(ctx context.Context, cfg config.Config, names []string, offline bool, runner ReportRunner) (DoctorReport, error) {
	report := DoctorReport{Status: probe.StatusReady, Offline: offline}
	if err := cfg.Validate(); err != nil {
		return report, fmt.Errorf("configuration is invalid; run validate-config")
	}
	registry, err := provider.FromConfig(cfg)
	if err != nil {
		return report, fmt.Errorf("provider configuration is invalid; run validate-config")
	}
	if len(names) == 0 {
		names = registry.Profiles()
	}
	names = append([]string(nil), names...)
	sort.Strings(names)
	if len(names) == 0 {
		return report, fmt.Errorf("no enabled profiles; run init first")
	}
	for i, name := range names {
		if _, ok := registry.Get(name); !ok {
			return report, fmt.Errorf("profile %q does not exist or is disabled", name)
		}
		if i > 0 && name == names[i-1] {
			return report, fmt.Errorf("profile selected more than once")
		}
	}
	var selected []string
	for _, name := range names {
		p := cfg.Profiles[name]
		adapter, _ := registry.Get(name)
		result := ProfileDiagnosis{Profile: name, Provider: p.Provider, Status: probe.StatusReady}
		result.Checks = append(result.Checks, Check{Name: "configuration", Status: "pass", Message: "配置与 provider 参数有效。"})
		credential := Check{Name: "credentials", Status: "configured_unverified", Message: "已配置 SDK 凭证来源；尚未证明云端认证成功。", NextStep: credentialsFor(p.Provider).Help}
		if missing := MissingEnvironment(p); len(missing) > 0 {
			credential.Status = "fail"
			credential.Message = "当前进程缺少环境变量：" + strings.Join(missing, ", ")
			credential.NextStep = "在启动 mcpcloud 的进程环境中设置这些变量，再运行 doctor。"
			result.Status = probe.StatusNotReady
		} else if p.Provider == model.ProviderGCP && p.Credential.Source == "env" {
			key := p.Credential.Env["GOOGLE_APPLICATION_CREDENTIALS"]
			if key == "" {
				key = "GOOGLE_APPLICATION_CREDENTIALS"
			}
			f, e := os.Open(os.Getenv(key))
			if e == nil {
				info, statErr := f.Stat()
				f.Close()
				if statErr != nil || !info.Mode().IsRegular() {
					e = fmt.Errorf("not a regular file")
				}
			}
			if e != nil {
				credential.Status = "fail"
				credential.Message = "凭证文件不存在、不可读或不是普通文件。"
				credential.NextStep = "检查凭证文件路径及当前进程的读取权限。"
				result.Status = probe.StatusNotReady
			}
		}
		result.Checks = append(result.Checks, credential)
		boundary := Check{Name: "scope_configuration", Status: "pass", Message: "已配置明确范围和区域；云端证据在 scope 检查中单独报告。"}
		scoped, ok := adapter.(interface{ ConfiguredScopesFor(model.Source) []string })
		if !ok || len(scoped.ConfiguredScopesFor(model.SourceResources)) == 0 || len(p.Regions) == 0 || slices.Contains(p.Regions, "*") {
			boundary.Status = "warning"
			boundary.Message = "资源作用域或区域未明确收窄。"
			boundary.NextStep = "配置明确的 scopes/asset_scope 和 regions；init 默认要求明确范围。"
		}
		result.Checks = append(result.Checks, boundary)
		for _, capability := range adapter.Capabilities() {
			if capability.Source == model.SourceResources {
				continue
			}
			check := Check{Name: string(capability.Source), Status: "configured_unverified", Message: "配置条件满足；未执行该功能的真实查询。", NextStep: "使用允许范围内的目标运行代表性查询，以验证相应云权限。"}
			if capability.Status != "available" && capability.Status != "inventory" {
				check.Status = capability.Status
				check.Message = capability.Notes
				check.NextStep = "按 docs/configuration.md 和 docs/permissions.md 补齐该功能条件；不影响基础资源检查。"
			}
			result.Checks = append(result.Checks, check)
		}
		result.Checks = append(result.Checks, Check{Name: "details", Status: "not_checked", Message: "未读取指定实例、集群或存储桶详情。", NextStep: "选择允许范围内的具体资源，调用对应 cloud_native_read 操作。"})
		if result.Status != probe.StatusNotReady && !offline {
			selected = append(selected, name)
		}
		report.Profiles = append(report.Profiles, result)
	}
	observations := map[string]probe.ProfileResult{}
	if len(selected) > 0 {
		if runner == nil {
			return report, fmt.Errorf("online doctor requires a probe runner")
		}
		result, err := runner.Run(ctx, selected)
		if err != nil {
			return report, fmt.Errorf("cloud probe could not run; check profile selection and retry")
		}
		for _, profile := range result.Profiles {
			observations[profile.Profile] = profile
		}
	}
	for i := range report.Profiles {
		result := &report.Profiles[i]
		cloud := Check{Name: "cloud_access", Status: "not_checked", Message: "未执行云端读取。"}
		scope := Check{Name: "scope", Status: "not_checked", Message: "尚未验证资源行中的作用域。"}
		if observation, ok := observations[result.Profile]; ok {
			cloud.Operation = observation.Operation
			if observation.Authenticated {
				cloud.Status = "pass"
				cloud.Message = "所列库存操作已成功读取；不代表其他接口或所有区域均可访问。"
			} else {
				cloud.Status = "fail"
				cloud.Message = "云端库存读取失败。"
				if observation.Error != nil {
					cloud.Message += " 错误代码：" + observation.Error.Code
				}
				cloud.NextStep = probeNextStep(observation.Error)
			}
			if observation.ScopeVerified {
				scope.Status = "pass"
				scope.Message = "库存返回的资源提供了配置作用域的匹配证据。"
			} else {
				scope.Status = "unverified"
				scope.Message = "范围未充分验证；空库存、过滤后无资源或多个范围证据不足都可能导致此结果。"
				scope.NextStep = "确认账号/项目和区域，在其中选择已有资源；可拆分 profile 后重试。"
			}
			result.Status = probe.StatusNotReady
			if observation.Authenticated {
				result.Status = probe.StatusDegraded
				if observation.ScopeVerified {
					result.Status = probe.StatusReady
				}
			}
		} else if !offline && result.Status != probe.StatusNotReady {
			result.Status = probe.StatusNotReady
			cloud.Status = "fail"
			cloud.Message = "探测未返回该 profile 的结果。"
		}
		result.Checks = append(result.Checks, cloud, scope)
		if result.Status == probe.StatusNotReady {
			report.Status = probe.StatusNotReady
		} else if result.Status == probe.StatusDegraded && report.Status != probe.StatusNotReady {
			report.Status = probe.StatusDegraded
		}
		if offline && result.Status != probe.StatusNotReady {
			result.Status = "offline"
		}
	}
	if offline && report.Status == probe.StatusReady {
		report.Status = "offline"
	}
	return report, nil
}

func probeNextStep(err *probe.SafeError) string {
	if err != nil {
		code := strings.ToLower(err.Code)
		switch {
		case strings.Contains(code, "credential"), strings.Contains(code, "auth"), strings.Contains(code, "token"):
			return "检查当前进程的凭证来源、会话是否过期及目标角色；刷新登录后重试。"
		case strings.Contains(code, "denied"), strings.Contains(code, "permission"), strings.Contains(code, "forbidden"):
			return "按 docs/permissions.md 检查此库存操作的只读权限及云服务是否启用。"
		case err.Retryable, strings.Contains(code, "timeout"), strings.Contains(code, "deadline"):
			return "检查网络、服务限流和超时时间后重试。"
		}
	}
	return "检查凭证、云端只读权限、库存服务是否启用和网络连通性；错误内容不会输出密钥。"
}

func PrintDoctor(w io.Writer, report DoctorReport) error {
	if _, err := fmt.Fprintf(w, "doctor: %s (offline=%t)\n", report.Status, report.Offline); err != nil {
		return err
	}
	for _, profile := range report.Profiles {
		if _, err := fmt.Fprintf(w, "\n%s (%s): %s\n", profile.Profile, profile.Provider, profile.Status); err != nil {
			return err
		}
		for _, check := range profile.Checks {
			if _, err := fmt.Fprintf(w, "  [%s] %s: %s\n", check.Status, check.Name, check.Message); err != nil {
				return err
			}
			if check.Operation != "" {
				if _, err := fmt.Fprintf(w, "    operation: %s\n", check.Operation); err != nil {
					return err
				}
			}
			if check.NextStep != "" {
				if _, err := fmt.Fprintf(w, "    下一步：%s\n", check.NextStep); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
