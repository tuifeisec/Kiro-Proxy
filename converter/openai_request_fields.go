package converter

import (
	"log"
	"strings"
)

// OpenAI Chat Completions 有两个回复长度字段：OpenAI 官方与 Azure OpenAI 的新模型
// 拒绝 max_tokens（"Use 'max_completion_tokens' instead"），DeepSeek、GLM 与多数
// 兼容中转只认 max_tokens。这里只做按 host 的静态规则；上游报错后的学习重试由
// 能力观测层负责，不在请求构建时猜测。
const (
	ChatMaxTokensField           = "max_tokens"
	ChatMaxCompletionTokensField = "max_completion_tokens"

	openAIOfficialHost  = "api.openai.com"
	azureOpenAIHostTail = ".openai.azure.com"
)

// ChatTokenLimitField 返回发往 upstreamHost 的 Chat 请求应使用的回复长度字段名。
// host 未知（空串）时按多数兼容上游使用 max_tokens。
func ChatTokenLimitField(upstreamHost string) string {
	host := strings.ToLower(strings.TrimSpace(upstreamHost))
	if host == openAIOfficialHost || strings.HasSuffix(host, azureOpenAIHostTail) {
		return ChatMaxCompletionTokensField
	}
	return ChatMaxTokensField
}

// chatUpstreamRejectsSampling 判断上游是否会拒绝非默认 temperature/top_p。
// 只覆盖 OpenAI 官方 host 上的推理模型：Azure 的部署名可任意命名，按模型名判断不可靠；
// 其他上游拒绝采样参数的情况交给运行时学习规则处理。
func chatUpstreamRejectsSampling(upstreamHost, model string) bool {
	if strings.ToLower(strings.TrimSpace(upstreamHost)) != openAIOfficialHost {
		return false
	}
	return isOpenAIReasoningModel(model)
}

// isOpenAIReasoningModel 识别 o1/o3/o4 系列与 gpt-5 系列（不含非推理的 gpt-5-chat）。
func isOpenAIReasoningModel(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	for _, family := range []string{"o1", "o3", "o4"} {
		if rest, ok := strings.CutPrefix(name, family); ok && (rest == "" || rest[0] == '-') {
			return true
		}
	}
	rest, ok := strings.CutPrefix(name, "gpt-5")
	if !ok || (rest != "" && rest[0] != '-' && rest[0] != '.') {
		return false
	}
	return !strings.HasPrefix(name, "gpt-5-chat")
}

// applyOpenAIChatGenerationFields 写入回复长度与采样参数。
// 推理模型丢弃采样参数时记录日志，保证这次改写可观测。
func applyOpenAIChatGenerationFields(req *OpenAIRequest, conv *ParsedConversation, model, upstreamHost string) {
	if conv.MaxTokens > 0 {
		if ChatTokenLimitField(upstreamHost) == ChatMaxCompletionTokensField {
			req.MaxCompletionTokens = conv.MaxTokens
		} else {
			req.MaxTokens = conv.MaxTokens
		}
	}
	if conv.Temperature == nil && conv.TopP == nil {
		return
	}
	if !chatUpstreamRejectsSampling(upstreamHost, model) {
		req.Temperature = conv.Temperature
		req.TopP = conv.TopP
		return
	}
	dropped := make([]string, 0, 2)
	if conv.Temperature != nil {
		dropped = append(dropped, "temperature")
	}
	if conv.TopP != nil {
		dropped = append(dropped, "top_p")
	}
	log.Printf("[kiro_to_openai] 上游 %s 的推理模型 %s 不接受采样参数，已省略 %s", upstreamHost, model, strings.Join(dropped, ","))
}
