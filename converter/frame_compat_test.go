package converter

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"

	json "github.com/goccy/go-json"
)

// ── 忠实复刻 ai-proxy（已验证可用）的帧编码器，用于字节级对拍 ──

func ref_buildEventStreamFrame(eventType string, payload interface{}) []byte {
	payloadBytes, _ := json.Marshal(payload)

	headerBytes := ref_buildEventHeaders(eventType)
	headersLen := uint32(len(headerBytes))
	payloadLen := uint32(len(payloadBytes))
	totalLen := uint32(12) + headersLen + payloadLen + 4

	prelude := make([]byte, 8)
	binary.BigEndian.PutUint32(prelude[0:4], totalLen)
	binary.BigEndian.PutUint32(prelude[4:8], headersLen)
	preludeCRC := crc32.ChecksumIEEE(prelude)

	var buf bytes.Buffer
	buf.Write(prelude)
	_ = binary.Write(&buf, binary.BigEndian, preludeCRC)
	buf.Write(headerBytes)
	buf.Write(payloadBytes)

	messageCRC := crc32.ChecksumIEEE(buf.Bytes())
	_ = binary.Write(&buf, binary.BigEndian, messageCRC)

	return buf.Bytes()
}

func ref_buildEventHeaders(eventType string) []byte {
	var buf bytes.Buffer
	ref_writeStringHeader(&buf, ":message-type", "event")
	ref_writeStringHeader(&buf, ":event-type", eventType)
	ref_writeStringHeader(&buf, ":content-type", "application/json")
	return buf.Bytes()
}

func ref_writeStringHeader(buf *bytes.Buffer, name, value string) {
	buf.WriteByte(byte(len(name)))
	buf.WriteString(name)
	buf.WriteByte(byte(7))
	var vl [2]byte
	binary.BigEndian.PutUint16(vl[:], uint16(len(value)))
	buf.Write(vl[:])
	buf.WriteString(value)
}

// 对拍：kiro-proxy 的 BuildKiroEventFrame 必须与 ai-proxy 字节完全一致
func TestFrameByteCompat_Generic(t *testing.T) {
	cases := []struct {
		event   string
		payload map[string]interface{}
	}{
		{"assistantResponseEvent", map[string]interface{}{"content": "你好，世界", "modelId": "gpt-5.5"}},
		{"toolUseEvent", map[string]interface{}{"toolUseId": "call_abc", "name": "execute_pwsh"}},
		{"toolUseEvent", map[string]interface{}{"toolUseId": "call_abc", "name": "x", "stop": true}},
		{"contextUsageEvent", map[string]interface{}{"contextUsagePercentage": 1.01}},
	}
	for _, c := range cases {
		got := BuildKiroEventFrame(c.event, c.payload)
		want := ref_buildEventStreamFrame(c.event, c.payload)
		if !bytes.Equal(got, want) {
			t.Errorf("事件 %s 帧字节不一致:\n got=%x\nwant=%x", c.event, got, want)
		}
	}
}

// 对拍高频手写帧 AssistantResponseFrame 与参考实现
func TestFrameByteCompat_AssistantHandwritten(t *testing.T) {
	text := "你好，世界"
	modelID := "gpt-5.5"
	got := AssistantResponseFrame(text, modelID)
	want := ref_buildEventStreamFrame("assistantResponseEvent", map[string]interface{}{
		"content": text,
		"modelId": modelID,
	})
	if !bytes.Equal(got, want) {
		t.Errorf("AssistantResponseFrame 与参考不一致:\n got=%x\nwant=%x\n got-str=%s\nwant-str=%s",
			got, want, frameDump(got), frameDump(want))
	}
}

func TestFrameByteCompat_ToolHandwritten(t *testing.T) {
	got := ToolUseStartFrame("call_abc", "execute_pwsh")
	// 注意字段顺序：手写实现是 toolUseId 在前、name 在后
	want := ref_buildEventStreamFrame("toolUseEvent", orderedToolStart("call_abc", "execute_pwsh"))
	if !bytes.Equal(got, want) {
		t.Errorf("ToolUseStartFrame payload 可能字段顺序/内容不一致:\n got=%s\nwant=%s", frameDump(got), frameDump(want))
	}
}

// orderedToolStart 用 json.RawMessage 固定字段顺序，模拟手写 payload
func orderedToolStart(id, name string) json.RawMessage {
	return json.RawMessage(`{"toolUseId":` + mustJSON(id) + `,"name":` + mustJSON(name) + `}`)
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// frameDump 提取帧的 payload 字符串，便于肉眼比对
func frameDump(frame []byte) string {
	if len(frame) < 16 {
		return "<too short>"
	}
	totalLen := binary.BigEndian.Uint32(frame[0:4])
	headersLen := binary.BigEndian.Uint32(frame[4:8])
	if int(totalLen) > len(frame) {
		return "<bad totalLen>"
	}
	payload := frame[12+headersLen : totalLen-4]
	return string(payload)
}
