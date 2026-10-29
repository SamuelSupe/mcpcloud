package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	alists "github.com/aliyun/alibaba-cloud-sdk-go/services/sts"
	awssts "github.com/aws/aws-sdk-go-v2/service/sts"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	volcsts "github.com/volcengine/volcengine-go-sdk/service/sts"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
)

// SetupDiscovery contains candidates, never an allowlist. Lists are limited to
// one response; callers must explicitly select scopes before saving a profile.
type SetupDiscovery struct {
	Scopes     []string
	Region     string
	Incomplete bool
}

// DiscoverSetup reuses provider credentials for fixed read-only identity APIs.
// Huawei projects are entered manually from the API Credentials console.
func DiscoverSetup(ctx context.Context, p config.Profile) (SetupDiscovery, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, err := discoverSetup(ctx, p)
	if err != nil {
		return SetupDiscovery{}, fmt.Errorf("scope discovery failed; enter scope IDs manually")
	}
	valid := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	scopes := make([]string, 0, len(result.Scopes))
	for _, id := range result.Scopes {
		if valid.MatchString(id) && !slices.Contains(scopes, id) {
			scopes = append(scopes, id)
		}
	}
	result.Scopes = scopes
	return result, nil
}

func discoverSetup(ctx context.Context, p config.Profile) (SetupDiscovery, error) {
	const operation = "setup.discover_scopes"
	switch p.Provider {
	case model.ProviderAWS:
		a := awsAdapter{profile: p}
		cfg, err := a.sdkConfig(ctx, "", operation)
		if err != nil {
			return SetupDiscovery{}, err
		}
		hint := cfg.Region
		if cfg.Region == "" {
			cfg.Region = "us-east-1"
		}
		identity, err := awssts.NewFromConfig(cfg).GetCallerIdentity(ctx, &awssts.GetCallerIdentityInput{})
		if err != nil {
			return SetupDiscovery{}, err
		}
		result := SetupDiscovery{Region: hint}
		if identity.Account != nil {
			result.Scopes = []string{*identity.Account}
		}
		return result, nil
	case model.ProviderGCP:
		client, err := (&gcpAdapter{profile: p}).httpClient(ctx)
		if err != nil {
			return SetupDiscovery{}, err
		}
		var response struct {
			Projects []struct {
				ProjectID string `json:"projectId"`
				State     string `json:"state"`
			} `json:"projects"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := setupGet(ctx, client, "https://cloudresourcemanager.googleapis.com/v3/projects:search?pageSize=100", "", &response); err != nil {
			return SetupDiscovery{}, err
		}
		result := SetupDiscovery{Incomplete: response.NextPageToken != ""}
		for _, project := range response.Projects {
			if project.State == "ACTIVE" {
				result.Scopes = append(result.Scopes, project.ProjectID)
			}
		}
		return result, nil
	case model.ProviderAzure:
		token, err := (&azureAdapter{profile: p}).accessToken(ctx, operation)
		if err != nil {
			return SetupDiscovery{}, err
		}
		var response struct {
			Value []struct {
				ID    string `json:"subscriptionId"`
				State string `json:"state"`
			} `json:"value"`
			NextLink string `json:"nextLink"`
		}
		if err := setupGet(ctx, http.DefaultClient, "https://management.azure.com/subscriptions?api-version=2022-12-01", token, &response); err != nil {
			return SetupDiscovery{}, err
		}
		result := SetupDiscovery{Incomplete: response.NextLink != ""}
		for _, subscription := range response.Value {
			if subscription.State == "Enabled" {
				result.Scopes = append(result.Scopes, subscription.ID)
			}
		}
		return result, nil
	case model.ProviderAlibaba:
		client, err := (&alibabaAdapter{profile: p}).client()
		if err != nil {
			return SetupDiscovery{}, err
		}
		client.SetTransport(setupContextTransport{ctx, http.DefaultTransport})
		request := alists.CreateGetCallerIdentityRequest()
		request.Scheme = "https"
		request.Domain = "sts.cn-hangzhou.aliyuncs.com"
		setAlibabaTimeout(ctx, request.RpcRequest)
		response := alists.CreateGetCallerIdentityResponse()
		if err := client.DoAction(request, response); err != nil {
			return SetupDiscovery{}, err
		}
		return SetupDiscovery{Scopes: []string{response.AccountId}}, nil
	case model.ProviderTencent:
		client, err := (&tencentAdapter{profile: p}).tencentClient("sts.tencentcloudapi.com", "ap-guangzhou", operation)
		if err != nil {
			return SetupDiscovery{}, err
		}
		request := tchttp.NewCommonRequest("sts", "2018-08-13", "GetCallerIdentity")
		request.SetContext(ctx)
		response := tchttp.NewCommonResponse()
		if err := client.Send(request, response); err != nil {
			return SetupDiscovery{}, err
		}
		var envelope struct {
			Response struct {
				AccountID string `json:"AccountId"`
				Error     *struct {
					Code string `json:"Code"`
				} `json:"Error"`
			} `json:"Response"`
		}
		if err := json.Unmarshal(response.GetBody(), &envelope); err != nil {
			return SetupDiscovery{}, err
		}
		if envelope.Response.Error != nil {
			return SetupDiscovery{}, fmt.Errorf("identity request failed")
		}
		return SetupDiscovery{Scopes: []string{envelope.Response.AccountID}}, nil
	case model.ProviderVolcengine:
		session, err := (&volcengineAdapter{profile: p}).session("cn-beijing", operation)
		if err != nil {
			return SetupDiscovery{}, err
		}
		response, err := volcsts.New(session).GetCallerIdentityWithContext(ctx, &volcsts.GetCallerIdentityInput{})
		if err != nil {
			return SetupDiscovery{}, err
		}
		result := SetupDiscovery{}
		if response.AccountId != nil && *response.AccountId > 0 {
			result.Scopes = []string{strconv.FormatInt(*response.AccountId, 10)}
		}
		return result, nil
	case model.ProviderHuawei:
		return SetupDiscovery{}, nil
	default:
		return SetupDiscovery{}, fmt.Errorf("unsupported provider")
	}
}

// setupGet never follows redirects or pagination links supplied by an API.
func setupGet(ctx context.Context, client *http.Client, url, token string, result any) error {
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := bounded.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("discovery HTTP status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("discovery response exceeds limit")
	}
	return json.Unmarshal(data, result)
}

type setupContextTransport struct {
	ctx  context.Context
	next http.RoundTripper
}

func (t setupContextTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.next.RoundTrip(r.Clone(t.ctx))
}
