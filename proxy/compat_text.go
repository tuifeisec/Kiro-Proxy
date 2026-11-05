package proxy

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

const (
	// 只需读取足以识别拒绝原因的前缀；其余正文原样拼回，不截断透传给客户端的错误。
	compatErrorPeekLimit   = 64 << 10
	compatEvidenceMaxRunes = 160
)

// 证据只来自规则正则的命中片段，再去掉疑似凭据，避免把提示正文或密钥写进日志。
var compatSecretPattern = regexp.MustCompile(`(?i)\b(?:sk|pk|rk|ak)-[a-z0-9_\-]{6,}|\bbearer\s+[a-z0-9._\-]{6,}|\b(?:api[_-]?key|access[_-]?token|secret)\s*[=:]\s*\S+`)

// peekUpstreamErrorBody 读取错误响应前缀用于识别，并把正文原样恢复，不改 ContentLength。
func peekUpstreamErrorBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	original := resp.Body
	timedOut := make(chan struct{})
	timer := time.AfterFunc(upstreamErrorBodyTimeout, func() {
		close(timedOut)
		_ = original.Close()
	})
	prefix, _ := io.ReadAll(io.LimitReader(original, compatErrorPeekLimit))
	if !timer.Stop() {
		<-timedOut
	}
	resp.Body = &prefixedReadCloser{reader: io.MultiReader(bytes.NewReader(prefix), original), closer: original}
	return prefix
}

// compatRefusal 是一次上游拒绝的识别输入：状态码、可匹配文本与 OpenAI 风格的 param 字段。
type compatRefusal struct {
	status int
	text   string
	param  string
}

func newCompatRefusal(status int, body []byte) compatRefusal {
	refusal := compatRefusal{status: status, text: compatErrorText(body)}
	var parsed struct {
		Param string `json:"param"`
		Error struct {
			Param string `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		refusal.param = strings.TrimSpace(parsed.Error.Param)
		if refusal.param == "" {
			refusal.param = strings.TrimSpace(parsed.Param)
		}
	}
	return refusal
}

// compatErrorText 返回原始错误文本与 JSON 中解码后的字符串值，
// 使 \uXXXX 转义的中文或被转义的引号也能被规则匹配。
func compatErrorText(body []byte) string {
	text := string(body)
	var parsed interface{}
	if json.Unmarshal(body, &parsed) != nil {
		return text
	}
	var builder strings.Builder
	builder.WriteString(text)
	appendJSONStrings(&builder, parsed, 0)
	return builder.String()
}

func appendJSONStrings(builder *strings.Builder, value interface{}, depth int) {
	if depth > 6 {
		return
	}
	switch typed := value.(type) {
	case string:
		builder.WriteByte('\n')
		builder.WriteString(typed)
	case map[string]interface{}:
		// 校验器的字段路径 loc 在消息之前，使 "reasoning\nExtra inputs are not permitted" 可被匹配；
		// param 已单独解析，不拼进文本，以免与消息相邻造成误匹配。
		for _, key := range []string{"loc", "message", "msg", "error", "detail", "type", "code"} {
			if nested, ok := typed[key]; ok {
				appendJSONStrings(builder, nested, depth+1)
			}
		}
	case []interface{}:
		for _, item := range typed {
			appendJSONStrings(builder, item, depth+1)
		}
	}
}

// compatEvidence 把规则命中的片段整理成单行、限长、去凭据的证据。
func compatEvidence(match string) string {
	evidence := strings.Join(strings.Fields(match), " ")
	evidence = compatSecretPattern.ReplaceAllString(evidence, "<redacted>")
	if runes := []rune(evidence); len(runes) > compatEvidenceMaxRunes {
		evidence = string(runes[:compatEvidenceMaxRunes]) + "..."
	}
	return evidence
}
