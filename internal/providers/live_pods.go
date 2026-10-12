package providers

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func init() {
	for _, cloud := range []model.Provider{model.ProviderAzure, model.ProviderGCP} {
		keys := []string{"project_id", "location", "cluster"}
		service := "container"
		if cloud == model.ProviderAzure {
			keys = []string{"subscription_id", "resource_group", "cluster"}
			service = "containerservice"
		}
		deepDetailCatalog[cloud] = append(deepDetailCatalog[cloud], deepDetailSpec{operation: string(cloud) + ".kubernetes.pods.list", service: service, domain: "live_pods", kind: "pod", description: "Page through actual Pods with scoped continuation; identity/status only, no env, annotations, Secret references or spec bodies", parameters: detailParameters(keys...), required: keys})
	}
}

type podCursor struct {
	Profile  string `json:"profile"`
	Binding  string `json:"binding"`
	Continue string `json:"continue"`
}

func podPageSize(req provider.NativeRequest) int {
	if req.Limit <= 0 {
		return 100
	}
	return min(req.Limit, 100)
}

func podBinding(req provider.NativeRequest) string {
	value, _ := json.Marshal(struct {
		Operation, Region string
		Params            map[string]any
		Limit             int
	}{req.Operation, req.Region, req.Params, podPageSize(req)})
	return fmt.Sprintf("%x", sha256.Sum256(value))
}

func podContinuation(req provider.NativeRequest, profile string) (string, error) {
	if req.PageToken == "" {
		return "", nil
	}
	bad := &provider.Error{Code: "invalid_cursor", Operation: req.Operation, Message: "Pod cursor is invalid or belongs to another profile, cluster, region, operation or page size"}
	if len(req.PageToken) > 8192 {
		return "", bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(req.PageToken)
	if err != nil {
		return "", bad
	}
	var cursor podCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.Profile == "" || (profile != "" && cursor.Profile != profile) || cursor.Binding != podBinding(req) || !validPodContinue(cursor.Continue) {
		return "", bad
	}
	return cursor.Continue, nil
}

func validPodContinue(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\r\n\x00")
}

func podQuery(req provider.NativeRequest, profile string) (string, error) {
	next, err := podContinuation(req, profile)
	if err != nil {
		return "", err
	}
	values := url.Values{"limit": {strconv.Itoa(podPageSize(req))}}
	if next != "" {
		values.Set("continue", next)
	}
	return values.Encode(), nil
}

func decodeLivePods(body io.Reader, cloud model.Provider, profile, scope, region, cluster string, req provider.NativeRequest) (provider.Page, error) {
	var list struct {
		Metadata struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
				UID       string `json:"uid"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
			Status struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				ContainerStatuses []struct {
					Ready        bool `json:"ready"`
					RestartCount int  `json:"restartCount"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 8<<20)).Decode(&list); err != nil {
		return provider.Page{}, fmt.Errorf("invalid Pod response")
	}
	if len(list.Items) > podPageSize(req) {
		return provider.Page{}, fmt.Errorf("Pod response exceeds requested page size")
	}
	page := provider.Page{Requests: 2, Scanned: len(list.Items)}
	if list.Metadata.Continue != "" {
		previous, err := podContinuation(req, profile)
		if err != nil {
			return provider.Page{}, err
		}
		if !validPodContinue(list.Metadata.Continue) || list.Metadata.Continue == previous {
			return provider.Page{}, fmt.Errorf("invalid or repeated Pod continuation")
		}
		cursor := podCursor{Profile: profile, Binding: podBinding(req), Continue: list.Metadata.Continue}
		raw, _ := json.Marshal(cursor)
		page.NextToken = base64.RawURLEncoding.EncodeToString(raw)
		if len(page.NextToken) > 8192 {
			return provider.Page{}, fmt.Errorf("Pod continuation exceeds supported cursor size")
		}
	}
	for _, pod := range list.Items {
		if pod.Metadata.Name == "" || pod.Metadata.Namespace == "" {
			return provider.Page{}, fmt.Errorf("invalid Pod identity")
		}
		row := baseRow(cloud, profile, cluster+"/namespaces/"+pod.Metadata.Namespace+"/pods/"+pod.Metadata.Name, pod.Metadata.Name, "kubernetes", "Pod", region, scope, time.Now().UTC())
		row["domain"], row["kind"], row["state"] = "kubernetes", "pod", pod.Status.Phase
		attrs := map[string]any{"cluster": cluster, "namespace": pod.Metadata.Namespace, "uid": pod.Metadata.UID, "node_name": pod.Spec.NodeName, "phase": pod.Status.Phase, "data_source": "kubernetes_api"}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" {
				attrs["ready"] = condition.Status
			}
		}
		ready, restarts := 0, 0
		for _, container := range pod.Status.ContainerStatuses {
			if container.Ready {
				ready++
			}
			restarts += container.RestartCount
		}
		attrs["container_count"], attrs["ready_containers"], attrs["restart_count"] = len(pod.Status.ContainerStatuses), ready, restarts
		row["attributes"] = attrs
		page.Rows = append(page.Rows, row)
	}
	return page, nil
}
