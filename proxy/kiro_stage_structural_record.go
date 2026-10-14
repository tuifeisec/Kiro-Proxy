package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

const kiroStageStructuralRecordPrefix = "\n/*kiro-proxy-stage-v3:"

type kiroStageStructuralEdit struct {
	Offset    int    `json:"offset"`
	Original  string `json:"original"`
	Installed string `json:"installed"`
}

// 恢复记录和代码在同一次原子替换中提交，不存在旁路记录提交的中间态。
type kiroStageStructuralRecord struct {
	Version        int                       `json:"version"`
	AdapterSHA256  string                    `json:"adapter_sha256"`
	Family         string                    `json:"family"`
	OriginalSHA256 string                    `json:"original_sha256"`
	PayloadSHA256  string                    `json:"payload_sha256"`
	Edits          []kiroStageStructuralEdit `json:"edits"`
}

func wrapKiroStageStructuralCandidate(original, payload []byte, family, adapterHash string, edits []kiroStageStructuralEdit) ([]byte, error) {
	record := kiroStageStructuralRecord{Version: 3, Family: family, AdapterSHA256: adapterHash,
		OriginalSHA256: kiroStageSHA256(original), PayloadSHA256: kiroStageSHA256(payload), Edits: edits}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	candidate := append(append([]byte(nil), payload...), []byte(kiroStageStructuralRecordPrefix)...)
	candidate = append(candidate, base64.RawStdEncoding.EncodeToString(data)...)
	candidate = append(candidate, "*/\n"...)
	reconstructed, found, err := unwrapKiroStageStructuralCandidate(candidate)
	if err != nil || !found || !bytes.Equal(reconstructed, original) {
		return nil, fmt.Errorf("自动桥接恢复记录自检失败: %v", err)
	}
	return candidate, nil
}

func unwrapKiroStageStructuralCandidate(current []byte) ([]byte, bool, error) {
	original, _, found, err := parseKiroStageStructuralCandidate(current)
	return original, found, err
}

func parseKiroStageStructuralCandidate(current []byte) ([]byte, kiroStageStructuralRecord, bool, error) {
	index := bytes.LastIndex(current, []byte(kiroStageStructuralRecordPrefix))
	if index < 0 {
		return nil, kiroStageStructuralRecord{}, false, nil
	}
	fail := func(detail string) ([]byte, kiroStageStructuralRecord, bool, error) {
		return nil, kiroStageStructuralRecord{}, true, fmt.Errorf("Kiro 自动桥接恢复记录无效: %s", detail)
	}
	if !bytes.HasSuffix(current, []byte("*/\n")) {
		return fail("尾记录被截断或修改")
	}
	encoded := current[index+len(kiroStageStructuralRecordPrefix) : len(current)-3]
	data, err := base64.RawStdEncoding.Strict().DecodeString(string(encoded))
	if err != nil {
		return fail("编码无效")
	}
	var record kiroStageStructuralRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return fail("JSON 无效: " + err.Error())
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fail("存在多余 JSON")
	}
	if record.Version != 3 || record.Family == "" || !validKiroStageHash(record.AdapterSHA256) ||
		!validKiroStageHash(record.OriginalSHA256) || !validKiroStageHash(record.PayloadSHA256) || len(record.Edits) == 0 {
		return fail("版本、摘要或编辑列表无效")
	}
	payload := current[:index]
	if kiroStageSHA256(payload) != record.PayloadSHA256 {
		return fail("代码摘要不符")
	}
	previousEnd := 0
	for _, edit := range record.Edits {
		if edit.Offset < previousEnd || edit.Offset > len(payload) || len(edit.Installed) > len(payload)-edit.Offset || edit.Original == edit.Installed {
			return fail("编辑位置越界、重叠或无效")
		}
		if !bytes.Equal(payload[edit.Offset:edit.Offset+len(edit.Installed)], []byte(edit.Installed)) {
			return fail("编辑内容不符")
		}
		previousEnd = edit.Offset + len(edit.Installed)
	}
	// 编辑使用候选文件偏移且已确认有序、无重叠；一次重建，避免每项编辑复制整个 bundle。
	original := make([]byte, 0, len(payload))
	previousEnd = 0
	for _, edit := range record.Edits {
		original = append(original, payload[previousEnd:edit.Offset]...)
		original = append(original, edit.Original...)
		previousEnd = edit.Offset + len(edit.Installed)
	}
	original = append(original, payload[previousEnd:]...)
	if kiroStageSHA256(original) != record.OriginalSHA256 {
		return fail("重建原版摘要不符")
	}
	return original, record, true, nil
}
