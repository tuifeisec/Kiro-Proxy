package proxy

import (
	"bufio"
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"kiro-proxy/router"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	autoHistoryMaxFileBytes = int64(128 << 20)
	autoHistoryMaxRoutes    = 1024
	autoHistoryMaxContexts  = 2048
	autoHistoryMaxWarnings  = 64
)

type autoHistoryItem struct {
	router.AutoObservation
	SourceFile           string `json:"source_file"`
	SourceLine           int64  `json:"source_line"`
	SourceLineRelative   bool   `json:"source_line_relative"`
	SourceByteOffset     int64  `json:"source_byte_offset"`
	WorkflowName         string `json:"workflow_name"`
	SessionExcerpt       string `json:"session_excerpt"`
	SessionExcerptStatus string `json:"session_excerpt_status"`
	SessionExcerptDetail string `json:"session_excerpt_status_detail"`
	ActivitySourceFile   string `json:"activity_source_file,omitempty"`
	ActivitySourceLine   int64  `json:"activity_source_line,omitempty"`
	ActivityLineRelative bool   `json:"activity_source_line_relative,omitempty"`
	ActivityByteOffset   int64  `json:"activity_source_byte_offset,omitempty"`
	ExcerptSourceFile    string `json:"excerpt_source_file,omitempty"`
	ExcerptSourceLine    int64  `json:"excerpt_source_line,omitempty"`
	ExcerptLineRelative  bool   `json:"excerpt_source_line_relative,omitempty"`
	ExcerptByteOffset    int64  `json:"excerpt_source_byte_offset,omitempty"`
}

type autoHistoryFile struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	SizeBytes       int64  `json:"size_bytes"`
	ReadBytes       int64  `json:"read_bytes"`
	StartLine       int64  `json:"start_line"`
	EndLine         int64  `json:"end_line"`
	LineRelative    bool   `json:"line_relative"`
	StartByteOffset int64  `json:"start_byte_offset"`
	Truncated       bool   `json:"truncated"`
	RouteCount      uint64 `json:"route_count"`
	ActivityCount   uint64 `json:"activity_count"`
}

type autoHistoryWarning struct {
	File    string `json:"file"`
	Line    int64  `json:"line"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type autoHistoryActivityNode struct {
	WorkflowID         string `json:"workflow_id"`
	NodeID             string `json:"node_id"`
	ConversationID     string `json:"conversation_id"`
	WorkflowName       string `json:"workflow_name"`
	ActivityCount      uint64 `json:"activity_count"`
	SourceFile         string `json:"source_file"`
	SourceLine         int64  `json:"source_line"`
	SourceLineRelative bool   `json:"source_line_relative"`
	SourceByteOffset   int64  `json:"source_byte_offset"`
	HasAutoRoute       bool   `json:"has_auto_route"`
	ContextStatus      string `json:"context_status"`
}

type autoHistoryResponse struct {
	Items            []autoHistoryItem         `json:"items"`
	ActivityNodes    []autoHistoryActivityNode `json:"activity_nodes"`
	Files            []autoHistoryFile         `json:"files"`
	Warnings         []autoHistoryWarning      `json:"warnings"`
	WarningCount     uint64                    `json:"warning_count"`
	RouteCount       uint64                    `json:"route_count"`
	ActivityCount    uint64                    `json:"activity_count"`
	ItemsTruncated   bool                      `json:"items_truncated"`
	ContextTruncated bool                      `json:"context_truncated"`
}

type autoHistorySource struct {
	file     string
	line     int64
	relative bool
	offset   int64
}

type autoHistoryContext struct {
	value                string
	status               string
	fingerprint          [32]byte
	ambiguous            bool
	source               autoHistorySource
	workflowID           string
	nodeID               string
	conversationID       string
	activityCount        uint64
	latestActivitySource autoHistorySource
	hasAutoRoute         bool
}

type autoHistoryRoute struct {
	key  string
	item autoHistoryItem
}

type autoHistoryCollector struct {
	response autoHistoryResponse
	routes   map[string]*list.Element
	order    *list.List
	contexts map[string]autoHistoryContext
}

func newAutoHistoryCollector() *autoHistoryCollector {
	return &autoHistoryCollector{
		response: autoHistoryResponse{Items: []autoHistoryItem{}, ActivityNodes: []autoHistoryActivityNode{}, Files: []autoHistoryFile{}, Warnings: []autoHistoryWarning{}},
		routes:   make(map[string]*list.Element), order: list.New(), contexts: make(map[string]autoHistoryContext),
	}
}

func (c *autoHistoryCollector) warn(source autoHistorySource, code, message string) {
	c.response.WarningCount++
	if len(c.response.Warnings) < autoHistoryMaxWarnings {
		if runes := []rune(message); len(runes) > 512 {
			message = string(runes[:512]) + "…"
		}
		c.response.Warnings = append(c.response.Warnings, autoHistoryWarning{source.file, source.line, code, message})
	}
}

func (s *Server) handleAutoNodeHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	response, err := readAutoNodeHistory(r.Context(), s.cfg.Proxy.LogFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestTimeout)
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}

type autoHistorySnapshot struct {
	file    *os.File
	info    autoHistoryFile
	partial bool
}

// 仅读取配置的日志和一份轮转文件；解析前固定两者大小，不包含后续追加。
func readAutoNodeHistory(ctx context.Context, logFile string) (autoHistoryResponse, error) {
	c := newAutoHistoryCollector()
	if strings.TrimSpace(logFile) == "" {
		c.warn(autoHistorySource{}, "log_not_configured", "未配置流量日志文件")
		return c.response, nil
	}
	snapshots := make([]autoHistorySnapshot, 0, 2)
	defer func() {
		for _, snapshot := range snapshots {
			if snapshot.file != nil {
				_ = snapshot.file.Close()
			}
		}
	}()
	for _, path := range []string{logFile + ".1", logFile} {
		if err := ctx.Err(); err != nil {
			return c.response, err
		}
		snapshot := autoHistorySnapshot{info: autoHistoryFile{Name: filepath.Base(path), Status: "ok"}}
		file, err := os.Open(path)
		if err != nil {
			snapshot.info.Status = "error"
			if errors.Is(err, os.ErrNotExist) {
				snapshot.info.Status = "missing"
			}
			if path == logFile || snapshot.info.Status != "missing" {
				c.warn(autoHistorySource{file: snapshot.info.Name}, "file_open_failed", historyIOError(err))
			}
			snapshots = append(snapshots, snapshot)
			continue
		}
		snapshot.file = file
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			snapshot.info.Status = "error"
			message := "日志目标不是普通文件"
			if err != nil {
				message = historyIOError(err)
			}
			c.warn(autoHistorySource{file: snapshot.info.Name}, "file_stat_failed", message)
			snapshots = append(snapshots, snapshot)
			continue
		}
		snapshot.info.SizeBytes = info.Size()
		if info.Size() > autoHistoryMaxFileBytes {
			snapshot.info.Truncated, snapshot.info.LineRelative = true, true
			snapshot.info.StartByteOffset = info.Size() - autoHistoryMaxFileBytes
			c.warn(autoHistorySource{file: snapshot.info.Name}, "file_window_truncated", "仅扫描快照末尾 128 MiB；行号相对于截取窗口")
			var preceding [1]byte
			if _, err := file.ReadAt(preceding[:], snapshot.info.StartByteOffset-1); err != nil {
				snapshot.info.Status = "error"
				c.warn(autoHistorySource{file: snapshot.info.Name}, "file_boundary_failed", historyIOError(err))
			} else {
				snapshot.partial = preceding[0] != '\n'
			}
		}
		snapshots = append(snapshots, snapshot)
	}
	for _, snapshot := range snapshots {
		if err := ctx.Err(); err != nil {
			return c.response, err
		}
		fileInfo := snapshot.info
		if snapshot.file != nil && fileInfo.Status == "ok" {
			length := fileInfo.SizeBytes - fileInfo.StartByteOffset
			reader := io.NewSectionReader(snapshot.file, fileInfo.StartByteOffset, length)
			if err := parseAutoNodeHistory(ctx, reader, &fileInfo, snapshot.partial, c); err != nil {
				if ctx.Err() != nil {
					return c.response, ctx.Err()
				}
				fileInfo.Status = "error"
				c.warn(autoHistorySource{file: fileInfo.Name, line: fileInfo.EndLine}, "file_read_failed", historyIOError(err))
			}
			if fileInfo.ReadBytes < length && ctx.Err() == nil {
				fileInfo.Status = "error"
				c.warn(autoHistorySource{file: fileInfo.Name, line: fileInfo.EndLine}, "snapshot_short_read", "快照读取不全，文件可能在读取期间截断")
			}
		}
		c.response.Files = append(c.response.Files, fileInfo)
	}
	if err := ctx.Err(); err != nil {
		return c.response, err
	}
	c.finish()
	return c.response, nil
}

// 路径和解析片段可能含私有信息；保留底层 I/O 错误，JSON 诊断另附偏移。
func historyIOError(err error) string {
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return pathError.Op + ": " + pathError.Err.Error()
	}
	return err.Error()
}

type autoHistoryReader struct {
	ctx   context.Context
	input io.Reader
	bytes int64
}

func (r *autoHistoryReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.input.Read(p)
	r.bytes += int64(n)
	return n, err
}

// 可注入 io.Reader 的解析边界，不打开文件；正文仅在当前行解析期间驻留。
// 不将完整正文、headers 或工具入参存入聚合结果。
func parseAutoNodeHistory(ctx context.Context, input io.Reader, file *autoHistoryFile, skipPartial bool, c *autoHistoryCollector) error {
	counting := &autoHistoryReader{ctx: ctx, input: input}
	reader := bufio.NewReaderSize(counting, 64<<10)
	defer func() { file.ReadBytes = counting.bytes }()
	for lineNumber := int64(1); ; lineNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		source := autoHistorySource{file: file.Name, line: lineNumber, relative: file.LineRelative,
			offset: file.StartByteOffset + counting.bytes - int64(reader.Buffered())}
		line, tooLong, readErr := readLogLine(reader, maxLogLineBytes)
		if len(line) == 0 && !tooLong && errors.Is(readErr, io.EOF) {
			return nil
		}
		file.EndLine = lineNumber
		if skipPartial {
			skipPartial = false
			c.warn(source, "partial_first_line_discarded", "截取窗口的首个不完整行已舍弃")
		} else {
			if file.StartLine == 0 {
				file.StartLine = lineNumber
			}
			if tooLong {
				file.Truncated = true
				c.warn(source, "line_too_long", "日志行超过 16 MiB，已舍弃")
			} else if len(bytes.TrimSpace(line)) != 0 {
				c.parseLine(ctx, line, source, file)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func historyJSONError(err error) string {
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		return fmt.Sprintf("%s（字节偏移 %d）", syntaxError.Error(), syntaxError.Offset)
	}
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		// 数值类型错误的原文可能含整段原始值，不返回该值。
		return fmt.Sprintf("json: 字段 %s 无法解码为 %s（字节偏移 %d）", typeError.Field, typeError.Type, typeError.Offset)
	}
	return err.Error()
}

func (c *autoHistoryCollector) parseLine(ctx context.Context, line []byte, source autoHistorySource, file *autoHistoryFile) {
	var entry struct {
		Category string          `json:"category"`
		FullTime string          `json:"full_time"`
		Metadata json.RawMessage `json:"metadata"`
		Detail   json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		c.warn(source, "log_json_invalid", historyJSONError(err))
		return
	}
	if entry.Category == "" {
		c.warn(source, "log_record_invalid", "日志记录缺少 category")
		return
	}
	if ctx.Err() != nil {
		return
	}
	switch entry.Category {
	case "auto-routing":
		observation, err := parseHistoryObservation(entry.Metadata)
		if err != nil {
			c.warn(source, "route_metadata_invalid", historyJSONError(err))
			return
		}
		c.recordAutoEvidence(observation, source)
		if timestamp, err := time.Parse(time.RFC3339Nano, entry.FullTime); err == nil {
			observation.LastSeen = timestamp
		} else {
			c.warn(source, "route_time_invalid", "路由记录缺少有效 full_time，最近时间未知")
			return
		}
		observation.Count = 1
		file.RouteCount++
		c.response.RouteCount++
		c.recordRoute(observation, source)
	case "interceptor":
		if len(entry.Detail) == 0 || bytes.Equal(entry.Detail, []byte("null")) {
			return
		}
		var detail string
		if err := json.Unmarshal(entry.Detail, &detail); err != nil {
			c.warn(source, "activity_detail_invalid", historyJSONError(err))
			return
		}
		// 其他 interceptor JSON 可以没有活动 payload。
		var activity struct {
			Payload []json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(detail), &activity); err != nil {
			c.warn(source, "activity_json_invalid", historyJSONError(err))
			return
		}
		for _, raw := range activity.Payload {
			if ctx.Err() != nil {
				return
			}
			c.parseActivity(raw, source, file)
		}
	}
}

func parseHistoryObservation(raw json.RawMessage) (router.AutoObservation, error) {
	var observation router.AutoObservation
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return observation, errors.New("路由 metadata 缺失")
	}
	// 仅解码允许展示的 metadata 子集；超长身份直接拒绝，避免截断生成新身份。
	if err := json.Unmarshal(raw, &observation); err != nil {
		return observation, err
	}
	var extra struct {
		ModelConfigID string `json:"selected_model_config_id"`
	}
	if err := json.Unmarshal(raw, &extra); err != nil {
		return observation, err
	}
	observation.ModelConfigID = extra.ModelConfigID
	fields := []*string{&observation.DefinitionID, &observation.DefinitionRevision, &observation.NodePath, &observation.NodeID,
		&observation.TaskType, &observation.SelectedDuty, &observation.SelectionSource, &observation.UnknownReason,
		&observation.TargetGroupID, &observation.TargetGroupName, &observation.ModelConfigID, &observation.OutboundModel,
		&observation.EffectiveEffort, &observation.ChannelIdentity, &observation.LockScope, &observation.WorkflowStage,
		&observation.WorkflowStageSource, &observation.WorkflowStageStatus, &observation.SelectedStage, &observation.WorkflowID,
		&observation.AgentTaskType, &observation.AgentMode, &observation.ChatTriggerType, &observation.ConversationID, &observation.ExecutionID}
	for _, field := range fields {
		if len(*field) > 4096 || len([]rune(*field)) > 1024 {
			return router.AutoObservation{}, errors.New("路由 metadata 字段超过 1024 字符或 4096 字节")
		}
	}
	return observation, nil
}

func (c *autoHistoryCollector) recordRoute(observation router.AutoObservation, source autoHistorySource) {
	identity := observation
	identity.Count, identity.LastSeen = 0, time.Time{}
	keyBytes, _ := json.Marshal(identity)
	key := string(keyBytes)
	item := autoHistoryItem{AutoObservation: observation, SourceFile: source.file, SourceLine: source.line,
		SourceLineRelative: source.relative, SourceByteOffset: source.offset}
	if existing, ok := c.routes[key]; ok {
		record := existing.Value.(*autoHistoryRoute)
		item.Count += record.item.Count
		if record.item.LastSeen.After(item.LastSeen) {
			record.item.Count = item.Count
		} else {
			record.item = item
		}
		c.order.MoveToBack(existing)
		return
	}
	if len(c.routes) == autoHistoryMaxRoutes {
		oldest := c.order.Front()
		delete(c.routes, oldest.Value.(*autoHistoryRoute).key)
		c.order.Remove(oldest)
		if !c.response.ItemsTruncated {
			c.warn(source, "route_limit_reached", "超过 1024 个路由变体，仅保留最近扫描的变体；计数可能只覆盖保留期间")
		}
		c.response.ItemsTruncated = true
	}
	c.routes[key] = c.order.PushBack(&autoHistoryRoute{key: key, item: item})
}

func historyContextKey(kind string, fields ...string) string {
	data, _ := json.Marshal(append([]string{kind}, fields...))
	return string(data)
}

// 精确 Auto 证据和活动共享上下文容量；路由变体淘汰不抹掉已保留的关联证据。
// 空标题占位没有活动次数，不会输出为活动节点，也不会伪造标题或活动来源。
func (c *autoHistoryCollector) recordAutoEvidence(observation router.AutoObservation, source autoHistorySource) {
	if observation.ConversationID == "" || observation.WorkflowID == "" || observation.NodeID == "" {
		return
	}
	key := historyContextKey("workflow", observation.ConversationID, observation.WorkflowID, observation.NodeID)
	c.recordContext(key, "", "", [32]byte{}, source)
	if context, ok := c.contexts[key]; ok {
		context.hasAutoRoute = true
		c.contexts[key] = context
	}
}

func (c *autoHistoryCollector) recordContext(key, value, status string, fingerprint [32]byte, source autoHistorySource) {
	if existing, ok := c.contexts[key]; ok {
		// 标题空值表示未提供；不覆盖已有证据，也不与后续真实标题构成冲突。
		if status == "" {
			if value == "" {
				return
			}
			if existing.value == "" {
				existing.value, existing.fingerprint, existing.source = value, fingerprint, source
				c.contexts[key] = existing
				return
			}
		}
		if existing.fingerprint != fingerprint {
			if !existing.ambiguous {
				c.warn(source, "context_ambiguous", "同一关联键有不同上下文，已停止附加该信息")
			}
			existing.ambiguous = true
		}
		existing.source = source
		c.contexts[key] = existing
		return
	}
	if len(c.contexts) == autoHistoryMaxContexts {
		if !c.response.ContextTruncated {
			c.warn(source, "context_limit_reached", "超过 2048 个路由关联、活动或会话上下文，新关联键已舍弃；活动线索及关联信息可能不全")
		}
		c.response.ContextTruncated = true
		return
	}
	c.contexts[key] = autoHistoryContext{value: value, status: status, fingerprint: fingerprint, source: source}
}

func (c *autoHistoryCollector) parseActivity(raw json.RawMessage, source autoHistorySource, file *autoHistoryFile) {
	var activity struct {
		SessionID string `json:"sessionId"`
		Content   struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
			Meta    struct {
				Kiro struct {
					Workflow struct {
						WorkflowID   string `json:"workflowId"`
						WorkflowName string `json:"workflowName"`
						NodeID       string `json:"nodeId"`
					} `json:"workflow"`
				} `json:"kiro"`
			} `json:"_meta"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &activity); err != nil {
		c.warn(source, "activity_payload_invalid", historyJSONError(err))
		return
	}
	file.ActivityCount++
	c.response.ActivityCount++
	workflow := activity.Content.Meta.Kiro.Workflow
	for _, field := range []struct {
		name  string
		value string
	}{
		{"sessionId", activity.SessionID},
		{"workflowId", workflow.WorkflowID},
		{"nodeId", workflow.NodeID},
	} {
		if len(field.value) > 1024 {
			c.warn(source, "activity_association_field_too_long", "活动关联字段 "+field.name+" 超过 1024 字节，未建立该字段对应的活动关联")
			c.response.ContextTruncated = true
		}
	}
	if activity.SessionID == "" || len(activity.SessionID) > 1024 {
		return
	}
	if workflow.WorkflowID != "" && workflow.NodeID != "" && len(workflow.WorkflowID) <= 1024 && len(workflow.NodeID) <= 1024 {
		if len(workflow.WorkflowName) > 4096 || len([]rune(workflow.WorkflowName)) > 1024 {
			c.warn(source, "activity_title_too_long", "活动标题超过 1024 字符或 4096 字节，标题已省略")
			workflow.WorkflowName = ""
		}
		key := historyContextKey("workflow", activity.SessionID, workflow.WorkflowID, workflow.NodeID)
		c.recordContext(key, workflow.WorkflowName, "", sha256.Sum256([]byte(workflow.WorkflowName)), source)
		if context, ok := c.contexts[key]; ok {
			context.workflowID, context.nodeID, context.conversationID = workflow.WorkflowID, workflow.NodeID, activity.SessionID
			context.activityCount++
			context.latestActivitySource = source
			c.contexts[key] = context
		}
	}
	if activity.Content.Type != "user" {
		return
	}
	var content string
	if err := json.Unmarshal(activity.Content.Content, &content); err != nil {
		return // 结构化 user 内容不作为普通文本节选。
	}
	excerpt, fingerprint, ok := historySessionExcerpt(content)
	if ok {
		status := "available"
		if excerpt == "" {
			status = "suppressed_sensitive_pattern"
			c.warn(source, "excerpt_suppressed", "原始会话请求含凭据特征，节选已省略")
		}
		c.recordContext(historyContextKey("session", activity.SessionID), excerpt, status, fingerprint, source)
	}
}

var historyMessageTag = regexp.MustCompile(`</?message(?:\s+index="[0-9]+")?>`)

// 仅移除协议的原始请求包装和消息分隔标签，不解释其他标记；结果按普通文本展示。
func historySessionExcerpt(content string) (string, [32]byte, bool) {
	const open, close = "<original_user_request>", "</original_user_request>"
	if strings.Count(content, open) != 1 || strings.Count(content, close) != 1 {
		return "", [32]byte{}, false
	}
	start := strings.Index(content, open) + len(open)
	end := strings.Index(content, close)
	if end < start {
		return "", [32]byte{}, false
	}
	original := content[start:end]
	fingerprint := sha256.Sum256([]byte(original))
	text := historyMessageTag.ReplaceAllString(original, " ")
	text = strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " ")
	if text == "" {
		return "", fingerprint, false
	}
	// 有明确凭据特征时省略整段节选；诊断不包含被舍弃的文本或匹配值。
	if historySecretPattern.MatchString(text) {
		return "", fingerprint, true
	}
	runes := []rune(text)
	if len(runes) > 280 {
		text = string(runes[:280])
	}
	return text, fingerprint, true
}

var historySecretPattern = regexp.MustCompile(`(?i)(?:\b(?:authorization|password|passwd|api[_ -]?key|access[_ -]?token|secret|private[_ -]?key)\s*[:=]|\bBearer\s+[A-Za-z0-9._~+/=-]+|\bsk-[A-Za-z0-9_-]{12,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|https?://[^\s/]+:[^\s/@]+@)`)

func (c *autoHistoryCollector) finish() {
	for element := c.order.Front(); element != nil; element = element.Next() {
		item := element.Value.(*autoHistoryRoute).item
		item.SessionExcerptStatus = "unavailable"
		item.SessionExcerptDetail = "未取得可明确关联且带 original_user_request 标记的会话原始请求"
		if item.ConversationID != "" && item.WorkflowID != "" && item.NodeID != "" {
			key := historyContextKey("workflow", item.ConversationID, item.WorkflowID, item.NodeID)
			if activity, ok := c.contexts[key]; ok && activity.activityCount > 0 && !activity.ambiguous {
				item.WorkflowName = activity.value
				if activity.value != "" {
					item.ActivitySourceFile, item.ActivitySourceLine = activity.source.file, activity.source.line
					item.ActivityLineRelative, item.ActivityByteOffset = activity.source.relative, activity.source.offset
				}
				if excerpt, ok := c.contexts[historyContextKey("session", item.ConversationID)]; ok {
					if excerpt.ambiguous {
						item.SessionExcerptStatus = "ambiguous"
						item.SessionExcerptDetail = "同会话出现不同原始请求，无法唯一确定节选"
					} else {
						item.SessionExcerptStatus = excerpt.status
						item.SessionExcerptDetail = "关联会话原始请求节选，非节点职责定义"
						if excerpt.status == "suppressed_sensitive_pattern" {
							item.SessionExcerptDetail = "原始请求含可识别的凭据特征，节选已省略"
						}
						item.SessionExcerpt = excerpt.value
						item.ExcerptSourceFile, item.ExcerptSourceLine = excerpt.source.file, excerpt.source.line
						item.ExcerptLineRelative, item.ExcerptByteOffset = excerpt.source.relative, excerpt.source.offset
					}
				}
			} else if ok && activity.ambiguous {
				item.SessionExcerptStatus = "ambiguous"
				item.SessionExcerptDetail = "精确工作流关联存在冲突，无法唯一确定节选来源"
			}
		}
		c.response.Items = append(c.response.Items, item)
	}
	sort.SliceStable(c.response.Items, func(i, j int) bool {
		return c.response.Items[i].LastSeen.After(c.response.Items[j].LastSeen)
	})
	for _, context := range c.contexts {
		if context.activityCount == 0 {
			continue
		}
		node := autoHistoryActivityNode{
			WorkflowID: context.workflowID, NodeID: context.nodeID, ConversationID: context.conversationID,
			ActivityCount: context.activityCount, SourceFile: context.latestActivitySource.file,
			SourceLine: context.latestActivitySource.line, SourceLineRelative: context.latestActivitySource.relative,
			SourceByteOffset: context.latestActivitySource.offset, HasAutoRoute: context.hasAutoRoute,
			ContextStatus: "unavailable",
		}
		if context.ambiguous {
			node.ContextStatus = "ambiguous"
		} else if context.value != "" {
			node.ContextStatus, node.WorkflowName = "available", context.value
		}
		c.response.ActivityNodes = append(c.response.ActivityNodes, node)
	}
	// 无活动时间证据时，仅按精确关联键稳定排序，不宣称时间先后。
	sort.Slice(c.response.ActivityNodes, func(i, j int) bool {
		a, b := c.response.ActivityNodes[i], c.response.ActivityNodes[j]
		return historyContextKey("workflow", a.ConversationID, a.WorkflowID, a.NodeID) <
			historyContextKey("workflow", b.ConversationID, b.WorkflowID, b.NodeID)
	})
}
