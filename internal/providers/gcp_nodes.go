package providers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"golang.org/x/oauth2"
	"io"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"net"
	"net/http"
	"net/url"
	"time"
)

func init() {
	deepDetailCatalog[model.ProviderGCP] = append(deepDetailCatalog[model.ProviderGCP], deepDetailSpec{operation: "gcp.kubernetes.nodes.list", service: "container", domain: "live_nodes", kind: "node", description: "List actual Kubernetes Nodes with original read-only Google identity", parameters: detailParameters("project_id", "location", "cluster"), required: []string{"project_id", "location", "cluster"}})
}

type liveNode struct {
	Metadata struct {
		Name string `json:"name"`
		UID  string `json:"uid"`
	} `json:"metadata"`
	Spec struct {
		ProviderID    string `json:"providerID"`
		Unschedulable bool   `json:"unschedulable"`
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
		NodeInfo struct {
			KubeletVersion  string `json:"kubeletVersion"`
			OperatingSystem string `json:"operatingSystem"`
			Architecture    string `json:"architecture"`
		} `json:"nodeInfo"`
	} `json:"status"`
}

func liveNodeRows(profile, project, region, cluster string, nodes []liveNode) provider.Page {
	page := provider.Page{Requests: 2, Scanned: len(nodes)}
	for _, n := range nodes {
		row := baseRow(model.ProviderGCP, profile, cluster+"/nodes/"+n.Metadata.Name, n.Metadata.Name, "kubernetes", "Node", region, project, time.Now())
		row["domain"] = "kubernetes"
		row["kind"] = "node"
		attrs := row["attributes"].(map[string]any)
		attrs["cluster"] = cluster
		attrs["provider_id"] = n.Spec.ProviderID
		attrs["unschedulable"] = n.Spec.Unschedulable
		attrs["kubelet_version"] = n.Status.NodeInfo.KubeletVersion
		attrs["operating_system"] = n.Status.NodeInfo.OperatingSystem
		attrs["architecture"] = n.Status.NodeInfo.Architecture
		attrs["data_source"] = "kubernetes_api"
		for _, c := range n.Status.Conditions {
			if c.Type == "Ready" {
				attrs["ready"] = c.Status
			}
		}
		page.Rows = append(page.Rows, row)
	}
	return page
}

func (a *gcpAdapter) readLiveNodes(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
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
	project, err := nativeIdentifier(req.Params, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	project, err = exactDetailScope(a.profile.Scopes.Projects, project, spec.operation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	location, err := nativeIdentifier(req.Params, "location")
	if err != nil {
		return provider.Page{}, err
	}
	cluster, err := nativeIdentifier(req.Params, "cluster")
	if err != nil {
		return provider.Page{}, err
	}
	client, err := a.httpClient(ctx)
	if err != nil {
		return provider.Page{}, err
	}
	var detail struct {
		Endpoint   string `json:"endpoint"`
		MasterAuth struct {
			CA string `json:"clusterCaCertificate"`
		} `json:"masterAuth"`
	}
	endpoint := "https://container.googleapis.com/v1/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(location) + "/clusters/" + url.PathEscape(cluster)
	if err = gcpJSONRequest(ctx, client, http.MethodGet, endpoint, nil, spec.operation, &detail); err != nil {
		return provider.Page{}, err
	}
	ip := net.ParseIP(detail.Endpoint)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
		return provider.Page{}, fmt.Errorf("live nodes require a public IP cluster endpoint")
	}
	cert, err := base64.StdEncoding.DecodeString(detail.MasterAuth.CA)
	if err != nil {
		return provider.Page{}, fmt.Errorf("invalid cluster CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		return provider.Page{}, fmt.Errorf("invalid cluster CA")
	}
	auth, ok := client.Transport.(*oauth2.Transport)
	if !ok {
		return provider.Page{}, fmt.Errorf("unsupported authentication transport")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	kube := &http.Client{Transport: &oauth2.Transport{Source: auth.Source, Base: transport}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resource := "nodes"
	if spec.domain == "live_pods" {
		resource = "pods"
	}
	query := "limit=500"
	if spec.domain == "live_pods" {
		query = podParams
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort(detail.Endpoint, "443")+"/api/v1/"+resource+"?"+query, nil)
	if err != nil {
		return provider.Page{}, err
	}
	response, err := kube.Do(request)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "cluster_connection_error", Operation: spec.operation, Message: "cluster API connection or certificate validation failed"}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return provider.Page{}, &provider.Error{Code: "cluster_api_error", Operation: spec.operation, Message: fmt.Sprintf("Kubernetes list HTTP %d", response.StatusCode)}
	}
	if spec.domain == "live_pods" {
		return decodeLivePods(response.Body, a.Provider(), a.name, project, gcpRegionForLocation(location), cluster, req)
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
		return provider.Page{}, fmt.Errorf("node list exceeds supported limit; incomplete list rejected")
	}
	return liveNodeRows(a.name, project, gcpRegionForLocation(location), cluster, list.Items), nil
}
