// logger.go
// 全量日志：所有请求/响应/转换过程都推送到 WebSocket + 写入文件

package proxy

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	json "github.com/goccy/go-json"
)

// LogEntry 单条日志，推送给前端并写入 JSONL 文件。
type LogEntry struct {
	ID               string                 `json:"id"`
	Date             string                 `json:"date"`
	Time             string                 `json:"time"`
	FullTime         string                 `json:"full_time"`
	Level            string                 `json:"level"`    // info | warn | error | debug
	Category         string                 `json:"category"` // proxy | interceptor | forwarder | converter | router
	Message          string                 `json:"message"`
	Detail           string                 `json:"detail,omitempty"` // 详细数据（body、headers 等）
	Model            string                 `json:"model,omitempty"`
	Status           int                    `json:"status,omitempty"`
	InputTokens      int64                  `json:"input_tokens,omitempty"`
	OutputTokens     int64                  `json:"output_tokens,omitempty"`
	CacheReadTokens  int64                  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64                  `json:"cache_write_tokens,omitempty"`
	Credits          float64                `json:"credits,omitempty"`
	LatencyMS        int64                  `json:"latency_ms,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
}

// LogMeta 是日志结构化字段，常用字段会提升到 LogEntry 顶层，其他字段放入 metadata。
type LogMeta struct {
	Model            string
	Status           int
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Credits          float64
	LatencyMS        int64
	Metadata         map[string]interface{}
}

// TrafficLogger 日志记录器
type TrafficLogger struct {
	mu             sync.Mutex
	optionsMu      sync.RWMutex
	file           *os.File
	hub            *Hub
	seq            int64
	detailMode     string
	maxDetailBytes int
}

type LogOptions struct {
	DetailMode     string
	MaxDetailBytes int
}

func NewTrafficLogger(logFile string, hub *Hub, opts ...LogOptions) (*TrafficLogger, error) {
	tl := &TrafficLogger{hub: hub}
	tl.SetOptions(firstLogOptions(opts...))
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("打开日志文件失败: %w", err)
		}
		tl.file = f
	}
	return tl, nil
}

func (tl *TrafficLogger) Close() {
	if tl.file != nil {
		tl.file.Close()
	}
}

// Log 记录一条日志（推送到 WebSocket + 写文件 + 控制台）
func (tl *TrafficLogger) Log(level, category, message string, detail ...string) {
	det := ""
	if len(detail) > 0 {
		det = tl.prepareDetailString(detail[0])
	}
	tl.logPrepared(level, category, message, det, nil)
}

func (tl *TrafficLogger) LogWithMeta(level, category, message string, meta LogMeta, detail ...string) {
	det := ""
	if len(detail) > 0 {
		det = tl.prepareDetailString(detail[0])
	}
	tl.logPrepared(level, category, message, det, &meta)
}

func (tl *TrafficLogger) logPrepared(level, category, message, det string, meta *LogMeta) {
	template := tl.newEntry(level, category, message, det, meta)

	// 控制台
	prefix := ""
	switch level {
	case "error":
		prefix = "✗"
	case "warn":
		prefix = "⚠"
	default:
		prefix = "→"
	}
	logMsg := fmt.Sprintf("[%s] %s [%s] %s", template.Time, prefix, category, message)
	log.Println(logMsg)

	if tl.file == nil && (tl.hub == nil || tl.hub.ClientCount() == 0) {
		return
	}

	data, _ := json.Marshal(template)
	if tl.file != nil {
		fmt.Fprintf(tl.file, "%s\n", data)
	}
	if tl.hub != nil {
		tl.hub.Broadcast(data)
	}
}

func (tl *TrafficLogger) newEntry(level, category, message, det string, meta *LogMeta) *LogEntry {
	tl.mu.Lock()
	tl.seq++
	id := fmt.Sprintf("%06d", tl.seq)
	tl.mu.Unlock()

	now := time.Now()
	entry := &LogEntry{
		ID:       id,
		Date:     logStatsDay(now),
		Time:     now.Format("15:04:05.000"),
		FullTime: now.Format(time.RFC3339Nano),
		Level:    level,
		Category: category,
		Message:  message,
		Detail:   det,
	}
	if meta != nil {
		entry.Model = meta.Model
		entry.Status = meta.Status
		entry.InputTokens = meta.InputTokens
		entry.OutputTokens = meta.OutputTokens
		entry.CacheReadTokens = meta.CacheReadTokens
		entry.CacheWriteTokens = meta.CacheWriteTokens
		entry.Credits = meta.Credits
		entry.LatencyMS = meta.LatencyMS
		entry.Metadata = meta.Metadata
	}
	return entry
}

func (tl *TrafficLogger) Info(category, msg string, detail ...string) {
	tl.Log("info", category, msg, detail...)
}

func (tl *TrafficLogger) InfoWithMeta(category, msg string, meta LogMeta, detail ...string) {
	tl.LogWithMeta("info", category, msg, meta, detail...)
}

func (tl *TrafficLogger) Warn(category, msg string, detail ...string) {
	tl.Log("warn", category, msg, detail...)
}

func (tl *TrafficLogger) WarnWithMeta(category, msg string, meta LogMeta, detail ...string) {
	tl.LogWithMeta("warn", category, msg, meta, detail...)
}

func (tl *TrafficLogger) Error(category, msg string, detail ...string) {
	tl.Log("error", category, msg, detail...)
}

func (tl *TrafficLogger) ErrorWithMeta(category, msg string, meta LogMeta, detail ...string) {
	tl.LogWithMeta("error", category, msg, meta, detail...)
}

func (tl *TrafficLogger) Debug(category, msg string, detail ...string) {
	tl.Log("debug", category, msg, detail...)
}

func (tl *TrafficLogger) DebugWithMeta(category, msg string, meta LogMeta, detail ...string) {
	tl.LogWithMeta("debug", category, msg, meta, detail...)
}

func (tl *TrafficLogger) InfoBytes(category, msg string, detail []byte) {
	tl.LogBytes("info", category, msg, detail)
}

func (tl *TrafficLogger) InfoBytesWithMeta(category, msg string, detail []byte, meta LogMeta) {
	tl.LogBytesWithMeta("info", category, msg, detail, meta)
}

func (tl *TrafficLogger) LogBytes(level, category, message string, detail []byte) {
	tl.logPrepared(level, category, message, tl.prepareDetailBytes(detail), nil)
}

func (tl *TrafficLogger) LogBytesWithMeta(level, category, message string, detail []byte, meta LogMeta) {
	tl.logPrepared(level, category, message, tl.prepareDetailBytes(detail), &meta)
}

func (tl *TrafficLogger) SetOptions(opts LogOptions) {
	mode := opts.DetailMode
	if mode != "full" {
		mode = "summary"
	}
	maxBytes := opts.MaxDetailBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxLogDetailBytes
	}
	tl.optionsMu.Lock()
	tl.detailMode = mode
	tl.maxDetailBytes = maxBytes
	tl.optionsMu.Unlock()
}

func (tl *TrafficLogger) Options() LogOptions {
	tl.optionsMu.RLock()
	defer tl.optionsMu.RUnlock()
	return LogOptions{DetailMode: tl.detailMode, MaxDetailBytes: tl.maxDetailBytes}
}

func firstLogOptions(opts ...LogOptions) LogOptions {
	if len(opts) == 0 {
		return LogOptions{DetailMode: "summary", MaxDetailBytes: defaultMaxLogDetailBytes}
	}
	return opts[0]
}

func (tl *TrafficLogger) prepareDetailString(detail string) string {
	opts := tl.Options()
	if opts.DetailMode == "full" {
		return detail
	}
	return truncateLogDetail(detail, opts.MaxDetailBytes)
}

func (tl *TrafficLogger) prepareDetailBytes(detail []byte) string {
	opts := tl.Options()
	if opts.DetailMode == "full" || len(detail) <= opts.MaxDetailBytes {
		return string(detail)
	}
	return string(detail[:opts.MaxDetailBytes]) + fmt.Sprintf("\n... [truncated log detail: %d -> %d bytes]", len(detail), opts.MaxDetailBytes)
}

const defaultMaxLogDetailBytes = 64 * 1024

func truncateLogDetail(detail string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = defaultMaxLogDetailBytes
	}
	if len(detail) <= maxBytes {
		return detail
	}
	return detail[:maxBytes] + fmt.Sprintf("\n... [truncated log detail: %d -> %d bytes]", len(detail), maxBytes)
}

func logStatsDay(t time.Time) string {
	if t.Hour() < 6 {
		t = t.AddDate(0, 0, -1)
	}
	return t.Format("2006-01-02")
}

func (tl *TrafficLogger) InfoWithMetaLazyDetail(category, msg string, meta LogMeta, detail func(maxBytes int) string) {
	tl.LogWithMetaLazyDetail("info", category, msg, meta, detail)
}

func (tl *TrafficLogger) ErrorWithMetaLazyDetail(category, msg string, meta LogMeta, detail func(maxBytes int) string) {
	tl.LogWithMetaLazyDetail("error", category, msg, meta, detail)
}

func (tl *TrafficLogger) LogWithMetaLazyDetail(level, category, message string, meta LogMeta, detail func(maxBytes int) string) {
	det := ""
	if detail != nil {
		opts := tl.Options()
		maxBytes := opts.MaxDetailBytes
		if opts.DetailMode == "full" {
			maxBytes = 0
		}
		det = detail(maxBytes)
	}
	tl.logPrepared(level, category, message, det, &meta)
}

func (tl *TrafficLogger) DetailLimit() int {
	opts := tl.Options()
	if opts.DetailMode == "full" {
		return 0
	}
	return opts.MaxDetailBytes
}
