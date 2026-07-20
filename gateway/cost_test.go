package gateway

import (
	"math"
	"testing"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestEstimateCostUSD(t *testing.T) {
	cases := []struct {
		name    string
		usage   Usage
		pricing Pricing
		want    float64
	}{
		{
			name:    "exactly 1M prompt tokens costs exactly the per-1M prompt price",
			usage:   Usage{PromptTokens: 1_000_000, CompletionTokens: 0},
			pricing: Pricing{PromptPer1M: 0.05, CompletionPer1M: 0.08},
			want:    0.05,
		},
		{
			name:    "exactly 1M completion tokens costs exactly the per-1M completion price",
			usage:   Usage{PromptTokens: 0, CompletionTokens: 1_000_000},
			pricing: Pricing{PromptPer1M: 0.05, CompletionPer1M: 0.08},
			want:    0.08,
		},
		{
			name:    "zero usage costs zero regardless of pricing",
			usage:   Usage{PromptTokens: 0, CompletionTokens: 0},
			pricing: Pricing{PromptPer1M: 5.00, CompletionPer1M: 15.00},
			want:    0,
		},
		{
			name:    "realistic small request: 1000 prompt + 500 completion tokens",
			usage:   Usage{PromptTokens: 1000, CompletionTokens: 500},
			pricing: Pricing{PromptPer1M: 0.05, CompletionPer1M: 0.08},
			want:    1000.0/1_000_000*0.05 + 500.0/1_000_000*0.08, // 0.00009
		},
		{
			name:    "zero pricing (e.g. a free/local provider) costs zero regardless of usage",
			usage:   Usage{PromptTokens: 50_000, CompletionTokens: 20_000},
			pricing: Pricing{PromptPer1M: 0, CompletionPer1M: 0},
			want:    0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EstimateCostUSD(c.usage, c.pricing)
			if !almostEqual(got, c.want) {
				t.Fatalf("EstimateCostUSD(%+v, %+v) = %.10f, want %.10f", c.usage, c.pricing, got, c.want)
			}
		})
	}
}
