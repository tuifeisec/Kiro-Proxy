package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"net/http"
	"net/url"
	"strings"
	"sync"

	json "github.com/goccy/go-json"
)

// ForwardRawResponses forwards a native OpenAI Responses request for a single
// stage-selected model. It intentionally leaves the request and response
// protocol untouched apart from the documented local routing fields.
func (f *Forwarder) ForwardRawResponses(req *http.Request, pre *Preflight, route *router.Route, logger *TrafficLogger) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("raw Responses request is nil")
	}
	if pre == nil {
		return nil, fmt.Errorf("raw Responses preflight is nil")
	}
	if route == nil || route.Model == nil {
		return nil, fmt.Errorf("raw Responses route has no selected model")
	}
	model := route.Model
	if route.AutoMode != store.AutoRoutingModeStageFixed && route.AutoMode != store.AutoRoutingModeDutyFixed {
		return nil, fmt.Errorf("raw Responses requires a fixed Auto route")
	}
	bindAutoReasoningEffort(pre, route, nil)
	if model.Provider != "openai" {
		return nil, fmt.Errorf("raw Responses only supports provider openai; got %q", model.Provider)
	}
	if req.Method != http.MethodPost {
		return nil, fmt.Errorf("raw Responses requires POST")
	}

	upstreamURL, err := localCompatUpstreamURL(localCompatResponses, model)
	if err != nil {
		return nil, fmt.Errorf("resolve raw Responses upstream URL: %w", err)
	}
	if !isOpenAIResponsesUpstream(model, upstreamURL) {
		return nil, fmt.Errorf("raw Responses requires an OpenAI upstream endpoint ending in /responses")
	}

	body := pre.Body
	if body == nil && req.Body != nil {
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("read raw Responses request body: %w", err)
		}
	}
	upstreamBody, err := prepareRawResponsesBody(body, pre, model)
	if err != nil {
		return nil, err
	}

	usage := pre.usage
	if usage == nil {
		requestPath := ""
		if req.URL != nil {
			requestPath = req.URL.Path
		}
		usage = newRequestUsageRecorder(f.db, pre.StartTime, model, requestPath, pre.Protocol)
	}
	if pre.usage != nil {
		bindUsageModel(pre, model)
	} else {
		usage.setModel(model)
		usage.setReasoningEffort(resolveReasoningEffort(pre, model))
	}
	usage.setRoute(route)
	limit := 0
	if route.CandidateLimits != nil {
		limit = route.CandidateLimits[model.ID]
	}
	lease, acquired := f.load.acquire(model.ID, limit)
	if !acquired {
		return nil, fmt.Errorf("model %s has reached its concurrency limit", statsModelID(model))
	}

	sessionID := ""
	if pre.Conv != nil {
		sessionID = pre.Conv.ConversationID
	} else if pre.ConversationID != "" {
		sessionID = pre.ConversationID
	}
	resp, err := f.sendRawResponsesWithRetryBudget(req.Context(), upstreamURL, upstreamBody, model, logger, sessionID, usage, singleModelFirstByteTimeout)
	if err != nil {
		lease.release()
		safeErr := redactRawResponsesEndpointError(err, upstreamURL)
		usage.finish(http.StatusBadGateway, localCompatUsage{}, safeErr.Error())
		return nil, safeErr
	}
	if resp == nil {
		lease.release()
		usage.finish(http.StatusBadGateway, localCompatUsage{}, "raw Responses upstream returned no response")
		return nil, fmt.Errorf("raw Responses upstream returned no response")
	}
	if resp.Body == nil {
		usage.finish(resp.StatusCode, localCompatUsage{}, "upstream response has no body")
		f.recordRawResponsesStats(model, localCompatUsage{}, resp.StatusCode, "upstream response has no body", req.Context())
		lease.release()
		return resp, nil
	}

	usage.wrapResponseForModel(resp, model, upstreamURL, f.contentLatencyHook(usage, model, singleModelFirstByteTimeout))
	observed := newRawResponsesUsageBody(resp.Body, usage, resp.StatusCode, strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream"))
	observed.lease = lease
	observed.stats = f.stats
	observed.model = model
	observed.ctx = req.Context()
	resp.Body = observed
	observed.watchContext(req.Context())
	return resp, nil
}

func (f *Forwarder) recordRawResponsesStats(model *store.Model, usage localCompatUsage, status int, errorMessage string, ctx context.Context) {
	if f == nil || f.stats == nil {
		return
	}
	clientCanceled := ctx != nil && ctx.Err() != nil && (errorMessage == "raw Responses body closed before EOF" || errorMessage == context.Canceled.Error() || errorMessage == context.DeadlineExceeded.Error())
	isError := status >= http.StatusBadRequest || errorMessage != "" && !clientCanceled
	credits := store.CalculateCredits(usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, model)
	modelID := ""
	if model != nil {
		modelID = statsModelID(model)
	}
	f.stats.RecordRequestFullWithCredits(usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, credits, modelID, isError)
}

func redactRawResponsesEndpointError(err error, upstreamURL string) error {
	if err == nil {
		return nil
	}
	parsed, parseErr := url.Parse(upstreamURL)
	if parseErr != nil {
		return err
	}
	original := parsed.String()
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	safeURL := parsed.String()
	message := strings.ReplaceAll(err.Error(), upstreamURL, safeURL)
	message = strings.ReplaceAll(message, original, safeURL)
	return fmt.Errorf("%s", message)
}

// stripRawWorkflowMetadata removes only fields owned by the local workflow
// bridge. RawMessage keeps every other JSON value, including large numbers,
// byte-for-byte intact when the containing object needs to be re-encoded.
func stripRawWorkflowMetadata(body []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("parse raw Responses JSON: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("raw Responses body must be a JSON object")
	}
	metadataRaw, ok := root["metadata"]
	if !ok || bytes.Equal(bytes.TrimSpace(metadataRaw), []byte("null")) {
		return body, nil
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(metadataRaw, &metadata); err != nil || metadata == nil {
		if err == nil {
			err = fmt.Errorf("metadata must be an object or null")
		}
		return nil, fmt.Errorf("parse raw Responses metadata: %w", err)
	}
	_, hasContinuation := metadata["kiro:proxy_continuation_id"]
	_, hasToolResults := metadata["kiro:proxy_current_tool_results"]
	if !hasContinuation && !hasToolResults {
		return body, nil
	}
	delete(metadata, "kiro:proxy_continuation_id")
	delete(metadata, "kiro:proxy_current_tool_results")
	if len(metadata) == 0 {
		delete(root, "metadata")
	} else {
		encodedMetadata, err := json.Marshal(metadata)
		if err != nil {
			return nil, fmt.Errorf("encode raw Responses metadata: %w", err)
		}
		root["metadata"] = encodedMetadata
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode raw Responses body: %w", err)
	}
	return encoded, nil
}

func prepareRawResponsesBody(body []byte, pre *Preflight, model *store.Model) ([]byte, error) {
	var original map[string]json.RawMessage
	if err := json.Unmarshal(body, &original); err != nil {
		return nil, fmt.Errorf("parse raw Responses JSON: %w", err)
	}
	if original == nil {
		return nil, fmt.Errorf("raw Responses body must be a JSON object")
	}
	for _, key := range []string{"previous_response_id", "conversation"} {
		if value, ok := original[key]; ok && rawJSONNonEmpty(value) {
			return nil, fmt.Errorf("raw Responses %s is not supported", key)
		}
	}

	cleaned, err := stripRawWorkflowMetadata(body)
	if err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(cleaned, &root); err != nil {
		return nil, fmt.Errorf("parse cleaned raw Responses JSON: %w", err)
	}
	modelRaw, err := json.Marshal(model.ModelID)
	if err != nil {
		return nil, fmt.Errorf("encode upstream model: %w", err)
	}
	root["model"] = modelRaw

	reasoningRaw, reasoningExists := root["reasoning"]
	var reasoning map[string]json.RawMessage
	if reasoningExists {
		if bytes.Equal(bytes.TrimSpace(reasoningRaw), []byte("null")) {
			reasoningExists = false
			reasoning = make(map[string]json.RawMessage)
		} else if err := json.Unmarshal(reasoningRaw, &reasoning); err != nil || reasoning == nil {
			if err == nil {
				err = fmt.Errorf("reasoning must be an object")
			}
			return nil, fmt.Errorf("parse raw Responses reasoning: %w", err)
		}
	} else {
		reasoning = make(map[string]json.RawMessage)
	}
	effortRaw, effortExists := reasoning["effort"]
	shouldSetEffort := !effortExists
	if effortExists {
		var effort string
		if json.Unmarshal(effortRaw, &effort) == nil && effort == "" {
			shouldSetEffort = true
		}
	}
	if pre != nil && pre.AutoReasoningEffort != nil && !pre.IsClassifier {
		effort := *pre.AutoReasoningEffort
		if effort == "" {
			delete(reasoning, "effort")
		} else {
			effortJSON, err := json.Marshal(effort)
			if err != nil {
				return nil, fmt.Errorf("encode fixed raw Responses reasoning effort: %w", err)
			}
			reasoning["effort"] = effortJSON
		}
		if len(reasoning) == 0 {
			delete(root, "reasoning")
		} else {
			encodedReasoning, err := json.Marshal(reasoning)
			if err != nil {
				return nil, fmt.Errorf("encode fixed raw Responses reasoning: %w", err)
			}
			root["reasoning"] = encodedReasoning
		}
	} else if shouldSetEffort {
		if effort := resolveReasoningEffort(pre, model); effort != "" {
			effortJSON, err := json.Marshal(effort)
			if err != nil {
				return nil, fmt.Errorf("encode raw Responses reasoning effort: %w", err)
			}
			reasoning["effort"] = effortJSON
			encodedReasoning, err := json.Marshal(reasoning)
			if err != nil {
				return nil, fmt.Errorf("encode raw Responses reasoning: %w", err)
			}
			root["reasoning"] = encodedReasoning
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode raw Responses body: %w", err)
	}
	return encoded, nil
}

func rawJSONNonEmpty(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		return false
	}
	return true
}

type rawResponsesUsageBody struct {
	io.ReadCloser
	usage       *requestUsageRecorder
	status      int
	isSSE       bool
	stats       *store.Stats
	model       *store.Model
	ctx         context.Context
	localUsage  localCompatUsage
	pending     []byte
	jsonBody    []byte
	eventType   string
	terminal    bool
	failure     bool
	dropping    bool
	lease       *modelLease
	finalized   bool
	mu          sync.Mutex
	finishOnce  sync.Once
	closeOnce   sync.Once
	closeErr    error
	stopContext func() bool
}

func newRawResponsesUsageBody(body io.ReadCloser, usage *requestUsageRecorder, status int, isSSE bool) *rawResponsesUsageBody {
	return &rawResponsesUsageBody{ReadCloser: body, usage: usage, status: status, isSSE: isSSE}
}

func (b *rawResponsesUsageBody) watchContext(ctx context.Context) {
	if ctx == nil {
		return
	}
	stop := context.AfterFunc(ctx, func() { _ = b.Close() })
	b.mu.Lock()
	if b.finalized {
		b.mu.Unlock()
		stop()
		return
	}
	b.stopContext = stop
	b.mu.Unlock()
}

func (b *rawResponsesUsageBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	if !b.finalized {
		if n > 0 {
			if b.isSSE {
				b.feedSSELocked(p[:n])
			} else if len(b.jsonBody) < localCompatBodyLimit {
				remaining := localCompatBodyLimit - len(b.jsonBody)
				if remaining > n {
					remaining = n
				}
				b.jsonBody = append(b.jsonBody, p[:remaining]...)
			}
		}
	}
	b.mu.Unlock()
	if err == io.EOF {
		b.finishEOF()
	} else if err != nil {
		b.finish(err.Error())
	}
	return n, err
}

func (b *rawResponsesUsageBody) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.ReadCloser.Close()
		b.finish("raw Responses body closed before EOF")
	})
	return b.closeErr
}

func (b *rawResponsesUsageBody) feedSSELocked(data []byte) {
	for len(data) > 0 {
		if b.dropping {
			newline := bytes.IndexByte(data, '\n')
			if newline < 0 {
				return
			}
			b.dropping = false
			data = data[newline+1:]
			continue
		}
		newline := bytes.IndexByte(data, '\n')
		if newline >= 0 {
			if len(b.pending)+newline <= localCompatBodyLimit {
				b.pending = append(b.pending, data[:newline]...)
				b.consumeSSELineLocked(b.pending)
			}
			b.pending = nil
			data = data[newline+1:]
			continue
		}
		if len(b.pending)+len(data) > localCompatBodyLimit {
			b.pending = nil
			b.dropping = true
			return
		}
		b.pending = append(b.pending, data...)
		return
	}
}

func (b *rawResponsesUsageBody) consumeSSELineLocked(line []byte) {
	line = bytes.TrimSpace(line)
	if bytes.HasPrefix(line, []byte("event:")) {
		b.eventType = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
		return
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	mergeLocalCompatUsage(&b.localUsage, localCompatResponses, data)
	var event struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(data, &event) == nil {
		eventType := event.Type
		if eventType == "" {
			eventType = b.eventType
		}
		if len(event.Error) > 0 && !bytes.Equal(bytes.TrimSpace(event.Error), []byte("null")) {
			b.failure = true
			b.terminal = true
		}
		switch eventType {
		case "response.completed":
			b.terminal = true
		case "response.failed", "response.incomplete", "error":
			b.failure = true
			b.terminal = true
		}
	}
	b.eventType = ""
}

func (b *rawResponsesUsageBody) finishEOF() {
	errorMessage := ""
	b.mu.Lock()
	if b.finalized {
		b.mu.Unlock()
		return
	}
	if b.isSSE {
		if len(b.pending) > 0 && !b.dropping {
			b.consumeSSELineLocked(b.pending)
			b.pending = nil
		}
		if b.failure {
			errorMessage = "raw Responses stream reported failure"
		} else if !b.terminal {
			errorMessage = "raw Responses stream ended without a terminal event"
		}
	} else {
		mergeLocalCompatUsage(&b.localUsage, localCompatResponses, b.jsonBody)
	}
	b.mu.Unlock()
	b.finish(errorMessage)
}

func (b *rawResponsesUsageBody) finish(errorMessage string) {
	b.finishOnce.Do(func() {
		b.mu.Lock()
		b.finalized = true
		usage := b.localUsage
		observedFailure := b.failure
		ctxErr := error(nil)
		if b.ctx != nil {
			ctxErr = b.ctx.Err()
		}
		clientCanceled := false
		switch {
		case observedFailure:
			errorMessage = "raw Responses stream reported failure"
		case ctxErr != nil:
			errorMessage = ctxErr.Error()
			clientCanceled = true
		}
		stopContext := b.stopContext
		b.stopContext = nil
		b.mu.Unlock()
		if stopContext != nil {
			stopContext()
		}
		if b.usage != nil {
			b.usage.finish(b.status, usage, errorMessage)
		}
		if b.stats != nil {
			isError := b.status >= http.StatusBadRequest || errorMessage != "" && !clientCanceled
			credits := store.CalculateCredits(usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, b.model)
			modelID := ""
			if b.model != nil {
				modelID = statsModelID(b.model)
			}
			b.stats.RecordRequestFullWithCredits(usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, credits, modelID, isError)
		}
		if b.lease != nil {
			b.lease.release()
		}
	})
}
