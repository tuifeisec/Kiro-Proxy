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
	"strings"
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
	AutoReasoningEffort  *string // Auto 的固定有效等级；空字符串也覆盖客户端值。
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
		// 代理私有阶段信息必须留在本地，capture/passthrough 同样不能泄漏。
		workflowHeaders := req.Header.Values(converter.WorkflowStageHeader)
		req.Header.Del(converter.WorkflowStageHeader)
		if len(workflowHeaders) > 0 {
			req = req.WithContext(withWorkflowStageHeaders(req.Context(), workflowHeaders))
		}
		host := req.URL.Hostname()
		if host == "" {
			host = req.Host
		}
		path := req.URL.Path

		// 所有经过代理的请求都记录
		s.logger.Debug("proxy", fmt.Sprintf("%s %s%s", req.Method, host, path))

		protocol := router.DetectProtocol(host, path)
		var preReadBody []byte
		if protocol == "kiro-runtime-responses" {
			// 手选本地模型不依赖阶段桥接；Auto 仍只接管带头候选。
			if req.Method != http.MethodPost {
				protocol = "kiro-passthrough"
			} else {
				var err error
				preReadBody, err = readAndRestoreMITMRequestBody(req)
				if err != nil {
					return req, errorResponse(req, requestBodyErrorStatus(err), "invalid request body")
				}
				clean, err := stripRawWorkflowMetadata(preReadBody)
				if err != nil && !IsCaptureMode() {
					return req, errorResponse(req, http.StatusBadRequest, "invalid raw Responses body")
				}
				if err == nil {
					req.Body = io.NopCloser(bytes.NewReader(clean))
					req.ContentLength = int64(len(clean))
					req.Header.Set("Content-Length", fmt.Sprint(len(clean)))
				}
				var model struct {
					Model string `json:"model"`
				}
				if IsCaptureMode() {
					// 抓包只观察，不以生成协议的解析规则拒绝原请求。
					protocol = "kiro-passthrough"
				} else if json.Unmarshal(preReadBody, &model) != nil {
					return req, errorResponse(req, http.StatusBadRequest, "invalid raw Responses model")
				} else {
					isAuto := strings.EqualFold(model.Model, "auto")
					if isAuto && len(workflowHeaders) == 0 || !isAuto && !s.router.HasConfiguredTarget(model.Model) {
						if !isAuto {
							// 原厂模型仍需清理可关联执行域，本地模型在路由时清理。
							if conv, err := converter.ParseKiroRawResponsesManualRouting(preReadBody); err == nil {
								s.router.ForgetAuto(conv)
							}
						}
						protocol = "kiro-passthrough"
					}
				}
			}
		}
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

		if resp, handled := s.tryTypesafeClassifier(req, pre); handled {
			return req, resp
		}

		// 用户选择不使用 Classifier 时：不调用上游/官方，直接返回 chat 分类结果
		if pre.IsClassifier && s.isClassifierDisabled() {
			s.logger.Info("interceptor", "用户设置不使用 classifier，返回 chat 分类结果")
			return req, s.buildClassifierChatResponse(req, "classifier-disabled")
		}

		// 彩蛋模型 GPT-8.8：不调用上游，也不调用 classifier
		if s.isEasterEggClassifier(pre) {
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
			route, err = s.resolveMainRoute(pre)
			if err != nil {
				s.logger.Error("router", "Auto 阶段路由失败: "+err.Error())
				return req, errorResponse(req, http.StatusServiceUnavailable, err.Error())
			}
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

		if route.Group != nil {
			s.logger.InfoWithMeta("router", fmt.Sprintf("路由命中: %s → 分组 %s", pre.SourceModel, route.Group.Name), LogMeta{
				Metadata: map[string]interface{}{
					"source_model": pre.SourceModel, "protocol": pre.Protocol,
					"classifier": pre.IsClassifier, "conversation_id": pre.ConversationID,
					"auto_route_reason": route.AutoReason,
					"target_group_id":   route.Group.ID, "target_group_name": route.Group.Name,
				},
			})
		} else {
			s.logger.InfoWithMeta("router", fmt.Sprintf("路由命中: %s → %s (%s)", pre.SourceModel, route.Model.ModelID, route.Model.BaseURL), LogMeta{
				Model: statsModelID(route.Model),
				Metadata: map[string]interface{}{
					"source_model":             pre.SourceModel,
					"provider":                 route.Model.Provider,
					"base_url":                 route.Model.BaseURL,
					"endpoint":                 route.Model.Endpoint,
					"protocol":                 pre.Protocol,
					"classifier":               pre.IsClassifier,
					"conversation_id":          pre.ConversationID,
					"auto_route_reason":        route.AutoReason,
					"selected_model_config_id": route.Model.ID,
				},
			})
			s.logAutoRoute(pre, route)
		}
		usageModel := route.Model
		if route.Group != nil {
			usageModel = nil
		}
		pre.usage = newRequestUsageRecorder(s.db, time.Now(), usageModel, req.URL.Path, pre.Protocol)
		pre.usage.setRoute(route)
		pre.usage.setReasoningEffort(resolveReasoningEffort(pre, route.Model))

		// 转发
		var resp *http.Response
		if pre.Protocol == "kiro-runtime-responses" {
			resp, err = s.forwarder.ForwardRawResponses(req, pre, route, s.logger)
		} else {
			resp, err = s.forwarder.Forward(req, pre, route, s.logger)
		}
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
			if pre.Protocol == "kiro-runtime-responses" {
				return req, errorResponse(req, http.StatusBadGateway, err.Error())
			}
			return req, buildKiroExceptionStreamResponse(
				req,
				"upstream_forward_failed",
				"模型转发失败："+err.Error(),
			)
		}
		if route.Group != nil {
			s.router.RecordAutoDispatch(pre.Conv, route)
			s.logAutoRoute(pre, route)
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
	if protocol == "kiro-runtime-responses" {
		var model struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(pre.Body, &model); err != nil {
			return nil, err
		}
		if !strings.EqualFold(model.Model, "auto") {
			conv, err := converter.ParseKiroRawResponsesManualRouting(pre.Body)
			if err != nil {
				return nil, err
			}
			pre.Conv, pre.SourceModel, pre.ConversationID = conv, conv.ModelID, conv.ConversationID
			return pre, nil
		}
		conv, err := converter.ParseKiroRawResponsesRouting(pre.Body)
		if err != nil {
			return nil, err
		}
		values := workflowStageHeaders(req)
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return nil, fmt.Errorf("raw Responses requires one nonempty workflow header")
		}
		if err := converter.ApplyWorkflowStageHeader(conv, values[0]); err != nil {
			return nil, err
		}
		pre.Conv, pre.SourceModel, pre.ConversationID = conv, conv.ModelID, conv.ConversationID
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
		if !pre.IsClassifier && strings.EqualFold(conv.ModelID, "auto") {
			values := workflowStageHeaders(req)
			if len(values) > 1 || (len(values) == 1 && strings.TrimSpace(values[0]) == "") {
				return nil, fmt.Errorf("workflow header must contain one nonempty value")
			}
			if len(values) == 1 {
				if err := converter.ApplyWorkflowStageHeader(conv, values[0]); err != nil {
					return nil, err
				}
			}
		}
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

// resolveMainRoute 仅对 Kiro 的 Auto 主请求执行阶段策略。
func (s *Server) resolveMainRoute(pre *Preflight) (*router.Route, error) {
	if pre.Protocol == "kiro-runtime-responses" && pre.Conv != nil && strings.EqualFold(pre.SourceModel, "auto") {
		route, err := s.router.ResolveStageAuto(pre.Conv)
		return bindAutoReasoningEffort(pre, route, err)
	}
	if pre.Protocol == "kiro" && pre.Conv != nil && strings.EqualFold(pre.SourceModel, "auto") {
		route, err := s.router.ResolveAuto(pre.Conv)
		return bindAutoReasoningEffort(pre, route, err)
	}
	if pre.Conv != nil && !pre.IsClassifier {
		s.router.ForgetAuto(pre.Conv)
	}
	if pre.Protocol == "kiro-runtime-responses" {
		return s.router.ResolveConfigured(pre.SourceModel), nil
	}
	return s.router.Resolve(pre.SourceModel), nil
}

func bindAutoReasoningEffort(pre *Preflight, route *router.Route, err error) (*router.Route, error) {
	if err == nil && pre != nil && !pre.IsClassifier && route != nil && route.AutoEffortFixed {
		effort := route.AutoEffort
		pre.AutoReasoningEffort = &effort
	}
	return route, err
}

// logAutoRoute 只记录结构化选模依据，不将提示正文加入理由。
func (s *Server) logAutoRoute(pre *Preflight, route *router.Route) {
	if route == nil || route.Model == nil || pre == nil || route.AutoReason == "" || pre.Conv == nil {
		return
	}
	metadata := map[string]interface{}{
		"requested_model":           pre.SourceModel,
		"auto_route_reason":         route.AutoReason,
		"selected_model_config_id":  route.Model.ID,
		"auto_mode":                 route.AutoMode,
		"workflow_stage":            pre.Conv.WorkflowStage,
		"workflow_stage_source":     pre.Conv.WorkflowStageSource,
		"workflow_stage_status":     workflowStageStatus(pre.Conv),
		"selected_stage":            route.AutoStage,
		"outbound_model":            route.Model.ModelID,
		"channel_identity":          route.AutoChannelIdentity,
		"workflow_metadata_version": pre.Conv.WorkflowMetadataVersion,
		"workflow_id":               pre.Conv.WorkflowID,
		"conversation_id":           pre.Conv.ConversationID,
		"definition_id":             pre.Conv.WorkflowDefinitionID,
		"definition_revision":       pre.Conv.WorkflowDefinitionRevision,
		"node_path":                 pre.Conv.WorkflowNodePath,
		"node_id":                   pre.Conv.WorkflowNodeID,
		"task_type":                 pre.Conv.WorkflowTaskType,
		"selected_duty":             route.AutoDuty,
		"selection_source":          route.AutoSelectionSource,
		"unknown_reason":            route.AutoUnknownReason,
		"execution_id":              route.AutoExecutionID,
		"lock_scope":                route.AutoLockScope,
		"requested_effort":          pre.Conv.ReasoningEffort,
		"effective_effort":          resolveReasoningEffort(pre, route.Model),
		"effort_fixed":              route.AutoEffortFixed,
		"effort_conflict":           route.AutoEffortFixed && pre.Conv.ReasoningEffort != "" && pre.Conv.ReasoningEffort != route.AutoEffort,
		"agent_task_type":           pre.Conv.AgentTaskType,
		"agent_mode":                pre.Conv.AgentMode,
		"chat_trigger_type":         pre.Conv.ChatTriggerType,
		"continuation_state_key":    pre.Conv.ContinuationStateKey,
	}
	if route.Group != nil {
		metadata["target_group_id"] = route.Group.ID
		metadata["target_group_name"] = route.Group.Name
	}
	s.logger.InfoWithMeta("auto-routing", "Auto 选模: "+route.AutoReason+" → "+route.Model.ModelID, LogMeta{
		Model:    statsModelID(route.Model),
		Metadata: metadata,
	})
}

// 主会话和独立 agent 分支分别记忆 classifier 跟随的实际模型。
func classifierMemoryKey(pre *Preflight) string {
	if pre.Conv != nil && pre.Conv.ContinuationStateKey != "" {
		return pre.Conv.ContinuationStateKey
	}
	return pre.ConversationID
}

func (s *Server) resolveClassifierRoute(pre *Preflight) *router.Route {
	if route := s.router.ResolveClassifier(); route != nil {
		return route
	}

	if model := s.mainModelForConversation(classifierMemoryKey(pre)); model != "" {
		if strings.EqualFold(model, "auto") {
			return s.router.Resolve(model)
		}
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

	if strings.EqualFold(pre.SourceModel, "auto") && (route.AutoReason == "" || route.AutoReason == "policy_disabled") {
		// 未启用阶段策略的兼容路由保留分组；阶段策略跟随已选实际模型。
		s.rememberModelName(pre, "auto")
		return
	}
	s.rememberModelName(pre, route.Model.ID)
}

func (s *Server) rememberEasterEggModel(pre *Preflight) {
	if s.router != nil && pre != nil && pre.Conv != nil {
		s.router.ForgetAuto(pre.Conv)
	}
	s.rememberModelName(pre, "GPT-8.8")
}

func (s *Server) isEasterEggClassifier(pre *Preflight) bool {
	return pre.IsClassifier && (pre.SourceModel == "GPT-8.8" || s.mainModelForConversation(classifierMemoryKey(pre)) == "GPT-8.8")
}

func (s *Server) rememberModelName(pre *Preflight, modelName string) {
	if pre == nil || modelName == "" {
		return
	}

	s.classifierMu.Lock()
	defer s.classifierMu.Unlock()

	memoryKey := classifierMemoryKey(pre)
	if memoryKey != "" {
		now := time.Now()
		if s.conversationPrunedAt.IsZero() || now.Sub(s.conversationPrunedAt) >= conversationPruneInterval || len(s.conversationModels) >= conversationModelMaxItems {
			s.pruneConversationModelsLocked(now)
			s.conversationPrunedAt = now
		}
		if _, exists := s.conversationModels[memoryKey]; !exists && len(s.conversationModels) >= conversationModelMaxItems {
			s.evictOldestConversationModelLocked()
		}
		s.conversationModels[memoryKey] = conversationModelEntry{modelName: modelName, lastSeen: now}
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
		return ""
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
