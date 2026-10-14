package proxy

import (
	"strings"

	json "github.com/goccy/go-json"
)

// classifyRawResponsesTerminal observes protocol state without changing the
// response bytes. Diagnostic values are allowlisted; upstream messages may
// contain prompts or credentials and must never enter request_usage.
func classifyRawResponsesTerminal(eventType string, data []byte) (terminal, failure bool, diagnostic string) {
	var payload struct {
		Type              string          `json:"type"`
		Status            string          `json:"status"`
		Error             json.RawMessage `json:"error"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Response struct {
			Status            string          `json:"status"`
			Error             json.RawMessage `json:"error"`
			IncompleteDetails struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return false, false, ""
	}
	if payload.Type != "" && (!isSSEFailureEventType(eventType) || isSSEFailureEventType(payload.Type)) {
		eventType = payload.Type
	}
	status := payload.Status
	if payload.Response.Status != "" {
		status = payload.Response.Status
	}
	errRaw := payload.Error
	if !rawJSONNonEmpty(errRaw) {
		errRaw = payload.Response.Error
	}
	failure = rawJSONNonEmpty(errRaw) || status == "failed" || status == "incomplete"
	switch eventType {
	case "response.failed", "response.incomplete", "response.error", "error":
		failure = true
	case "response.completed":
		terminal = true
	}
	terminal = terminal || failure || status == "completed"
	if !failure {
		return terminal, false, ""
	}
	parts := make([]string, 0, 5)
	switch eventType {
	case "response.failed", "response.incomplete", "response.error", "error":
		parts = append(parts, "event="+eventType)
	default:
		switch status {
		case "failed", "incomplete":
			parts = append(parts, "status="+status)
		default:
			parts = append(parts, "event=error")
		}
	}
	var detail struct {
		Code  string `json:"code"`
		Type  string `json:"type"`
		Param string `json:"param"`
	}
	_ = json.Unmarshal(errRaw, &detail)
	if knownRawResponsesErrorType(detail.Type) {
		parts = append(parts, "type="+detail.Type)
	}
	if knownRawResponsesErrorCode(detail.Code) {
		parts = append(parts, "code="+detail.Code)
	}
	if knownRawResponsesErrorParam(detail.Param) {
		parts = append(parts, "param="+detail.Param)
	}
	reason := payload.IncompleteDetails.Reason
	if payload.Response.IncompleteDetails.Reason != "" {
		reason = payload.Response.IncompleteDetails.Reason
	}
	if reason == "max_output_tokens" || reason == "content_filter" {
		parts = append(parts, "reason="+reason)
	}
	return true, true, "raw Responses stream reported failure (" + strings.Join(parts, ", ") + ")"
}

func knownRawResponsesErrorType(value string) bool {
	switch value {
	case "invalid_request_error", "server_error", "rate_limit_error", "authentication_error", "permission_error", "not_found_error", "overloaded_error", "api_error":
		return true
	}
	return false
}

func knownRawResponsesErrorCode(value string) bool {
	switch value {
	case "unsupported_parameter", "invalid_parameter", "invalid_request", "invalid_request_error", "model_not_found", "context_length_exceeded", "rate_limit_exceeded", "insufficient_quota", "server_error", "internal_error", "invalid_api_key", "unsupported_value", "unsupported_model", "content_policy_violation", "upstream_stream_interrupted":
		return true
	}
	return false
}

func knownRawResponsesErrorParam(value string) bool {
	switch value {
	case "metadata", "model", "input", "instructions", "tools", "tool_choice", "reasoning", "reasoning.effort", "store", "stream", "max_output_tokens", "temperature", "top_p", "text", "text.format", "previous_response_id", "conversation", "prompt_cache_key":
		return true
	}
	return false
}
