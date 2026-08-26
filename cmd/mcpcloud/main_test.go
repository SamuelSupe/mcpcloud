package main

import (
	"strings"
	"testing"
)

func TestProbeProfileRequiresExactlyOneSelectionBeforeLoadingConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "neither", args: nil},
		{name: "profile and all", args: []string{"--profile", "aws-prod", "--all"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := probeProfile(tc.args)
			if err == nil || !strings.Contains(err.Error(), "exactly one of --profile or --all") {
				t.Fatalf("probeProfile(%#v) error = %v, want selection error", tc.args, err)
			}
		})
	}
}
