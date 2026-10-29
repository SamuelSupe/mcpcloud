package providers

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"mcpcloud/internal/provider"
)

// Detail APIs only return project identity. Resolve hierarchy before the read
// when the profile grants access through a folder or organization instead.
func (a *gcpAdapter) readScopedDetail(ctx context.Context, req provider.NativeRequest, read func(*gcpAdapter) (provider.Page, error)) (provider.Page, error) {
	if len(a.profile.Scopes.Projects) > 0 || strings.HasPrefix(a.profile.Options["asset_scope"], "projects/") {
		return read(a)
	}
	allowed := a.configuredScopes()
	if len(allowed) == 0 {
		return read(a)
	}
	project, err := nativeIdentifier(req.Params, "project_id")
	if err != nil || project == "" {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: req.Operation, Message: "project_id is required for a scoped detail read"}
	}
	client, err := a.httpClient(ctx)
	if err != nil {
		return provider.Page{}, err
	}
	var ancestry struct {
		Ancestor []struct {
			ResourceID struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"resourceId"`
		} `json:"ancestor"`
	}
	endpoint := "https://cloudresourcemanager.googleapis.com/v1beta1/projects/" + url.PathEscape(project) + ":getAncestry"
	if err := gcpJSONRequest(ctx, client, http.MethodPost, endpoint, nil, req.Operation, &ancestry); err != nil {
		return provider.Page{Requests: 1}, err
	}
	var folders []string
	organization := ""
	matched := false
	for _, ancestor := range ancestry.Ancestor {
		id := ancestor.ResourceID.ID
		if id == "" {
			continue
		}
		switch ancestor.ResourceID.Type {
		case "folder":
			folders = append(folders, "folders/"+id)
			matched = matched || stringIn(allowed, "folders/"+id)
		case "organization":
			organization = id
			matched = matched || stringIn(allowed, "organizations/"+id)
		}
	}
	if !matched {
		return provider.Page{Requests: 1}, &provider.Error{Code: "scope_not_allowed", Operation: req.Operation, Message: "project ancestry is outside the profile allowlist"}
	}
	// Restrict this request's adapter copy to the verified project; never broaden
	// or mutate the shared profile while concurrent reads are in flight.
	scoped := *a
	scoped.profile.Scopes.Projects = []string{project}
	page, err := read(&scoped)
	page.Requests++
	for _, row := range page.Rows {
		scope := row["scope"].(map[string]any)
		scope["folder_ids"] = folders
		scope["organization_id"] = organization
	}
	return page, err
}
