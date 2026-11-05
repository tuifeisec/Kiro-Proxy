package proxy

import (
	"fmt"

	"kiro-proxy/converter"
	"kiro-proxy/store"
)

// applyRawResponsesAnthropicThinking writes thinking/output_config with the
// same builder as the Kiro path, so one model gets one thinking shape on both
// paths. request["max_tokens"] must already hold the value sent upstream: the
// budget form is checked against it instead of raising it.
func applyRawResponsesAnthropicThinking(request map[string]interface{}, model *store.Model, effort string) error {
	maxTokens, ok := request["max_tokens"].(int)
	if !ok {
		return fmt.Errorf("raw Responses Anthropic max_tokens is %T, want int", request["max_tokens"])
	}
	endpoint, err := rawResponsesCandidateURL(model)
	if err != nil {
		return err
	}
	params, err := converter.BuildAnthropicThinking(converter.AnthropicThinkingInput{
		ModelID:       model.ModelID,
		UpstreamHost:  converter.UpstreamHostOf(endpoint),
		ThinkingStyle: model.ThinkingStyle,
		Effort:        effort,
		MaxTokens:     maxTokens,
	})
	if err != nil {
		return fmt.Errorf("raw Responses reasoning effort %q for Anthropic: %w", effort, err)
	}
	if params.Thinking != nil {
		request["thinking"] = params.Thinking
	}
	if params.OutputConfig != nil {
		request["output_config"] = params.OutputConfig
	}
	return nil
}
