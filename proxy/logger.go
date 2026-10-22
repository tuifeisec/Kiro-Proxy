// logger.go
// 全量日志：所有请求/响应/转换过程都推送到 WebSocket + 写入文件

package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
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
	mu                   sync.Mutex
	optionsMu            sync.RWMutex
	fileMu               sync.Mutex
	autoDiagnosticMu     sync.Mutex
	queueMu              sync.Mutex
	file                 *os.File
	filePath             string
	fileSize             int64
	autoDiagnosticFile   *os.File
	autoDiagnosticPath   string
	autoDiagnosticSize   int64
	autoDiagnosticClosed bool
	autoDiagnosticRunID  string
	hub                  *Hub
	seq                  int64
	detailMode           string
	maxDetailBytes       int
	queue                chan []byte
	queueClosed          bool
	workerWG             sync.WaitGroup
	dropped              uint64
}

type LogOptions struct {
	DetailMode     string
	MaxDetailBytes int
}

const (
	maxTrafficLogBytes    int64 = 100 * 1024 * 1024
	maxFullLogDetailBytes       = 16 << 20
)

var (
	autoDiagnosticRunIDOnce  sync.Once
	autoDiagnosticRunIDValue string
	autoDiagnosticRunIDErr   error
)

func NewTrafficLogger(logFile string, hub *Hub, opts ...LogOptions) (*TrafficLogger, error) {
	tl := &TrafficLogger{hub: hub}
	tl.SetOptions(firstLogOptions(opts...))
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("打开日志文件失败: %w", err)
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("读取日志文件信息失败: %w", err)
		}
		tl.file = f
		tl.filePath = logFile
		tl.fileSize = info.Size()
	}
	if logFile != "" {
		runID, err := getAutoDiagnosticRunID()
		if err != nil {
			if tl.file != nil {
				_ = tl.file.Close()
			}
			return nil, fmt.Errorf("生成 Auto 诊断运行标识失败: %w", err)
		}
		autoPath := logFile + ".auto-diagnostic.log"
		autoFile, err := os.OpenFile(autoPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			if tl.file != nil {
				_ = tl.file.Close()
			}
			return nil, fmt.Errorf("打开 Auto 诊断日志文件失败: %w", err)
		}
		info, err := autoFile.Stat()
		if err != nil {
			_ = autoFile.Close()
			if tl.file != nil {
				_ = tl.file.Close()
			}
			return nil, fmt.Errorf("读取 Auto 诊断日志文件信息失败: %w", err)
		}
		tl.autoDiagnosticFile = autoFile
		tl.autoDiagnosticPath = autoPath
		tl.autoDiagnosticSize = info.Size()
		tl.autoDiagnosticRunID = runID
	}
	if tl.file != nil || tl.hub != nil {
		tl.queue = make(chan []byte, 512)
		tl.workerWG.Add(1)
		go tl.run()
	}
	return tl, nil
}

func (tl *TrafficLogger) Close() {
	tl.queueMu.Lock()
	if tl.queue != nil && !tl.queueClosed {
		tl.queueClosed = true
		close(tl.queue)
	}
	tl.queueMu.Unlock()
	tl.workerWG.Wait()

	tl.fileMu.Lock()
	if tl.file != nil {
		_ = tl.file.Close()
	}
	tl.fileMu.Unlock()

	tl.autoDiagnosticMu.Lock()
	defer tl.autoDiagnosticMu.Unlock()
	if !tl.autoDiagnosticClosed {
		tl.autoDiagnosticClosed = true
		if tl.autoDiagnosticFile != nil {
			_ = tl.autoDiagnosticFile.Close()
			tl.autoDiagnosticFile = nil
		}
	}
}

func (tl *TrafficLogger) run() {
	defer tl.workerWG.Done()
	for data := range tl.queue {
		tl.writeFile(data)
		if tl.hub != nil {
			tl.hub.Broadcast(data)
		}
	}
}

func (tl *TrafficLogger) enqueue(data []byte) {
	tl.queueMu.Lock()
	defer tl.queueMu.Unlock()
	if tl.queue == nil || tl.queueClosed {
		return
	}
	select {
	case tl.queue <- data:
	default:
		tl.dropped++
		if tl.dropped == 1 || tl.dropped%100 == 0 {
			log.Printf("[logger] async log queue full, dropped %d entries", tl.dropped)
		}
	}
}

func (tl *TrafficLogger) writeFile(data []byte) {
	tl.fileMu.Lock()
	defer tl.fileMu.Unlock()
	if tl.file == nil {
		return
	}

	entrySize := int64(len(data) + 1)
	if tl.fileSize+entrySize > maxTrafficLogBytes {
		if err := tl.rotateFile(); err != nil {
			log.Printf("[logger] 轮转超限流量日志失败: %v", err)
			if tl.file == nil {
				return
			}
		}
	}

	n, err := tl.file.Write(append(data, '\n'))
	if err != nil {
		log.Printf("[logger] 写入流量日志失败: %v", err)
		return
	}
	tl.fileSize += int64(n)
}

func (tl *TrafficLogger) rotateFile() error {
	return rotateLogFile(&tl.file, tl.filePath, &tl.fileSize)
}

func rotateLogFile(file **os.File, filePath string, fileSize *int64) error {
	if *file == nil || filePath == "" {
		return nil
	}
	if err := (*file).Close(); err != nil {
		return err
	}
	*file = nil
	backupPath := filePath + ".1"
	reopenCurrent := func() {
		current, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return
		}
		*file = current
		if info, err := current.Stat(); err == nil {
			*fileSize = info.Size()
		}
	}
	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		reopenCurrent()
		return err
	}
	if err := os.Rename(filePath, backupPath); err != nil {
		reopenCurrent()
		return err
	}
	current, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		_ = os.Rename(backupPath, filePath)
		reopenCurrent()
		return err
	}
	*file = current
	*fileSize = 0
	return nil
}

// AutoDiagnostic synchronously appends one structured Auto diagnostic event to
// the isolated diagnostic log. It never uses the traffic log queue or hub.
func (tl *TrafficLogger) AutoDiagnostic(event string, metadata map[string]interface{}) error {
	if tl == nil {
		return fmt.Errorf("写入 Auto 诊断日志失败: logger 为空")
	}
	tl.autoDiagnosticMu.Lock()
	defer tl.autoDiagnosticMu.Unlock()
	if tl.autoDiagnosticClosed {
		return fmt.Errorf("写入 Auto 诊断日志失败: logger 已关闭")
	}
	if tl.autoDiagnosticFile == nil {
		if tl.autoDiagnosticPath == "" {
			return nil
		}
		return fmt.Errorf("写入 Auto 诊断日志失败: 文件未打开")
	}
	eventID, err := randomAutoDiagnosticID()
	if err != nil {
		return fmt.Errorf("生成 Auto 诊断事件标识失败: %w", err)
	}
	entryMetadata := make(map[string]interface{}, len(metadata)+3)
	for key, value := range metadata {
		entryMetadata[key] = value
	}
	entryMetadata["schema_version"] = 1
	entryMetadata["run_id"] = tl.autoDiagnosticRunID
	entryMetadata["event_id"] = eventID
	now := time.Now()
	entry := &LogEntry{
		ID:       eventID,
		Date:     logStatsDay(now),
		Time:     now.Format("15:04:05.000"),
		FullTime: now.Format(time.RFC3339Nano),
		Level:    "info",
		Category: "auto-diagnostic",
		Message:  event,
		Metadata: entryMetadata,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("编码 Auto 诊断日志失败: %w", err)
	}
	entrySize := int64(len(data) + 1)
	if tl.autoDiagnosticSize+entrySize > maxTrafficLogBytes {
		if err := rotateLogFile(&tl.autoDiagnosticFile, tl.autoDiagnosticPath, &tl.autoDiagnosticSize); err != nil {
			return fmt.Errorf("轮转 Auto 诊断日志失败: %w", err)
		}
		if tl.autoDiagnosticFile == nil {
			return fmt.Errorf("轮转 Auto 诊断日志失败: 轮转后文件未打开")
		}
	}
	n, err := tl.autoDiagnosticFile.Write(append(data, '\n'))
	if err != nil {
		return fmt.Errorf("写入 Auto 诊断日志失败: %w", err)
	}
	if n != len(data)+1 {
		return fmt.Errorf("写入 Auto 诊断日志失败: %w", io.ErrShortWrite)
	}
	tl.autoDiagnosticSize += entrySize
	return nil
}

func getAutoDiagnosticRunID() (string, error) {
	autoDiagnosticRunIDOnce.Do(func() {
		autoDiagnosticRunIDValue, autoDiagnosticRunIDErr = randomAutoDiagnosticID()
	})
	return autoDiagnosticRunIDValue, autoDiagnosticRunIDErr
}

func randomAutoDiagnosticID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
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
	tl.enqueue(data)
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
		return truncateLogDetail(detail, maxFullLogDetailBytes)
	}
	return truncateLogDetail(detail, opts.MaxDetailBytes)
}

func (tl *TrafficLogger) prepareDetailBytes(detail []byte) string {
	opts := tl.Options()
	limit := opts.MaxDetailBytes
	if opts.DetailMode == "full" {
		limit = maxFullLogDetailBytes
	}
	if len(detail) <= limit {
		return string(detail)
	}
	return string(detail[:limit]) + fmt.Sprintf("\n... [truncated log detail: %d -> %d bytes]", len(detail), limit)
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
			maxBytes = maxFullLogDetailBytes
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
