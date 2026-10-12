package providers

import "testing"

func TestAlibabaMetricUnitExactIdentity(t *testing.T) {
	for _, tt := range []struct{ namespace, metric, want string }{
		{"acs_ecs_dashboard", "CPUUtilization", "%"},
		{"acs_rds_dashboard", "CPUUtilization", ""},
		{"acs_ecs_dashboard", "cpuutilization", ""},
		{"acs_ecs_dashboard", "CPUUtilizationExtra", ""},
		{"acs_ecs_dashboard", "NetworkOut", ""},
		{"", "", ""},
	} {
		if got := alibabaMetricUnit(tt.namespace, tt.metric); got != tt.want {
			t.Errorf("unit(%q, %q) = %q, want %q", tt.namespace, tt.metric, got, tt.want)
		}
	}
}
