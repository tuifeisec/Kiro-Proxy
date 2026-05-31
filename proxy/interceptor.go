// interceptor.go
// 请求拦截：一次读取 body，一次解析，决定路由，交给 forwarder

package proxy

import (
	"bytes"
	"fmt"
	json "github.com/goccy/go-json"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"net/http"
	"strings"
	"time"

	"github.com/elazarl/goproxy"
)

// Preflight 一次解析的结果，贯穿整个请求生命周期
type Preflight struct {
	Body           []byte
	Conv           *converter.ParsedConversation // nil 表示非 Kiro 请求
	Protocol       string
	SourceModel    string
	IsClassifier   bool
	ConversationID string
	StartTime      time.Time
}

// setupInterceptor 注册请求拦截器
func (s *Server) setupInterceptor() {
	s.proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		host := req.URL.Hostname()
		if host == "" {
			host = req.Host
		}
		path := req.URL.Path

		// 所有经过代理的请求都记录
		s.logger.Debug("proxy", fmt.Sprintf("%s %s%s", req.Method, host, path))

		protocol := router.DetectProtocol(host, path)
		if protocol == "" {
			return req, nil // 非目标请求，直接放行
		}

		s.logger.Info("interceptor", fmt.Sprintf("拦截 [%s]: %s %s%s", protocol, req.Method, host, path))

		// 抓包模式：只记录不转发，放行到原始服务器
		if IsCaptureMode() {
			// 读取 body 记录到日志（不截断）
			if req.Body != nil {
				body, _ := io.ReadAll(req.Body)
				req.Body.Close()
				req.Body = io.NopCloser(bytes.NewReader(body))
				s.logger.Debug("capture", fmt.Sprintf("[请求] %s %s%s", req.Method, host, path), formatCaptureRequestDetail(req, protocol, body))
			}
			return req, nil // 放行
		}

		// 特殊协议
		if protocol == "kiro-models" {
			s.logger.Info("interceptor", "返回模型列表")
			return req, s.buildModelsResponse(req)
		}
		if protocol == "kiro-usage-limits" {
			s.logger.Info("interceptor", "返回用量限制")
			return req, s.buildUsageLimitsResponse(req)
		}
		if protocol == "kiro-passthrough" {
			s.logger.Debug("interceptor", fmt.Sprintf("放行到原始服务器: %s%s", host, path))
			return req, nil // 不拦截，让请求正常发到 AWS
		}

		// 一次性读取 body
		pre, err := s.parseRequest(req, protocol)
		if err != nil {
			s.logger.Error("interceptor", "解析请求失败: "+err.Error())
			return req, errorResponse(req, http.StatusBadRequest, "invalid request body")
		}

		s.logger.Info("interceptor", fmt.Sprintf("解析完成: model=%s, classifier=%v, convId=%s",
			pre.SourceModel, pre.IsClassifier, pre.ConversationID), string(pre.Body))

		var route *router.Route
		if pre.IsClassifier {
			route = s.resolveClassifierRoute(pre)
		} else {
			route = s.router.Resolve(pre.SourceModel)
		}
		if route == nil {
			s.logger.Error("interceptor", "无可用路由: model="+pre.SourceModel)
			return req, errorResponse(req, http.StatusServiceUnavailable, "no route for model: "+pre.SourceModel)
		}

		s.logger.Info("router", fmt.Sprintf("路由命中: %s → %s (%s)", pre.SourceModel, route.Model.ModelID, route.Model.BaseURL))
		s.rememberMainModel(pre, route)

		// 转发
		resp, err := s.forwarder.Forward(req, pre, route, s.logger)
		if err != nil {
			s.logger.Error("forwarder", "转发失败: "+err.Error())
			s.stats.RecordRequestFull(0, 0, 0, 0, statsModelID(route.Model), true)
			return req, errorResponse(req, http.StatusBadGateway, "upstream error: "+err.Error())
		}

		s.logger.Info("forwarder", fmt.Sprintf("上游响应: status=%d", resp.StatusCode), formatUpstreamResponseDetail(resp))
		return req, resp
	})
}

// parseRequest 一次性读取并解析请求
func (s *Server) parseRequest(req *http.Request, protocol string) (*Preflight, error) {
	pre := &Preflight{
		Protocol:  protocol,
		StartTime: time.Now(),
	}

	// 读取 body
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		pre.Body = body
	}

	if len(pre.Body) == 0 {
		return pre, nil
	}

	// Kiro 协议：结构化解析
	if protocol == "kiro" {
		conv, err := converter.ParseKiroRequest(pre.Body)
		if err != nil {
			return nil, err
		}
		pre.Conv = conv
		pre.SourceModel = conv.ModelID
		pre.IsClassifier = conv.IsClassifier || router.IsClassifierModel(conv.ModelID)
		conv.IsClassifier = pre.IsClassifier
		pre.ConversationID = conv.ConversationID
		return pre, nil
	}

	// 其他协议：只提取 model 字段
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(pre.Body, &parsed); err == nil {
		if modelRaw, ok := parsed["model"]; ok {
			var model string
			json.Unmarshal(modelRaw, &model)
			pre.SourceModel = model
		}
	}
	return pre, nil
}

// buildModelsResponse 构造 Kiro 模型列表响应（拦截 /ListAvailableModels）
// 严格对齐 AWS 原生响应格式，IDE 依赖这些字段展示模型下拉
//
// 关键逻辑：modelId 使用"显示名称"(Name)，而非"模型标识"(ModelID)。
// 这样 Kiro 下拉框里显示的是 Name，发送请求时也带 Name，路由通过 Name 匹配到
// 正确的渠道配置后，再把真正的 ModelID 发给上游。
// 好处：多个渠道可以使用相同的上游模型（如都是 gpt-5.5），只要显示名称不同就能区分路由。
func (s *Server) buildModelsResponse(req *http.Request) *http.Response {
	models := s.router.EnabledModels()

	entries := make([]map[string]interface{}, 0, len(models))
	var defaultEntry map[string]interface{}

	for i, m := range models {
		// 用 Name 作为 Kiro 看到的 modelId（下拉显示名称 = Kiro 发送的模型名）
		displayName := m.Name
		if displayName == "" {
			displayName = m.ModelID
		}
		entry := map[string]interface{}{
			"modelId":        displayName,
			"modelName":      displayName,
			"description":    m.Note,
			"rateMultiplier": m.RateMultiplier,
			"rateUnit":       "Credit",
			"promptCaching": map[string]interface{}{
				"maximumCacheCheckpointsPerRequest": 4,
			},
		}
		// 第一条作为默认模型
		if i == 0 {
			entry["isDefault"] = true
			clone := make(map[string]interface{}, len(entry))
			for k, v := range entry {
				clone[k] = v
			}
			defaultEntry = clone
		}
		entries = append(entries, entry)
	}

	resp := map[string]interface{}{
		"models":    entries,
		"nextToken": nil,
	}
	if defaultEntry != nil {
		resp["defaultModel"] = defaultEntry
	}

	body, _ := json.Marshal(resp)
	return goproxy.NewResponse(req, "application/json", http.StatusOK, string(body))
}

// buildUsageLimitsResponse 构造 Kiro 用量限制响应（读内存统计，不查数据库）
func (s *Server) buildUsageLimitsResponse(req *http.Request) *http.Response {
	snap := s.stats.Snapshot()
	body, _ := json.Marshal(map[string]interface{}{
		"quotaData": map[string]interface{}{
			"totalCredits":     99999,
			"usedCredits":      snap.InputTokens/1000 + snap.OutputTokens/1000,
			"remainingCredits": 99999,
		},
	})
	return goproxy.NewResponse(req, "application/json", http.StatusOK, string(body))
}

func errorResponse(req *http.Request, status int, msg string) *http.Response {
	return goproxy.NewResponse(req, goproxy.ContentTypeText, status, msg)
}

// setupCaptureResponse 注册响应拦截器（抓包模式下记录响应）
func (s *Server) setupCaptureResponse() {
	s.proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if !IsCaptureMode() || resp == nil || ctx.Req == nil {
			return resp
		}

		host := ctx.Req.URL.Hostname()
		if host == "" {
			host = ctx.Req.Host
		}
		protocol := router.DetectProtocol(host, ctx.Req.URL.Path)
		if protocol == "" {
			return resp
		}

		// 读取响应 body 记录
		if resp.Body != nil {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil {
				resp.Body = io.NopCloser(bytes.NewReader(body))
				s.logger.Debug("capture", fmt.Sprintf("[响应] status=%d %s%s", resp.StatusCode, host, ctx.Req.URL.Path), formatCaptureResponseDetail(ctx.Req, resp, protocol, body))
			}
		}
		return resp
	})
}

func formatCaptureRequestDetail(req *http.Request, protocol string, body []byte) string {
	return marshalCaptureDetail(map[string]interface{}{
		"type":         "request",
		"protocol":     protocol,
		"method":       req.Method,
		"url":          req.URL.String(),
		"host":         req.Host,
		"path":         req.URL.Path,
		"query":        req.URL.RawQuery,
		"headers":      captureHeaders(req.Header),
		"content_type": req.Header.Get("Content-Type"),
		"body":         captureBody(body, req.Header.Get("Content-Type")),
	})
}

func formatCaptureResponseDetail(req *http.Request, resp *http.Response, protocol string, body []byte) string {
	return marshalCaptureDetail(map[string]interface{}{
		"type":         "response",
		"protocol":     protocol,
		"method":       req.Method,
		"url":          req.URL.String(),
		"host":         req.Host,
		"path":         req.URL.Path,
		"status":       resp.StatusCode,
		"status_text":  resp.Status,
		"headers":      captureHeaders(resp.Header),
		"content_type": resp.Header.Get("Content-Type"),
		"body":         captureBody(body, resp.Header.Get("Content-Type")),
	})
}

func captureHeaders(headers http.Header) map[string][]string {
	out := make(map[string][]string, len(headers))
	for k, v := range headers {
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Proxy-Authorization") || strings.EqualFold(k, "x-api-key") || strings.EqualFold(k, "api-key") {
			out[k] = []string{"****"}
			continue
		}
		vv := make([]string, len(v))
		copy(vv, v)
		out[k] = vv
	}
	return out
}

func captureBody(body []byte, contentType string) interface{} {
	if len(body) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(string(body))
	if strings.Contains(strings.ToLower(contentType), "json") || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var parsed interface{}
		if err := json.Unmarshal(body, &parsed); err == nil {
			return parsed
		}
	}
	return string(body)
}

func marshalCaptureDetail(detail map[string]interface{}) string {
	b, err := json.Marshal(detail)
	if err != nil {
		return fmt.Sprintf("%v", detail)
	}
	return string(b)
}

func (s *Server) resolveClassifierRoute(pre *Preflight) *router.Route {
	if route := s.router.ResolveClassifier(); route != nil {
		return route
	}

	if model := s.mainModelForConversation(pre.ConversationID); model != "" {
		if route := s.router.ResolveByName(model); route != nil {
			return route
		}
		if route := s.router.Resolve(model); route != nil {
			return route
		}
	}

	if route := s.router.ResolveByName(pre.SourceModel); route != nil {
		return route
	}
	return s.router.Resolve(pre.SourceModel)
}

func (s *Server) rememberMainModel(pre *Preflight, route *router.Route) {
	if pre == nil || pre.IsClassifier || route == nil || route.Model == nil {
		return
	}

	modelName := route.Model.Name
	if modelName == "" {
		modelName = route.Model.ModelID
	}
	if modelName == "" {
		return
	}

	s.classifierMu.Lock()
	defer s.classifierMu.Unlock()

	if pre.ConversationID != "" {
		s.conversationModels[pre.ConversationID] = modelName
	}
	s.lastMainModel = modelName
}

func (s *Server) mainModelForConversation(conversationID string) string {
	s.classifierMu.RLock()
	defer s.classifierMu.RUnlock()

	if conversationID != "" {
		if model := s.conversationModels[conversationID]; model != "" {
			return model
		}
	}
	return s.lastMainModel
}
