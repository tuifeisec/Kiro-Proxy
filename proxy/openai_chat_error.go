package proxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"kiro-proxy/store"
)

// openAIChatGatewayError keeps only the fields needed to classify an upstream
// gateway response. It intentionally does not retain response messages/bodies.
type openAIChatGatewayError struct {
	HTTPStatus         int
	Code               string
	NestedCode         string
	ExtErrorStatusCode int
	ExtErrorType       string
	ExtErrorCode       string
	RequestID          string
}

func (e openAIChatGatewayError) isDefinitiveOpenAIChatRequestError() bool {
	return isRetryableOpenAIChatStatus(e.HTTPStatus) &&
		e.ExtErrorStatusCode == http.StatusBadRequest &&
		e.ExtErrorType == "invalid_request_error"
}

func isRetryableOpenAIChatStatus(status int) bool {
	return status == http.StatusInternalServerError ||
		status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

// parseOpenAIChatGatewayError extracts structured gateway metadata without
// preserving potentially sensitive message text. The gateway sometimes puts a
// second JSON document in error.message; that form is parsed as well.
func parseOpenAIChatGatewayError(httpStatus int, headers http.Header, body []byte) openAIChatGatewayError {
	parsed := openAIChatGatewayError{
		HTTPStatus: httpStatus,
	}

	root, ok := decodeJSONObject(body)
	if !ok {
		parsed.RequestID = requestIDFromHeaders(headers)
		return parsed
	}

	applyGatewayFields(root, &parsed)

	var nestedMessages []json.RawMessage
	if errObject, ok := rawJSONObject(rootValue(root, "error")); ok {
		applyGatewayFields(errObject, &parsed)
		if message := rootValue(errObject, "message", "msg"); len(message) != 0 {
			nestedMessages = append(nestedMessages, message)
		}
	}
	if message := rootValue(root, "message", "msg"); len(message) != 0 {
		nestedMessages = append(nestedMessages, message)
	}

	for _, message := range nestedMessages {
		nested := unwrapJSONObject(message)
		if nested == nil {
			continue
		}
		if parsed.NestedCode == "" {
			parsed.NestedCode = rawScalarText(rootValue(nested, "code"))
		}
		applyGatewayFields(nested, &parsed)
		if errObject, ok := rawJSONObject(rootValue(nested, "error")); ok {
			applyGatewayFields(errObject, &parsed)
		}
	}

	if parsed.RequestID == "" {
		parsed.RequestID = requestIDFromHeaders(headers)
	}
	return parsed
}

func applyGatewayFields(fields map[string]json.RawMessage, parsed *openAIChatGatewayError) {
	if parsed.Code == "" {
		parsed.Code = rawScalarText(rootValue(fields, "code"))
	}
	if parsed.RequestID == "" {
		parsed.RequestID = rawString(rootValue(fields, "requestId", "request_id"))
	}
	if extError, ok := rawJSONObject(rootValue(fields, "extError", "ext_error")); ok {
		if parsed.ExtErrorStatusCode == 0 {
			parsed.ExtErrorStatusCode = rawInt(rootValue(extError, "StatusCode", "statusCode", "status_code"))
		}
		if parsed.ExtErrorType == "" {
			parsed.ExtErrorType = rawString(rootValue(extError, "type"))
		}
		if parsed.ExtErrorCode == "" {
			parsed.ExtErrorCode = rawScalarText(rootValue(extError, "code"))
		}
	}
}

func decodeJSONObject(data []byte) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

func rawJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	return decodeJSONObject(raw)
}

func unwrapJSONObject(raw json.RawMessage) map[string]json.RawMessage {
	if object, ok := rawJSONObject(raw); ok {
		return object
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil
	}
	object, _ := decodeJSONObject([]byte(text))
	return object
}

func rootValue(fields map[string]json.RawMessage, names ...string) json.RawMessage {
	for key, value := range fields {
		for _, name := range names {
			if strings.EqualFold(key, name) {
				return value
			}
		}
	}
	return nil
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func rawScalarText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed[0] == '{' || trimmed[0] == '[' {
		return ""
	}
	return trimmed
}

func rawInt(raw json.RawMessage) int {
	text := rawScalarText(raw)
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return value
}

func requestIDFromHeaders(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for _, wanted := range []string{"X-Request-ID", "Request-ID"} {
		for key, values := range headers {
			if !strings.EqualFold(key, wanted) {
				continue
			}
			for _, value := range values {
				if value = strings.TrimSpace(value); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func isOpenAIChatUpstream(model *store.Model, upstreamURL string) bool {
	if model == nil || !strings.EqualFold(strings.TrimSpace(model.Provider), "openai") {
		return false
	}
	u, err := url.Parse(upstreamURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	return strings.HasSuffix(u.Path, "/chat/completions")
}
