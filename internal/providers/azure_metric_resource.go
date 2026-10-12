package providers

import (
	"fmt"
	"net/url"
	"strings"
)

// Accept only ARM resource IDs in the already-authorized subscription and
// matching metric namespace. Never accept a caller-provided host or query.
func azureMetricResourcePath(subscription, namespace, resourceID string) (string, error) {
	parts := strings.Split(resourceID, "/")
	if len(parts) < 9 || len(parts)%2 != 1 || parts[0] != "" || !strings.EqualFold(parts[1], "subscriptions") || !strings.EqualFold(parts[2], subscription) || !strings.EqualFold(parts[3], "resourceGroups") || !strings.EqualFold(parts[5], "providers") {
		return "", fmt.Errorf("resource_id must be an ARM resource ID in the allowed subscription")
	}
	nativeType := parts[6]
	for i := 7; i < len(parts); i += 2 {
		nativeType += "/" + parts[i]
	}
	if !strings.EqualFold(nativeType, namespace) {
		return "", fmt.Errorf("resource_id type must match the metric namespace")
	}
	for i := 1; i < len(parts); i++ {
		if parts[i] == "" || parts[i] == "." || parts[i] == ".." || strings.ContainsAny(parts[i], "\\%?#\r\n") {
			return "", fmt.Errorf("resource_id contains an invalid path segment")
		}
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/"), nil
}
