package providers

// This file uses the SDK's already-present config, credentials, and SigV4 signer
// for two APIs whose generated service modules are unavailable in this offline
// build environment. Requests remain fixed, read-only AWS actions.

import (
	"context"
	"crypto/sha256"
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
	q := url.Values{"Action": {action}, "Version": {"2015-02-02"}, "MaxRecords": {""}}
	q.Set("MaxRecords", strconv.Itoa(int(limit)))
	if req.PageToken != "" {
		q.Set("Marker", req.PageToken)
	}
	endpoint := "https://elasticache." + region + ".amazonaws.com.cn/?" + q.Encode()
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
	page := provider.Page{Rows: []map[string]any{}, Requests: 1}
	row := func(id, kind string) map[string]any {
		return newDeepDetailRow(a.Provider(), a.name, id, id, "elasticache", "AWS::ElastiCache::"+kind, "database", "cache", region, account)
	}
	for _, v := range out.Clusters {
		r := row(v.ID, "CacheCluster")
		r["state"] = v.Status
		r["attributes"] = map[string]any{"engine": v.Engine, "node_count": v.Nodes}
		page.Rows = append(page.Rows, r)
	}
	for _, v := range out.Groups {
		r := row(v.ID, "ReplicationGroup")
		r["state"] = v.Status
		r["attributes"] = map[string]any{"engine": v.Engine}
		page.Rows = append(page.Rows, r)
	}
	page.NextToken = out.Marker
	if page.NextToken == "" {
		page.NextToken = out.GroupMarker
	}
	page.Scanned = len(page.Rows)
	return page, nil
}

func (a *awsAdapter) rawECS(ctx context.Context, cfg awsbase.Config, req provider.NativeRequest, region, account string, limit int32) (provider.Page, error) {
	action := map[string]string{"aws.ecs.list_clusters": "ListClusters", "aws.ecs.list_services": "ListServices", "aws.ecs.list_tasks": "ListTasks"}[req.Operation]
	payload := map[string]any{"maxResults": limit}
	if req.PageToken != "" {
		payload["nextToken"] = req.PageToken
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
	endpoint := "https://ecs." + region + ".amazonaws.com.cn/"
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
	page := provider.Page{Rows: []map[string]any{}, NextToken: out.NextToken, Requests: 1}
	for _, id := range values {
		page.Rows = append(page.Rows, newDeepDetailRow(a.Provider(), a.name, id, lastName(id), "ecs", "AWS::ECS::"+ecsResourceKind(kind), "compute", kind, region, account))
	}
	page.Scanned = len(page.Rows)
	return page, nil
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
