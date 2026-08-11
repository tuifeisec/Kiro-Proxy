package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
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

const maxLogLineBytes = 16 << 20

// handleLogs 从当前 JSONL 日志文件按统计日期查询日志。
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.requireTrafficLogPermission(w) {
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
	if strings.TrimSpace(logFile) == "" || limit <= 0 {
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

	matched := make([]LogEntry, limit)
	matchedCount := 0
	next := 0
	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		line, tooLong, readErr := readLogLine(reader, maxLogLineBytes)
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return nil, fmt.Errorf("扫描日志文件失败: %w", readErr)
			}
			continue
		}
		if !tooLong {
			var entry LogEntry
			if err := json.Unmarshal(line, &entry); err == nil && entryDate(entry) == date {
				matched[next] = entry
				next = (next + 1) % limit
				if matchedCount < limit {
					matchedCount++
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("扫描日志文件失败: %w", readErr)
		}
	}

	result := make([]LogEntry, 0, matchedCount)
	for i := 0; i < matchedCount; i++ {
		index := (next - 1 - i + limit) % limit
		result = append(result, matched[index])
	}
	return result, nil
}

func readLogLine(reader *bufio.Reader, limit int) ([]byte, bool, error) {
	var line []byte
	tooLong := false
	for {
		fragment, isPrefix, err := reader.ReadLine()
		if !tooLong {
			if len(line)+len(fragment) > limit {
				line = nil
				tooLong = true
			} else {
				line = append(line, fragment...)
			}
		}
		if err != nil || !isPrefix {
			return line, tooLong, err
		}
	}
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
