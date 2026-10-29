package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

var ErrCanceled = errors.New("setup canceled")

type Discovery struct {
	Scopes     []string `json:"scopes"`
	Region     string   `json:"region_hint,omitempty"`
	Incomplete bool     `json:"incomplete"`
}

type DiscoverFunc func(context.Context, config.Profile) (Discovery, error)

type Wizard struct {
	Input    io.Reader
	Output   io.Writer
	Discover DiscoverFunc
	Offline  bool
	scanner  *bufio.Scanner
}

func (w *Wizard) ask(label, fallback string) (string, error) {
	if fallback != "" {
		fmt.Fprintf(w.Output, "%s [%s]: ", label, fallback)
	} else {
		fmt.Fprintf(w.Output, "%s: ", label)
	}
	if !w.scanner.Scan() {
		if err := w.scanner.Err(); err != nil {
			return "", err
		}
		return "", io.ErrUnexpectedEOF
	}
	value := strings.TrimSpace(w.scanner.Text())
	if value == "" {
		value = fallback
	}
	return value, nil
}

func (w *Wizard) choose(label string, values []string, fallback string) (string, error) {
	for {
		value, err := w.ask(label+" ("+strings.Join(values, " / ")+")", fallback)
		if err != nil {
			return "", err
		}
		if slices.Contains(values, value) {
			return value, nil
		}
		fmt.Fprintln(w.Output, "请从列出的选项中选择。")
	}
}

func (w *Wizard) required(label string) (string, error) {
	for {
		value, err := w.ask(label, "")
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(w.Output, "此项需要明确填写。")
	}
}

func csv(value string) []string {
	var result []string
	for _, part := range strings.Split(strings.ReplaceAll(value, "，", ","), ",") {
		part = strings.TrimSpace(part)
		if part != "" && !slices.Contains(result, part) {
			result = append(result, part)
		}
	}
	return result
}

// Run collects one explicit profile. It never writes files or grants cloud IAM.
func (w *Wizard) Run(ctx context.Context, existing config.Config) (string, config.Profile, error) {
	w.scanner = bufio.NewScanner(w.Input)
	w.scanner.Buffer(make([]byte, 1024), 64<<10)
	var p config.Profile
	fail := func(err error) (string, config.Profile, error) { return "", config.Profile{}, err }
	fmt.Fprintln(w.Output, "步骤 1/2：连接云身份（只输入凭证引用，不要输入密钥值）")
	clouds := make([]string, len(model.Providers))
	for i, c := range model.Providers {
		clouds[i] = string(c)
	}
	cloud, err := w.choose("云平台", clouds, "")
	if err != nil {
		return fail(err)
	}
	p.Provider = model.Provider(cloud)
	name, err := w.ask("本地 profile 名称", cloud+"-main")
	if err != nil {
		return fail(err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`).MatchString(name) {
		return fail(fmt.Errorf("profile 名称只能包含字母、数字、下划线或短横线，最多 64 字符"))
	}
	if _, ok := existing.Profiles[name]; ok {
		return fail(fmt.Errorf("profile %q 已存在，请选择其他名称", name))
	}
	spec := credentialsFor(p.Provider)
	fmt.Fprintln(w.Output, spec.Help)
	sourceDefault := spec.Sources[0]
	envProfile := p
	envProfile.Credential.Source = "env"
	if len(MissingEnvironment(envProfile)) == 0 {
		sourceDefault = "env"
	}
	profiles := []string(nil)
	if p.Provider == model.ProviderAWS {
		profiles = AWSProfiles()
		if len(profiles) > 0 {
			fmt.Fprintf(w.Output, "发现 AWS profiles：%s\n", strings.Join(profiles, ", "))
			if sourceDefault != "env" {
				sourceDefault = "profile"
			}
		}
	}
	p.Credential.Source, err = w.choose("凭证来源", spec.Sources, sourceDefault)
	if err != nil {
		return fail(err)
	}
	if p.Credential.Source == "workload" {
		fmt.Fprintln(w.Output, "workload 当前使用 SDK 默认身份链，请确认部署环境提供的是预期身份。")
	}
	if p.Credential.Source == "profile" {
		fallback := ""
		if len(profiles) == 1 {
			fallback = profiles[0]
		}
		if fallback == "" {
			p.Credential.Profile, err = w.required("AWS 共享 profile 名称")
		} else {
			p.Credential.Profile, err = w.ask("AWS 共享 profile 名称", fallback)
		}
		if err != nil {
			return fail(err)
		}
	}
	if p.Credential.Source == "env" {
		custom, e := w.choose("自定义环境变量名", []string{"n", "y"}, "n")
		if e != nil {
			return fail(e)
		}
		if custom == "y" {
			p.Credential.Env = map[string]string{}
			keys := append(append([]string{}, spec.Required...), spec.Optional...)
			for _, key := range keys {
				value, e := w.ask(key+" 对应的变量名", key)
				if e != nil {
					return fail(e)
				}
				if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(value) {
					return fail(fmt.Errorf("请输入环境变量名，不是凭证内容"))
				}
				if value != key {
					p.Credential.Env[key] = value
				}
			}
		}
		if missing := MissingEnvironment(p); len(missing) > 0 {
			fmt.Fprintf(w.Output, "当前进程缺少变量：%s。可先保存配置，部署时再提供。\n", strings.Join(missing, ", "))
		}
	}
	if p.Provider == model.ProviderAWS {
		p.Credential.RoleARN, err = w.ask("角色 ARN（直接使用当前身份可留空）", "")
		if err != nil {
			return fail(err)
		}
	}
	var discovery Discovery
	if !w.Offline && p.Provider != model.ProviderHuawei && w.Discover != nil && len(MissingEnvironment(p)) == 0 {
		fmt.Fprintln(w.Output, "正在执行有界的只读身份/范围发现；候选范围需要在下一步确认。")
		discovery, err = w.Discover(ctx, p)
		if err != nil {
			fmt.Fprintln(w.Output, "未能发现范围；可手动填写。保存后运行 doctor 检查凭证、网络和权限。")
		}
	}
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	fmt.Fprintln(w.Output, "步骤 2/2：确认访问范围与需要配置的功能")
	scopeKind := "accounts"
	switch p.Provider {
	case model.ProviderGCP:
		scopeKind, err = w.choose("作用域类型", []string{"projects", "folders", "organizations"}, "projects")
	case model.ProviderAzure:
		scopeKind = "subscriptions"
	case model.ProviderHuawei:
		scopeKind = "projects"
	}
	if err != nil {
		return fail(err)
	}
	if scopeKind != "folders" && scopeKind != "organizations" {
		for i, id := range discovery.Scopes {
			fmt.Fprintf(w.Output, "  #%d %s\n", i+1, id)
		}
	} else {
		discovery.Scopes = nil
	}
	if discovery.Incomplete {
		fmt.Fprintln(w.Output, "候选列表只包含第一页；未列出的范围可以手动填写。")
	}
	scopeText, err := w.required(scopeKind + "（明确填写 ID，或 #序号；多个用逗号分隔）")
	if err != nil {
		return fail(err)
	}
	scopes := csv(scopeText)
	for i, id := range scopes {
		if strings.HasPrefix(id, "#") {
			index, e := strconv.Atoi(strings.TrimPrefix(id, "#"))
			if e != nil || index < 1 || index > len(discovery.Scopes) {
				return fail(fmt.Errorf("无效候选序号"))
			}
			scopes[i] = discovery.Scopes[index-1]
		}
		scopes[i] = strings.TrimPrefix(scopes[i], scopeKind+"/")
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`).MatchString(scopes[i]) {
			return fail(fmt.Errorf("作用域必须是明确 ID，不能使用通配符或 URL"))
		}
	}
	if len(scopes) == 0 {
		return fail(fmt.Errorf("至少选择一个明确作用域"))
	}
	switch scopeKind {
	case "accounts":
		p.Scopes.Accounts = scopes
	case "projects":
		p.Scopes.Projects = scopes
	case "subscriptions":
		p.Scopes.Subscriptions = scopes
	case "organizations":
		p.Scopes.Organizations = scopes
	case "folders":
		if len(scopes) != 1 {
			return fail(fmt.Errorf("一个 GCP folder profile 只能指定一个 folder"))
		}
		p.Options = map[string]string{"asset_scope": "folders/" + scopes[0]}
	}
	regionText, err := w.ask("允许的区域（逗号分隔；全局资源仍按现有规则处理）", discovery.Region)
	if err != nil {
		return fail(err)
	}
	p.Regions = csv(regionText)
	if len(p.Regions) == 0 || slices.Contains(p.Regions, "*") {
		return fail(fmt.Errorf("向导要求至少一个明确区域；不会自动开放全部区域"))
	}
	services, err := w.ask("资源领域（compute,storage,network,iam,database,kubernetes,monitoring,logging,cost）", "compute,storage,network")
	if err != nil {
		return fail(err)
	}
	p.Services = csv(services)
	if len(p.Services) == 0 {
		return fail(fmt.Errorf("至少选择一个资源领域"))
	}
	for _, service := range p.Services {
		if !slices.Contains(provider.CoreDomains, service) {
			return fail(fmt.Errorf("未知资源领域 %q", service))
		}
	}
	featureText, err := w.ask("需要补齐的功能（resources,metrics,costs；基础资源读取始终检查）", "resources")
	if err != nil {
		return fail(err)
	}
	features := csv(featureText)
	for _, feature := range features {
		if !slices.Contains([]string{"resources", "metrics", "costs"}, feature) {
			return fail(fmt.Errorf("未知功能 %q", feature))
		}
	}
	addFeatureServices(&p, features)
	if slices.Contains(features, "costs") {
		if err := w.costOptions(&p); err != nil {
			return fail(err)
		}
	}
	prepared, err := PrepareRequest(ConfigurationRequest{Name: name, Profile: p, Features: features})
	if err != nil {
		return fail(err)
	}
	p = prepared.Profile
	preview, err := yaml.Marshal(map[string]any{"profiles": map[string]config.Profile{name: p}})
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(w.Output, "\n即将保存的配置（功能选择用于补齐条件，不是新增的 API 授权开关）：\n%s", preview)
	answer, err := w.choose("保存此范围", []string{"n", "y"}, "n")
	if err != nil {
		return fail(err)
	}
	if answer != "y" {
		return fail(ErrCanceled)
	}
	return name, p, nil
}

func (w *Wizard) costOptions(p *config.Profile) error {
	if p.Options == nil {
		p.Options = map[string]string{}
	}
	ask := func(key, label string, required bool) error {
		var err error
		if required {
			p.Options[key], err = w.required(label)
		} else {
			p.Options[key], err = w.ask(label, "")
		}
		if p.Options[key] == "" {
			delete(p.Options, key)
		}
		return err
	}
	switch p.Provider {
	case model.ProviderGCP:
		if len(p.Scopes.Projects) == 0 {
			return fmt.Errorf("GCP 费用需要项目级 profile；folder/organization profile 可单独用于资源读取")
		}
		for _, v := range []struct {
			k, l string
			r    bool
		}{{"billing_table", "现有账单表（project.dataset.table）", true}, {"billing_project", "查询作业项目（留空使用账单表项目）", false}, {"billing_location", "BigQuery 数据集位置（可留空）", false}} {
			if err := ask(v.k, v.l, v.r); err != nil {
				return err
			}
		}
	case model.ProviderHuawei:
		account, err := w.required("费用所属账号 ID")
		if err != nil {
			return err
		}
		p.Scopes.Accounts = csv(account)
		if len(p.Scopes.Accounts) == 0 {
			return fmt.Errorf("费用必须指定至少一个账号 ID")
		}
		for _, id := range p.Scopes.Accounts {
			if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`).MatchString(id) {
				return fmt.Errorf("费用账号必须是明确 ID")
			}
		}
		p.Options["billing_site"], err = w.choose("账单站点", []string{"china", "intl"}, "")
		return err
	case model.ProviderTencent:
		var err error
		p.Options["billing_source"], err = w.choose("账单来源", []string{"billing", "intl_partner_customer"}, "billing")
		if err != nil {
			return err
		}
		return ask("billing_currency", "账单币种（例如 CNY / USD，必须与账单一致）", true)
	}
	return nil
}
