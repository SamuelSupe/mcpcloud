package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTencentRedisFieldIsolation(t *testing.T) {
	var d tencentRedisDetail
	if err := json.Unmarshal([]byte(`{"InstanceId":"crs-test","Status":2,"Size":8192,"RedisShardNum":1,"RedisReplicasNum":1,"ProjectId":0,"Engine":"Redis","InstanceTags":[{"TagKey":"cluster","TagValue":"test"},{}],"WanIp":"secret-ip","WanAddress":"secret-host","Password":"secret-password","NodeSet":[{"UserScript":"secret-script"}]}`), &d); err != nil {
		t.Fatal(err)
	}
	a := &tencentAdapter{name: "test"}
	row := a.tencentRedisRow(d, "ap-jakarta", "123")
	attrs := row["attributes"].(map[string]any)
	if row["state"] != "running" || attrs["memory_mb"] != float64(8192) || attrs["shard_count"] != 1 || attrs["replica_count"] != 1 {
		t.Fatalf("incorrect row: %v", row)
	}
	if len(row["tags"].(map[string]any)) != 1 {
		t.Fatal("empty tag included")
	}
	raw, _ := json.Marshal(row)
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("secret field leaked")
	}
	absent := a.tencentRedisRow(tencentRedisDetail{}, "ap-jakarta", "123")["attributes"].(map[string]any)
	if _, ok := absent["memory_mb"]; ok {
		t.Fatal("missing memory became zero")
	}
}
