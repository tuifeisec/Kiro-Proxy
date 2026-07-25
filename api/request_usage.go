package api

import (
	"kiro-proxy/store"
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

	page, _ := strconv.Atoi(query.Get("page"))
	pageSizeValue := query.Get("page_size")
	if pageSizeValue == "" {
		pageSizeValue = query.Get("limit")
	}
	pageSize, _ := strconv.Atoi(pageSizeValue)
	result, err := s.db.QueryRequestUsagePage(store.RequestUsageQuery{
		StartDate: startDate,
		EndDate:   endDate,
		Keyword:   query.Get("q"),
		Provider:  query.Get("provider"),
		Status:    query.Get("status"),
		Page:      page,
		PageSize:  pageSize,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]interface{}{
		"items":       result.Items,
		"summary":     result.Summary,
		"start":       startDate,
		"end":         endDate,
		"page":        result.Page,
		"page_size":   result.PageSize,
		"total":       result.Total,
		"total_pages": result.TotalPages,
	})
}
