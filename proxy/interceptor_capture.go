package proxy

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	json "github.com/goccy/go-json"
)

func formatCaptureRequestDetail(req *http.Request, protocol string, body []byte) string {
	return marshalCaptureDetail(map[string]interface{}{
		"type":         "request",
		"protocol":     protocol,
		"method":       req.Method,
		"url":          req.URL.String(),
		"host":         req.Host,
		"path":         req.URL.Path,
		"query":        req.URL.RawQuery,
		"headers":      captureHeaders(req.Header),
		"content_type": req.Header.Get("Content-Type"),
		"body":         captureBody(body, req.Header.Get("Content-Type")),
	})
}

func formatCaptureResponseDetail(req *http.Request, resp *http.Response, protocol string, body []byte) string {
	detail := captureResponseMetadata(req, resp, protocol)
	detail["body"] = captureBody(body, resp.Header.Get("Content-Type"))
	return marshalCaptureDetail(detail)
}

// 只保留独立的标量和 header 副本，流结束时不再读取可变请求对象。
func captureResponseMetadata(req *http.Request, resp *http.Response, protocol string) map[string]interface{} {
	return map[string]interface{}{
		"type":         "response",
		"protocol":     protocol,
		"method":       req.Method,
		"url":          req.URL.String(),
		"host":         req.Host,
		"path":         req.URL.Path,
		"status":       resp.StatusCode,
		"status_text":  resp.Status,
		"headers":      captureHeaders(resp.Header),
		"content_type": resp.Header.Get("Content-Type"),
	}
}

func captureHeaders(headers http.Header) map[string][]string {
	out := make(map[string][]string, len(headers))
	for k, v := range headers {
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Proxy-Authorization") || strings.EqualFold(k, "x-api-key") || strings.EqualFold(k, "api-key") {
			out[k] = []string{"****"}
			continue
		}
		vv := make([]string, len(v))
		copy(vv, v)
		out[k] = vv
	}
	return out
}

func captureBody(body []byte, contentType string) interface{} {
	if len(body) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(string(body))
	lowerContentType := strings.ToLower(contentType)
	if strings.Contains(lowerContentType, "json") || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var parsed interface{}
		if err := json.Unmarshal(body, &parsed); err == nil {
			return parsed
		}
	}
	if events := captureKiroEventStream(body); len(events) > 0 {
		return map[string]interface{}{
			"encoding": "aws-event-stream",
			"events":   events,
		}
	}
	if utf8.Valid(body) {
		return string(body)
	}
	return map[string]interface{}{
		"encoding": "base64",
		"size":     len(body),
		"data":     base64.StdEncoding.EncodeToString(body),
	}
}

func captureKiroEventStream(body []byte) []map[string]interface{} {
	const minFrameSize = 16
	if len(body) < minFrameSize {
		return nil
	}

	events := make([]map[string]interface{}, 0)
	for offset := 0; offset < len(body); {
		if len(body)-offset < minFrameSize {
			return nil
		}

		totalLen := int(binary.BigEndian.Uint32(body[offset : offset+4]))
		headersLen := int(binary.BigEndian.Uint32(body[offset+4 : offset+8]))
		if totalLen < minFrameSize || headersLen < 0 || totalLen > len(body)-offset || 12+headersLen > totalLen-4 {
			return nil
		}

		headersStart := offset + 12
		headersEnd := headersStart + headersLen
		payloadEnd := offset + totalLen - 4
		headers := captureEventStreamHeaders(body[headersStart:headersEnd])
		payload := body[headersEnd:payloadEnd]

		event := map[string]interface{}{
			"headers": headers,
		}
		if eventType, ok := headers[":event-type"]; ok {
			event["event_type"] = eventType
		}
		event["payload"] = captureBody(payload, headers[":content-type"])
		events = append(events, event)
		offset += totalLen
	}
	return events
}

func captureEventStreamHeaders(data []byte) map[string]string {
	headers := make(map[string]string)
	for i := 0; i < len(data); {
		nameLen := int(data[i])
		i++
		if nameLen == 0 || i+nameLen+1 > len(data) {
			return headers
		}
		name := string(data[i : i+nameLen])
		i += nameLen
		typeID := data[i]
		i++

		if typeID != 7 {
			return headers
		}
		if i+2 > len(data) {
			return headers
		}
		valueLen := int(binary.BigEndian.Uint16(data[i : i+2]))
		i += 2
		if i+valueLen > len(data) {
			return headers
		}
		headers[name] = string(data[i : i+valueLen])
		i += valueLen
	}
	return headers
}

func marshalCaptureDetail(detail map[string]interface{}) string {
	b, err := json.Marshal(detail)
	if err != nil {
		return fmt.Sprintf("%v", detail)
	}
	return string(b)
}
