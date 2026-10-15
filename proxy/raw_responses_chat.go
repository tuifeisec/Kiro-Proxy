package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

// rawResponsesUpstreamURL resolves the explicitly configured raw Responses
// endpoint. A Chat Completions endpoint is adapted at the request and response
// boundary; it is never rewritten into a sibling /responses endpoint.
func rawResponsesUpstreamURL(model *store.Model) (endpoint string, chat bool, err error) {
	if model == nil {
		return "", false, fmt.Errorf("raw Responses model is missing")
	}
	if strings.TrimSpace(model.Provider) != "openai" {
		return "", false, fmt.Errorf("raw Responses upstream requires provider=openai")
	}

	baseURL := strings.TrimSpace(model.BaseURL)
	configuredEndpoint := strings.TrimSpace(model.Endpoint)
	endpoint = buildUpstreamURL(strings.TrimRight(baseURL, "/"), configuredEndpoint)
	if model.DisableAutoSuffix {
		endpoint = baseURL
	}
	parsed, parseErr := url.Parse(endpoint)
	if parseErr != nil {
		return "", false, fmt.Errorf("invalid raw Responses endpoint")
	}
	if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false, fmt.Errorf("raw Responses endpoint must be an absolute HTTP URL")
	}
	configuredPath := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(configuredPath, "/responses"):
		return endpoint, false, nil
	case strings.HasSuffix(configuredPath, "/chat/completions"):
		return endpoint, true, nil
	default:
		return "", false, fmt.Errorf("raw Responses endpoint must end in /responses or /chat/completions")
	}
}

// prepareRawResponsesChatBody adapts a prepared Responses request for a
// configured Chat Completions endpoint. The shared Responses converter handles
// the transcript, tools and stable prompt-cache key; this boundary adds the
// Chat-specific fields that have no direct top-level equivalent in Responses.
func prepareRawResponsesChatBody(body []byte, model *store.Model) ([]byte, error) {
	if model == nil {
		return nil, fmt.Errorf("raw Responses model is missing")
	}
	converted, _, err := responsesToChatFallback(body, model.ModelID)
	if err != nil {
		return nil, err
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(body, &source); err != nil {
		return nil, fmt.Errorf("parse prepared raw Responses body: %w", err)
	}
	var chat map[string]json.RawMessage
	if err := json.Unmarshal(converted, &chat); err != nil {
		return nil, fmt.Errorf("parse converted Chat body: %w", err)
	}
	return finishRawResponsesChatRoot(source, chat, model)
}

func prepareRawResponsesChatRoot(source map[string]json.RawMessage, model *store.Model) ([]byte, error) {
	if _, _, err := ensureResponsesPromptCacheKeyRoot(source, model.ModelID); err != nil {
		return nil, err
	}
	chat, _, err := responsesToChatFallbackRootWithPrevious(source, model.ModelID, nil)
	if err != nil {
		return nil, err
	}
	return finishRawResponsesChatRoot(source, chat, model)
}

func finishRawResponsesChatRoot(source, chat map[string]json.RawMessage, model *store.Model) ([]byte, error) {
	encodedModel, err := json.Marshal(model.ModelID)
	if err != nil {
		return nil, fmt.Errorf("encode configured Chat model: %w", err)
	}
	chat["model"] = encodedModel
	if raw := source["reasoning"]; len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var reasoning map[string]json.RawMessage
		if err := json.Unmarshal(raw, &reasoning); err != nil || reasoning == nil {
			if err == nil {
				err = fmt.Errorf("reasoning must be an object")
			}
			return nil, fmt.Errorf("parse raw Responses reasoning: %w", err)
		}
		if effortRaw := reasoning["effort"]; len(effortRaw) > 0 && !bytes.Equal(bytes.TrimSpace(effortRaw), []byte(`""`)) {
			var effort string
			if err := json.Unmarshal(effortRaw, &effort); err != nil {
				return nil, fmt.Errorf("raw Responses reasoning effort must be a string: %w", err)
			}
			encodedEffort, err := json.Marshal(effort)
			if err != nil {
				return nil, fmt.Errorf("encode Chat reasoning effort: %w", err)
			}
			chat["reasoning_effort"] = encodedEffort
		}
	}
	if raw := source["tool_choice"]; len(raw) > 0 {
		choice, err := rawResponsesToolChoiceToChat(raw)
		if err != nil {
			return nil, err
		}
		chat["tool_choice"] = choice
	}
	encoded, err := json.Marshal(chat)
	if err != nil {
		return nil, fmt.Errorf("encode prepared Chat body: %w", err)
	}
	return encoded, nil
}

func rawResponsesToolChoiceToChat(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("null"), nil
	}
	var value string
	if json.Unmarshal(trimmed, &value) == nil {
		switch value {
		case "auto", "none", "required":
			return append(json.RawMessage(nil), trimmed...), nil
		default:
			return nil, fmt.Errorf("Responses tool_choice %q is not supported by Chat Completions", value)
		}
	}
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &choice); err != nil || choice == nil {
		if err == nil {
			err = fmt.Errorf("tool_choice must be a string or object")
		}
		return nil, fmt.Errorf("parse raw Responses tool_choice: %w", err)
	}
	var typ string
	if err := json.Unmarshal(choice["type"], &typ); err != nil || typ != "function" {
		if err != nil {
			return nil, fmt.Errorf("parse raw Responses tool_choice type: %w", err)
		}
		return nil, fmt.Errorf("Responses tool_choice type %q is not supported by Chat Completions", typ)
	}
	var name string
	if err := json.Unmarshal(choice["name"], &name); err != nil || strings.TrimSpace(name) == "" {
		if err != nil {
			return nil, fmt.Errorf("parse raw Responses function tool_choice name: %w", err)
		}
		return nil, fmt.Errorf("Responses function tool_choice requires a non-empty name")
	}
	converted, err := json.Marshal(map[string]interface{}{
		"type":     "function",
		"function": map[string]string{"name": name},
	})
	if err != nil {
		return nil, fmt.Errorf("encode Chat function tool_choice: %w", err)
	}
	return converted, nil
}

// adaptRawChatResponse converts only successful Chat Completions responses to
// the Responses wire format. HTTP error bodies remain available unchanged for
// the caller's normal error handling.
func adaptRawChatResponse(ctx context.Context, resp *http.Response) (*http.Response, error) {
	if resp == nil {
		return nil, fmt.Errorf("raw Chat response is missing")
	}
	if resp.StatusCode >= http.StatusBadRequest || resp.Body == nil {
		return resp, nil
	}
	if isSSEResponse(resp) {
		if ctx == nil {
			ctx = context.Background()
		}
		resp.Body = newRawChatResponsesLazyBody(ctx, resp.Body, func(ctx context.Context, source io.ReadCloser, writer *io.PipeWriter) error {
			_, _, _, err := copyChatStreamAsResponsesWithState(ctx, rawChatResponsesPipeWriter{writer: writer}, source)
			return err
		})
		resp.Header = adaptedRawChatResponseHeaders(resp.Header, true)
	} else {
		if ctx == nil {
			ctx = context.Background()
		}
		resp.Body = newRawChatResponsesLazyBody(ctx, resp.Body, func(_ context.Context, source io.ReadCloser, writer *io.PipeWriter) error {
			var original bytes.Buffer
			payload, _, err := readAndConvertChatResponse(io.TeeReader(source, &original))
			if err != nil {
				return fmt.Errorf("convert raw Chat response: %w", err)
			}
			if err := validateRawChatJSONResponse(original.Bytes()); err != nil {
				return err
			}
			if _, err := writer.Write(payload); err != nil {
				return err
			}
			return nil
		})
		resp.Header = adaptedRawChatResponseHeaders(resp.Header, false)
	}
	resp.ContentLength = -1
	resp.TransferEncoding = nil
	resp.Trailer = nil
	return resp, nil
}

func validateRawChatJSONResponse(payload []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil || root == nil {
		return fmt.Errorf("invalid Chat Completions JSON response")
	}
	if raw := root["error"]; len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("Chat Completions JSON response reported failure")
	}
	var choices []json.RawMessage
	if err := json.Unmarshal(root["choices"], &choices); err != nil || len(choices) == 0 {
		return fmt.Errorf("Chat Completions JSON response has no choices")
	}
	var first map[string]json.RawMessage
	if err := json.Unmarshal(choices[0], &first); err != nil || first == nil {
		return fmt.Errorf("Chat Completions JSON response has an invalid choice")
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(first["message"], &message); err != nil || message == nil {
		return fmt.Errorf("Chat Completions JSON response has no assistant message")
	}
	return nil
}

func adaptedRawChatResponseHeaders(source http.Header, streaming bool) http.Header {
	header := source.Clone()
	if header == nil {
		header = make(http.Header)
	}
	for _, name := range []string{
		"Content-Length", "Content-Encoding", "Content-Md5", "Transfer-Encoding", "Trailer",
	} {
		header.Del(name)
	}
	if streaming {
		header.Set("Content-Type", "text/event-stream")
	} else {
		header.Set("Content-Type", "application/json")
	}
	return header
}

type rawChatResponsesLazyBody struct {
	reader     *io.PipeReader
	writer     *io.PipeWriter
	source     io.ReadCloser
	ctx        context.Context
	cancel     context.CancelFunc
	convert    func(context.Context, io.ReadCloser, *io.PipeWriter) error
	startOnce  sync.Once
	sourceOnce sync.Once
	closeOnce  sync.Once
	stopCancel func() bool
}

func newRawChatResponsesLazyBody(ctx context.Context, source io.ReadCloser, convert func(context.Context, io.ReadCloser, *io.PipeWriter) error) *rawChatResponsesLazyBody {
	pipeReader, pipeWriter := io.Pipe()
	streamCtx, cancel := context.WithCancel(ctx)
	body := &rawChatResponsesLazyBody{
		reader:  pipeReader,
		writer:  pipeWriter,
		source:  source,
		ctx:     streamCtx,
		cancel:  cancel,
		convert: convert,
	}
	body.stopCancel = context.AfterFunc(streamCtx, func() {
		body.closeSource()
		_ = body.writer.CloseWithError(streamCtx.Err())
	})
	return body
}

func (b *rawChatResponsesLazyBody) Read(p []byte) (int, error) {
	b.startOnce.Do(func() { go b.convertBody() })
	return b.reader.Read(p)
}

func (b *rawChatResponsesLazyBody) Close() error {
	var closeErr error
	b.closeOnce.Do(func() {
		b.cancel()
		b.closeSource()
		closeErr = b.reader.Close()
	})
	return closeErr
}

func (b *rawChatResponsesLazyBody) closeSource() {
	b.sourceOnce.Do(func() {
		if b.source != nil {
			_ = b.source.Close()
		}
	})
}

func (b *rawChatResponsesLazyBody) convertBody() {
	defer func() {
		b.stopCancel()
		b.cancel()
	}()

	err := b.convert(b.ctx, b.source, b.writer)
	b.closeSource()
	if err != nil {
		_ = b.writer.CloseWithError(err)
		return
	}
	_ = b.writer.Close()
}

type rawChatResponsesPipeWriter struct {
	writer *io.PipeWriter
}

func (w rawChatResponsesPipeWriter) Header() http.Header { return make(http.Header) }
func (w rawChatResponsesPipeWriter) WriteHeader(int)     {}
func (w rawChatResponsesPipeWriter) Write(data []byte) (int, error) {
	return w.writer.Write(data)
}
func (rawChatResponsesPipeWriter) Flush() {}
