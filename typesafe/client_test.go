package typesafe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kiro-proxy/store"

	json "github.com/goccy/go-json"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (fn doerFunc) Do(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestClassifyMapsIntentAndSendsStructuredChoiceQuestion(t *testing.T) {
	tests := []struct {
		name          string
		choice        string
		probabilities map[string]float64
		confidence    float64
	}{
		{name: "chat", choice: "chat", probabilities: map[string]float64{"chat": 0.8, "do": 0.1, "spec": 0.1}, confidence: 0.03},
		{name: "do", choice: "do", probabilities: map[string]float64{"chat": 0.1, "do": 0.8, "spec": 0.1}, confidence: 0.92},
		{name: "spec", choice: "spec", probabilities: map[string]float64{"chat": 0.1, "do": 0.2, "spec": 0.7}, confidence: 0.4},
		{name: "rounded probabilities and zero confidence", choice: "chat", probabilities: map[string]float64{"chat": 0.33333317, "do": 0.33333317, "spec": 0.33333317}, confidence: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			state := map[string]interface{}{
				"current_message":    map[string]interface{}{"content": "Please explain this error"},
				"history":            []interface{}{map[string]interface{}{"role": "user", "content": "Earlier text"}},
				"classifier_context": "Original Kiro category definitions and text history",
				"metadata":           map[string]string{"agent_task_type": "spec"},
			}
			var calls int
			client := NewClient(doerFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Method != http.MethodPost {
					t.Errorf("HTTP 方法错误：%s", request.Method)
				}
				if request.URL.String() != "https://typesafe.example/api/v1/systemone" {
					t.Errorf("请求地址错误：%s", request.URL.String())
				}
				if got := request.Header.Get("Authorization"); got != "Bearer test-api-key" {
					t.Errorf("Authorization 头错误：%q", got)
				}
				if got := request.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type 错误：%q", got)
				}
				if request.GetBody != nil {
					t.Error("请求不应设置 GetBody，以免传输层重放")
				}
				if deadline, ok := request.Context().Deadline(); !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
					t.Errorf("请求缺少预期的超时上下文：deadline=%v, ok=%v", deadline, ok)
				}

				var sent systemOneRequest
				if err := json.NewDecoder(request.Body).Decode(&sent); err != nil {
					t.Fatalf("解码请求失败：%v", err)
				}
				if sent.Model != "requested-model" {
					t.Errorf("请求模型错误：%q", sent.Model)
				}
				var sentState map[string]interface{}
				if err := json.Unmarshal(sent.State, &sentState); err != nil {
					t.Fatalf("解码 state 失败：%v", err)
				}
				if sentState["classifier_context"] != state["classifier_context"] || sentState["metadata"] == nil {
					t.Errorf("state 的分类上下文或元数据未保留：%s", sent.State)
				}
				question, ok := sent.Questions["intent"]
				if !ok || len(sent.Questions) != 1 {
					t.Fatalf("请求问题不是唯一的 intent：%+v", sent.Questions)
				}
				if question.Type != "choice" || len(question.Criteria) != 3 {
					t.Fatalf("intent 问题类型或选项错误：%+v", question)
				}
				for _, option := range intentOptions {
					if question.Criteria[option] == "" {
						t.Errorf("缺少 %s 类别说明", option)
					}
				}
				for _, phrase := range []string{"current_message", "classifier_context", "Ignore wrapper", "metadata"} {
					if !strings.Contains(question.Instructions, phrase) {
						t.Errorf("instructions 未包含 %q：%s", phrase, question.Instructions)
					}
				}
				return mockResponse(http.StatusOK, responseFor(test.choice, test.probabilities, test.confidence)), nil
			}))

			result, err := client.Classify(context.Background(), config, state)
			if err != nil {
				t.Fatalf("Classify 返回错误：%v", err)
			}
			if calls != 1 {
				t.Fatalf("请求次数为 %d，期望 1", calls)
			}
			if result.Choice != test.choice || result.Confidence != test.confidence {
				t.Errorf("意图或 confidence 错误：choice=%q confidence=%v", result.Choice, result.Confidence)
			}
			if result.Model != "jev-1.13.0-actual" {
				t.Errorf("未保留实际模型 ID：%q", result.Model)
			}
			if result.Usage == nil || result.Usage.InputTokens != 23 || result.Usage.OutputTokens != 7 {
				t.Errorf("usage 错误：%+v", result.Usage)
			}
			for option, want := range test.probabilities {
				if got := result.Probabilities[option]; got != want {
					t.Errorf("概率 %s=%v，期望 %v", option, got, want)
				}
			}
		})
	}
}

func TestClassifyUsesDefaults(t *testing.T) {
	config := validConfig()
	config.BaseURL = ""
	config.Model = ""
	config.TimeoutSeconds = 0
	client := NewClient(doerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.typesafe.ai/v1/systemone" {
			t.Errorf("默认 URL 错误：%s", request.URL.String())
		}
		var body systemOneRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("解码请求失败：%v", err)
		}
		if body.Model != defaultModel {
			t.Errorf("默认模型错误：%q", body.Model)
		}
		deadline, ok := request.Context().Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining <= 0 || remaining > defaultTimeout {
			t.Errorf("默认超时错误：remaining=%v, ok=%v", remaining, ok)
		}
		return mockResponse(http.StatusOK, responseFor("chat", map[string]float64{"chat": 1, "do": 0, "spec": 0}, 1)), nil
	}))
	if _, err := client.Classify(context.Background(), config, "hello"); err != nil {
		t.Fatalf("Classify 返回错误：%v", err)
	}
}

func TestClassifyHTTPErrorPreservesBodyAndUsageWithoutRetry(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "unprocessable", status: http.StatusUnprocessableEntity},
		{name: "rate limited", status: http.StatusTooManyRequests},
		{name: "overloaded", status: 529},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			originalBody := `{"model":"billed-model","usage":{"input_tokens":9,"output_tokens":2},"error":"private-prompt-and-key-marker"}`
			var calls int
			client := NewClient(doerFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return mockResponse(test.status, []byte(originalBody)), nil
			}))
			result, err := client.Classify(context.Background(), validConfig(), map[string]string{"message": "text"})
			if err == nil {
				t.Fatal("HTTP 错误状态未返回错误")
			}
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("错误类型不是 HTTPError：%T %v", err, err)
			}
			if httpErr.StatusCode != test.status || httpErr.Body != originalBody {
				t.Errorf("HTTPError 内容错误：status=%d body=%q", httpErr.StatusCode, httpErr.Body)
			}
			if strings.Contains(httpErr.Error(), originalBody) || strings.Contains(httpErr.Error(), "private-prompt") || strings.Contains(httpErr.Error(), "test-api-key") {
				t.Errorf("HTTPError.Error 泄漏了响应正文或密钥：%s", httpErr.Error())
			}
			if result.Model != "billed-model" || result.Usage == nil || result.Usage.InputTokens != 9 || result.Usage.OutputTokens != 2 {
				t.Errorf("未保留错误响应中可验证的计量字段：%+v", result)
			}
			if calls != 1 {
				t.Errorf("请求次数为 %d，529/限流响应不得自动重试", calls)
			}
		})
	}
}

func TestClassifyRejectsInvalidAnswerButPreservesModelAndUsage(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(map[string]interface{})
		wantModel string
		wantUsage bool
	}{
		{name: "missing model", mutate: func(body map[string]interface{}) { delete(body, "model") }, wantUsage: true},
		{name: "null model", mutate: func(body map[string]interface{}) { body["model"] = nil }, wantUsage: true},
		{name: "missing usage", mutate: func(body map[string]interface{}) { delete(body, "usage") }, wantModel: "actual-model"},
		{name: "null input usage", mutate: func(body map[string]interface{}) { testUsage(body)["input_tokens"] = nil }, wantModel: "actual-model"},
		{name: "missing output usage", mutate: func(body map[string]interface{}) { delete(testUsage(body), "output_tokens") }, wantModel: "actual-model"},
		{name: "negative usage", mutate: func(body map[string]interface{}) { testUsage(body)["input_tokens"] = -1 }, wantModel: "actual-model"},
		{name: "fractional usage", mutate: func(body map[string]interface{}) { testUsage(body)["output_tokens"] = 2.5 }, wantModel: "actual-model"},
		{name: "missing probabilities", mutate: func(body map[string]interface{}) { delete(testIntent(body), "probabilities") }, wantModel: "actual-model", wantUsage: true},
		{name: "missing probability option", mutate: func(body map[string]interface{}) { delete(testProbabilities(body), "spec") }, wantModel: "actual-model", wantUsage: true},
		{name: "null probability", mutate: func(body map[string]interface{}) {
			testIntent(body)["choice"] = "do"
			probabilities := testProbabilities(body)
			probabilities["chat"] = nil
			probabilities["do"] = 1
			probabilities["spec"] = 0
		}, wantModel: "actual-model", wantUsage: true},
		{name: "probability outside range", mutate: func(body map[string]interface{}) { testProbabilities(body)["chat"] = 1.2 }, wantModel: "actual-model", wantUsage: true},
		{name: "probabilities do not sum to one", mutate: func(body map[string]interface{}) { testProbabilities(body)["spec"] = 0.2 }, wantModel: "actual-model", wantUsage: true},
		{name: "unknown choice", mutate: func(body map[string]interface{}) { testIntent(body)["choice"] = "other" }, wantModel: "actual-model", wantUsage: true},
		{name: "choice is not maximum", mutate: func(body map[string]interface{}) { testIntent(body)["choice"] = "do" }, wantModel: "actual-model", wantUsage: true},
		{name: "wrong answer type", mutate: func(body map[string]interface{}) { testIntent(body)["type"] = "score" }, wantModel: "actual-model", wantUsage: true},
		{name: "null confidence", mutate: func(body map[string]interface{}) { testIntent(body)["confidence"] = nil }, wantModel: "actual-model", wantUsage: true},
		{name: "confidence outside range", mutate: func(body map[string]interface{}) { testIntent(body)["confidence"] = 1.1 }, wantModel: "actual-model", wantUsage: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := baseResponseObject()
			test.mutate(body)
			client := NewClient(doerFunc(func(*http.Request) (*http.Response, error) {
				return mockResponse(http.StatusOK, mustJSON(t, body)), nil
			}))
			result, err := client.Classify(context.Background(), validConfig(), map[string]string{"message": "text"})
			if err == nil {
				t.Fatal("无效响应未返回错误")
			}
			if result.Model != test.wantModel {
				t.Errorf("model=%q，期望 %q", result.Model, test.wantModel)
			}
			if test.wantUsage {
				if result.Usage == nil || result.Usage.InputTokens != 23 || result.Usage.OutputTokens != 7 {
					t.Errorf("完整用量未保留：%+v", result.Usage)
				}
			} else if result.Usage != nil {
				t.Errorf("不完整或无效用量不应作为 0 计量：%+v", result.Usage)
			}
		})
	}
}

func TestClassifyRejectsInvalidJSONAndTrailingData(t *testing.T) {
	valid := responseFor("chat", map[string]float64{"chat": 1, "do": 0, "spec": 0}, 1)
	tests := []struct {
		name string
		body []byte
	}{
		{name: "invalid JSON", body: []byte(`{"model":`)},
		{name: "trailing JSON", body: append(append([]byte(nil), valid...), []byte(` {}`)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient(doerFunc(func(*http.Request) (*http.Response, error) {
				return mockResponse(http.StatusOK, test.body), nil
			}))
			result, err := client.Classify(context.Background(), validConfig(), map[string]string{"message": "text"})
			if err == nil {
				t.Fatal("无效 JSON 未返回错误")
			}
			if result.Model != "" || result.Usage != nil {
				t.Errorf("非法 JSON 不应贡献计量字段：%+v", result)
			}
		})
	}
}

func TestClassifyHonorsTimeoutAndCancellation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		var calls atomic.Int32
		client := NewClient(doerFunc(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			<-request.Context().Done()
			return nil, request.Context().Err()
		}))
		config := validConfig()
		config.TimeoutSeconds = 1
		_, err := client.Classify(context.Background(), config, map[string]string{"message": "text"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("超时错误未保留：%v", err)
		}
		if calls.Load() != 1 {
			t.Errorf("请求次数为 %d，期望 1", calls.Load())
		}
	})

	t.Run("caller cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := NewClient(doerFunc(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		}))
		_, err := client.Classify(ctx, validConfig(), map[string]string{"message": "text"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消错误未保留：%v", err)
		}
	})
}

func TestNewClientStopsRedirectsForStandardHTTPClient(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalls.Add(1)
	}))
	defer target.Close()

	var sourceAuthorization string
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sourceAuthorization = request.Header.Get("Authorization")
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer source.Close()

	config := validConfig()
	config.BaseURL = source.URL
	client := NewClient(&http.Client{})
	_, err := client.Classify(context.Background(), config, map[string]string{"message": "text"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusFound {
		t.Fatalf("重定向响应应作为状态错误返回：%v", err)
	}
	if sourceAuthorization != "Bearer test-api-key" {
		t.Errorf("源请求缺少预期 Authorization：%q", sourceAuthorization)
	}
	if targetCalls.Load() != 0 {
		t.Errorf("认证请求被跟随到重定向目标：calls=%d", targetCalls.Load())
	}
}

func validConfig() store.TypesafeClassifierConfig {
	return store.TypesafeClassifierConfig{
		Enabled:        true,
		BaseURL:        "https://typesafe.example/api",
		APIKey:         "test-api-key",
		Model:          "requested-model",
		TimeoutSeconds: 5,
	}
}

func responseFor(choice string, probabilities map[string]float64, confidence float64) []byte {
	return mustJSON(nil, map[string]interface{}{
		"model": "jev-1.13.0-actual",
		"answers": map[string]interface{}{
			"intent": map[string]interface{}{
				"type":          "choice",
				"choice":        choice,
				"probabilities": probabilities,
				"confidence":    confidence,
			},
		},
		"usage": map[string]interface{}{"input_tokens": 23, "output_tokens": 7},
	})
}

func baseResponseObject() map[string]interface{} {
	return map[string]interface{}{
		"model": "actual-model",
		"answers": map[string]interface{}{
			"intent": map[string]interface{}{
				"type":          "choice",
				"choice":        "chat",
				"probabilities": map[string]interface{}{"chat": 0.8, "do": 0.1, "spec": 0.1},
				"confidence":    0.3,
			},
		},
		"usage": map[string]interface{}{"input_tokens": 23, "output_tokens": 7},
	}
}

func testIntent(response map[string]interface{}) map[string]interface{} {
	return response["answers"].(map[string]interface{})["intent"].(map[string]interface{})
}

func testUsage(response map[string]interface{}) map[string]interface{} {
	return response["usage"].(map[string]interface{})
}

func testProbabilities(response map[string]interface{}) map[string]interface{} {
	return testIntent(response)["probabilities"].(map[string]interface{})
}

func mustJSON(t *testing.T, value interface{}) []byte {
	if t == nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		return encoded
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("编码测试 JSON 失败：%v", err)
	}
	return encoded
}

func mockResponse(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}
