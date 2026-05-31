// api/test_model.go
// 模型连通性测试：发送一条简单消息，测量首字节延迟和吞吐速度

package api

import (
	"bytes"
	"fmt"
	json "github.com/goccy/go-json"
	"io"
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

	result := runModelTest(s.db, m, req.Model)
	writeOK(w, result)
}

// runModelTest 向指定模型配置发送测试请求
func runModelTest(db *store.DB, m *store.Model, model string) testResult {
	baseURL := strings.TrimRight(m.BaseURL, "/")
	if model == "" {
		model = m.ModelID
	}

	endpoint := strings.TrimSpace(m.Endpoint)
	if endpoint == "" {
		return testResult{Error: "接口端点不能为空，请在模型配置中完整填写端点路径"}
	}
	upstreamURL := baseURL + endpoint

	var reqBody []byte
	switch {
	case m.Provider == "anthropic":
		reqBody, _ = json.Marshal(map[string]interface{}{
			"model":      model,
			"max_tokens": 50,
			"stream":     true,
			"messages":   []map[string]string{{"role": "user", "content": "Hi"}},
		})
	case strings.Contains(endpoint, "/responses"):
		reqBody, _ = json.Marshal(map[string]interface{}{
			"model":  model,
			"stream": true,
			"store":  true,
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
	httpReq.Header.Set("User-Agent", ua.CodexUserAgent())
	switch m.Provider {
	case "anthropic":
		httpReq.Header.Set("x-api-key", m.APIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	default:
		httpReq.Header.Set("Authorization", "Bearer "+m.APIKey)
	}

	proxyAddr, err := db.GetSetting(upstreamproxy.SettingKey)
	if err != nil {
		return testResult{Error: "读取代理设置失败: " + err.Error()}
	}
	client, err := upstreamproxy.NewClient(proxyAddr, 30*time.Second)
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
