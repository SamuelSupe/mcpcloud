package providers

import (
	"encoding/json"
	"testing"
)

func TestTencentLiveResponseFieldRegression(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	var db tencentCDBDetailResponse
	if err := json.Unmarshal([]byte(`{"Response":{"Items":[{"InstanceId":"db-test","Status":1,"TagList":[{"TagKey":"cluster","TagValue":"test"},{}]}]}}`), &db); err != nil {
		t.Fatal(err)
	}
	row := a.tencentCDBDetailRow(db.Response.Items[0], "ap-jakarta", "123")
	if row["state"] != "running" {
		t.Fatalf("state: %v", row["state"])
	}
	tags := row["tags"].(map[string]any)
	if len(tags) != 1 || tags["cluster"] != "test" {
		t.Fatalf("tags: %v", tags)
	}
	var cluster tencentTKEDetailResponse
	if err := json.Unmarshal([]byte(`{"Response":{"Clusters":[{"ClusterId":"cls-test","ClusterNodeNum":49,"ClusterNetworkSettings":{"VpcId":"vpc-test","Subnets":["subnet-test"]}}]}}`), &cluster); err != nil {
		t.Fatal(err)
	}
	row = a.tencentTKEDetailRow(cluster.Response.Clusters[0], "ap-jakarta", "123")
	attrs := row["attributes"].(map[string]any)
	if attrs["vpc_id"] != "vpc-test" || attrs["node_count"] != 49 {
		t.Fatalf("network/node mapping: %v", attrs)
	}
	if err := json.Unmarshal([]byte(`{"Response":{"Items":[{"Status":99}]}}`), &db); err != nil {
		t.Fatal(err)
	}
	row = a.tencentCDBDetailRow(db.Response.Items[0], "ap-jakarta", "123")
	if row["state"] != "unknown" {
		t.Fatalf("unknown state mislabeled: %v", row["state"])
	}
}

func TestTencentMetricUnits(t *testing.T) {
	for _, pair := range [][2]string{{"QCE/CDB", "CpuUseRate"}, {"QCE/CDB", "VolumeRate"}, {"QCE/REDIS_MEM", "MemUtil"}} {
		if tencentMetricUnit(pair[0], pair[1]) != "%" {
			t.Errorf("missing percent unit: %v", pair)
		}
	}
	for _, tt := range []struct{ ns, name, want string }{{"QCE/CVM", "CpuUsage", "%"}, {"QCE/CVM", "MemUsage", "%"}, {"QCE/CVM", "WanIntraffic", "Mbps"}, {"QCE/CVM", "CpuLoadavg", "1"}, {"OTHER", "CpuUsage", ""}, {"QCE/CVM", "Unknown", ""}} {
		if got := tencentMetricUnit(tt.ns, tt.name); got != tt.want {
			t.Errorf("%s/%s unit=%q want=%q", tt.ns, tt.name, got, tt.want)
		}
	}
}
