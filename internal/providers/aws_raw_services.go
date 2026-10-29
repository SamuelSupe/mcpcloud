package providers

// This file uses the SDK's already-present config, credentials, and SigV4 signer
// for two APIs whose generated service modules are unavailable in this offline
// build environment. Requests remain fixed, read-only AWS actions.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	awsbase "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"mcpcloud/internal/provider"
)

type rawCachePage struct {
	Clusters []struct {
		ID     string `xml:"CacheClusterId"`
		Engine string `xml:"Engine"`
		Status string `xml:"CacheClusterStatus"`
		Nodes  int    `xml:"NumCacheNodes"`
	} `xml:"DescribeCacheClustersResult>CacheClusters>CacheCluster"`
	Groups []struct {
		ID     string `xml:"ReplicationGroupId"`
		Status string `xml:"Status"`
		Engine string `xml:"Engine"`
	} `xml:"DescribeReplicationGroupsResult>ReplicationGroups>ReplicationGroup"`
	Marker      string `xml:"DescribeCacheClustersResult>Marker"`
	GroupMarker string `xml:"DescribeReplicationGroupsResult>Marker"`
}

type rawCacheCursor struct {
	Marker string `json:"marker,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

func decodeRawCacheCursor(token, operation string) (rawCacheCursor, error) {
	if token == "" {
		return rawCacheCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	var cursor rawCacheCursor
	if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Offset < 0 || (cursor.Marker == "" && cursor.Offset == 0) {
		return rawCacheCursor{}, &provider.Error{Code: "invalid_cursor", Operation: operation, Message: "invalid ElastiCache cursor"}
	}
	return cursor, nil
}

func encodeRawCacheCursor(cursor rawCacheCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

type rawECSCursor struct {
	Token  string `json:"t,omitempty"`
	Offset int    `json:"o,omitempty"`
}

func encodeRawECSCursor(cursor rawECSCursor) string {
	raw, _ := json.Marshal(cursor)
	return "v1." + base64.RawURLEncoding.EncodeToString(raw)
}

func decodeRawECSCursor(token, operation string) (rawECSCursor, error) {
	if token == "" {
		return rawECSCursor{}, nil
	}
	bad := func() (rawECSCursor, error) {
		return rawECSCursor{}, &provider.Error{Code: "invalid_cursor", Operation: operation, Message: "invalid ECS cursor"}
	}
	if !strings.HasPrefix(token, "v1.") {
		return bad()
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "v1."))
	var cursor rawECSCursor
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &cursor) != nil || cursor.Offset < 0 {
		return bad()
	}
	return cursor, nil
}

func awsRegionalEndpoint(region, service string) string {
	suffix := "amazonaws.com"
	if strings.HasPrefix(region, "cn-") {
		suffix = "amazonaws.com.cn"
	}
	return "https://" + service + "." + region + "." + suffix + "/"
}

func (a *awsAdapter) fetchAWSRawService(ctx context.Context, cfg awsbase.Config, req provider.NativeRequest, region, account string, limit int32) (provider.Page, error) {
	if strings.HasPrefix(req.Operation, "aws.elasticache.") {
		return a.rawElastiCache(ctx, cfg, req, region, account, limit)
	}
	return a.rawECS(ctx, cfg, req, region, account, limit)
}

func rawSigned(ctx context.Context, cfg awsbase.Config, service, method, endpoint string, body io.Reader, hash string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, err
	}
	if err = v4.NewSigner().SignHTTP(ctx, creds, req, hash, service, cfg.Region, time.Now()); err != nil {
		return nil, err
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

func (a *awsAdapter) rawElastiCache(ctx context.Context, cfg awsbase.Config, req provider.NativeRequest, region, account string, limit int32) (provider.Page, error) {
	action := "DescribeCacheClusters"
	if req.Operation == "aws.elasticache.describe_replication_groups" {
		action = "DescribeReplicationGroups"
	}
	cursor, err := decodeRawCacheCursor(req.PageToken, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	// ElastiCache requires MaxRecords >= 20. Re-read a provider page with an
	// offset cursor when MCP clients request a smaller page so the MCP limit is
	// still honored.
	const providerLimit = 100
	q := url.Values{"Action": {action}, "Version": {"2015-02-02"}, "MaxRecords": {strconv.Itoa(int(providerLimit))}}
	if cursor.Marker != "" {
		q.Set("Marker", cursor.Marker)
	}
	endpoint := strings.TrimSuffix(awsRegionalEndpoint(region, "elasticache"), "/") + "/?" + q.Encode()
	empty := sha256.Sum256(nil)
	resp, err := rawSigned(ctx, cfg, "elasticache", http.MethodGet, endpoint, nil, hex.EncodeToString(empty[:]), nil)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(classifyAWSError(err), req.Operation)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return provider.Page{}, err
	}
	if resp.StatusCode >= 300 {
		return provider.Page{}, &provider.Error{Code: "aws_api_error", Operation: req.Operation, Message: "ElastiCache read returned HTTP " + strconv.Itoa(resp.StatusCode)}
	}
	var out rawCachePage
	if err = xml.Unmarshal(b, &out); err != nil {
		return provider.Page{}, err
	}
	rows := []map[string]any{}
	row := func(id, kind string) map[string]any {
		return newDeepDetailRow(a.Provider(), a.name, id, id, "elasticache", "AWS::ElastiCache::"+kind, "database", "cache", region, account)
	}
	for _, v := range out.Clusters {
		r := row(v.ID, "CacheCluster")
		r["state"] = v.Status
		r["attributes"] = map[string]any{"engine": v.Engine, "node_count": v.Nodes}
		rows = append(rows, r)
	}
	for _, v := range out.Groups {
		r := row(v.ID, "ReplicationGroup")
		r["state"] = v.Status
		r["attributes"] = map[string]any{"engine": v.Engine}
		rows = append(rows, r)
	}
	if cursor.Offset >= len(rows) && cursor.Offset != 0 {
		return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: req.Operation, Message: "ElastiCache cursor offset is outside the provider page"}
	}
	end := min(cursor.Offset+int(limit), len(rows))
	page := provider.Page{Rows: rows[cursor.Offset:end], Requests: 1}
	providerMarker := out.Marker
	if providerMarker == "" {
		providerMarker = out.GroupMarker
	}
	if end < len(rows) {
		page.NextToken = encodeRawCacheCursor(rawCacheCursor{Marker: cursor.Marker, Offset: end})
	} else if providerMarker != "" {
		page.NextToken = encodeRawCacheCursor(rawCacheCursor{Marker: providerMarker})
	}
	page.Scanned = len(page.Rows)
	return page, nil
}

func (a *awsAdapter) rawECS(ctx context.Context, cfg awsbase.Config, req provider.NativeRequest, region, account string, limit int32) (provider.Page, error) {
	action := map[string]string{"aws.ecs.list_clusters": "ListClusters", "aws.ecs.list_services": "ListServices", "aws.ecs.list_tasks": "ListTasks"}[req.Operation]
	cursor, err := decodeRawECSCursor(req.PageToken, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	const providerLimit = 100
	payload := map[string]any{"maxResults": providerLimit}
	if cursor.Token != "" {
		payload["nextToken"] = cursor.Token
	}
	if req.Operation != "aws.ecs.list_clusters" {
		cluster, e := awsDirectIdentifier(req, "cluster_arn")
		if e != nil {
			return provider.Page{}, e
		}
		payload["cluster"] = cluster
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	endpoint := awsRegionalEndpoint(region, "ecs")
	resp, err := rawSigned(ctx, cfg, "ecs", http.MethodPost, endpoint, strings.NewReader(string(b)), hex.EncodeToString(sum[:]), map[string]string{"Content-Type": "application/x-amz-json-1.1", "X-Amz-Target": "AmazonEC2ContainerServiceV20141113." + action})
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(classifyAWSError(err), req.Operation)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return provider.Page{}, &provider.Error{Code: "aws_api_error", Operation: req.Operation, Message: "ECS read returned HTTP " + strconv.Itoa(resp.StatusCode)}
	}
	var out struct {
		ClusterArns, ServiceArns, TaskArns []string
		NextToken                          string
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return provider.Page{}, err
	}
	values := out.ClusterArns
	kind := "cluster"
	if req.Operation == "aws.ecs.list_services" {
		values = out.ServiceArns
		kind = "service"
	}
	if req.Operation == "aws.ecs.list_tasks" {
		values = out.TaskArns
		kind = "task"
	}
	providerRows := []map[string]any{}
	for _, id := range values {
		providerRows = append(providerRows, newDeepDetailRow(a.Provider(), a.name, id, lastName(id), "ecs", "AWS::ECS::"+ecsResourceKind(kind), "compute", kind, region, account))
	}
	if cursor.Offset >= len(providerRows) && cursor.Offset != 0 {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: req.Operation, Message: "ECS cursor offset is outside the provider page"}
	}
	end := min(cursor.Offset+int(limit), len(providerRows))
	rows := providerRows[cursor.Offset:end]
	next := ""
	if end < len(providerRows) {
		next = encodeRawECSCursor(rawECSCursor{Token: cursor.Token, Offset: end})
	} else if out.NextToken != "" {
		if out.NextToken == cursor.Token {
			return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: req.Operation, Message: "ECS returned a repeated pagination cursor"}
		}
		next = encodeRawECSCursor(rawECSCursor{Token: out.NextToken})
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}

func ecsResourceKind(kind string) string {
	switch kind {
	case "service":
		return "Service"
	case "task":
		return "Task"
	default:
		return "Cluster"
	}
}
