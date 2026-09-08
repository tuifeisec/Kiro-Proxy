// api/test_model.go
// 模型连通性测试：发送一条简单消息，测量首字节延迟和吞吐速度

package api

import (
	"bytes"
	"context"
	"fmt"
	json "github.com/goccy/go-json"
	"github.com/google/uuid"
	"io"
	"kiro-proxy/codex"
	"kiro-proxy/store"
	"kiro-proxy/ua"
	"kiro-proxy/upstreamproxy"
	"net/http"
	"strings"
	"time"
)

type testRequest struct {
	ChannelID string `json:"model_id"`
	Model     string `json:"model"`
}

type testResult struct {
	OK           bool    `json:"ok"`
	TTFB         int64   `json:"ttfb_ms"` // 首字节延迟（毫秒）
	TokensPerSec float64 `json:"tokens_per_sec"`
	Error        string  `json:"error,omitempty"`
}

func (s *Server) handleTestModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req testRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	m, err := s.db.GetModel(req.ChannelID)
	if err != nil {
		writeError(w, http.StatusNotFound, "model not found")
		return
	}

	result := runModelTestWithManager(r.Context(), m, req.Model, s.credentials)
	writeOK(w, result)
}

// runModelTest 向指定模型配置发送测试请求
func runModelTest(m *store.Model, model string) testResult {
	return runModelTestWithManager(context.Background(), m, model, nil)
}

func runModelTestWithManager(ctx context.Context, input *store.Model, model string, manager *codex.CredentialManager) testResult {
	if input == nil {
		return testResult{Error: "model is required"}
	}
	copy := *input
	m := &copy
	if ctx == nil {
		ctx = context.Background()
	}
	if manager != nil && m.Provider == "codex" {
		ensured, err := manager.Ensure(ctx, *m)
		if err != nil {
			return testResult{Error: err.Error()}
		}
		*m = ensured
	}
	baseURL := strings.TrimRight(m.BaseURL, "/")
	if model == "" {
		model = m.ModelID
	}

	endpoint := strings.TrimSpace(m.Endpoint)
	if endpoint == "" {
		return testResult{Error: "接口端点不能为空，请在模型配置中完整填写端点路径"}
	}
	upstreamURL := endpoint
	if m.DisableAutoSuffix {
		upstreamURL = strings.TrimSpace(m.BaseURL)
	} else if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		upstreamURL = baseURL + endpoint
	}

	var reqBody []byte
	switch {
	case m.Provider == "anthropic":
		reqBody, _ = json.Marshal(map[string]interface{}{
			"model":      model,
			"max_tokens": 50,
			"stream":     true,
			"messages":   []map[string]string{{"role": "user", "content": "Hi"}},
		})
	case m.Provider == "codex":
		if manager == nil {
			if err := codex.EnsureAccessToken(ctx, m); err != nil {
				return testResult{Error: err.Error()}
			}
		}
		reqBody, _ = json.Marshal(map[string]interface{}{
			"model":        model,
			"instructions": "",
			"stream":       true,
			"store":        false,
			"input":        []map[string]string{{"type": "message", "role": "user", "content": "Hi"}},
		})
	case strings.Contains(endpoint, "/responses"):
		reqBody, _ = json.Marshal(map[string]interface{}{
			"model":  model,
			"stream": true,
			"store":  m.EnableContinuation,
			"input":  []map[string]string{{"role": "user", "content": "Hi"}},
		})
	default:
		reqBody, _ = json.Marshal(map[string]interface{}{
			"model":      model,
			"max_tokens": 50,
			"stream":     true,
			"messages":   []map[string]string{{"role": "user", "content": "Hi"}},
		})
	}

	httpReq, err := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(reqBody))
	if err != nil {
		return testResult{Error: "创建请求失败: " + err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept-Encoding", "identity")
	httpReq.Header.Set("User-Agent", ua.ProviderUserAgent(m.Provider))
	switch m.Provider {
	case "anthropic":
		httpReq.Header.Set("x-api-key", m.APIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	case "codex":
		codex.ApplyHeaders(httpReq, m.APIKey, m.CodexAccountID, true)
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
		httpReq.Header.Set("x-openai-internal-codex-residency", "us")
		httpReq.Header.Set("x-client-request-id", uuid.NewString())
	default:
		httpReq.Header.Set("Authorization", "Bearer "+m.APIKey)
	}

	client, err := upstreamproxy.NewClient(m.ProxyURL, 30*time.Second)
	if err != nil {
		return testResult{Error: err.Error()}
	}
	start := time.Now()

	resp, err := client.Do(httpReq)
	if err != nil {
		return testResult{Error: "请求失败: " + err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return testResult{Error: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body))}
	}

	// 读取第一个有内容的 SSE 事件，测量 TTFB
	ttfb := int64(0)
	totalChars := 0
	buf := make([]byte, 4096)

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			if ttfb == 0 && (strings.Contains(chunk, `"content"`) || strings.Contains(chunk, `"delta"`)) {
				ttfb = time.Since(start).Milliseconds()
			}
			// 粗略统计输出字符数
			for _, line := range strings.Split(chunk, "\n") {
				if strings.HasPrefix(line, "data:") {
					data := strings.TrimSpace(line[5:])
					if data != "[DONE]" {
						totalChars += len(data)
					}
				}
			}
		}
		if readErr != nil {
			break
		}
		// 读够 50 个字符就停止（避免等太久）
		if totalChars > 200 {
			break
		}
	}

	elapsed := time.Since(start).Seconds()
	tps := 0.0
	if elapsed > 0 && totalChars > 0 {
		// 粗略估算：4 字符 ≈ 1 token
		tps = float64(totalChars/4) / elapsed
	}

	return testResult{
		OK:           true,
		TTFB:         ttfb,
		TokensPerSec: tps,
	}
}
