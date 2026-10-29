package setup

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
)

type credentialSpec struct {
	Sources  []string
	Required []string
	Optional []string
	Help     string
}

func credentialsFor(cloud model.Provider) credentialSpec {
	switch cloud {
	case model.ProviderAWS:
		return credentialSpec{[]string{"default", "profile", "env", "workload"}, []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}, []string{"AWS_SESSION_TOKEN"}, "使用已有 AWS profile/SSO，或运行环境提供的云身份。"}
	case model.ProviderGCP:
		return credentialSpec{[]string{"default", "env", "workload"}, []string{"GOOGLE_APPLICATION_CREDENTIALS"}, nil, "本地默认身份需要 ADC：gcloud auth application-default login。env 模式引用外部凭证文件的环境变量。"}
	case model.ProviderAzure:
		return credentialSpec{[]string{"default", "env", "workload"}, []string{"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET"}, nil, "本地可复用 az login；服务器使用部署环境中的身份。"}
	case model.ProviderAlibaba:
		return credentialSpec{[]string{"default", "env"}, []string{"ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_SECRET"}, []string{"ALIBABA_CLOUD_SECURITY_TOKEN"}, "使用 SDK 默认凭证，或在运行环境中设置 AK/SK 和可选临时令牌。"}
	case model.ProviderHuawei:
		return credentialSpec{[]string{"env"}, []string{"HUAWEICLOUD_SDK_AK", "HUAWEICLOUD_SDK_SK"}, []string{"HUAWEICLOUD_SDK_SECURITY_TOKEN", "HUAWEICLOUD_DOMAIN_ID"}, "在运行环境中设置 AK/SK；使用临时凭证时同时设置 Security Token。项目 ID 请从控制台“我的凭证 / API 凭证”填写。"}
	case model.ProviderTencent:
		return credentialSpec{[]string{"default", "env"}, []string{"TENCENTCLOUD_SECRET_ID", "TENCENTCLOUD_SECRET_KEY"}, []string{"TENCENTCLOUD_TOKEN"}, "使用 SDK 默认凭证，或在运行环境中设置 SecretId/SecretKey 和可选 Token。"}
	case model.ProviderVolcengine:
		return credentialSpec{[]string{"default", "env"}, []string{"VOLCENGINE_ACCESS_KEY_ID", "VOLCENGINE_SECRET_ACCESS_KEY"}, []string{"VOLCENGINE_SESSION_TOKEN"}, "使用 SDK 默认凭证，或在运行环境中设置 AK/SK 和可选 Session Token。"}
	}
	return credentialSpec{}
}

// MissingEnvironment reports names only, never credential values.
func MissingEnvironment(p config.Profile) []string {
	if p.Credential.Source != "env" {
		return nil
	}
	var missing []string
	for _, key := range credentialsFor(p.Provider).Required {
		name := p.Credential.Env[key]
		if name == "" {
			name = key
		}
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

// AWSProfiles reads section names only. No credential value leaves these files.
func AWSProfiles() []string {
	home, _ := os.UserHomeDir()
	paths := []string{os.Getenv("AWS_CONFIG_FILE"), os.Getenv("AWS_SHARED_CREDENTIALS_FILE")}
	if paths[0] == "" {
		paths[0] = filepath.Join(home, ".aws", "config")
	}
	if paths[1] == "" {
		paths[1] = filepath.Join(home, ".aws", "credentials")
	}
	found := map[string]bool{}
	for index, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
				continue
			}
			name := strings.TrimSpace(line[1 : len(line)-1])
			if index == 0 {
				if name != "default" && !strings.HasPrefix(name, "profile ") {
					continue
				}
				name = strings.TrimSpace(strings.TrimPrefix(name, "profile "))
			}
			if name != "" && len(name) <= 256 {
				found[name] = true
			}
		}
		f.Close()
	}
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
