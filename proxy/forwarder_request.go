package proxy

import (
	"fmt"
	"kiro-proxy/codex"
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"strings"

	json "github.com/goccy/go-json"
)

// useResponsesAPI 是请求/响应两侧唯一的"是否走 Responses API"判定口径。
// classifier（simple-task）请求必须走 chat/completions（上游返回可被分类器解析的纯文本），
// 绝不能走 responses。请求侧与响应侧必须使用同一判定，否则会出现请求按 chat 发、
// 响应却按 responses 解析（或反之）导致内容整段丢失。
func useResponsesAPI(pre *Preflight, m *store.Model) bool {
	if pre.Protocol != "kiro" || pre.Conv == nil {
		return false
	}
	if m.Provider == "anthropic" || m.Provider == "gemini" || m.Provider == "codex" {
		return false
	}
	if pre.Conv.IsClassifier {
		return false
	}
	return strings.Contains(m.Endpoint, "/responses")
}

// convertRequest 将请求体转换为上游格式
func convertRequest(pre *Preflight, m *store.Model) ([]byte, string, error) {
	baseURL := strings.TrimRight(m.BaseURL, "/")
	endpoint := strings.TrimSpace(m.Endpoint)
	if endpoint == "" {
		return nil, "", fmt.Errorf("接口端点不能为空，请在模型配置中完整填写端点路径")
	}
	upstreamURL := baseURL + endpoint

	if pre.Protocol != "kiro" || pre.Conv == nil {
		return replaceModelInBody(pre.Body, m.ModelID), upstreamURL, nil
	}

	reasoningEffort := strings.TrimSpace(m.ReasoningEffort)

	// Kiro 协议：根据 provider + endpoint 选择转换器
	var body []byte
	var err error
	switch {
	case m.Provider == "anthropic":
		body, err = converter.KiroToAnthropic(pre.Conv, m.ModelID, reasoningEffort)
	case m.Provider == "gemini":
		body, err = converter.KiroToGemini(pre.Conv, m.ModelID, reasoningEffort)
		// Gemini URL: 替换 {model} 占位符，追加 ?alt=sse
		upstreamURL = strings.Replace(upstreamURL, "{model}", m.ModelID, 1)
		if !strings.Contains(upstreamURL, "alt=sse") {
			if strings.Contains(upstreamURL, "?") {
				upstreamURL += "&alt=sse"
			} else {
				upstreamURL += "?alt=sse"
			}
		}
	case m.Provider == "codex":
		body, err = converter.KiroToResponses(pre.Conv, m.ModelID, reasoningEffort)
		if err == nil {
			body = codex.NewRequestBodyFromResponses(body, m.ServiceTier)
		}
	case useResponsesAPI(pre, m):
		body, err = converter.KiroToResponses(pre.Conv, m.ModelID, reasoningEffort)
	default: // chat/completions，或 classifier 请求
		if strings.Contains(endpoint, "/responses") {
			// 配置成 responses，但本次走 chat（classifier）：替换端点路径
			chatEndpoint := strings.Replace(endpoint, "/responses", "/chat/completions", 1)
			upstreamURL = baseURL + chatEndpoint
		}
		body, err = converter.KiroToOpenAI(pre.Conv, m.ModelID, reasoningEffort)
	}
	return body, upstreamURL, err
}

func replaceModelInBody(body []byte, newModel string) []byte {
	if newModel == "" || len(body) == 0 {
		return body
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body
	}
	parsed["model"] = newModel
	result, _ := json.Marshal(parsed)
	return result
}
