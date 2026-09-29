package proxy

import (
	"net/http"
	"testing"

	"kiro-proxy/store"
)

func TestParseOpenAIChatGatewayErrorNestedMessage(t *testing.T) {
	body := `{"error":{"message":"{\"code\":11129,\"msg\":\"invalid function call parameters\",\"requestId\":\"body-request\",\"extError\":{\"code\":\"invalid_request_error\",\"type\":\"invalid_request_error\",\"StatusCode\":400}}"}}`
	headers := http.Header{"X-Request-ID": []string{"header-request"}}

	got := parseOpenAIChatGatewayError(http.StatusServiceUnavailable, headers, []byte(body))
	if got.HTTPStatus != http.StatusServiceUnavailable || got.Code != "11129" {
		t.Fatalf("outer status/code = %d/%q, want 503/11129", got.HTTPStatus, got.Code)
	}
	if got.ExtErrorStatusCode != http.StatusBadRequest || got.ExtErrorType != "invalid_request_error" || got.ExtErrorCode != "invalid_request_error" {
		t.Fatalf("inner error = status %d, type %q, code %q", got.ExtErrorStatusCode, got.ExtErrorType, got.ExtErrorCode)
	}
	if got.RequestID != "body-request" {
		t.Fatalf("request ID = %q, want body request ID to take precedence", got.RequestID)
	}
	if !got.isDefinitiveOpenAIChatRequestError() {
		t.Fatal("nested invalid request on retryable 503 was not classified as definitive")
	}
}

func TestParseOpenAIChatGatewayErrorOrdinary503(t *testing.T) {
	body := `{"error":{"code":"service_unavailable","message":"upstream unavailable","type":"server_error"}}`
	got := parseOpenAIChatGatewayError(http.StatusServiceUnavailable, nil, []byte(body))
	if got.Code != "service_unavailable" {
		t.Fatalf("outer code = %q, want service_unavailable", got.Code)
	}
	if got.isDefinitiveOpenAIChatRequestError() {
		t.Fatal("ordinary 503 was classified as a definitive request error")
	}
}

func TestParseOpenAIChatGatewayErrorMalformed(t *testing.T) {
	headers := http.Header{"Request-Id": []string{"header-request"}}
	got := parseOpenAIChatGatewayError(http.StatusServiceUnavailable, headers, []byte(`{"error":`))
	if got.HTTPStatus != http.StatusServiceUnavailable || got.Code != "" || got.ExtErrorStatusCode != 0 || got.ExtErrorType != "" || got.ExtErrorCode != "" {
		t.Fatalf("malformed body produced parsed fields: %+v", got)
	}
	if got.RequestID != "header-request" {
		t.Fatalf("request ID = %q, want header fallback", got.RequestID)
	}
	if got.isDefinitiveOpenAIChatRequestError() {
		t.Fatal("malformed response was classified as a definitive request error")
	}
}

func TestParseOpenAIChatGatewayErrorDirectBodyAndHeaderID(t *testing.T) {
	body := `{"code":11129,"requestId":"","extError":{"StatusCode":400,"type":"invalid_request_error","code":"invalid_request_error"}}`
	headers := http.Header{"X-Request-Id": []string{"header-request"}}
	got := parseOpenAIChatGatewayError(http.StatusBadGateway, headers, []byte(body))
	if got.RequestID != "header-request" {
		t.Fatalf("request ID = %q, want header fallback", got.RequestID)
	}
	if got.Code != "11129" || got.ExtErrorStatusCode != http.StatusBadRequest || !got.isDefinitiveOpenAIChatRequestError() {
		t.Fatalf("direct response was not parsed/classified: %+v", got)
	}
}

func TestParseOpenAIChatGatewayErrorOtherClientStatuses(t *testing.T) {
	body := `{"extError":{"StatusCode":400,"type":"invalid_request_error","code":"invalid_request_error"}}`
	for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests} {
		got := parseOpenAIChatGatewayError(status, nil, []byte(body))
		if got.isDefinitiveOpenAIChatRequestError() {
			t.Errorf("outer status %d was classified as a retryable wrapped request error", status)
		}
	}
}

func TestIsOpenAIChatUpstream(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		upstreamURL string
		want        bool
	}{
		{name: "openai path with v1 and query", provider: "openai", upstreamURL: "https://api.example/v1/chat/completions?mode=fast", want: true},
		{name: "provider case insensitive", provider: "OpenAI", upstreamURL: "https://api.example/chat/completions", want: true},
		{name: "query text is not path", provider: "openai", upstreamURL: "https://api.example/other?next=/chat/completions", want: false},
		{name: "other provider", provider: "anthropic", upstreamURL: "https://api.example/v1/chat/completions", want: false},
		{name: "path must end at endpoint", provider: "openai", upstreamURL: "https://api.example/v1/chat/completions/extra", want: false},
		{name: "relative URL", provider: "openai", upstreamURL: "/v1/chat/completions", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &store.Model{Provider: tt.provider}
			if got := isOpenAIChatUpstream(model, tt.upstreamURL); got != tt.want {
				t.Fatalf("isOpenAIChatUpstream(%q, %q) = %v, want %v", tt.provider, tt.upstreamURL, got, tt.want)
			}
		})
	}
	if isOpenAIChatUpstream(nil, "https://api.example/v1/chat/completions") {
		t.Fatal("nil model was classified as OpenAI Chat")
	}
	if !isOpenAIChatUpstream(&store.Model{Provider: " openai "}, "https://api.example/v1/chat/completions") {
		t.Fatal("provider whitespace/case normalization changed")
	}
}
