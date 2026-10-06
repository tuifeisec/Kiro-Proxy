package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"kiro-proxy/converter"
	"kiro-proxy/license"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"kiro-proxy/typesafe"

	json "github.com/goccy/go-json"
)

// licenseChecker 让分类器的授权边界可在本地 Mock，不改变生产检查。
type licenseChecker interface {
	CheckCached() (license.CheckResult, error)
}

// tryTypesafeClassifier 只消费已识别的 Kiro classifier，不参与主模型选择。
func (s *Server) tryTypesafeClassifier(req *http.Request, pre *Preflight) (*http.Response, bool) {
	if IsCaptureMode() || pre == nil || pre.Protocol != "kiro" || !isTypesafeIntentClassifier(req, pre) {
		return nil, false
	}
	raw, err := s.db.GetSetting(store.TypesafeClassifierSetting)
	if err != nil {
		s.logger.Error("classifier", "读取 Typesafe 配置失败: "+err.Error())
		return errorResponse(req, http.StatusServiceUnavailable, "读取 Typesafe 配置失败"), true
	}
	config, err := store.ParseTypesafeClassifierConfig(raw)
	if err != nil {
		// 无效地址的解析错误可能带原配置值，先遮罩再记录。
		var invalidConfig store.TypesafeClassifierConfig
		_ = json.Unmarshal([]byte(raw), &invalidConfig)
		message := typesafeClassifierErrorSummary(err, invalidConfig)
		s.logger.Error("classifier", "Typesafe 配置无效: "+message)
		return errorResponse(req, http.StatusServiceUnavailable, "Typesafe 配置无效: "+message), true
	}
	if !config.Enabled || pre.SourceModel == "GPT-8.8" || s.mainModelForConversation(classifierMemoryKey(pre)) == "GPT-8.8" {
		return nil, false
	}
	if err := store.ValidateTypesafeClassifierConfig(config); err != nil {
		return errorResponse(req, http.StatusServiceUnavailable, "Typesafe 配置无效: "+err.Error()), true
	}
	if resp := s.requireTypesafeClassifierLicense(req); resp != nil {
		return resp, true
	}
	if pre.Conv == nil {
		return errorResponse(req, http.StatusBadRequest, "missing Kiro classifier conversation"), true
	}

	model := typesafeClassifierModel(config, config.Model)
	pre.usage = newRequestUsageRecorder(s.db, pre.StartTime, model, req.URL.Path, pre.Protocol)
	pre.usage.setModel(model)
	pre.usage.setRoute(&router.Route{RequestedName: pre.SourceModel, Model: model})
	pre.usage.setDispatchReason("classifier:typesafe")
	client, err := s.forwarder.clientForProxy(config.ProxyURL)
	var result typesafe.Result
	if err == nil {
		pre.usage.beginAttempt()
		result, err = typesafe.NewClient(client).Classify(pre.usage.traceContext(req.Context()), config, typesafeClassifierState(pre.Conv))
	}
	if result.Model != "" {
		model = typesafeClassifierModel(config, result.Model)
		pre.usage.setModel(model)
	}
	usage := localCompatUsage{}
	if result.Usage != nil {
		usage.InputTokens = result.Usage.InputTokens
		usage.OutputTokens = result.Usage.OutputTokens
	} else {
		pre.usage.setDispatchReason("classifier:typesafe;usage_unavailable")
	}
	status := http.StatusOK
	errorMessage := ""
	if err != nil {
		status = http.StatusBadGateway
		var httpErr *typesafe.HTTPError
		if errors.As(err, &httpErr) {
			status = httpErr.StatusCode
		} else if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		errorMessage = typesafeClassifierErrorSummary(err, config)
	} else {
		pre.usage.markFirstByte(time.Now())
	}
	pre.usage.finish(status, usage, errorMessage)
	credits := store.CalculateCredits(usage.InputTokens, usage.OutputTokens, 0, 0, model)
	s.stats.RecordRequestFullWithCredits(usage.InputTokens, usage.OutputTokens, 0, 0, credits, statsModelID(model), err != nil)
	metadata := map[string]interface{}{
		"provider": "typesafe", "classifier": true, "requested_model": pre.SourceModel,
		"actual_model": result.Model, "usage_available": result.Usage != nil,
		"usd_cost":        store.CalculateCreditBreakdown(usage.InputTokens, usage.OutputTokens, 0, 0, model).Total(),
		"dispatch_reason": "classifier:typesafe",
	}
	logMeta := LogMeta{Model: statsModelID(model), Status: status, InputTokens: int64(usage.InputTokens),
		OutputTokens: int64(usage.OutputTokens), Credits: credits, LatencyMS: time.Since(pre.StartTime).Milliseconds(), Metadata: metadata}
	if err != nil {
		metadata["error_kind"] = typesafeClassifierErrorKind(err)
		s.logger.ErrorWithMeta("classifier", "Typesafe 意图分类失败: "+errorMessage, logMeta)
		return errorResponse(req, status, errorMessage), true
	}
	metadata["intent"] = result.Choice
	metadata["probabilities"] = result.Probabilities
	metadata["confidence"] = result.Confidence
	s.logger.InfoWithMeta("classifier", "Typesafe 意图分类完成: "+result.Choice, logMeta)
	return buildTypesafeClassifierResponse(req, pre.ConversationID, result, credits), true
}

func isTypesafeIntentClassifier(req *http.Request, pre *Preflight) bool {
	mode := strings.TrimSpace(req.Header.Get("X-Amzn-Kiro-Agent-Mode"))
	if strings.EqualFold(mode, "session-title") {
		return false
	}
	if strings.EqualFold(mode, "intent-classification") {
		return true
	}
	if pre.Conv == nil {
		return false
	}
	// 与 Kiro 解析器相同的两个稳定标记；模型 ID 自身不证明请求用途。
	isClassifierPrompt := func(content string) bool {
		return strings.Contains(content, "intent classifier") && strings.Contains(content, "Respond ONLY with a raw JSON object")
	}
	if isClassifierPrompt(pre.Conv.CurrentContent) || isClassifierPrompt(pre.Conv.SystemText) {
		return true
	}
	for _, entry := range pre.Conv.History {
		if isClassifierPrompt(entry.UserContent) {
			return true
		}
	}
	return false
}

func (s *Server) requireTypesafeClassifierLicense(req *http.Request) *http.Response {
	if s.licenseClient == nil {
		return errorResponse(req, http.StatusForbidden, "授权检测不可用")
	}
	result, err := s.licenseClient.CheckCached()
	if err == nil && result.Authorized {
		return nil
	}
	message := "未授权，请加入群获取免费授权并在后台添加机器码"
	if err != nil {
		message = "授权检测失败: " + err.Error()
	} else if result.Tampered {
		message = "程序防篡改校验失败，请重新下载官方版本"
	}
	s.logger.Error("license", message)
	return errorResponse(req, http.StatusForbidden, message)
}

func typesafeClassifierModel(config store.TypesafeClassifierConfig, actualModel string) *store.Model {
	return &store.Model{ID: "typesafe-classifier", Name: "Typesafe classifier / " + actualModel,
		ModelID: actualModel, Provider: "typesafe", BaseURL: config.BaseURL, Endpoint: "/v1/systemone",
		InputPrice: config.InputPricePerMillion, RateMultiplier: 1}
}

// 仅传文本和角色结构，系统分类上下文单独保留；不猜测用户正文分隔符。
func typesafeClassifierState(conv *converter.ParsedConversation) map[string]interface{} {
	history := make([]map[string]interface{}, 0, len(conv.History)*2)
	for _, entry := range conv.History {
		if !entry.IsSystem && (entry.UserContent != "" || len(entry.ToolResults) > 0 || len(entry.Images) > 0) {
			history = append(history, map[string]interface{}{"role": "user", "content": entry.UserContent,
				"tool_results": typesafeToolTexts(entry.ToolResults), "has_images": hasNonEmptyImage(entry.Images)})
		}
		if entry.HasAssistant {
			tools := make([]map[string]string, 0, len(entry.ToolUses))
			for _, tool := range entry.ToolUses {
				tools = append(tools, map[string]string{"name": tool.Name, "arguments": string(tool.Input)})
			}
			history = append(history, map[string]interface{}{"role": "assistant", "content": entry.AssistantContent, "tool_uses": tools})
		}
	}
	return map[string]interface{}{
		"current_message": map[string]interface{}{"content": conv.CurrentContent,
			"tool_results": typesafeToolTexts(conv.CurrentToolResults), "has_images": hasNonEmptyImage(conv.CurrentImages)},
		"history": history, "classifier_context": conv.SystemText,
		"metadata": map[string]string{"agent_mode": conv.AgentMode, "agent_task_type": conv.AgentTaskType, "chat_trigger_type": conv.ChatTriggerType},
	}
}

func hasNonEmptyImage(images []converter.KiroImage) bool {
	for _, img := range images {
		if strings.TrimSpace(img.Source.Bytes) != "" {
			return true
		}
	}
	return false
}

func typesafeToolTexts(results []converter.ParsedToolResult) []map[string]interface{} {
	texts := make([]map[string]interface{}, 0, len(results))
	for _, result := range results {
		texts = append(texts, map[string]interface{}{"text": result.Content, "is_error": result.IsError})
	}
	return texts
}

func buildTypesafeClassifierResponse(req *http.Request, conversationID string, result typesafe.Result, credits float64) *http.Response {
	text, _ := json.Marshal(result.Probabilities) // 客户端已校验全部有限概率。
	body := converter.InitialResponseFrame(conversationID)
	body = append(body, converter.AssistantResponseFrame(string(text), result.Model)...)
	body = append(body, converter.BuildKiroEventFrame("messageMetadataEvent", map[string]interface{}{
		"tokenUsage": map[string]int{"uncachedInputTokens": result.Usage.InputTokens, "outputTokens": result.Usage.OutputTokens},
	})...)
	body = append(body, converter.MetadataStopFrame("END_TURN")...)
	body = append(body, converter.MeteringFrame(credits)...)
	return newKiroEventStreamResponse(req, io.NopCloser(bytes.NewReader(body)))
}

func typesafeClassifierErrorSummary(err error, config store.TypesafeClassifierConfig) string {
	message := err.Error()
	for _, secret := range []string{config.APIKey, config.ProxyURL, config.BaseURL} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
			if trimmed := strings.TrimSpace(secret); trimmed != "" {
				message = strings.ReplaceAll(message, trimmed, "[redacted]")
			}
		}
	}
	return sanitizeUsageError(message)
}

func typesafeClassifierErrorKind(err error) string {
	var httpErr *typesafe.HTTPError
	if errors.As(err, &httpErr) {
		return fmt.Sprintf("http_%d", httpErr.StatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "request_or_response"
}
