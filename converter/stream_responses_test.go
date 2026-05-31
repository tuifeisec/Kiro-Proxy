package converter

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"sync"
	"testing"

	json "github.com/goccy/go-json"
)

// 鈹€鈹€ AWS Event Stream 甯цВ鐮侊紙浠呮祴璇曠敤锛岀嫭绔嬪疄鐜颁互鏍￠獙缂栫爜姝ｇ‘鎬э級 鈹€鈹€

type decodedFrame struct {
	eventType string
	payload   string
}

func decodeFrames(t *testing.T, buf []byte) []decodedFrame {
	t.Helper()
	var frames []decodedFrame
	off := 0
	for off < len(buf) {
		if len(buf)-off < 16 {
			t.Fatalf("tail remaining %d bytes, not enough for one frame", len(buf)-off)
		}
		totalLen := binary.BigEndian.Uint32(buf[off : off+4])
		headersLen := binary.BigEndian.Uint32(buf[off+4 : off+8])
		if int(totalLen) > len(buf)-off {
			t.Fatalf("totalLen=%d exceeds remaining %d", totalLen, len(buf)-off)
		}
		frame := buf[off : off+int(totalLen)]

		// prelude CRC
		preludeCRC := binary.BigEndian.Uint32(frame[8:12])
		if got := crc32.ChecksumIEEE(frame[0:8]); got != preludeCRC {
			t.Fatalf("prelude CRC mismatch: got=%x want=%x", got, preludeCRC)
		}
		// message CRC
		msgCRC := binary.BigEndian.Uint32(frame[totalLen-4 : totalLen])
		if got := crc32.ChecksumIEEE(frame[0 : totalLen-4]); got != msgCRC {
			t.Fatalf("message CRC mismatch: got=%x want=%x", got, msgCRC)
		}

		headers := frame[12 : 12+headersLen]
		payload := frame[12+headersLen : totalLen-4]

		var eventType string
		i := 0
		for i < len(headers) {
			nameLen := int(headers[i])
			i++
			name := string(headers[i : i+nameLen])
			i += nameLen
			i++ // type byte (7 = string)
			valLen := int(binary.BigEndian.Uint16(headers[i : i+2]))
			i += 2
			val := string(headers[i : i+valLen])
			i += valLen
			if name == ":event-type" {
				eventType = val
			}
		}
		// payload 蹇呴』鏄悎娉?JSON
		var tmp map[string]interface{}
		if err := json.Unmarshal(payload, &tmp); err != nil {
			t.Fatalf("invalid JSON payload %s: %v", string(payload), err)
		}

		frames = append(frames, decodedFrame{eventType: eventType, payload: string(payload)})
		off += int(totalLen)
	}
	return frames
}

func runResponsesPipeline(sse string) []byte {
	sc := NewResponsesStreamConverter(io.NopCloser(strings.NewReader(sse)), "gpt-5.5", 200000, 1.0)
	out, _ := io.ReadAll(sc)
	return out
}

func countEvent(frames []decodedFrame, et string) int {
	n := 0
	for _, f := range frames {
		if f.eventType == et {
			n++
		}
	}
	return n
}

func concatText(frames []decodedFrame) string {
	var b strings.Builder
	for _, f := range frames {
		if f.eventType == "assistantResponseEvent" {
			var p struct {
				Content string `json:"content"`
			}
			_ = json.Unmarshal([]byte(f.payload), &p)
			b.WriteString(p.Content)
		}
	}
	return b.String()
}

// 鈹€鈹€ 娴嬭瘯鏁版嵁 鈹€鈹€

const ssePlainText = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"浣犲ソ"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"锛屼笘鐣?}

event: response.output_text.done
data: {"type":"response.output_text.done","item_id":"msg_1","text":"浣犲ソ锛屼笘鐣?}

event: response.completed
data: {"type":"response.completed","response":{"usage":{"input_tokens":1087,"output_tokens":5,"input_tokens_details":{"cached_tokens":11}}}}

`

const sseToolCall = `event: response.created
data: {"type":"response.created","response":{"id":"resp_2"}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"execute_pwsh","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_abc","delta":"{\"command\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_abc","delta":"\"dir\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"execute_pwsh","arguments":"{\"command\":\"dir\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"usage":{"input_tokens":2000,"output_tokens":12}}}

`

// done 鎵嶅甫瀹屾暣 arguments銆佸叏绋嬫棤 delta 鐨勪笂娓稿彉浣?
const sseToolNoDelta = `event: response.output_item.added
data: {"type":"response.output_item.added","item":{"id":"fc_9","type":"function_call","call_id":"call_x","name":"read_file","arguments":""}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"fc_9","type":"function_call","call_id":"call_x","name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":4}}}

`

func TestResponses_PlainText(t *testing.T) {
	frames := decodeFrames(t, runResponsesPipeline(ssePlainText))
	if got := concatText(frames); got == "" {
		t.Fatalf("unexpected value: %q", got)
	}
	if countEvent(frames, "contextUsageEvent") != 1 || countEvent(frames, "meteringEvent") != 1 {
		t.Fatalf("missing tail frames: %+v", frames)
	}
	// 缂撳瓨鍛戒腑搴斾綋鐜板湪 metadata
	hasCache := false
	for _, f := range frames {
		if f.eventType == "messageMetadataEvent" && strings.Contains(f.payload, "cacheReadInputTokens") {
			hasCache = true
		}
	}
	if !hasCache {
		t.Errorf("test assertion failed: %v", frames)
	}
}

func TestResponses_ToolCall(t *testing.T) {
	frames := decodeFrames(t, runResponsesPipeline(sseToolCall))
	// 鏈熸湜锛歴tart + 2*input + stop = 4 涓?toolUseEvent
	if n := countEvent(frames, "toolUseEvent"); n != 4 {
		t.Fatalf("expected 4 toolUseEvent, got %d: %+v", n, frames)
	}
	// 閲嶅缓 arguments
	var args strings.Builder
	var sawStart, sawStop bool
	for _, f := range frames {
		if f.eventType != "toolUseEvent" {
			continue
		}
		var p struct {
			ToolUseID string `json:"toolUseId"`
			Name      string `json:"name"`
			Input     string `json:"input"`
			Stop      bool   `json:"stop"`
		}
		_ = json.Unmarshal([]byte(f.payload), &p)
		if p.Stop {
			sawStop = true
			if p.ToolUseID != "call_abc" || p.Name != "execute_pwsh" {
				t.Errorf("payload issue: %s", f.payload)
			}
			continue
		}
		if p.Input == "" {
			sawStart = true
			if p.ToolUseID != "call_abc" || p.Name != "execute_pwsh" {
				t.Errorf("payload issue: %s", f.payload)
			}
		} else {
			args.WriteString(p.Input)
			if p.ToolUseID != "call_abc" || p.Name != "execute_pwsh" {
				t.Errorf("payload issue: %s", f.payload)
			}
		}
	}
	if !sawStart || !sawStop {
		t.Fatalf("missing start/stop frame")
	}
	if args.String() != `{"command":"dir"}` {
		t.Fatalf("args mismatch: %q", args.String())
	}
}

func TestResponses_ToolNoDelta(t *testing.T) {
	frames := decodeFrames(t, runResponsesPipeline(sseToolNoDelta))
	var args strings.Builder
	for _, f := range frames {
		if f.eventType != "toolUseEvent" {
			continue
		}
		var p struct {
			Input string `json:"input"`
			Stop  bool   `json:"stop"`
		}
		_ = json.Unmarshal([]byte(f.payload), &p)
		if !p.Stop && p.Input != "" {
			args.WriteString(p.Input)
		}
	}
	if args.String() != `{"path":"a.txt"}` {
		t.Fatalf("args mismatch: %q", args.String())
	}
}

// 澶氬伐鍏峰苟琛岃皟鐢紙涓嶅悓 item_id / call_id 浜ら敊锛?
func TestResponses_MultiTool(t *testing.T) {
	sse := `event: response.output_item.added
data: {"type":"response.output_item.added","item":{"id":"fc_a","type":"function_call","call_id":"ca","name":"toolA","arguments":""}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"id":"fc_b","type":"function_call","call_id":"cb","name":"toolB","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_a","call_id":"ca","delta":"{\"x\":1}"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_b","call_id":"cb","delta":"{\"y\":2}"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"fc_a","type":"function_call","call_id":"ca","name":"toolA","arguments":"{\"x\":1}"}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"fc_b","type":"function_call","call_id":"cb","name":"toolB","arguments":"{\"y\":2}"}}

event: response.completed
data: {"type":"response.completed","response":{"usage":{"input_tokens":50,"output_tokens":8}}}

`
	frames := decodeFrames(t, runResponsesPipeline(sse))
	argsByTool := map[string]string{}
	for _, f := range frames {
		if f.eventType != "toolUseEvent" {
			continue
		}
		var p struct {
			Name  string `json:"name"`
			Input string `json:"input"`
			Stop  bool   `json:"stop"`
		}
		_ = json.Unmarshal([]byte(f.payload), &p)
		if !p.Stop && p.Input != "" {
			argsByTool[p.Name] += p.Input
		}
	}
	if argsByTool["toolA"] != `{"x":1}` || argsByTool["toolB"] != `{"y":2}` {
		t.Fatalf("test assertion failed: %v", argsByTool)
	}
}

// 骞跺彂绋冲畾鎬э細澶ч噺骞跺彂娴侊紝姣忔潯閮藉繀椤讳骇鍑哄畬鍏ㄤ竴鑷寸殑鍚堟硶缁撴灉
func TestResponses_ConcurrentStable(t *testing.T) {
	const workers = 64
	const iters = 50
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iters)

	// 鍏堢畻鍑哄熀鍑嗚緭鍑?
	baseFrames := decodeFrames(t, runResponsesPipeline(sseToolCall))
	baseText := fmt.Sprintf("%v", baseFrames)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				out := runResponsesPipeline(sseToolCall)
				// 瑙ｇ爜鏍￠獙锛圕RC + JSON锛夛紝澶辫触鐩存帴鎶ラ敊
				var frames []decodedFrame
				off := 0
				ok := true
				for off < len(out) {
					if len(out)-off < 16 {
						ok = false
						break
					}
					total := binary.BigEndian.Uint32(out[off : off+4])
					if int(total) > len(out)-off {
						ok = false
						break
					}
					fr := out[off : off+int(total)]
					if crc32.ChecksumIEEE(fr[0:8]) != binary.BigEndian.Uint32(fr[8:12]) {
						ok = false
						break
					}
					if crc32.ChecksumIEEE(fr[0:total-4]) != binary.BigEndian.Uint32(fr[total-4:total]) {
						ok = false
						break
					}
					hl := binary.BigEndian.Uint32(fr[4:8])
					var et string
					j := 12
					hdr := fr[12 : 12+hl]
					k := 0
					for k < len(hdr) {
						nl := int(hdr[k])
						k++
						nm := string(hdr[k : k+nl])
						k += nl
						k++
						vl := int(binary.BigEndian.Uint16(hdr[k : k+2]))
						k += 2
						vv := string(hdr[k : k+vl])
						k += vl
						if nm == ":event-type" {
							et = vv
						}
					}
					_ = j
					frames = append(frames, decodedFrame{eventType: et, payload: string(fr[12+hl : total-4])})
					off += int(total)
				}
				if !ok {
					errCh <- fmt.Errorf("test assertion failed")
					return
				}
				if got := fmt.Sprintf("%v", frames); got != baseText {
					errCh <- fmt.Errorf("mismatch: got %v want %v", got, baseText)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// 鎬ц兘鍩哄噯锛氱函鏂囨湰涓庡伐鍏疯皟鐢ㄤ袱绉嶅吀鍨嬫祦
func BenchmarkResponses_Text(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sc := NewResponsesStreamConverter(io.NopCloser(strings.NewReader(ssePlainText)), "gpt-5.5", 200000, 1.0)
		_, _ = io.Copy(io.Discard, sc)
	}
}

func BenchmarkResponses_Tool(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sc := NewResponsesStreamConverter(io.NopCloser(strings.NewReader(sseToolCall)), "gpt-5.5", 200000, 1.0)
		_, _ = io.Copy(io.Discard, sc)
	}
}

func TestResponsesStream_DeepSeekPromptCacheUsage(t *testing.T) {
	sse := `event: response.completed

data: {"type":"response.completed","response":{"id":"resp_ds","usage":{"prompt_cache_hit_tokens":777,"prompt_cache_miss_tokens":223,"output_tokens":9}}}

`
	sc := NewResponsesStreamConverter(io.NopCloser(strings.NewReader(sse)), "gpt-5.5", 200000, 1.0)
	_, _ = io.ReadAll(sc)
	in, out := sc.Usage()
	cacheRead, cacheWrite := sc.CacheTokens()

	if in != 1000 {
		t.Fatalf("input tokens = %d, want 1000", in)
	}
	if out != 9 {
		t.Fatalf("output tokens = %d, want 9", out)
	}
	if cacheRead != 777 || cacheWrite != 0 {
		t.Fatalf("cache tokens = (%d,%d), want (777,0)", cacheRead, cacheWrite)
	}
}
