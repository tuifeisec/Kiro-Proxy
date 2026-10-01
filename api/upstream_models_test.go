package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

const (
	openAIModelsUserAgent    = "OpenAI/Go 3.56.0"
	anthropicModelsUserAgent = "Anthropic/Go 1.71.0"
	geminiModelsUserAgent    = "google-genai-sdk/1.71.0 gl-go/"
)

func TestFetchUpstreamModelsSetsProviderUserAgentAndPreservesRequest(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		path          string
		wantUserAgent string
		checkRequest  func(*testing.T, *http.Request)
		response      string
	}{
		{
			name:          "openai",
			provider:      "openai",
			path:          "/v1/models",
			wantUserAgent: openAIModelsUserAgent,
			response:      `{"data":[{"id":"gpt-test"}]}`,
			checkRequest: func(t *testing.T, r *http.Request) {
				t.Helper()
				if got, want := r.Header.Get("Authorization"), "Bearer openai-secret"; got != want {
					t.Errorf("Authorization = %q, want %q", got, want)
				}
				if r.URL.RawQuery != "" {
					t.Errorf("query = %q, want empty", r.URL.RawQuery)
				}
			},
		},
		{
			name:          "anthropic",
			provider:      "anthropic",
			path:          "/v1/models",
			wantUserAgent: anthropicModelsUserAgent,
			response:      `{"data":[],"has_more":false}`,
			checkRequest: func(t *testing.T, r *http.Request) {
				t.Helper()
				if got, want := r.Header.Get("x-api-key"), "anthropic-secret"; got != want {
					t.Errorf("x-api-key = %q, want %q", got, want)
				}
				if got, want := r.Header.Get("anthropic-version"), "2023-06-01"; got != want {
					t.Errorf("anthropic-version = %q, want %q", got, want)
				}
				if got, want := r.URL.Query().Get("limit"), "1000"; got != want {
					t.Errorf("limit = %q, want %q", got, want)
				}
				if got := r.URL.Query().Get("after_id"); got != "" {
					t.Errorf("after_id = %q, want empty", got)
				}
			},
		},
		{
			name:          "gemini",
			provider:      "gemini",
			path:          "/v1beta/models",
			wantUserAgent: geminiModelsUserAgent + runtime.Version(),
			response:      `{"models":[]}`,
			checkRequest: func(t *testing.T, r *http.Request) {
				t.Helper()
				if got, want := r.URL.Query().Get("key"), "gemini-secret"; got != want {
					t.Errorf("key = %q, want %q", got, want)
				}
				if got, want := r.URL.Query().Get("pageSize"), "1000"; got != want {
					t.Errorf("pageSize = %q, want %q", got, want)
				}
				if got := r.URL.Query().Get("pageToken"); got != "" {
					t.Errorf("pageToken = %q, want empty", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got, want := r.Method, http.MethodGet; got != want {
					t.Errorf("method = %q, want %q", got, want)
				}
				if got, want := r.URL.Path, tt.path; got != want {
					t.Errorf("path = %q, want %q", got, want)
				}
				if got, want := r.Header.Get("Accept"), "application/json"; got != want {
					t.Errorf("Accept = %q, want %q", got, want)
				}
				if got := r.Header.Get("User-Agent"); got != tt.wantUserAgent {
					t.Errorf("User-Agent = %q, want %q", got, tt.wantUserAgent)
				}
				tt.checkRequest(t, r)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tt.response)
			}))
			defer server.Close()

			models, err := fetchUpstreamModels(context.Background(), upstreamModelsRequest{
				Provider: tt.provider,
				BaseURL:  server.URL,
				APIKey:   map[string]string{"openai": "openai-secret", "anthropic": "anthropic-secret", "gemini": "gemini-secret"}[tt.provider],
			})
			if err != nil {
				t.Fatalf("fetchUpstreamModels() error = %v", err)
			}
			if len(models) == 0 && tt.provider == "openai" {
				t.Fatal("fetchUpstreamModels() returned no OpenAI models")
			}
		})
	}
}

func TestFetchUpstreamModelsKeepsUserAgentOnEveryAnthropicAndGeminiPage(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		wantUserAgent string
		checkPage     func(*testing.T, *http.Request, int)
		response      func(int) string
	}{
		{
			name:          "anthropic",
			provider:      "anthropic",
			wantUserAgent: anthropicModelsUserAgent,
			checkPage: func(t *testing.T, r *http.Request, page int) {
				t.Helper()
				if got, want := r.URL.Query().Get("limit"), "1000"; got != want {
					t.Errorf("limit = %q, want %q", got, want)
				}
				wantAfterID := ""
				if page == 2 {
					wantAfterID = "first-id"
				}
				if got := r.URL.Query().Get("after_id"); got != wantAfterID {
					t.Errorf("after_id = %q, want %q", got, wantAfterID)
				}
			},
			response: func(page int) string {
				if page == 1 {
					return `{"data":[{"id":"first"}],"has_more":true,"last_id":"first-id"}`
				}
				return `{"data":[{"id":"second"}],"has_more":false}`
			},
		},
		{
			name:          "gemini",
			provider:      "gemini",
			wantUserAgent: geminiModelsUserAgent + runtime.Version(),
			checkPage: func(t *testing.T, r *http.Request, page int) {
				t.Helper()
				if got, want := r.URL.Query().Get("pageSize"), "1000"; got != want {
					t.Errorf("pageSize = %q, want %q", got, want)
				}
				if got, want := r.URL.Query().Get("key"), "gemini-secret"; got != want {
					t.Errorf("key = %q, want %q", got, want)
				}
				wantToken := ""
				if page == 2 {
					wantToken = "next token"
				}
				if got := r.URL.Query().Get("pageToken"); got != wantToken {
					t.Errorf("pageToken = %q, want %q", got, wantToken)
				}
			},
			response: func(page int) string {
				if page == 1 {
					return `{"models":[{"name":"models/first"}],"nextPageToken":"next token"}`
				}
				return `{"models":[{"name":"models/second"}]}`
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page++
				if page > 2 {
					t.Errorf("unexpected page %d", page)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if got, want := r.Header.Get("User-Agent"), tt.wantUserAgent; got != want {
					t.Errorf("page %d User-Agent = %q, want %q", page, got, want)
				}
				if got, want := r.URL.Path, "/v1/models"; tt.provider == "anthropic" && got != want {
					t.Errorf("path = %q, want %q", got, want)
				}
				if tt.provider == "gemini" && r.URL.Path != "/v1beta/models" {
					t.Errorf("path = %q, want %q", r.URL.Path, "/v1beta/models")
				}
				tt.checkPage(t, r, page)
				_, _ = fmt.Fprint(w, tt.response(page))
			}))
			defer server.Close()

			key := "anthropic-secret"
			if tt.provider == "gemini" {
				key = "gemini-secret"
			}
			models, err := fetchUpstreamModels(context.Background(), upstreamModelsRequest{
				Provider: tt.provider,
				BaseURL:  server.URL,
				APIKey:   key,
			})
			if err != nil {
				t.Fatalf("fetchUpstreamModels() error = %v", err)
			}
			if len(models) != 2 {
				t.Fatalf("len(models) = %d, want 2", len(models))
			}
			if page != 2 {
				t.Fatalf("requests = %d, want 2", page)
			}
		})
	}
}

func TestFetchUpstreamModelsKeepsUserAgentOnSameHostRedirect(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("User-Agent"); got != openAIModelsUserAgent {
			t.Errorf("request %d User-Agent = %q, want %q", requests, got, openAIModelsUserAgent)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer redirect-secret"; got != want {
			t.Errorf("request %d Authorization = %q, want %q", requests, got, want)
		}
		if requests == 1 {
			if got, want := r.URL.Path, "/v1/models"; got != want {
				t.Errorf("initial path = %q, want %q", got, want)
			}
			if got := r.URL.RawQuery; got != "" {
				t.Errorf("initial query = %q, want empty", got)
			}
			http.Redirect(w, r, "/v1/models?redirected=1", http.StatusTemporaryRedirect)
			return
		}
		if got, want := r.URL.Path, "/v1/models"; got != want {
			t.Errorf("redirect path = %q, want %q", got, want)
		}
		if got, want := r.URL.Query().Get("redirected"), "1"; got != want {
			t.Errorf("redirect query redirected = %q, want %q", got, want)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"redirected-model"}]}`)
	}))
	defer server.Close()

	models, err := fetchUpstreamModels(context.Background(), upstreamModelsRequest{
		Provider: "openai",
		BaseURL:  server.URL,
		APIKey:   "redirect-secret",
	})
	if err != nil {
		t.Fatalf("fetchUpstreamModels() error = %v", err)
	}
	if len(models) != 1 || models[0] != "redirected-model" {
		t.Fatalf("models = %v, want [redirected-model]", models)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestFetchUpstreamModelsPreservesNon2xxErrorAndSendsUserAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/v1/models"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got := r.URL.RawQuery; got != "" {
			t.Errorf("query = %q, want empty", got)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer status-secret"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if got := r.Header.Get("User-Agent"); got != openAIModelsUserAgent {
			t.Errorf("User-Agent = %q, want %q", got, openAIModelsUserAgent)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":"limited"}`)
	}))
	defer server.Close()

	_, err := fetchUpstreamModels(context.Background(), upstreamModelsRequest{
		Provider: "openai",
		BaseURL:  server.URL,
		APIKey:   "status-secret",
	})
	if err == nil {
		t.Fatal("fetchUpstreamModels() error = nil, want HTTP 429 error")
	}
	var fetchErr *upstreamModelsFetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("error %T is not an upstreamModelsFetchError", err)
	}
	if got, want := fetchErr.message, "上游模型列表请求返回 HTTP 429"; got != want {
		t.Fatalf("fetch error message = %q, want %q", got, want)
	}
	if got, want := fetchErr.err.Error(), "upstream returned HTTP 429"; got != want {
		t.Fatalf("wrapped error = %q, want %q", got, want)
	}
}
