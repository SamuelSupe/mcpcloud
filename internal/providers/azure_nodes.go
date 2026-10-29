package providers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"go.yaml.in/yaml/v3"
	"io"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func init() {
	deepDetailCatalog[model.ProviderAzure] = append(deepDetailCatalog[model.ProviderAzure], deepDetailSpec{operation: "azure.kubernetes.nodes.list", service: "containerservice", domain: "live_nodes", kind: "node", description: "List actual AKS Nodes with Entra read-only identity; never use admin or local credentials", parameters: detailParameters("subscription_id", "resource_group", "cluster"), required: []string{"subscription_id", "resource_group", "cluster"}})
}

func (a *azureAdapter) readLiveNodes(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, req); err != nil {
		return provider.Page{}, err
	}
	podParams := ""
	if spec.domain == "live_pods" {
		var err error
		podParams, err = podQuery(req, a.name)
		if err != nil {
			return provider.Page{}, err
		}
	}
	sub, err := nativeIdentifier(req.Params, "subscription_id")
	if err != nil {
		return provider.Page{}, err
	}
	sub, err = exactDetailScope(a.profile.Scopes.Subscriptions, sub, spec.operation, "subscription_id")
	if err != nil {
		return provider.Page{}, err
	}
	rg, err := nativeIdentifier(req.Params, "resource_group")
	if err != nil {
		return provider.Page{}, err
	}
	cluster, err := nativeIdentifier(req.Params, "cluster")
	if err != nil {
		return provider.Page{}, err
	}
	token, err := a.accessToken(ctx, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := a.managedClusterRegion(ctx, token, sub, rg, cluster, req.Region, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	endpoint := "https://management.azure.com/subscriptions/" + url.PathEscape(sub) + "/resourceGroups/" + url.PathEscape(rg) + "/providers/Microsoft.ContainerService/managedClusters/" + url.PathEscape(cluster) + "/listClusterUserCredential?api-version=2025-10-01&format=exec"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return provider.Page{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return provider.Page{}, fmt.Errorf("cluster user configuration request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return provider.Page{}, &provider.Error{Code: "cluster_credentials_error", Operation: spec.operation, Message: fmt.Sprintf("user configuration HTTP %d", response.StatusCode)}
	}
	var credentials struct {
		Kubeconfigs []struct {
			Value string `json:"value"`
		} `json:"kubeconfigs"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&credentials); err != nil || len(credentials.Kubeconfigs) != 1 {
		return provider.Page{}, fmt.Errorf("invalid user configuration")
	}
	raw, err := base64.StdEncoding.DecodeString(credentials.Kubeconfigs[0].Value)
	if err != nil {
		return provider.Page{}, fmt.Errorf("invalid user configuration")
	}
	var cfg struct {
		Clusters []struct {
			Cluster struct {
				Server string `yaml:"server"`
				CA     string `yaml:"certificate-authority-data"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
		Users []struct {
			User struct {
				Exec      any    `yaml:"exec"`
				ClientKey string `yaml:"client-key-data"`
				Token     string `yaml:"token"`
			} `yaml:"user"`
		} `yaml:"users"`
	}
	if err = yaml.Unmarshal(raw, &cfg); err != nil || len(cfg.Clusters) != 1 || len(cfg.Users) != 1 || cfg.Users[0].User.Exec == nil || cfg.Users[0].User.ClientKey != "" || cfg.Users[0].User.Token != "" {
		return provider.Page{}, fmt.Errorf("Entra exec user configuration required; local credentials rejected")
	}
	server, err := url.Parse(cfg.Clusters[0].Cluster.Server)
	if err != nil || server.Scheme != "https" || !strings.HasSuffix(server.Hostname(), ".azmk8s.io") || server.User != nil || server.RawQuery != "" || server.Fragment != "" || (server.Port() != "" && server.Port() != "443") {
		return provider.Page{}, fmt.Errorf("invalid AKS endpoint")
	}
	cert, err := base64.StdEncoding.DecodeString(cfg.Clusters[0].Cluster.CA)
	if err != nil {
		return provider.Page{}, fmt.Errorf("invalid cluster CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		return provider.Page{}, fmt.Errorf("invalid cluster CA")
	}
	defaultCred, secretCred, err := a.credential()
	if err != nil {
		return provider.Page{}, fmt.Errorf("Entra credential unavailable")
	}
	options := policy.TokenRequestOptions{Scopes: []string{"6dae42f8-4368-4678-94ff-3960e28e3630/.default"}}
	bearer := ""
	if defaultCred != nil {
		access, e := defaultCred.GetToken(ctx, options)
		if e != nil {
			return provider.Page{}, fmt.Errorf("Entra AKS token request failed")
		}
		bearer = access.Token
	} else {
		access, e := secretCred.GetToken(ctx, options)
		if e != nil {
			return provider.Page{}, fmt.Errorf("Entra AKS token request failed")
		}
		bearer = access.Token
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	kube := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	server.Path = "/api/v1/nodes"
	if spec.domain == "live_pods" {
		server.Path = "/api/v1/pods"
	}
	server.RawQuery = "limit=500"
	if spec.domain == "live_pods" {
		server.RawQuery = podParams
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, server.String(), nil)
	if err != nil {
		return provider.Page{}, err
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	response, err = kube.Do(request)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "cluster_connection_error", Operation: spec.operation, Message: "cluster API connection or TLS validation failed"}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return provider.Page{}, &provider.Error{Code: "cluster_api_error", Operation: spec.operation, Message: fmt.Sprintf("Kubernetes list HTTP %d", response.StatusCode)}
	}
	if spec.domain == "live_pods" {
		page, err := decodeLivePods(response.Body, a.Provider(), a.name, sub, region, cluster, req)
		page.Requests++
		return page, err
	}
	var list struct {
		Items    []liveNode `json:"items"`
		Metadata struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&list); err != nil {
		return provider.Page{}, fmt.Errorf("invalid node response")
	}
	if list.Metadata.Continue != "" {
		return provider.Page{}, fmt.Errorf("incomplete node list rejected")
	}
	page := liveNodeRows(a.name, sub, region, cluster, list.Items)
	page.Requests++
	for _, row := range page.Rows {
		row["provider"] = string(model.ProviderAzure)
		scope := row["scope"].(map[string]any)
		delete(scope, "project_id")
		scope["subscription_id"] = sub
	}
	return page, nil
}
