// logger.go
// 全量日志：所有请求/响应/转换过程都推送到 WebSocket + 写入文件

package proxy

import (
	"fmt"
	json "github.com/goccy/go-json"
	"log"
	"os"
	"sync"
	"time"
)

// LogEntry 单条日志，推送给前端
type LogEntry struct {
	ID       string `json:"id"`
	Time     string `json:"time"`
	Level    string `json:"level"`    // info | warn | error | debug
	Category string `json:"category"` // proxy | interceptor | forwarder | converter | router
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"` // 详细数据（body、headers 等）
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
	tl.logPrepared(level, category, message, det)
}

func (tl *TrafficLogger) logPrepared(level, category, message, det string) {
	tl.mu.Lock()
	tl.seq++
	id := fmt.Sprintf("%06d", tl.seq)
	tl.mu.Unlock()

	entry := &LogEntry{
		ID:       id,
		Time:     time.Now().Format("15:04:05.000"),
		Level:    level,
		Category: category,
		Message:  message,
		Detail:   det,
	}

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
	logMsg := fmt.Sprintf("[%s] %s [%s] %s", entry.Time, prefix, category, message)
	log.Println(logMsg)

	// 写文件
	if tl.file != nil {
		data, _ := json.Marshal(entry)
		fmt.Fprintf(tl.file, "%s\n", data)
	}

	// WebSocket 广播
	if tl.hub != nil {
		data, _ := json.Marshal(entry)
		tl.hub.Broadcast(data)
	}
}

func (tl *TrafficLogger) Info(category, msg string, detail ...string) {
	tl.Log("info", category, msg, detail...)
}

func (tl *TrafficLogger) Warn(category, msg string, detail ...string) {
	tl.Log("warn", category, msg, detail...)
}

func (tl *TrafficLogger) Error(category, msg string, detail ...string) {
	tl.Log("error", category, msg, detail...)
}

func (tl *TrafficLogger) Debug(category, msg string, detail ...string) {
	tl.Log("debug", category, msg, detail...)
}

func (tl *TrafficLogger) InfoBytes(category, msg string, detail []byte) {
	tl.LogBytes("info", category, msg, detail)
}

func (tl *TrafficLogger) LogBytes(level, category, message string, detail []byte) {
	tl.logPrepared(level, category, message, tl.prepareDetailBytes(detail))
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
