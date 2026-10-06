package store

import (
	"math"
	"testing"
)

func TestCalculateCreditsUsesUSDPerCreditAndProviderDefaults(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		wantCosts CreditBreakdown
		want      float64
	}{
		{name: "openai", provider: "openai", wantCosts: CreditBreakdown{Input: 4.25, Output: 30, CacheRead: 0.05, CacheWrite: 0.3125}, want: 865.3125},
		{name: "anthropic", provider: "anthropic", wantCosts: CreditBreakdown{Input: 4.25, Output: 25, CacheRead: 0.05, CacheWrite: 0.3125}, want: 740.3125},
		{name: "gemini", provider: "gemini", wantCosts: CreditBreakdown{Input: 1.275, Output: 9, CacheRead: 0.015, CacheWrite: 0.075}, want: 259.125},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &Model{Provider: tt.provider}
			gotCosts := CalculateCreditBreakdown(1_000_000, 1_000_000, 100_000, 50_000, model)
			if math.Abs(gotCosts.Input-tt.wantCosts.Input) > 1e-12 ||
				math.Abs(gotCosts.Output-tt.wantCosts.Output) > 1e-12 ||
				math.Abs(gotCosts.CacheRead-tt.wantCosts.CacheRead) > 1e-12 ||
				math.Abs(gotCosts.CacheWrite-tt.wantCosts.CacheWrite) > 1e-12 {
				t.Fatalf("USD breakdown = %+v, want %+v", gotCosts, tt.wantCosts)
			}
			got := CalculateCredits(1_000_000, 1_000_000, 100_000, 50_000, model)
			if math.Abs(got-tt.want) > 1e-12 {
				t.Fatalf("credits = %.12f, want %.12f", got, tt.want)
			}
			if math.Abs(got-gotCosts.Total()/0.04) > 1e-12 {
				t.Fatalf("credits = %.12f, want USD total / 0.04 = %.12f", got, gotCosts.Total()/0.04)
			}
		})
	}
}

func TestCalculateCreditsAppliesMultiplierAndClampsNegativeCacheCounts(t *testing.T) {
	model := &Model{Provider: "openai", InputPrice: 5, OutputPrice: 30, CacheInputPrice: 0.5, CacheOutputPrice: 6.25, RateMultiplier: 2.5}
	got := CalculateCredits(1_000_000, 0, -100, -50, model)
	if math.Abs(got-312.5) > 1e-12 {
		t.Fatalf("credits = %v, want 312.5", got)
	}

	model.RateMultiplier = 0
	got = CalculateCredits(1_000_000, 0, 0, 0, model)
	if math.Abs(got-125) > 1e-12 {
		t.Fatalf("credits with zero multiplier = %v, want 125", got)
	}
}
