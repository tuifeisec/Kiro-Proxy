package proxy

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

type logsQueryResponse struct {
	Items []LogEntry `json:"items"`
	Date  string     `json:"date"`
	Limit int        `json:"limit"`
}

// handleLogs 从当前 JSONL 日志文件按统计日期查询日志。
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date == "" {
		date = logStatsDay(time.Now())
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		http.Error(w, "invalid date, expected YYYY-MM-DD", http.StatusBadRequest)
		return
	}

	limit := parseLogsLimit(r.URL.Query().Get("limit"))
	items, err := queryLogEntriesByDate(s.cfg.Proxy.LogFile, date, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(logsQueryResponse{Items: items, Date: date, Limit: limit})
}

func parseLogsLimit(raw string) int {
	limit := 100
	if raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func queryLogEntriesByDate(logFile, date string, limit int) ([]LogEntry, error) {
	if strings.TrimSpace(logFile) == "" {
		return []LogEntry{}, nil
	}

	f, err := os.Open(logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []LogEntry{}, nil
		}
		return nil, fmt.Errorf("读取日志文件失败: %w", err)
	}
	defer f.Close()

	matched := make([]LogEntry, 0, limit)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), defaultMaxLogDetailBytes+256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entryDate(entry) != date {
			continue
		}
		matched = append(matched, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("扫描日志文件失败: %w", err)
	}

	for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
		matched[i], matched[j] = matched[j], matched[i]
	}
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

func entryDate(entry LogEntry) string {
	if entry.Date != "" {
		return entry.Date
	}
	if entry.FullTime != "" {
		if t, err := time.Parse(time.RFC3339Nano, entry.FullTime); err == nil {
			return logStatsDay(t)
		}
	}
	return ""
}
