package providers

import (
	"context"
	"net/url"
)

func verifyAzureResourceRegion(actual, requested string, configured []string, operation string) (string, error) {
	return verifyDetailRegion(actual, requested, configured, operation)
}

func (a *azureAdapter) managedClusterRegion(ctx context.Context, token, subscription, resourceGroup, cluster, requested, operation string) (string, error) {
	endpoint := "https://management.azure.com/subscriptions/" + url.PathEscape(subscription) + "/resourceGroups/" + url.PathEscape(resourceGroup) + "/providers/Microsoft.ContainerService/managedClusters/" + url.PathEscape(cluster) + "?api-version=2025-10-01"
	var detail struct {
		ID       string `json:"id"`
		Location string `json:"location"`
	}
	if err := azureDetailGET(ctx, endpoint, token, operation, &detail); err != nil {
		return "", err
	}
	if detail.ID == "" {
		return "", deepNotFound(operation, "cluster")
	}
	return verifyAzureResourceRegion(detail.Location, requested, a.profile.Regions, operation)
}
