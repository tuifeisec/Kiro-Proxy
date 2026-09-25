// interceptor.go
// 请求拦截：一次读取 body，一次解析，决定路由，交给 forwarder

package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"net/http"
	"time"

	json "github.com/goccy/go-json"

	"github.com/elazarl/goproxy"
)

const (
	mitmRequestBodyLimit      = 32 << 20
	conversationModelTTL      = 24 * time.Hour
	conversationModelMaxItems = 4096
	conversationPruneInterval = 5 * time.Minute
)

type conversationModelEntry struct {
	modelName string
	lastSeen  time.Time
}

var errMITMRequestBodyTooLarge = errors.New("MITM request body too large")

// Preflight 一次解析的结果，贯穿整个请求生命周期
type Preflight struct {
	Body                 []byte
	Conv                 *converter.ParsedConversation // nil 表示非 Kiro 请求
	Protocol             string
	SourceModel          string
	IsClassifier         bool
	ConversationID       string
	StartTime            time.Time
	GroupReasoningEffort string
	usage                *requestUsageRecorder
}

// isKiroConversationBody 检查 runtime 根路径请求是否具有对话操作的稳定结构。
func isKiroConversationBody(body []byte) bool {
	if len(body) == 0 {
		return false
	}

	var payload struct {
		ConversationState *struct {
			CurrentMessage *struct {
				UserInputMessage *json.RawMessage `json:"userInputMessage"`
			} `json:"currentMessage"`
		} `json:"conversationState"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload.ConversationState != nil &&
		payload.ConversationState.CurrentMessage != nil &&
		payload.ConversationState.CurrentMessage.UserInputMessage != nil
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
		var preReadBody []byte
		if protocol == "kiro-runtime-root" {
			if req.Method == http.MethodPost {
				var err error
				preReadBody, err = readAndRestoreMITMRequestBody(req)
				if err != nil {
					s.logger.Error("interceptor", "读取请求体失败: "+err.Error())
					return req, errorResponse(req, requestBodyErrorStatus(err), "invalid request body")
				}
			}
			if req.Method == http.MethodPost && isKiroConversationBody(preReadBody) {
				protocol = "kiro"
			} else {
				protocol = "kiro-passthrough"
			}
		}
		if protocol == "" {
			return req, nil // 非目标请求，直接放行
		}

		s.logger.Info("interceptor", fmt.Sprintf("拦截 [%s]: %s %s%s", protocol, req.Method, host, path))

		// 抓包模式：只记录不转发，放行到原始服务器
		if IsCaptureMode() {
			// 始终记录请求详情；有 body 时读取后恢复，避免影响后续放行。
			body := preReadBody
			if body == nil && req.Body != nil {
				var err error
				body, err = readAndRestoreMITMRequestBody(req)
				if err != nil {
					s.logger.Error("capture", "读取请求体失败: "+err.Error())
					return req, errorResponse(req, requestBodyErrorStatus(err), "invalid request body")
				}
			}
			s.logger.Debug("capture", fmt.Sprintf("[请求] %s %s%s", req.Method, host, path), formatCaptureRequestDetail(req, protocol, body))
			return req, nil // 放行
		}

		// 特殊协议
		if protocol == "kiro-management-root" {
			// 新版 management 根路径：需验证 POST + X-Amz-Target
			if req.Method == http.MethodPost && req.Header.Get("X-Amz-Target") == "KiroControlPlaneBearerService.ListAvailableModels" {
				s.logger.Info("interceptor", "返回模型列表")
				resp := s.buildModelsResponse(req)
				if resp != nil && resp.Body != nil {
					respBody, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					resp.Body = io.NopCloser(bytes.NewReader(respBody))
					s.logger.Debug("interceptor", "[kiro-models] 伪造响应体", string(respBody))
				}
				return req, resp
			}
			// 不匹配时放行
			s.logger.Debug("interceptor", fmt.Sprintf("management 根路径不匹配模型列表 operation，放行: method=%s, target=%s", req.Method, req.Header.Get("X-Amz-Target")))
			return req, nil
		}
		if protocol == "kiro-models" {
			// 旧版 AWS path：保持兼容
			s.logger.Info("interceptor", "返回模型列表")
			resp := s.buildModelsResponse(req)
			if resp != nil && resp.Body != nil {
				respBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(respBody))
				s.logger.Debug("interceptor", "[kiro-models] 伪造响应体", string(respBody))
			}
			return req, resp
		}
		if protocol == "kiro-usage-limits" {
			s.logger.Info("interceptor", "返回用量限制")
			resp := s.buildUsageLimitsResponse(req)
			if resp != nil && resp.Body != nil {
				respBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(respBody))
				s.logger.Debug("interceptor", "[kiro-usage-limits] 伪造响应体", string(respBody))
			}
			return req, resp
		}
		if protocol == "kiro-passthrough" {
			// 记录 passthrough 请求体
			body := preReadBody
			if body == nil && req.Body != nil {
				var err error
				body, err = readAndRestoreMITMRequestBody(req)
				if err != nil {
					s.logger.Error("interceptor", "读取 passthrough 请求体失败: "+err.Error())
					return req, errorResponse(req, requestBodyErrorStatus(err), "invalid request body")
				}
			}
			if len(body) > 0 {
				s.logger.Debug("interceptor", fmt.Sprintf("[kiro-passthrough] 请求体: %s %s%s", req.Method, host, path), string(body))
			}
			s.logger.Debug("interceptor", fmt.Sprintf("放行到原始服务器: %s%s", host, path))
			return req, nil // 不拦截，让请求正常发到 AWS
		}

		// 一次性读取 body
		pre, err := s.parseRequest(req, protocol, preReadBody)
		if err != nil {
			s.logger.Error("interceptor", "解析请求失败: "+err.Error())
			return req, errorResponse(req, requestBodyErrorStatus(err), "invalid request body")
		}

		s.logger.InfoBytes("interceptor", fmt.Sprintf("解析完成: model=%s, classifier=%v, convId=%s",
			pre.SourceModel, pre.IsClassifier, pre.ConversationID), pre.Body)

		// 用户选择不使用 Classifier 时：不调用上游/官方，直接返回 chat 分类结果
		if pre.IsClassifier && s.isClassifierDisabled() {
			s.logger.Info("interceptor", "用户设置不使用 classifier，返回 chat 分类结果")
			return req, s.buildClassifierChatResponse(req, "classifier-disabled")
		}

		// 彩蛋模型 GPT-8.8：不调用上游，也不调用 classifier
		if pre.IsClassifier && (pre.SourceModel == "GPT-8.8" || s.mainModelForConversation(pre.ConversationID) == "GPT-8.8") {
			s.logger.Info("interceptor", "彩蛋模型跳过 classifier，返回伪造分类结果")
			return req, s.buildEasterEggClassifierResponse(req)
		}
		if pre.SourceModel == "GPT-8.8" {
			s.rememberEasterEggModel(pre)
			return req, s.buildEasterEggResponse(req)
		}

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
		if !pre.IsClassifier && route.Group != nil {
			pre.GroupReasoningEffort = route.Group.ReasoningEffort
		}
		if result, err := s.licenseClient.CheckCached(); err != nil || !result.Authorized {
			msg := "未授权，请加入群获取免费授权并在后台添加机器码"
			if err != nil {
				msg = "授权检测失败: " + err.Error()
			} else if result.Tampered {
				msg = "程序防篡改校验失败，请重新下载官方版本"
			}
			s.logger.Error("license", msg)
			return req, errorResponse(req, http.StatusForbidden, msg)
		}

		s.logger.InfoWithMeta("router", fmt.Sprintf("路由命中: %s → %s (%s)", pre.SourceModel, route.Model.ModelID, route.Model.BaseURL), LogMeta{
			Model: statsModelID(route.Model),
			Metadata: map[string]interface{}{
				"source_model":    pre.SourceModel,
				"provider":        route.Model.Provider,
				"base_url":        route.Model.BaseURL,
				"endpoint":        route.Model.Endpoint,
				"protocol":        pre.Protocol,
				"classifier":      pre.IsClassifier,
				"conversation_id": pre.ConversationID,
			},
		})
		pre.usage = newRequestUsageRecorder(s.db, time.Now(), route.Model, req.URL.Path, pre.Protocol)
		pre.usage.setRoute(route)
		pre.usage.setReasoningEffort(resolveReasoningEffort(pre, route.Model))

		// 转发
		resp, err := s.forwarder.Forward(req, pre, route, s.logger)
		if err != nil {
			pre.usage.finish(http.StatusBadGateway, localCompatUsage{}, err.Error())
			s.logger.ErrorWithMeta("forwarder", "转发失败: "+err.Error(), LogMeta{
				Model:     statsModelID(route.Model),
				LatencyMS: time.Since(pre.StartTime).Milliseconds(),
				Metadata: map[string]interface{}{
					"protocol":        pre.Protocol,
					"classifier":      pre.IsClassifier,
					"conversation_id": pre.ConversationID,
				},
			})
			s.stats.RecordRequestFull(0, 0, 0, 0, statsModelID(route.Model), true)
			return req, buildKiroExceptionStreamResponse(
				req,
				"upstream_forward_failed",
				"模型转发失败："+err.Error(),
			)
		}
		s.rememberMainModel(pre, route)

		s.logger.InfoWithMeta("forwarder", fmt.Sprintf("上游响应: status=%d", resp.StatusCode), LogMeta{
			Model:     statsModelID(route.Model),
			Status:    resp.StatusCode,
			LatencyMS: time.Since(pre.StartTime).Milliseconds(),
			Metadata: map[string]interface{}{
				"protocol":        pre.Protocol,
				"classifier":      pre.IsClassifier,
				"conversation_id": pre.ConversationID,
			},
		}, formatUpstreamResponseDetail(resp))
		return req, resp
	})
}

// parseRequest 一次性读取并解析请求
func (s *Server) parseRequest(req *http.Request, protocol string, preReadBody []byte) (*Preflight, error) {
	pre := &Preflight{
		Protocol:  protocol,
		StartTime: time.Now(),
	}

	// 读取 body
	if preReadBody != nil {
		pre.Body = preReadBody
	} else if req.Body != nil {
		body, err := readAndRestoreMITMRequestBody(req)
		if err != nil {
			return nil, err
		}
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

func readAndRestoreMITMRequestBody(req *http.Request) ([]byte, error) {
	return readAndRestoreMITMRequestBodyWithLimit(req, mitmRequestBodyLimit)
}

func readAndRestoreMITMRequestBodyWithLimit(req *http.Request, limit int64) ([]byte, error) {
	if req == nil || req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, limit+1))
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return body, err
	}
	if int64(len(body)) > limit {
		return body, errMITMRequestBodyTooLarge
	}
	return body, nil
}

func requestBodyErrorStatus(err error) int {
	if errors.Is(err, errMITMRequestBodyTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
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

		// 在下游实际读取时旁路采集，禁止提前消费上游流。
		detail := captureResponseMetadata(ctx.Req, resp, protocol)
		contentType := resp.Header.Get("Content-Type")
		message := fmt.Sprintf("[响应] status=%d %s%s", resp.StatusCode, host, ctx.Req.URL.Path)
		limit := s.logger.DetailLimit()
		if limit <= 0 || limit > maxFullLogDetailBytes {
			limit = maxFullLogDetailBytes
		}
		complete := func(result capturedResponse) {
			detail["body"] = captureBody(result.Body, contentType)
			detail["observed_bytes"] = result.Total
			detail["captured_bytes"] = len(result.Body)
			detail["truncated"] = result.Total > int64(len(result.Body))
			detail["completion"] = result.Completion
			if result.Err != nil {
				detail["read_error"] = result.Err.Error()
			}
			level := "debug"
			if result.Err != nil {
				level = "warn"
			}
			s.logger.LogWithMeta(level, "capture", message, LogMeta{Metadata: map[string]interface{}{
				"observed_bytes": result.Total, "captured_bytes": len(result.Body),
				"truncated": result.Total > int64(len(result.Body)), "completion": result.Completion,
			}}, marshalCaptureDetail(detail))
		}
		if resp.Body != nil {
			resp.Body = newCaptureObserver(resp.Body, limit, complete)
		} else {
			complete(capturedResponse{Completion: "eof"})
		}
		return resp
	})
}

func (s *Server) resolveClassifierRoute(pre *Preflight) *router.Route {
	if route := s.router.ResolveClassifier(); route != nil {
		return route
	}

	if model := s.mainModelForConversation(pre.ConversationID); model != "" {
		if route := s.router.ResolveByID(model); route != nil {
			return route
		}
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

	s.rememberModelName(pre, route.Model.ID)
}

func (s *Server) rememberEasterEggModel(pre *Preflight) {
	s.rememberModelName(pre, "GPT-8.8")
}

func (s *Server) rememberModelName(pre *Preflight, modelName string) {
	if pre == nil || modelName == "" {
		return
	}

	s.classifierMu.Lock()
	defer s.classifierMu.Unlock()

	if pre.ConversationID != "" {
		now := time.Now()
		if s.conversationPrunedAt.IsZero() || now.Sub(s.conversationPrunedAt) >= conversationPruneInterval || len(s.conversationModels) >= conversationModelMaxItems {
			s.pruneConversationModelsLocked(now)
			s.conversationPrunedAt = now
		}
		if _, exists := s.conversationModels[pre.ConversationID]; !exists && len(s.conversationModels) >= conversationModelMaxItems {
			s.evictOldestConversationModelLocked()
		}
		s.conversationModels[pre.ConversationID] = conversationModelEntry{modelName: modelName, lastSeen: now}
	}
	s.lastMainModel = modelName
}

func (s *Server) mainModelForConversation(conversationID string) string {
	s.classifierMu.Lock()
	defer s.classifierMu.Unlock()

	if conversationID != "" {
		if entry, exists := s.conversationModels[conversationID]; exists {
			now := time.Now()
			if now.Sub(entry.lastSeen) <= conversationModelTTL {
				entry.lastSeen = now
				s.conversationModels[conversationID] = entry
				return entry.modelName
			}
			delete(s.conversationModels, conversationID)
		}
	}
	return s.lastMainModel
}

func (s *Server) pruneConversationModelsLocked(now time.Time) {
	for conversationID, entry := range s.conversationModels {
		if now.Sub(entry.lastSeen) > conversationModelTTL {
			delete(s.conversationModels, conversationID)
		}
	}
}

func (s *Server) evictOldestConversationModelLocked() {
	oldestID := ""
	var oldest time.Time
	for conversationID, entry := range s.conversationModels {
		if oldestID == "" || entry.lastSeen.Before(oldest) {
			oldestID = conversationID
			oldest = entry.lastSeen
		}
	}
	if oldestID != "" {
		delete(s.conversationModels, oldestID)
	}
}
