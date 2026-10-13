package dsl

import (
	"testing"
	"time"
)

func TestSecondMetricsRequireExclusiveTencentScope(t *testing.T) {
	for _, step := range []string{"5s", "10s"} {
		if _, e := Parse(`metrics | scope provider in ("tencent"), profile in ("tencent-test") | where metric == "QCE/CVM::CpuUsage?InstanceId=test" | range last 10m | step `+step, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	for _, scope := range []string{`scope provider in ("azure")`, `scope provider in ("tencent", "aws")`, `scope profile in ("tencent-test")`} {
		if _, e := Parse(`metrics | `+scope+` | where metric == "cpu" | range last 10m | step 10s`, time.Now()); e == nil {
			t.Fatalf("unscoped second query accepted: %s", scope)
		}
	}
	if _, e := Parse(`metrics | scope provider in ("tencent") | where metric == "cpu" | range last 10m | step 1s`, time.Now()); e == nil {
		t.Fatal("unsupported second interval accepted")
	}
}
