package config

import (
	"mcpcloud/internal/model"
	"testing"
)

func TestTencentPartnerBillingOptions(t *testing.T) {
	for _, tc := range []struct {
		options map[string]string
		valid   bool
	}{
		{map[string]string{"billing_source": "intl_partner_customer", "billing_currency": "USD"}, true},
		{map[string]string{"billing_source": "billing", "billing_currency": "CNY"}, true},
		{map[string]string{"billing_source": "intl_partner_customer", "billing_currency": "CNY"}, false},
		{map[string]string{"billing_source": "intl_partner_customer"}, false},
		{map[string]string{"billing_source": "typo"}, false},
	} {
		e := validateOptions("test", model.ProviderTencent, tc.options)
		if (e == nil) != tc.valid {
			t.Fatalf("%v: %v", tc.options, e)
		}
	}
}
