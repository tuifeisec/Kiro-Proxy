// interceptor.go
// 请求拦截：一次读取 body，一次解析，决定路由，交给 forwarder

package proxy

import (
	"bytes"
	"fmt"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"net/http"
	"time"

	json "github.com/goccy/go-json"

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
			// 始终记录请求详情；有 body 时读取后恢复，避免影响后续放行。
			var body []byte
			if req.Body != nil {
				body, _ = io.ReadAll(req.Body)
				req.Body.Close()
				req.Body = io.NopCloser(bytes.NewReader(body))
			}
			s.logger.Debug("capture", fmt.Sprintf("[请求] %s %s%s", req.Method, host, path), formatCaptureRequestDetail(req, protocol, body))
			return req, nil // 放行
		}

		// 特殊协议
		if protocol == "kiro-models" {
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
			if req.Body != nil {
				body, _ := io.ReadAll(req.Body)
				req.Body.Close()
				req.Body = io.NopCloser(bytes.NewReader(body))
				if len(body) > 0 {
					s.logger.Debug("interceptor", fmt.Sprintf("[kiro-passthrough] 请求体: %s %s%s", req.Method, host, path), string(body))
				}
			}
			s.logger.Debug("interceptor", fmt.Sprintf("放行到原始服务器: %s%s", host, path))
			return req, nil // 不拦截，让请求正常发到 AWS
		}

		// 一次性读取 body
		pre, err := s.parseRequest(req, protocol)
		if err != nil {
			s.logger.Error("interceptor", "解析请求失败: "+err.Error())
			return req, errorResponse(req, http.StatusBadRequest, "invalid request body")
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
		s.rememberMainModel(pre, route)

		// 转发
		resp, err := s.forwarder.Forward(req, pre, route, s.logger)
		if err != nil {
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
			return req, errorResponse(req, http.StatusBadGateway, "upstream error: "+err.Error())
		}

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

		// 读取响应 body 后恢复，确保详细日志能展示返回数据且不影响客户端读取。
		var body []byte
		if resp.Body != nil {
			var err error
			body, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				resp.Body = io.NopCloser(bytes.NewReader(nil))
				s.logger.Warn("capture", fmt.Sprintf("[响应] 读取失败 status=%d %s%s", resp.StatusCode, host, ctx.Req.URL.Path), err.Error())
				return resp
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
		s.logger.Debug("capture", fmt.Sprintf("[响应] status=%d %s%s", resp.StatusCode, host, ctx.Req.URL.Path), formatCaptureResponseDetail(ctx.Req, resp, protocol, body))
		return resp
	})
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
	s.rememberModelName(pre, modelName)
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
