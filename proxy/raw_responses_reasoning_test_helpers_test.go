package proxy

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
)

func reasoningTestSSEEvents(t *testing.T, sse string) []map[string]interface{} {
	t.Helper()
	var events []map[string]interface{}
	for _, line := range strings.Split(sse, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("parse SSE data %q: %v", data, err)
		}
		events = append(events, event)
	}
	return events
}

// kiroReplayReasoningItems 按 Kiro kiro.kiro-agent extension.js 的行为重建下一轮
// reasoning item：文本只取 response.reasoning_text.delta；encrypted_content 按 JSON
// 解析（tAd），只保留 signature 或 redactedContent（解码为字节后再编码，UGo）；
// 有签名无文本的阶段被丢弃（nvd）。Kiro 不会原样回传 encrypted_content。
func kiroReplayReasoningItems(t *testing.T, sse string) []interface{} {
	t.Helper()
	texts := make(map[string]*strings.Builder)
	var items []interface{}
	for _, event := range reasoningTestSSEEvents(t, sse) {
		switch event["type"] {
		case "response.reasoning_text.delta":
			id, _ := event["item_id"].(string)
			if texts[id] == nil {
				texts[id] = &strings.Builder{}
			}
			texts[id].WriteString(event["delta"].(string))
		case "response.output_item.done":
			item, _ := event["item"].(map[string]interface{})
			if item["type"] != "reasoning" {
				continue
			}
			encrypted, _ := item["encrypted_content"].(string)
			var envelope struct {
				RedactedContent string `json:"redactedContent"`
				ReasoningText   *struct {
					Signature *string `json:"signature"`
				} `json:"reasoningText"`
			}
			if json.Unmarshal([]byte(encrypted), &envelope) != nil {
				continue // cr.sse.reasoning.envelope.undecodable
			}
			text := ""
			if builder := texts[item["id"].(string)]; builder != nil {
				text = builder.String()
			}
			var rebuilt []byte
			switch {
			case envelope.RedactedContent != "":
				raw, err := base64.StdEncoding.DecodeString(envelope.RedactedContent)
				if err != nil {
					t.Fatalf("Kiro cannot decode redactedContent: %v", err)
				}
				rebuilt, _ = json.Marshal(map[string]string{"redactedContent": base64.StdEncoding.EncodeToString(raw)})
			case envelope.ReasoningText != nil && envelope.ReasoningText.Signature != nil && text != "":
				rebuilt, _ = json.Marshal(map[string]interface{}{"reasoningText": map[string]string{"text": text, "signature": *envelope.ReasoningText.Signature}})
			default:
				continue
			}
			items = append(items, map[string]interface{}{"type": "reasoning", "summary": []interface{}{}, "encrypted_content": string(rebuilt)})
		}
	}
	return items
}

func captureReasoningDiagnostics(t *testing.T) *[]string {
	t.Helper()
	var lines []string
	previous := reasoningDiagnosticLog
	reasoningDiagnosticLog = func(format string, args ...interface{}) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { reasoningDiagnosticLog = previous })
	return &lines
}

// reasoningEventIndex 返回首个满足条件的事件位置，未找到时为 -1。
func reasoningEventIndex(events []map[string]interface{}, eventType, itemType, itemID string) int {
	for index, event := range events {
		if event["type"] != eventType {
			continue
		}
		item, _ := event["item"].(map[string]interface{})
		if item["type"] == itemType && (itemID == "" || item["id"] == itemID || item["call_id"] == itemID) {
			return index
		}
	}
	return -1
}
