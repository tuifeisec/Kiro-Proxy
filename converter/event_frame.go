// event_frame.go
// AWS Event Stream 二进制帧构建 — 高性能版
//
// 帧格式：
//   [4B 总长度][4B 头部长度][4B preludeCRC][头部字节][载荷字节][4B 消息CRC]
//
// 头部必须包含三个字段（Kiro IDE 强制要求）：
//   :message-type  = "event"
//   :event-type    = 具体事件名
//   :content-type  = "application/json"
//
// 性能策略：
//   - 高频帧（text delta、tool input）避免 map 分配和 json.Marshal
//   - 使用预计算的头部字节缓存
//   - 手工 JSON 拼接替代反射序列化

package converter

import (
	"encoding/binary"
	"hash/crc32"
	"strconv"
	"sync"

	json "github.com/goccy/go-json"
)

const (
	headerMessageType = ":message-type"
	headerEventType   = ":event-type"
	headerContentType = ":content-type"
	headerTypeString  = byte(7) // AWS Event Stream string type
)

var crc32Table = crc32.MakeTable(crc32.IEEE)

// 预计算的固定头部片段（独立分配，不共享底层数组）
var (
	prebuiltHeaderPrefix []byte // :message-type=event
	prebuiltHeaderSuffix []byte // :content-type=application/json
)

func init() {
	// 使用独立 buffer，避免共享底层数组
	prefix := make([]byte, 0, 32)
	prefix = appendStringHeader(prefix, headerMessageType, "event")
	prebuiltHeaderPrefix = prefix

	suffix := make([]byte, 0, 48)
	suffix = appendStringHeader(suffix, headerContentType, "application/json")
	prebuiltHeaderSuffix = suffix
}

// headerCache 缓存已知 eventType 的完整头部字节
// 事件类型是有限集合（~6 种），不会无限增长
var headerCache sync.Map

func getCachedHeaders(eventType string) []byte {
	if v, ok := headerCache.Load(eventType); ok {
		return v.([]byte)
	}
	h := buildHeadersBytes(eventType)
	headerCache.Store(eventType, h)
	return h
}

// ── 帧构建核心 ───────────────────────────────────────────────────

// buildFrameFast 根据预缓存头部和 payload 构建完整帧
// 返回独立分配的 []byte，调用方可安全持有
func buildFrameFast(headerBytes, payload []byte) []byte {
	headersLen := uint32(len(headerBytes))
	payloadLen := uint32(len(payload))
	totalLen := 12 + headersLen + payloadLen + 4

	buf := make([]byte, 0, totalLen)

	// Prelude: [4B totalLen][4B headersLen]
	buf = binary.BigEndian.AppendUint32(buf, totalLen)
	buf = binary.BigEndian.AppendUint32(buf, headersLen)

	// Prelude CRC（对前 8 字节计算）
	preludeCRC := crc32.Checksum(buf[:8], crc32Table)
	buf = binary.BigEndian.AppendUint32(buf, preludeCRC)

	// Headers + Payload
	buf = append(buf, headerBytes...)
	buf = append(buf, payload...)

	// Message CRC（对整个消息体计算）
	messageCRC := crc32.Checksum(buf, crc32Table)
	buf = binary.BigEndian.AppendUint32(buf, messageCRC)

	return buf
}

// ── 公共 API（通用路径，低频使用） ───────────────────────────────

// BuildKiroEventFrame 构建一个完整的 AWS Event Stream 帧
func BuildKiroEventFrame(eventType string, payload map[string]interface{}) []byte {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return buildFrameFast(getCachedHeaders(eventType), payloadBytes)
}

// ── 高频帧：手工拼接 JSON，避免 map 分配 + json.Marshal ──────────

// AssistantResponseFrame 构建文本内容帧（最高频）
// payload: {"content":"<text>","modelId":"<id>"}
func AssistantResponseFrame(text, modelID string) []byte {
	buf := make([]byte, 0, 32+len(text)*2+len(modelID)+16)
	buf = append(buf, `{"content":`...)
	buf = appendJSONString(buf, text)
	if modelID != "" {
		buf = append(buf, `,"modelId":`...)
		buf = appendJSONString(buf, modelID)
	}
	buf = append(buf, '}')
	return buildFrameFast(getCachedHeaders("assistantResponseEvent"), buf)
}

// ToolUseStartFrame 工具调用开始帧
// payload: {"toolUseId":"<id>","name":"<name>"}
func ToolUseStartFrame(toolUseID, name string) []byte {
	buf := make([]byte, 0, 48+len(toolUseID)+len(name))
	buf = append(buf, `{"toolUseId":`...)
	buf = appendJSONString(buf, toolUseID)
	buf = append(buf, `,"name":`...)
	buf = appendJSONString(buf, name)
	buf = append(buf, '}')
	return buildFrameFast(getCachedHeaders("toolUseEvent"), buf)
}

// ToolUseInputFrame 工具调用输入片段帧（高频）
// payload: {"toolUseId":"<id>","name":"<name>","input":"<input>"}
func ToolUseInputFrame(toolUseID, name, input string) []byte {
	buf := make([]byte, 0, 64+len(toolUseID)+len(name)+len(input)*2)
	buf = append(buf, `{"toolUseId":`...)
	buf = appendJSONString(buf, toolUseID)
	buf = append(buf, `,"name":`...)
	buf = appendJSONString(buf, name)
	buf = append(buf, `,"input":`...)
	buf = appendJSONString(buf, input)
	buf = append(buf, '}')
	return buildFrameFast(getCachedHeaders("toolUseEvent"), buf)
}

// ToolUseStopFrame 工具调用结束帧
// payload: {"toolUseId":"<id>","name":"<name>","stop":true}
func ToolUseStopFrame(toolUseID, name string) []byte {
	buf := make([]byte, 0, 64+len(toolUseID)+len(name))
	buf = append(buf, `{"toolUseId":`...)
	buf = appendJSONString(buf, toolUseID)
	buf = append(buf, `,"name":`...)
	buf = appendJSONString(buf, name)
	buf = append(buf, `,"stop":true}`...)
	return buildFrameFast(getCachedHeaders("toolUseEvent"), buf)
}

// MetadataFrame 构建 token 用量帧
func MetadataFrame(inputTokens, outputTokens, cacheRead, cacheWrite int) []byte {
	// 防御性处理：确保 uncached 不为负
	uncached := inputTokens - cacheRead - cacheWrite
	if uncached < 0 {
		uncached = 0
	}
	// 如果全部为零（无缓存场景），uncached = inputTokens
	if cacheRead == 0 && cacheWrite == 0 {
		uncached = inputTokens
	}

	buf := make([]byte, 0, 128)
	buf = append(buf, `{"tokenUsage":{"outputTokens":`...)
	buf = strconv.AppendInt(buf, int64(outputTokens), 10)
	buf = append(buf, `,"uncachedInputTokens":`...)
	buf = strconv.AppendInt(buf, int64(uncached), 10)
	if cacheRead > 0 {
		buf = append(buf, `,"cacheReadInputTokens":`...)
		buf = strconv.AppendInt(buf, int64(cacheRead), 10)
	}
	if cacheWrite > 0 {
		buf = append(buf, `,"cacheWriteInputTokens":`...)
		buf = strconv.AppendInt(buf, int64(cacheWrite), 10)
	}
	buf = append(buf, `}}`...)
	return buildFrameFast(getCachedHeaders("messageMetadataEvent"), buf)
}

// ContextUsageFrame 构建上下文使用率帧
func ContextUsageFrame(usedTokens, maxTokens int) []byte {
	pct := 0.0
	if maxTokens > 0 {
		pct = float64(usedTokens) / float64(maxTokens) * 100.0
		if pct > 100.0 {
			pct = 100.0
		}
	}

	buf := make([]byte, 0, 48)
	buf = append(buf, `{"contextUsagePercentage":`...)
	buf = strconv.AppendFloat(buf, pct, 'f', 2, 64)
	buf = append(buf, '}')
	return buildFrameFast(getCachedHeaders("contextUsageEvent"), buf)
}

// MeteringFrame 构建计费帧
func MeteringFrame(credits float64) []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, `{"unit":"credit","unitPlural":"credits","usage":`...)
	buf = strconv.AppendFloat(buf, credits, 'f', 3, 64)
	buf = append(buf, '}')
	return buildFrameFast(getCachedHeaders("meteringEvent"), buf)
}

// ── 头部构建 ─────────────────────────────────────────────────────

func buildHeadersBytes(eventType string) []byte {
	size := len(prebuiltHeaderPrefix) + len(prebuiltHeaderSuffix) +
		1 + len(headerEventType) + 1 + 2 + len(eventType)
	buf := make([]byte, 0, size)
	buf = append(buf, prebuiltHeaderPrefix...)
	buf = appendStringHeader(buf, headerEventType, eventType)
	buf = append(buf, prebuiltHeaderSuffix...)
	return buf
}

// appendStringHeader 追加一个字符串类型的头部字段到 buf
// 格式: [1B name_len][name_bytes][1B type=7][2B value_len][value_bytes]
func appendStringHeader(buf []byte, name, value string) []byte {
	buf = append(buf, byte(len(name)))
	buf = append(buf, name...)
	buf = append(buf, headerTypeString)
	buf = append(buf, byte(len(value)>>8), byte(len(value)))
	buf = append(buf, value...)
	return buf
}

// ── JSON 字符串转义（手工实现，避免 encoding/json 反射开销） ─────

// appendJSONString 将字符串作为 JSON 字符串值追加到 buf（含引号）
func appendJSONString(buf []byte, s string) []byte {
	buf = append(buf, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			buf = append(buf, '\\', '"')
		case '\\':
			buf = append(buf, '\\', '\\')
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\r':
			buf = append(buf, '\\', 'r')
		case '\t':
			buf = append(buf, '\\', 't')
		case '\b':
			buf = append(buf, '\\', 'b')
		case '\f':
			buf = append(buf, '\\', 'f')
		default:
			if c < 0x20 {
				buf = append(buf, '\\', 'u', '0', '0',
					hexChar(c>>4), hexChar(c&0x0f))
			} else {
				buf = append(buf, c)
			}
		}
	}
	buf = append(buf, '"')
	return buf
}

func hexChar(c byte) byte {
	if c < 10 {
		return '0' + c
	}
	return 'a' + c - 10
}
