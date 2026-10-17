package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestCodexFastModelResolution(t *testing.T) {
	for _, tc := range []struct{ model, upstream, tier string }{
		{"gpt-6.1-sol", "gpt-6.1-sol", "ultrafast"},
		{"gpt-6.1-sol-fast", "gpt-6.1-sol", "priority"},
		{"gpt-6.1-sol-fast(high)", "gpt-6.1-sol", "priority"},
		{"gpt-6.1-sol(high)", "gpt-6.1-sol", "ultrafast"},
		{"another-fast", "another-fast", "ultrafast"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if got := CodexBaseModel(tc.model); got != tc.upstream {
				t.Fatalf("upstream = %q, want %q", got, tc.upstream)
			}
			body := ApplyCodexFastModelTier([]byte(`{"service_tier":"ultrafast"}`), tc.model)
			if got := gjson.GetBytes(body, "service_tier").String(); got != tc.tier {
				t.Fatalf("tier = %q, want %q", got, tc.tier)
			}
		})
	}
}
