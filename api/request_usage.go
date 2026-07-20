package api

import (
	"net/http"
	"strconv"
	"time"
)

func (s *Server) handleRequestUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	query := r.URL.Query()
	startDate := query.Get("start")
	endDate := query.Get("end")
	if startDate == "" {
		startDate = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	}
	if endDate == "" {
		endDate = time.Now().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", startDate); err != nil {
		writeError(w, http.StatusBadRequest, "invalid start date")
		return
	}
	if _, err := time.Parse("2006-01-02", endDate); err != nil {
		writeError(w, http.StatusBadRequest, "invalid end date")
		return
	}

	limit, _ := strconv.Atoi(query.Get("limit"))
	items, summary, err := s.db.QueryRequestUsage(startDate, endDate, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]interface{}{
		"items": items, "summary": summary, "start": startDate, "end": endDate,
	})
}
