package proxy

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"

	"kiro-proxy/store"
)

// 三类 trace 回调可能并发或迟到，各自记录到达顺序，不能按下标拼成物理发送。
type upstreamUserAgentTrace struct {
	mu             sync.Mutex
	headerObserved bool
	userAgents     []string
	protocols      []string
	writeResults   []bool
}

func withUpstreamUserAgentTrace(ctx context.Context) (context.Context, *upstreamUserAgentTrace) {
	observation := &upstreamUserAgentTrace{}
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			protocol := ""
			if conn, ok := info.Conn.(*tls.Conn); ok {
				protocol = conn.ConnectionState().NegotiatedProtocol
			}
			observation.mu.Lock()
			observation.protocols = append(observation.protocols, protocol)
			observation.mu.Unlock()
		},
		WroteHeaderField: func(name string, values []string) {
			if !strings.EqualFold(name, "User-Agent") {
				return
			}
			observation.mu.Lock()
			observation.headerObserved = true
			observation.userAgents = append(observation.userAgents, values...)
			observation.mu.Unlock()
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			observation.mu.Lock()
			observation.writeResults = append(observation.writeResults, info.Err == nil)
			observation.mu.Unlock()
		},
	}
	return httptrace.WithClientTrace(ctx, trace), observation
}

func (o *upstreamUserAgentTrace) log(logger *TrafficLogger, model *store.Model, upstreamURL, traceID string, attempt int, resp *http.Response, requestErr error) {
	if logger == nil {
		return
	}
	o.mu.Lock()
	headerObserved := o.headerObserved
	userAgents := append([]string{}, o.userAgents...)
	protocols := append([]string{}, o.protocols...)
	writeResults := append([]bool{}, o.writeResults...)
	o.mu.Unlock()
	metadata := map[string]interface{}{
		"trace_id":             traceID,
		"attempt":              attempt,
		"ua_header_observed":   headerObserved,
		"encoded_user_agents":  userAgents,
		"negotiated_protocols": protocols,
		"write_results":        writeResults,
		"request_error":        requestErr != nil,
	}
	if parsed, err := url.Parse(upstreamURL); err == nil {
		metadata["original_upstream_host"] = parsed.Host
		metadata["original_endpoint"] = parsed.Path
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
		metadata["response_protocol"] = resp.Proto
		if resp.Request != nil && resp.Request.URL != nil {
			metadata["response_host"] = resp.Request.URL.Host
			metadata["response_endpoint"] = resp.Request.URL.Path
		}
		requestID := resp.Header.Get("X-Request-Id")
		if requestID == "" {
			requestID = resp.Header.Get("Request-Id")
		}
		metadata["upstream_request_id"] = requestID
	}
	message := "上游 UA 编码观测: 未观察到 UA 编码事件"
	if headerObserved {
		message = "上游 UA 编码观测: " + strings.Join(userAgents, "; ")
	}
	if requestID, ok := metadata["upstream_request_id"].(string); ok && requestID != "" {
		message += " (final_response_request_id=" + requestID + ")"
	}
	logger.InfoWithMeta("upstream_identity", message, LogMeta{
		Model: statsModelID(model), Status: status, Metadata: metadata,
	})
}
