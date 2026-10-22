// kiro_activity_routing.go
// activity 驱动的 auto 路由：本地终结 /agents/activity 时对流式正文做只读解析，
// 提取白名单工作流元数据建立会话路由状态；无头 raw auto 请求命中状态时
// 合成 v2 workflow 头，复用既有 ApplyWorkflowStageHeader 绑定与阶段路由。
package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"kiro-proxy/converter"
)

// 会话状态容量与 TTL 来自契约；字段长度上限与桥接 v2 校验及
// converter.ApplyWorkflowStageHeader 的路由身份限制对齐，保证合成头能通过既有校验。
const (
	kiroActivityRoutingMaxSessions  = 512
	kiroActivityRoutingIdleTTL      = 24 * time.Hour
	kiroActivityMaxStringTokenBytes = 1 << 20
	kiroActivityMaxDepth            = 64
	kiroActivityMaxNodePathSegments = 32
	kiroActivityMaxBatchEvents      = 512

	kiroActivitySessionIDMaxBytes    = 512
	kiroActivityWorkflowIDMaxBytes   = 512
	kiroActivityWorkflowNameMaxBytes = 512
	kiroActivityNodeIDMaxBytes       = 128
	kiroActivityAgentModeMaxBytes    = 128
	kiroActivityExecutionIDMaxBytes  = 512
	kiroActivityNodePathMaxBytes     = 1024

	// 与 converter 内部 workflow 头 4096 字节上限一致；converter 未导出该常量。
	kiroActivitySynthesizedHeaderMaxBytes = 4096
)

var (
	// errKiroActivityUnroutable marks a valid document that contains no routable
	// workflow observation; executionId is optional diagnostic metadata.
	errKiroActivityUnroutable    = errors.New("kiro activity carries no routable session state")
	errKiroActivityTokenTooLarge = errors.New("kiro activity string token exceeds 1 MiB")
	errKiroActivityTrailingData  = errors.New("kiro activity body carries trailing data after the JSON document")
	errKiroActivityInvalidJSON   = errors.New("kiro activity JSON is invalid")
	errKiroActivityBatchLimit    = errors.New("kiro activity batch exceeds the bounded event limit")
	errKiroActivityDepthLimit    = errors.New("kiro activity nesting exceeds the bounded depth")
)

type kiroActivityParseCategory string

const (
	kiroActivityCategoryInvalidJSON     kiroActivityParseCategory = "invalid_json"
	kiroActivityCategoryTrailingData    kiroActivityParseCategory = "trailing_data"
	kiroActivityCategoryTokenTooLarge   kiroActivityParseCategory = "token_too_large"
	kiroActivityCategoryEventInvalid    kiroActivityParseCategory = "event_invalid"
	kiroActivityCategoryEventUnroutable kiroActivityParseCategory = "event_unroutable"
	kiroActivityCategorySubstream       kiroActivityParseCategory = "substream_ignored"
	kiroActivityCategorySequence        kiroActivityParseCategory = "sequence_ignored"
	kiroActivityCategoryBatchLimit      kiroActivityParseCategory = "batch_limit"
	kiroActivityCategoryPathLimit       kiroActivityParseCategory = "path_limit"
	kiroActivityCategoryDepthLimit      kiroActivityParseCategory = "depth_limit"
)

type kiroActivityParseReport struct {
	Entries    []kiroActivityRoutingMeta
	Categories []kiroActivityParseCategory
	singleton  bool
}

func (r *kiroActivityParseReport) addCategory(category kiroActivityParseCategory) {
	if category == "" {
		return
	}
	for _, existing := range r.Categories {
		if existing == category {
			return
		}
	}
	r.Categories = append(r.Categories, category)
}

func kiroActivityErrorCategory(err error) kiroActivityParseCategory {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errKiroActivityTokenTooLarge):
		return kiroActivityCategoryTokenTooLarge
	case errors.Is(err, errKiroActivityTrailingData):
		return kiroActivityCategoryTrailingData
	case errors.Is(err, errKiroActivityBatchLimit):
		return kiroActivityCategoryBatchLimit
	case errors.Is(err, errKiroActivityDepthLimit):
		return kiroActivityCategoryDepthLimit
	case errors.Is(err, errKiroActivityUnroutable):
		return kiroActivityCategoryEventUnroutable
	default:
		return kiroActivityCategoryInvalidJSON
	}
}

// kiroActivityRoutingMeta 是一条 activity 提取出的路由元数据；SessionID 同时充当存储键，
// 与 raw Responses 请求体的 kiro:conversation_id 属同一键空间。
type kiroActivityRoutingMeta struct {
	SessionID    string
	WorkflowID   string
	WorkflowName string
	NodeID       string
	NodePath     string
	AgentMode    string
	ExecutionID  string
	Sequence     int64
	HasSequence  bool
	UpdatedAt    time.Time
}

// kiroActivityRoutingStore 是进程内会话路由状态：mutex 并发安全、容量有界、
// 空闲超时惰性过期，不持久化；代理重启后为空属预期。
type kiroActivityRoutingStore struct {
	mu       sync.Mutex
	sessions map[string]kiroActivityRoutingMeta
}

func newKiroActivityRoutingStore() *kiroActivityRoutingStore {
	return &kiroActivityRoutingStore{sessions: make(map[string]kiroActivityRoutingMeta)}
}

func (s *Server) activityRoutingStore() *kiroActivityRoutingStore {
	s.kiroActivityRoutesOnce.Do(func() {
		if s.kiroActivityRoutes == nil {
			s.kiroActivityRoutes = newKiroActivityRoutingStore()
		}
	})
	return s.kiroActivityRoutes
}

// recordKiroActivityRouting 按同主流 seq 幂等接受新观察；未排序的单事件仅能建立初态，
// 不得覆盖任何已有状态。容量满时淘汰最久未更新条目。
func (s *Server) recordKiroActivityRouting(meta kiroActivityRoutingMeta) bool {
	if meta.SessionID == "" {
		return false
	}
	store := s.activityRoutingStore()
	now := time.Now()
	store.mu.Lock()
	defer store.mu.Unlock()
	for id, entry := range store.sessions {
		if now.Sub(entry.UpdatedAt) > kiroActivityRoutingIdleTTL {
			delete(store.sessions, id)
		}
	}
	if previous, exists := store.sessions[meta.SessionID]; exists {
		if !meta.HasSequence || (previous.HasSequence && meta.Sequence <= previous.Sequence) {
			return false
		}
	}
	if _, exists := store.sessions[meta.SessionID]; !exists && len(store.sessions) >= kiroActivityRoutingMaxSessions {
		oldestID := ""
		var oldest time.Time
		for id, entry := range store.sessions {
			if oldestID == "" || entry.UpdatedAt.Before(oldest) {
				oldestID, oldest = id, entry.UpdatedAt
			}
		}
		if oldestID != "" {
			delete(store.sessions, oldestID)
		}
	}
	meta.UpdatedAt = now
	store.sessions[meta.SessionID] = meta
	return true
}

// lookupKiroActivityRouting 按请求正文 conversationId 查询路由状态；
// 只读不续期，命中空闲过期条目时惰性删除。
func (s *Server) lookupKiroActivityRouting(conversationID string) (kiroActivityRoutingMeta, bool) {
	store := s.activityRoutingStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.sessions[conversationID]
	if !ok {
		return kiroActivityRoutingMeta{}, false
	}
	if time.Since(entry.UpdatedAt) > kiroActivityRoutingIdleTTL {
		delete(store.sessions, conversationID)
		return kiroActivityRoutingMeta{}, false
	}
	return entry, true
}

type kiroActivityJSONScanner struct {
	r            *bufio.Reader
	readErr      error
	report       kiroActivityParseReport
	pending      map[string]kiroActivityRoutingMeta
	conflicted   map[string]bool
	eventCount   int
	batch        bool
	rootHasEvent bool
}

// kiroActivityReadErrorRecorder preserves errors returned together with data.
// bufio.Reader delays exposing such errors until its buffered bytes are consumed,
// while a syntax error may stop parsing before that happens.
type kiroActivityReadErrorRecorder struct {
	r   io.Reader
	err error
}

func (r *kiroActivityReadErrorRecorder) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && err != io.EOF && r.err == nil {
		r.err = err
	}
	return n, err
}

type kiroActivityBodyReadError struct{ err error }

func (e *kiroActivityBodyReadError) Error() string {
	return "kiro activity body read failed: " + e.err.Error()
}
func (e *kiroActivityBodyReadError) Unwrap() error { return e.err }

type kiroActivityEvent struct {
	meta             kiroActivityRoutingMeta
	invalid          bool
	rootSeen         uint16
	contentSeen      uint8
	metaSeen         uint8
	kiroSeen         uint8
	workflowSeen     uint8
	topAgentMode     string
	nestedAgentMode  string
	topAgentModeSeen bool
	nestedModeSeen   bool
	subSession       string
	parentAgentID    string
	pathSegments     []string
	rootHasEvent     bool
}

const (
	activityRootSessionID uint16 = 1 << iota
	activityRootSequence
	activityRootTimestamp
	activityRootMessageID
	activityRootAgentMode
	activityRootSubSession
	activityRootParentAgentID
	activityRootContent
)

const (
	activityContentExecutionID uint8 = 1 << iota
	activityContentMeta
)

const (
	activityMetaKiro uint8 = 1 << iota
)

const (
	activityKiroAgentMode uint8 = 1 << iota
	activityKiroWorkflow
)

const (
	activityWorkflowID uint8 = 1 << iota
	activityWorkflowName
	activityWorkflowNodeID
	activityWorkflowNodePath
)

func (e *kiroActivityEvent) mark(seen *uint8, bit uint8, report *kiroActivityParseReport) bool {
	if *seen&bit != 0 {
		e.invalid = true
		report.addCategory(kiroActivityCategoryEventInvalid)
		return false
	}
	*seen |= bit
	return true
}

func (e *kiroActivityEvent) markRoot(bit uint16, report *kiroActivityParseReport) bool {
	if e.rootSeen&bit != 0 {
		e.invalid = true
		report.addCategory(kiroActivityCategoryEventInvalid)
		return false
	}
	e.rootSeen |= bit
	e.rootHasEvent = true
	return true
}

// parseKiroActivityRouting tokenizes JSON incrementally. String bytes are capped
// while being read, before a complete untrusted token can be allocated.
func parseKiroActivityRouting(body io.Reader) (kiroActivityParseReport, error) {
	readRecorder := &kiroActivityReadErrorRecorder{r: body}
	scanner := &kiroActivityJSONScanner{r: bufio.NewReaderSize(readRecorder, 32<<10), pending: make(map[string]kiroActivityRoutingMeta), conflicted: make(map[string]bool)}
	report, err := scanner.parseDocument()
	readErr := scanner.readErr
	if readRecorder.err != nil && !errors.Is(readErr, readRecorder.err) {
		readErr = errors.Join(readErr, readRecorder.err)
	}
	if readErr != nil {
		err = errors.Join(err, &kiroActivityBodyReadError{err: readErr})
	}
	if err != nil {
		report.Entries = nil // A syntax/limit error must never partially commit a batch.
		report.addCategory(kiroActivityErrorCategory(err))
		return report, err
	}
	for _, entry := range scanner.pending {
		report.Entries = append(report.Entries, entry)
	}
	return report, nil
}

func (p *kiroActivityJSONScanner) parseDocument() (kiroActivityParseReport, error) {
	first, err := p.nextSignificant()
	if err != nil || first != '{' {
		return p.report, errKiroActivityInvalidJSON
	}
	rootEvent := &kiroActivityEvent{}
	payloadSeen := false
	b, err := p.nextSignificant()
	if err != nil {
		return p.report, errKiroActivityInvalidJSON
	}
	if b == '}' {
		p.report.singleton = true
		if err := p.expectDocumentEnd(); err != nil {
			return p.report, err
		}
		return p.report, nil
	}
	for {
		if b != '"' {
			return p.report, errKiroActivityInvalidJSON
		}
		key, err := p.readStringAfterQuote()
		if err != nil {
			return p.report, err
		}
		if err := p.expect(':'); err != nil {
			return p.report, errKiroActivityInvalidJSON
		}
		if key == "payload" {
			if payloadSeen {
				return p.report, errKiroActivityInvalidJSON
			}
			payloadSeen = true
			p.batch = true
			if err := p.parsePayload(); err != nil {
				return p.report, err
			}
		} else if err := p.parseEventField(rootEvent, key, activityScopeRoot, 1); err != nil {
			return p.report, err
		}
		delimiter, err := p.nextSignificant()
		if err != nil {
			return p.report, errKiroActivityInvalidJSON
		}
		if delimiter == '}' {
			break
		}
		if delimiter != ',' {
			return p.report, errKiroActivityInvalidJSON
		}
		b, err = p.nextSignificant()
		if err != nil || b == '}' {
			return p.report, errKiroActivityInvalidJSON
		}
	}
	if p.batch {
		if rootEvent.rootHasEvent {
			p.report.addCategory(kiroActivityCategoryEventInvalid)
		}
	} else {
		p.report.singleton = true
		if err := p.acceptEvent(rootEvent, true); err != nil {
			return p.report, err
		}
	}
	if err := p.expectDocumentEnd(); err != nil {
		return p.report, err
	}
	return p.report, nil
}

func (p *kiroActivityJSONScanner) parsePayload() error {
	opening, err := p.nextSignificant()
	if err != nil || opening != '[' {
		return errKiroActivityInvalidJSON
	}
	isFirst := true
	for {
		b, err := p.nextSignificant()
		if err != nil {
			return errKiroActivityInvalidJSON
		}
		if b == ']' {
			if isFirst {
				return nil
			}
			return errKiroActivityInvalidJSON
		}
		isFirst = false
		p.eventCount++
		if p.eventCount > kiroActivityMaxBatchEvents {
			return errKiroActivityBatchLimit
		}
		if b == '{' {
			event := &kiroActivityEvent{}
			if err := p.parseObjectFields(event, activityScopeRoot, 3); err != nil {
				return err
			}
			if err := p.acceptEvent(event, false); err != nil {
				return err
			}
		} else {
			if err := p.unread(b); err != nil {
				return errKiroActivityInvalidJSON
			}
			if err := p.skipValue(2); err != nil {
				return err
			}
			p.report.addCategory(kiroActivityCategoryEventInvalid)
		}
		delimiter, err := p.nextSignificant()
		if err != nil {
			return errKiroActivityInvalidJSON
		}
		if delimiter == ']' {
			return nil
		}
		if delimiter != ',' {
			return errKiroActivityInvalidJSON
		}
		next, err := p.nextSignificant()
		if err != nil || next == ']' {
			return errKiroActivityInvalidJSON
		}
		if err := p.unread(next); err != nil {
			return errKiroActivityInvalidJSON
		}
	}
}

type kiroActivityScope uint8

const (
	activityScopeRoot kiroActivityScope = iota
	activityScopeContent
	activityScopeMeta
	activityScopeKiro
	activityScopeWorkflow
)

func (p *kiroActivityJSONScanner) parseObjectFields(event *kiroActivityEvent, scope kiroActivityScope, depth int) error {
	if depth > kiroActivityMaxDepth {
		return errKiroActivityDepthLimit
	}
	first := true
	for {
		b, err := p.nextSignificant()
		if err != nil {
			return errKiroActivityInvalidJSON
		}
		if b == '}' && first {
			return nil
		}
		if b != '"' {
			return errKiroActivityInvalidJSON
		}
		first = false
		key, err := p.readStringAfterQuote()
		if err != nil {
			return err
		}
		if err := p.expect(':'); err != nil {
			return errKiroActivityInvalidJSON
		}
		if err := p.parseEventField(event, key, scope, depth); err != nil {
			return err
		}
		delimiter, err := p.nextSignificant()
		if err != nil {
			return errKiroActivityInvalidJSON
		}
		if delimiter == '}' {
			return nil
		}
		if delimiter != ',' {
			return errKiroActivityInvalidJSON
		}
		next, err := p.nextSignificant()
		if err != nil || next == '}' {
			return errKiroActivityInvalidJSON
		}
		if err := p.unread(next); err != nil {
			return errKiroActivityInvalidJSON
		}
	}
}

func (p *kiroActivityJSONScanner) parseEventField(event *kiroActivityEvent, key string, scope kiroActivityScope, depth int) error {
	switch scope {
	case activityScopeRoot:
		var bit uint16
		switch key {
		case "sessionId":
			bit = activityRootSessionID
		case "seq":
			bit = activityRootSequence
		case "activityTimestamp":
			bit = activityRootTimestamp
		case "messageId":
			bit = activityRootMessageID
		case "agentMode":
			bit = activityRootAgentMode
		case "subSession":
			bit = activityRootSubSession
		case "parentAgentId":
			bit = activityRootParentAgentID
		case "content":
			bit = activityRootContent
		default:
			return p.skipValue(depth + 1)
		}
		if !event.markRoot(bit, &p.report) {
			return p.skipValue(depth + 1)
		}
		switch key {
		case "sessionId":
			value, valid, err := p.readStringValue(depth + 1)
			if err != nil {
				return err
			}
			if !valid {
				event.invalid = true
				return nil
			}
			event.meta.SessionID = value
		case "seq":
			value, valid, err := p.readIntegerValue(depth + 1)
			if err != nil {
				return err
			}
			if !valid || value < 0 {
				event.invalid = true
				p.report.addCategory(kiroActivityCategoryEventInvalid)
				return nil
			}
			event.meta.Sequence, event.meta.HasSequence = value, true
		case "activityTimestamp", "messageId", "subSession", "parentAgentId":
			value, valid, err := p.readStringValue(depth + 1)
			if err != nil {
				return err
			}
			if !valid {
				event.invalid = true
				return nil
			}
			if key == "subSession" {
				event.subSession = value
			}
			if key == "parentAgentId" {
				event.parentAgentID = value
			}
		case "agentMode":
			value, valid, err := p.readStringValue(depth + 1)
			if err != nil {
				return err
			}
			if !valid {
				event.invalid = true
				return nil
			}
			event.topAgentMode, event.topAgentModeSeen = value, true
		case "content":
			return p.readObjectValue(depth+1, func() error { return p.parseObjectFields(event, activityScopeContent, depth+1) }, event)
		}
	case activityScopeContent:
		var bit uint8
		switch key {
		case "executionId":
			bit = activityContentExecutionID
		case "_meta":
			bit = activityContentMeta
		default:
			return p.skipValue(depth + 1)
		}
		if !event.mark(&event.contentSeen, bit, &p.report) {
			return p.skipValue(depth + 1)
		}
		if key == "executionId" {
			value, valid, err := p.readStringValue(depth + 1)
			if err != nil {
				return err
			}
			if !valid {
				event.invalid = true
				return nil
			}
			event.meta.ExecutionID = value
			if len(value) > kiroActivityExecutionIDMaxBytes {
				event.invalid = true
				p.report.addCategory(kiroActivityCategoryEventInvalid)
			}
			return nil
		}
		return p.readObjectValue(depth+1, func() error { return p.parseObjectFields(event, activityScopeMeta, depth+1) }, event)
	case activityScopeMeta:
		if key != "kiro" {
			return p.skipValue(depth + 1)
		}
		if !event.mark(&event.metaSeen, activityMetaKiro, &p.report) {
			return p.skipValue(depth + 1)
		}
		return p.readObjectValue(depth+1, func() error { return p.parseObjectFields(event, activityScopeKiro, depth+1) }, event)
	case activityScopeKiro:
		var bit uint8
		switch key {
		case "agentMode":
			bit = activityKiroAgentMode
		case "workflow":
			bit = activityKiroWorkflow
		default:
			return p.skipValue(depth + 1)
		}
		if !event.mark(&event.kiroSeen, bit, &p.report) {
			return p.skipValue(depth + 1)
		}
		if key == "agentMode" {
			value, valid, err := p.readStringValue(depth + 1)
			if err != nil {
				return err
			}
			if !valid {
				event.invalid = true
				return nil
			}
			event.nestedAgentMode, event.nestedModeSeen = value, true
			return nil
		}
		return p.readObjectValue(depth+1, func() error { return p.parseObjectFields(event, activityScopeWorkflow, depth+1) }, event)
	case activityScopeWorkflow:
		var bit uint8
		switch key {
		case "workflowId":
			bit = activityWorkflowID
		case "workflowName":
			bit = activityWorkflowName
		case "nodeId":
			bit = activityWorkflowNodeID
		case "nodePath":
			bit = activityWorkflowNodePath
		default:
			return p.skipValue(depth + 1)
		}
		if !event.mark(&event.workflowSeen, bit, &p.report) {
			return p.skipValue(depth + 1)
		}
		if key == "nodePath" {
			return p.readNodePath(event, depth+1)
		}
		value, valid, err := p.readStringValue(depth + 1)
		if err != nil {
			return err
		}
		if !valid {
			event.invalid = true
			return nil
		}
		switch key {
		case "workflowId":
			event.meta.WorkflowID = value
		case "workflowName":
			event.meta.WorkflowName = value
		case "nodeId":
			event.meta.NodeID = value
		}
	}
	return nil
}

func (p *kiroActivityJSONScanner) readObjectValue(depth int, parse func() error, event *kiroActivityEvent) error {
	b, err := p.nextSignificant()
	if err != nil {
		return errKiroActivityInvalidJSON
	}
	if b != '{' {
		if err := p.unread(b); err != nil {
			return errKiroActivityInvalidJSON
		}
		if err := p.skipValue(depth); err != nil {
			return err
		}
		event.invalid = true
		p.report.addCategory(kiroActivityCategoryEventInvalid)
		return nil
	}
	return parse()
}

func (p *kiroActivityJSONScanner) readStringValue(depth int) (string, bool, error) {
	b, err := p.nextSignificant()
	if err != nil {
		return "", false, errKiroActivityInvalidJSON
	}
	if b != '"' {
		if err := p.unread(b); err != nil {
			return "", false, errKiroActivityInvalidJSON
		}
		if err := p.skipValue(depth); err != nil {
			return "", false, err
		}
		p.report.addCategory(kiroActivityCategoryEventInvalid)
		return "", false, nil
	}
	value, err := p.readStringAfterQuote()
	return value, err == nil, err
}

func (p *kiroActivityJSONScanner) readIntegerValue(depth int) (int64, bool, error) {
	b, err := p.nextSignificant()
	if err != nil {
		return 0, false, errKiroActivityInvalidJSON
	}
	if b != '-' && (b < '0' || b > '9') {
		if err := p.unread(b); err != nil {
			return 0, false, errKiroActivityInvalidJSON
		}
		if err := p.skipValue(depth); err != nil {
			return 0, false, err
		}
		p.report.addCategory(kiroActivityCategoryEventInvalid)
		return 0, false, nil
	}
	var raw []byte
	raw = append(raw, b)
	for {
		c, err := p.readByte()
		if err != nil && err != io.EOF {
			return 0, false, errKiroActivityInvalidJSON
		}
		if err == io.EOF || c == ',' || c == '}' || c == ']' || c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			if err == nil {
				_ = p.r.UnreadByte()
			}
			break
		}
		if len(raw) >= kiroActivityMaxStringTokenBytes {
			return 0, false, errKiroActivityTokenTooLarge
		}
		raw = append(raw, c)
	}
	if !json.Valid(raw) {
		return 0, false, errKiroActivityInvalidJSON
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		p.report.addCategory(kiroActivityCategoryEventInvalid)
		return 0, false, nil
	}
	return value, true, nil
}

func (p *kiroActivityJSONScanner) readNodePath(event *kiroActivityEvent, depth int) error {
	opening, err := p.nextSignificant()
	if err != nil {
		return errKiroActivityInvalidJSON
	}
	if opening != '[' {
		if err := p.unread(opening); err != nil {
			return errKiroActivityInvalidJSON
		}
		if err := p.skipValue(depth); err != nil {
			return err
		}
		event.invalid = true
		p.report.addCategory(kiroActivityCategoryEventInvalid)
		return nil
	}
	isFirst := true
	for {
		b, err := p.nextSignificant()
		if err != nil {
			return errKiroActivityInvalidJSON
		}
		if b == ']' {
			if isFirst {
				return nil
			}
			return errKiroActivityInvalidJSON
		}
		isFirst = false
		if b != '"' {
			if err := p.unread(b); err != nil {
				return errKiroActivityInvalidJSON
			}
			if err := p.skipValue(depth + 1); err != nil {
				return err
			}
			event.invalid = true
			p.report.addCategory(kiroActivityCategoryEventInvalid)
		} else {
			segment, err := p.readStringAfterQuote()
			if err != nil {
				return err
			}
			if len(event.pathSegments) >= kiroActivityMaxNodePathSegments || len(segment) > kiroActivityNodePathMaxBytes {
				event.invalid = true
				p.report.addCategory(kiroActivityCategoryPathLimit)
			} else {
				event.pathSegments = append(event.pathSegments, segment)
			}
		}
		delimiter, err := p.nextSignificant()
		if err != nil {
			return errKiroActivityInvalidJSON
		}
		if delimiter == ']' {
			return nil
		}
		if delimiter != ',' {
			return errKiroActivityInvalidJSON
		}
		next, err := p.nextSignificant()
		if err != nil || next == ']' {
			return errKiroActivityInvalidJSON
		}
		if err := p.unread(next); err != nil {
			return errKiroActivityInvalidJSON
		}
	}
}

func (p *kiroActivityJSONScanner) acceptEvent(event *kiroActivityEvent, allowMissingSequence bool) error {
	if event.invalid {
		p.report.addCategory(kiroActivityCategoryEventInvalid)
		return nil
	}
	if event.rootSeen&(activityRootSubSession|activityRootParentAgentID) != 0 {
		p.report.addCategory(kiroActivityCategorySubstream)
		return nil
	}
	if event.topAgentModeSeen && event.nestedModeSeen && event.topAgentMode != event.nestedAgentMode {
		p.report.addCategory(kiroActivityCategoryEventInvalid)
		return nil
	}
	if event.topAgentModeSeen {
		event.meta.AgentMode = event.topAgentMode
	} else if event.nestedModeSeen {
		event.meta.AgentMode = event.nestedAgentMode
	}
	meta := event.meta
	if !strings.HasPrefix(meta.SessionID, "sess_") || len(meta.SessionID) <= len("sess_") ||
		!validKiroActivityText(meta.SessionID, kiroActivitySessionIDMaxBytes, false) ||
		!validKiroActivityText(meta.WorkflowID, kiroActivityWorkflowIDMaxBytes, false) ||
		!validKiroActivityText(meta.NodeID, kiroActivityNodeIDMaxBytes, false) ||
		!validKiroActivityText(meta.WorkflowName, kiroActivityWorkflowNameMaxBytes, true) ||
		!validKiroActivityText(meta.AgentMode, kiroActivityAgentModeMaxBytes, true) {
		p.report.addCategory(kiroActivityCategoryEventUnroutable)
		return nil
	}
	meta.NodePath = buildKiroActivityNodePath(event.pathSegments, meta.WorkflowID)
	if meta.NodePath == "" || !validKiroActivityText(meta.NodePath, kiroActivityNodePathMaxBytes, false) {
		p.report.addCategory(kiroActivityCategoryEventUnroutable)
		return nil
	}
	if !meta.HasSequence && !allowMissingSequence {
		p.report.addCategory(kiroActivityCategorySequence)
		return nil
	}
	return p.addPending(meta)
}

func validKiroActivityText(value string, maxBytes int, allowEmpty bool) bool {
	if allowEmpty && value == "" {
		return true
	}
	return value != "" && len(value) <= maxBytes && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func (p *kiroActivityJSONScanner) addPending(meta kiroActivityRoutingMeta) error {
	if p.conflicted[meta.SessionID] {
		return nil
	}
	previous, exists := p.pending[meta.SessionID]
	if !exists {
		if len(p.pending) >= kiroActivityRoutingMaxSessions {
			return errKiroActivityBatchLimit
		}
		p.pending[meta.SessionID] = meta
		return nil
	}
	if meta.HasSequence && (!previous.HasSequence || meta.Sequence > previous.Sequence) {
		p.pending[meta.SessionID] = meta
		return nil
	}
	if meta.HasSequence && previous.HasSequence && meta.Sequence == previous.Sequence {
		if meta != previous {
			delete(p.pending, meta.SessionID)
			p.conflicted[meta.SessionID] = true
			p.report.addCategory(kiroActivityCategorySequence)
		}
		return nil
	}
	p.report.addCategory(kiroActivityCategorySequence)
	return nil
}

func (p *kiroActivityJSONScanner) nextSignificant() (byte, error) {
	for {
		b, err := p.readByte()
		if err != nil {
			return 0, err
		}
		if b != ' ' && b != '\n' && b != '\r' && b != '\t' {
			return b, nil
		}
	}
}

func (p *kiroActivityJSONScanner) readByte() (byte, error) {
	b, err := p.r.ReadByte()
	if err != nil && err != io.EOF {
		p.readErr = errors.Join(p.readErr, err)
	}
	return b, err
}

func (p *kiroActivityJSONScanner) unread(b byte) error {
	if err := p.r.UnreadByte(); err != nil {
		return errKiroActivityInvalidJSON
	}
	return nil
}

func (p *kiroActivityJSONScanner) expect(want byte) error {
	b, err := p.nextSignificant()
	if err != nil || b != want {
		return errKiroActivityInvalidJSON
	}
	return nil
}

func (p *kiroActivityJSONScanner) readStringAfterQuote() (string, error) {
	encoded := make([]byte, 1, 64)
	encoded[0] = '"'
	escaped := false
	for {
		b, err := p.readByte()
		if err != nil {
			return "", errKiroActivityInvalidJSON
		}
		if len(encoded) >= kiroActivityMaxStringTokenBytes {
			return "", errKiroActivityTokenTooLarge
		}
		encoded = append(encoded, b)
		if escaped {
			escaped = false
			continue
		}
		if b == '\\' {
			escaped = true
			continue
		}
		if b == '"' {
			var value string
			if err := json.Unmarshal(encoded, &value); err != nil {
				return "", errKiroActivityInvalidJSON
			}
			return value, nil
		}
	}
}

func (p *kiroActivityJSONScanner) skipValue(depth int) error {
	if depth > kiroActivityMaxDepth {
		return errKiroActivityDepthLimit
	}
	b, err := p.nextSignificant()
	if err != nil {
		return errKiroActivityInvalidJSON
	}
	switch b {
	case '"':
		_, err := p.readStringAfterQuote()
		return err
	case '{':
		first := true
		for {
			keyStart, err := p.nextSignificant()
			if err != nil {
				return errKiroActivityInvalidJSON
			}
			if keyStart == '}' && first {
				return nil
			}
			if keyStart != '"' {
				return errKiroActivityInvalidJSON
			}
			if _, err := p.readStringAfterQuote(); err != nil {
				return err
			}
			if err := p.expect(':'); err != nil {
				return err
			}
			if err := p.skipValue(depth + 1); err != nil {
				return err
			}
			first = false
			delimiter, err := p.nextSignificant()
			if err != nil {
				return errKiroActivityInvalidJSON
			}
			if delimiter == '}' {
				return nil
			}
			if delimiter != ',' {
				return errKiroActivityInvalidJSON
			}
		}
	case '[':
		first := true
		for {
			next, err := p.nextSignificant()
			if err != nil {
				return errKiroActivityInvalidJSON
			}
			if next == ']' && first {
				return nil
			}
			if err := p.unread(next); err != nil {
				return err
			}
			if err := p.skipValue(depth + 1); err != nil {
				return err
			}
			first = false
			delimiter, err := p.nextSignificant()
			if err != nil {
				return errKiroActivityInvalidJSON
			}
			if delimiter == ']' {
				return nil
			}
			if delimiter != ',' {
				return errKiroActivityInvalidJSON
			}
		}
	default:
		if err := p.unread(b); err != nil {
			return err
		}
		var raw []byte
		for {
			c, err := p.readByte()
			if err == io.EOF {
				break
			}
			if err != nil {
				return errKiroActivityInvalidJSON
			}
			if c == ',' || c == '}' || c == ']' || c == ' ' || c == '\n' || c == '\r' || c == '\t' {
				_ = p.r.UnreadByte()
				break
			}
			if len(raw) >= kiroActivityMaxStringTokenBytes {
				return errKiroActivityTokenTooLarge
			}
			raw = append(raw, c)
		}
		if len(raw) == 0 || !json.Valid(raw) {
			return errKiroActivityInvalidJSON
		}
		return nil
	}
}

func (p *kiroActivityJSONScanner) expectDocumentEnd() error {
	b, err := p.nextSignificant()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return errKiroActivityInvalidJSON
	}
	_ = b
	return errKiroActivityTrailingData
}

// buildKiroActivityNodePath drops only an initial workflowId frame, then encodes
// and joins every observed node segment. Repeat frames such as iter-N stay intact.
func buildKiroActivityNodePath(segments []string, workflowID string) string {
	if len(segments) == 0 {
		return ""
	}
	if segments[0] == workflowID {
		segments = segments[1:]
	}
	if len(segments) == 0 {
		return ""
	}
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		encoded := kiroActivityEncodeURIComponent(segment)
		parts = append(parts, encoded)
	}
	path := strings.Join(parts, "/")
	if len(path) > kiroActivityNodePathMaxBytes {
		return ""
	}
	return path
}

// kiroActivityEncodeURIComponent 按 JS encodeURIComponent 语义编码：
// A-Za-z0-9 与 -_.!~*'() 原样保留，其余字节按 UTF-8 百分号编码（大写十六进制）。
func kiroActivityEncodeURIComponent(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for i := 0; i < len(value); {
		if c := value[i]; kiroActivityURIUnescaped(c) {
			builder.WriteByte(c)
			i++
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(value[i:])
		if runeValue == utf8.RuneError && size <= 1 {
			// JS 侧无效字节先替换为 U+FFFD 再编码。
			builder.WriteString("%EF%BF%BD")
			i++
			continue
		}
		for j := 0; j < size; j++ {
			kiroActivityWritePercentEncoded(&builder, value[i+j])
		}
		i += size
	}
	return builder.String()
}

func kiroActivityURIUnescaped(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '!' || c == '~' || c == '*' ||
		c == '\'' || c == '(' || c == ')'
}

func kiroActivityWritePercentEncoded(builder *strings.Builder, value byte) {
	const hexDigits = "0123456789ABCDEF"
	builder.WriteByte('%')
	builder.WriteByte(hexDigits[value>>4])
	builder.WriteByte(hexDigits[value&0x0F])
}

// kiroActivitySynthesizedHeader 的字段集合是 converter.ApplyWorkflowStageHeader v2
// 白名单的全集：v2 路由身份字段均为 required key，definitionId/definitionRevision/taskType
// 官方 activity 不提供，按空字符串绑定（不猜测），绑定为空值即保持"不合成"语义。
type kiroActivitySynthesizedHeader struct {
	Version             int    `json:"version"`
	WorkflowID          string `json:"workflowId"`
	ConversationID      string `json:"conversationId"`
	AgentMode           string `json:"agentMode"`
	AgentContinuationID string `json:"agentContinuationId"`
	NodeID              string `json:"nodeId"`
	NodePath            string `json:"nodePath"`
	ExecutionID         string `json:"executionId"`
	DefinitionID        string `json:"definitionId"`
	DefinitionRevision  string `json:"definitionRevision"`
	TaskType            string `json:"taskType"`
}

// buildKiroActivityWorkflowHeader 合成 v2 workflow 头。conversationId/agentMode/
// agentContinuationId 以请求正文为准；workflowId/nodeId/nodePath 取自 activity 观察。
// executionId 只作诊断，不合成为同步 execution 锁；activity 字段无法通过既有路由身份校验时返回错误，
// 调用方回退既有无头行为，不引入新的失败模式。
func buildKiroActivityWorkflowHeader(conv *converter.ParsedConversation, meta kiroActivityRoutingMeta) (string, error) {
	for _, entry := range []struct {
		name  string
		value string
		limit int
	}{
		{"workflowId", meta.WorkflowID, kiroActivityWorkflowIDMaxBytes},
		{"nodeId", meta.NodeID, kiroActivityNodeIDMaxBytes},
		{"nodePath", meta.NodePath, kiroActivityNodePathMaxBytes},
	} {
		if entry.value != strings.TrimSpace(entry.value) || strings.ContainsAny(entry.value, "\x00\r\n") {
			return "", fmt.Errorf("kiro activity %s contains leading, trailing or control whitespace", entry.name)
		}
		if len(entry.value) > entry.limit {
			return "", fmt.Errorf("kiro activity %s exceeds %d bytes", entry.name, entry.limit)
		}
	}
	encoded, err := json.Marshal(kiroActivitySynthesizedHeader{
		Version:             2,
		WorkflowID:          meta.WorkflowID,
		ConversationID:      conv.ConversationID,
		AgentMode:           conv.AgentMode,
		AgentContinuationID: conv.AgentContinuationID,
		NodeID:              meta.NodeID,
		NodePath:            meta.NodePath,
		ExecutionID:         "",
	})
	if err != nil {
		return "", err
	}
	if len(encoded) > kiroActivitySynthesizedHeaderMaxBytes {
		return "", errors.New("synthesized workflow header exceeds 4096 bytes")
	}
	return string(encoded), nil
}

// synthesizeKiroActivityWorkflowHeader 在无头 raw auto 请求命中 activity 状态时合成
// v2 workflow 头并注入请求 context，由既有 parseRequest → ApplyWorkflowStageHeader →
// ResolveStageAuto 路径完成绑定与路由。未命中或合成失败时返回原请求与 false，
// 由 raw auto 的本地默认路由规则处理，不将 auto 转发给官方。
type kiroActivityWorkflowRequestContextKey struct{}

func isKiroActivityWorkflowRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	fromActivity, _ := req.Context().Value(kiroActivityWorkflowRequestContextKey{}).(bool)
	return fromActivity
}

func (s *Server) synthesizeKiroActivityWorkflowHeader(req *http.Request, body []byte) (*http.Request, bool) {
	conv, err := converter.ParseKiroRawResponsesRouting(body)
	if err != nil || strings.TrimSpace(conv.ConversationID) == "" {
		return req, false
	}
	meta, ok := s.lookupKiroActivityRouting(conv.ConversationID)
	if !ok {
		return req, false
	}
	if meta.AgentMode != "" && conv.AgentMode != "" && meta.AgentMode != conv.AgentMode {
		return req, false
	}
	header, err := buildKiroActivityWorkflowHeader(conv, meta)
	if err != nil {
		s.logger.Debug("kiro-activity", "activity 路由观察无效: header_invalid")
		return req, false
	}
	s.logger.Info("kiro-activity", "activity 观察命中本地 auto 路由")
	ctx := context.WithValue(req.Context(), kiroActivityWorkflowRequestContextKey{}, true)
	return req.WithContext(withWorkflowStageHeaders(ctx, []string{header})), true
}
