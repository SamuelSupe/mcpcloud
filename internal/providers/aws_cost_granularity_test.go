package providers

import (
	"testing"
	"time"

	costtypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

func TestAWSCostGranularity(t *testing.T) {
	utc := time.UTC
	if got := awsCostGranularity(time.Date(2026, time.August, 1, 0, 0, 0, 0, utc), time.Date(2026, time.September, 1, 0, 0, 0, 0, utc)); got != costtypes.GranularityMonthly {
		t.Fatalf("monthly boundary = %s, want MONTHLY", got)
	}
	if got := awsCostGranularity(time.Date(2026, time.August, 2, 0, 0, 0, 0, utc), time.Date(2026, time.September, 1, 0, 0, 0, 0, utc)); got != costtypes.GranularityDaily {
		t.Fatalf("partial range = %s, want DAILY", got)
	}
}
