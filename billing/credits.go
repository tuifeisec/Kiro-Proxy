// Package billing 提供统一的本地美元费用与积分计算。
package billing

// CreditUSD 是一个本地估算积分对应的美元金额。
const CreditUSD = 0.04

// Prices 的单价单位是每百万 Token 的美元价格。RateMultiplier 小于或等于零时按 1 计算。
type Prices struct {
	Input          float64
	Output         float64
	CacheRead      float64
	CacheWrite     float64
	RateMultiplier float64
}

// Breakdown 保存各项美元费用。
type Breakdown struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Total 返回各项费用之和，单位为美元。
func (b Breakdown) Total() float64 {
	return b.Input + b.Output + b.CacheRead + b.CacheWrite
}

// CalculateBreakdown 按 Token 数量和每百万 Token 单价计算美元费用。
// 缓存 Token 从普通输入中扣除，负缓存数量按零计算。
func CalculateBreakdown(inputTokens, outputTokens, cacheRead, cacheWrite int, prices Prices) Breakdown {
	if cacheRead < 0 {
		cacheRead = 0
	}
	if cacheWrite < 0 {
		cacheWrite = 0
	}
	regularInput := inputTokens - cacheRead - cacheWrite
	if regularInput < 0 {
		regularInput = 0
	}
	rateMultiplier := prices.RateMultiplier
	if rateMultiplier <= 0 {
		rateMultiplier = 1
	}
	factor := rateMultiplier / 1_000_000
	return Breakdown{
		Input:      float64(regularInput) * prices.Input * factor,
		Output:     float64(outputTokens) * prices.Output * factor,
		CacheRead:  float64(cacheRead) * prices.CacheRead * factor,
		CacheWrite: float64(cacheWrite) * prices.CacheWrite * factor,
	}
}

// CalculateCredits 返回本地估算积分，计算方式为美元费用除以 CreditUSD。
func CalculateCredits(inputTokens, outputTokens, cacheRead, cacheWrite int, prices Prices) float64 {
	return CalculateBreakdown(inputTokens, outputTokens, cacheRead, cacheWrite, prices).Total() / CreditUSD
}
