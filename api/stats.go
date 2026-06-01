// api/stats.go
// 统计数据接口：读内存计数器，O(1)，不查数据库
// 支持 ?type=daily&start=2024-01-01&end=2024-01-07 查询按日模型统计

package api

import (
	"net/http"
	"time"
)

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if err := s.stats.Clear(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w, s.stats.Snapshot())
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 检查是否查询日统计
	query := r.URL.Query()
	if query.Get("type") == "daily" {
		startDate := query.Get("start")
		endDate := query.Get("end")
		if startDate == "" {
			// 默认最近 7 天
			startDate = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
		}
		if endDate == "" {
			endDate = time.Now().Format("2006-01-02")
		}
		daily := s.stats.GetDailyStats(startDate, endDate)
		writeOK(w, map[string]interface{}{
			"daily": daily,
			"start": startDate,
			"end":   endDate,
		})
		return
	}

	writeOK(w, s.stats.Snapshot())
}
