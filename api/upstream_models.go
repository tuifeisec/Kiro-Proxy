package api

import (
	"context"
	"errors"
	"fmt"
	json "github.com/goccy/go-json"
	"io"
	"kiro-proxy/store"
	"kiro-proxy/ua"
	"kiro-proxy/upstreamproxy"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	upstreamModelsBodyLimit = 8 << 20
	upstreamModelsMaxPages  = 20
	upstreamModelsTimeout   = 20 * time.Second
)

type upstreamModelsRequest struct {
	SourceID string `json:"source_id"`
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
	ProxyURL string `json:"proxy_url"`
}

type upstreamModelEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type upstreamModelsFetchError struct {
	message string
	err     error
}

func (e *upstreamModelsFetchError) Error() string {
	if e.err == nil {
		return e.message
	}
	return e.message + ": " + e.err.Error()
}

func (e *upstreamModelsFetchError) Unwrap() error {
	return e.err
}

func (s *Server) handleUpstreamModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var request upstreamModelsRequest
	if err := readJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.resolveUpstreamModelsCredentials(&request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), upstreamModelsTimeout)
	defer cancel()
	models, err := fetchUpstreamModels(ctx, request)
	if err != nil {
		var fetchErr *upstreamModelsFetchError
		if errors.As(err, &fetchErr) {
			log.Printf("[models] 同步上游模型失败: provider=%s error=%v", request.Provider, fetchErr)
			writeError(w, http.StatusBadGateway, fetchErr.message)
			return
		}
		log.Printf("[models] 同步上游模型失败: provider=%s error=%v", request.Provider, err)
		writeError(w, http.StatusBadGateway, "拉取上游模型失败")
		return
	}

	writeOK(w, map[string]interface{}{"models": models})
}

func (s *Server) resolveUpstreamModelsCredentials(request *upstreamModelsRequest) error {
	request.SourceID = strings.TrimSpace(request.SourceID)
	request.Provider = strings.ToLower(strings.TrimSpace(request.Provider))
	request.BaseURL = strings.TrimSpace(request.BaseURL)
	request.APIKey = strings.TrimSpace(request.APIKey)
	request.ProxyURL = strings.TrimSpace(request.ProxyURL)

	var existing *store.Model
	if request.SourceID != "" {
		model, err := s.db.GetModel(request.SourceID)
		if err != nil {
			return fmt.Errorf("原模型配置不存在")
		}
		existing = model
	}

	if request.Provider == "" && existing != nil {
		request.Provider = strings.ToLower(strings.TrimSpace(existing.Provider))
	}
	if request.APIKey == "" || isMasked(request.APIKey) {
		if existing == nil {
			return fmt.Errorf("api_key 不能为空")
		}
		if !strings.EqualFold(request.Provider, existing.Provider) {
			return fmt.Errorf("切换 Provider 后需要填写对应的 API Key")
		}
		request.APIKey = strings.TrimSpace(existing.APIKey)
	}

	switch request.Provider {
	case "openai", "anthropic", "gemini":
	case "codex":
		return fmt.Errorf("Codex 模型列表使用专用接口，当前同步暂不适用")
	default:
		return fmt.Errorf("不支持的 Provider: %s", request.Provider)
	}
	if request.BaseURL == "" {
		return fmt.Errorf("base_url 不能为空")
	}
	if request.APIKey == "" {
		return fmt.Errorf("api_key 不能为空")
	}
	if _, err := parseUpstreamBaseURL(request.BaseURL); err != nil {
		return err
	}
	if _, err := upstreamproxy.ParseProxyURL(request.ProxyURL); err != nil {
		return err
	}
	return nil
}

func fetchUpstreamModels(ctx context.Context, request upstreamModelsRequest) ([]string, error) {
	client, err := upstreamproxy.NewClient(request.ProxyURL, upstreamModelsTimeout)
	if err != nil {
		return nil, &upstreamModelsFetchError{message: "上游代理配置错误", err: err}
	}
	client.CheckRedirect = sameHostRedirectPolicy

	var models []string
	switch request.Provider {
	case "openai":
		models, err = fetchOpenAIModels(ctx, client, request)
	case "anthropic":
		models, err = fetchAnthropicModels(ctx, client, request)
	case "gemini":
		models, err = fetchGeminiModels(ctx, client, request)
	default:
		err = fmt.Errorf("unsupported provider %q", request.Provider)
	}
	if err != nil {
		return nil, err
	}
	return dedupeAndSortModelIDs(models), nil
}

func fetchOpenAIModels(ctx context.Context, client *http.Client, request upstreamModelsRequest) ([]string, error) {
	modelsURL, err := buildUpstreamModelsURL(request.BaseURL, "/v1/models")
	if err != nil {
		return nil, &upstreamModelsFetchError{message: "模型列表地址无效", err: err}
	}
	body, err := requestUpstreamModelsPage(ctx, client, modelsURL, request, "")
	if err != nil {
		return nil, err
	}

	var response struct {
		Data   []upstreamModelEntry `json:"data"`
		Models []upstreamModelEntry `json:"models"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, &upstreamModelsFetchError{message: "上游模型列表不是有效 JSON", err: err}
	}
	if response.Data == nil && response.Models == nil {
		return nil, &upstreamModelsFetchError{message: "上游模型列表格式不受支持"}
	}
	return appendModelEntryIDs(nil, append(response.Data, response.Models...)), nil
}

func fetchAnthropicModels(ctx context.Context, client *http.Client, request upstreamModelsRequest) ([]string, error) {
	modelsURL, err := buildUpstreamModelsURL(request.BaseURL, "/v1/models")
	if err != nil {
		return nil, &upstreamModelsFetchError{message: "模型列表地址无效", err: err}
	}

	models := make([]string, 0)
	afterID := ""
	for page := 0; page < upstreamModelsMaxPages; page++ {
		pageURL := cloneURL(modelsURL)
		query := pageURL.Query()
		query.Set("limit", "1000")
		if afterID != "" {
			query.Set("after_id", afterID)
		}
		pageURL.RawQuery = query.Encode()

		body, requestErr := requestUpstreamModelsPage(ctx, client, pageURL, request, "")
		if requestErr != nil {
			return nil, requestErr
		}
		var response struct {
			Data    []upstreamModelEntry `json:"data"`
			HasMore bool                 `json:"has_more"`
			LastID  string               `json:"last_id"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, &upstreamModelsFetchError{message: "上游模型列表不是有效 JSON", err: err}
		}
		if response.Data == nil {
			return nil, &upstreamModelsFetchError{message: "上游模型列表格式不受支持"}
		}
		models = appendModelEntryIDs(models, response.Data)
		if !response.HasMore {
			return models, nil
		}
		nextID := strings.TrimSpace(response.LastID)
		if nextID == "" || nextID == afterID {
			return nil, &upstreamModelsFetchError{message: "上游模型列表分页信息无效"}
		}
		afterID = nextID
	}
	return nil, &upstreamModelsFetchError{message: "上游模型列表分页数量超过限制"}
}

func fetchGeminiModels(ctx context.Context, client *http.Client, request upstreamModelsRequest) ([]string, error) {
	modelsURL, err := buildUpstreamModelsURL(request.BaseURL, "/v1beta/models")
	if err != nil {
		return nil, &upstreamModelsFetchError{message: "模型列表地址无效", err: err}
	}

	models := make([]string, 0)
	pageToken := ""
	for page := 0; page < upstreamModelsMaxPages; page++ {
		pageURL := cloneURL(modelsURL)
		query := pageURL.Query()
		query.Set("pageSize", "1000")
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		pageURL.RawQuery = query.Encode()

		body, requestErr := requestUpstreamModelsPage(ctx, client, pageURL, request, request.APIKey)
		if requestErr != nil {
			return nil, requestErr
		}
		var response struct {
			Models        []upstreamModelEntry `json:"models"`
			NextPageToken string               `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, &upstreamModelsFetchError{message: "上游模型列表不是有效 JSON", err: err}
		}
		if response.Models == nil {
			return nil, &upstreamModelsFetchError{message: "上游模型列表格式不受支持"}
		}
		models = appendModelEntryIDs(models, response.Models)
		nextToken := strings.TrimSpace(response.NextPageToken)
		if nextToken == "" {
			return models, nil
		}
		if nextToken == pageToken {
			return nil, &upstreamModelsFetchError{message: "上游模型列表分页信息无效"}
		}
		pageToken = nextToken
	}
	return nil, &upstreamModelsFetchError{message: "上游模型列表分页数量超过限制"}
}

func requestUpstreamModelsPage(ctx context.Context, client *http.Client, target *url.URL, request upstreamModelsRequest, geminiKey string) ([]byte, error) {
	requestURL := cloneURL(target)
	if geminiKey != "" {
		query := requestURL.Query()
		query.Set("key", geminiKey)
		requestURL.RawQuery = query.Encode()
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, &upstreamModelsFetchError{message: "创建上游模型列表请求失败", err: err}
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", ua.ProviderUserAgent(request.Provider))
	switch request.Provider {
	case "anthropic":
		httpRequest.Header.Set("x-api-key", request.APIKey)
		httpRequest.Header.Set("anthropic-version", "2023-06-01")
	case "openai":
		httpRequest.Header.Set("Authorization", "Bearer "+request.APIKey)
	}

	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, &upstreamModelsFetchError{message: "请求上游模型列表失败", err: sanitizeUpstreamRequestError(err)}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, upstreamModelsBodyLimit+1))
	if err != nil {
		return nil, &upstreamModelsFetchError{message: "读取上游模型列表失败", err: err}
	}
	if len(body) > upstreamModelsBodyLimit {
		return nil, &upstreamModelsFetchError{message: "上游模型列表响应过大"}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &upstreamModelsFetchError{
			message: fmt.Sprintf("上游模型列表请求返回 HTTP %d", response.StatusCode),
			err:     fmt.Errorf("upstream returned HTTP %d", response.StatusCode),
		}
	}
	return body, nil
}

func sanitizeUpstreamRequestError(err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return fmt.Errorf("%s request failed: %w", urlError.Op, urlError.Err)
	}
	return err
}

func parseUpstreamBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("base_url 格式无效")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("base_url 仅支持 http 或 https")
	}
	parsed.Fragment = ""
	return parsed, nil
}

func buildUpstreamModelsURL(base, endpoint string) (*url.URL, error) {
	parsed, err := parseUpstreamBaseURL(base)
	if err != nil {
		return nil, err
	}
	endpoint = "/" + strings.TrimLeft(strings.TrimSpace(endpoint), "/")
	relative := endpoint
	if index := strings.LastIndex(endpoint, "/"); index >= 0 {
		relative = endpoint[index:]
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, endpoint) {
		versionPath := strings.TrimSuffix(endpoint, relative)
		if strings.HasSuffix(path, versionPath) {
			path += relative
		} else {
			path += endpoint
		}
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed, nil
}

func sameHostRedirectPolicy(request *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("redirect limit exceeded")
	}
	if len(via) > 0 && !strings.EqualFold(request.URL.Host, via[0].URL.Host) {
		return http.ErrUseLastResponse
	}
	return nil
}

func appendModelEntryIDs(models []string, entries []upstreamModelEntry) []string {
	for _, entry := range entries {
		modelID := strings.TrimSpace(entry.ID)
		if modelID == "" {
			modelID = strings.TrimSpace(entry.Name)
		}
		modelID = strings.TrimPrefix(modelID, "models/")
		if modelID != "" {
			models = append(models, modelID)
		}
	}
	return models
}

func dedupeAndSortModelIDs(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	sort.Strings(result)
	return result
}

func cloneURL(source *url.URL) *url.URL {
	clone := *source
	return &clone
}
