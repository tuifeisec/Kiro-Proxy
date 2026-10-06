package store

import (
	"math"
	"testing"
)

func TestTypesafeCreditsChargeInputOnly(t *testing.T) {
	tests := []struct {
		name          string
		inputPrice    float64
		wantInputCost float64
		wantCredits   float64
	}{
		{name: "provider default", wantInputCost: 0.042, wantCredits: 1.05},
		{name: "configured input price", inputPrice: 0.084, wantInputCost: 0.084, wantCredits: 2.1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &Model{Provider: "typesafe", InputPrice: tt.inputPrice}
			breakdown := CalculateCreditBreakdown(1_000_000, 1_000_000, 0, 0, model)
			if math.Abs(breakdown.Input-tt.wantInputCost) > 1e-12 ||
				breakdown.Output != 0 || breakdown.CacheRead != 0 || breakdown.CacheWrite != 0 {
				t.Fatalf("USD breakdown = %+v, want input=%v and free output/cache", breakdown, tt.wantInputCost)
			}

			credits := CalculateCredits(1_000_000, 1_000_000, 0, 0, model)
			if math.Abs(credits-tt.wantCredits) > 1e-12 {
				t.Fatalf("credits = %.12f, want %.12f", credits, tt.wantCredits)
			}
		})
	}
}
