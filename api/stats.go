// api/stats.go
// 统计数据接口：读内存计数器，O(1)，不查数据库
// 支持 ?type=daily&start=2024-01-01&end=2024-01-07 查询按日模型统计

package api

import (
	"kiro-proxy/billing"
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"net/http"
	"time"
)

type statsResponse struct {
	store.StatsSnapshot
	CostUSD      float64 `json:"cost_usd"`
	TodayCostUSD float64 `json:"today_cost_usd"`
}

type dailyStatsResponse struct {
	store.DailyModelStats
	CostUSD                float64  `json:"cost_usd"`
	AverageTokensPerSecond *float64 `json:"average_tokens_per_second"`
}

func newStatsResponse(snapshot store.StatsSnapshot) statsResponse {
	return statsResponse{
		StatsSnapshot: snapshot,
		CostUSD:       snapshot.Credits * billing.CreditUSD,
		TodayCostUSD:  snapshot.TodayCredits * billing.CreditUSD,
	}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if err := s.stats.Clear(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w, newStatsResponse(s.stats.Snapshot()))
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 检查是否查询日统计
	query := r.URL.Query()
	if query.Get("type") == "cache" {
		writeOK(w, map[string]interface{}{"tools": converter.GlobalToolCacheStats()})
		return
	}
	if query.Get("type") == "daily" {
		startDate := query.Get("start")
		endDate := query.Get("end")
		if startDate == "" {
			// 默认最近 7 天
			startDate = store.StatsDay(time.Now().AddDate(0, 0, -6))
		}
		if endDate == "" {
			endDate = store.StatsDay(time.Now())
		}
		daily := s.stats.GetDailyStats(startDate, endDate)
		dailyWithUSD := make([]dailyStatsResponse, 0, len(daily))
		for _, row := range daily {
			response := dailyStatsResponse{
				DailyModelStats: row,
				CostUSD:         row.Credits * billing.CreditUSD,
			}
			if row.GenerationSpeedSamples > 0 {
				averageTokensPerSecond := row.GenerationSpeedSum / float64(row.GenerationSpeedSamples)
				response.AverageTokensPerSecond = &averageTokensPerSecond
			}
			dailyWithUSD = append(dailyWithUSD, response)
		}
		writeOK(w, map[string]interface{}{
			"daily": dailyWithUSD,
			"start": startDate,
			"end":   endDate,
		})
		return
	}

	writeOK(w, newStatsResponse(s.stats.Snapshot()))
}
