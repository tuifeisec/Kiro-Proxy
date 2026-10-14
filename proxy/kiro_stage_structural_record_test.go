package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestKiroStageStructuralCandidateRestoresOriginal(t *testing.T) {
	original := []byte("const answer = old();\nconst label = \"before\";\n")
	payload := []byte("const answer = new();\nconst label = \"after\";\n")
	firstOffset := len("const answer = ")
	secondOffset := len("const answer = new();\nconst label = \"")
	edits := []kiroStageStructuralEdit{
		{Offset: firstOffset, Original: "old()", Installed: "new()"},
		{Offset: secondOffset, Original: "before", Installed: "after"},
	}
	originalBefore := append([]byte(nil), original...)
	payloadBefore := append([]byte(nil), payload...)

	candidate, err := wrapKiroStageStructuralCandidate(original, payload, "mock-family", kiroStageSHA256([]byte("mock adapter")), edits)
	if err != nil {
		t.Fatalf("wrap candidate: %v", err)
	}
	got, found, err := unwrapKiroStageStructuralCandidate(candidate)
	if err != nil || !found || !bytes.Equal(got, original) {
		t.Fatalf("unwrap candidate = %q, found %t, error %v; want original %q", got, found, err, original)
	}
	if !bytes.Equal(original, originalBefore) || !bytes.Equal(payload, payloadBefore) {
		t.Fatal("wrap or unwrap mutated its input")
	}
}

func TestKiroStageStructuralCandidateRejectsInvalidRecords(t *testing.T) {
	payload := []byte("B")
	original := []byte("A")
	edits := []kiroStageStructuralEdit{{Offset: 0, Original: "A", Installed: "B"}}
	validRecord := func() kiroStageStructuralRecord {
		return kiroStageStructuralRecord{
			Version: 3, Family: "mock-family", AdapterSHA256: kiroStageSHA256([]byte("mock adapter")),
			OriginalSHA256: kiroStageSHA256(original), PayloadSHA256: kiroStageSHA256(payload), Edits: edits,
		}
	}
	validCandidate := func() []byte {
		return encodeKiroStageStructuralRecordForTest(t, payload, validRecord())
	}
	reject := func(t *testing.T, candidate []byte, message string) {
		t.Helper()
		got, found, err := unwrapKiroStageStructuralCandidate(candidate)
		if !found || err == nil {
			t.Fatalf("invalid record accepted: restored %q, found %t, error %v", got, found, err)
		}
		if message != "" && !strings.Contains(err.Error(), message) {
			t.Fatalf("error %q does not contain %q", err, message)
		}
	}

	t.Run("damaged record encoding", func(t *testing.T) {
		candidate := validCandidate()
		index := bytes.Index(candidate, []byte(kiroStageStructuralRecordPrefix))
		if index < 0 {
			t.Fatal("record prefix missing from fixture")
		}
		candidate[index+len(kiroStageStructuralRecordPrefix)] = '!'
		reject(t, candidate, "编码无效")
	})

	t.Run("truncated trailer", func(t *testing.T) {
		candidate := validCandidate()
		reject(t, candidate[:len(candidate)-1], "尾记录被截断或修改")
	})

	t.Run("payload hash mismatch", func(t *testing.T) {
		candidate := validCandidate()
		candidate[0] = 'C'
		reject(t, candidate, "代码摘要不符")
	})

	t.Run("installed edit content changed", func(t *testing.T) {
		changedPayload := []byte("C")
		record := validRecord()
		record.PayloadSHA256 = kiroStageSHA256(changedPayload)
		candidate := encodeKiroStageStructuralRecordForTest(t, changedPayload, record)
		reject(t, candidate, "编辑内容不符")
	})

	t.Run("edit out of bounds", func(t *testing.T) {
		record := validRecord()
		record.Edits = []kiroStageStructuralEdit{{Offset: len(payload) + 1, Original: "A", Installed: "X"}}
		candidate := encodeKiroStageStructuralRecordForTest(t, payload, record)
		reject(t, candidate, "编辑位置越界、重叠或无效")
	})

	t.Run("overlapping edits", func(t *testing.T) {
		currentPayload := []byte("abcde")
		currentOriginal := []byte("ABCDE")
		record := kiroStageStructuralRecord{
			Version: 3, Family: "mock-family", AdapterSHA256: kiroStageSHA256([]byte("mock adapter")),
			OriginalSHA256: kiroStageSHA256(currentOriginal), PayloadSHA256: kiroStageSHA256(currentPayload),
			Edits: []kiroStageStructuralEdit{
				{Offset: 1, Original: "BCD", Installed: "bcd"},
				{Offset: 3, Original: "DE", Installed: "de"},
			},
		}
		candidate := encodeKiroStageStructuralRecordForTest(t, currentPayload, record)
		reject(t, candidate, "编辑位置越界、重叠或无效")
	})

	t.Run("reconstructed original hash mismatch", func(t *testing.T) {
		record := validRecord()
		record.OriginalSHA256 = kiroStageSHA256([]byte("different original"))
		candidate := encodeKiroStageStructuralRecordForTest(t, payload, record)
		reject(t, candidate, "重建原版摘要不符")
	})

	t.Run("unknown JSON field", func(t *testing.T) {
		data, err := json.Marshal(validRecord())
		if err != nil {
			t.Fatal(err)
		}
		data = append(bytes.TrimSuffix(data, []byte("}")), []byte(`,"unknown":true}`)...)
		candidate := encodeKiroStageStructuralRecordJSONForTest(payload, data)
		reject(t, candidate, "JSON 无效")
	})

	t.Run("trailing JSON value", func(t *testing.T) {
		data, err := json.Marshal(validRecord())
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, []byte(" {}")...)
		candidate := encodeKiroStageStructuralRecordJSONForTest(payload, data)
		reject(t, candidate, "存在多余 JSON")
	})

	t.Run("record absent", func(t *testing.T) {
		got, found, err := unwrapKiroStageStructuralCandidate([]byte("plain source"))
		if err != nil || found || got != nil {
			t.Fatalf("unwrap without a record = %q, found %t, error %v", got, found, err)
		}
	})
}

func encodeKiroStageStructuralRecordForTest(t *testing.T, payload []byte, record kiroStageStructuralRecord) []byte {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return encodeKiroStageStructuralRecordJSONForTest(payload, data)
}

func encodeKiroStageStructuralRecordJSONForTest(payload, data []byte) []byte {
	candidate := append([]byte(nil), payload...)
	candidate = append(candidate, []byte(kiroStageStructuralRecordPrefix)...)
	candidate = append(candidate, base64.RawStdEncoding.EncodeToString(data)...)
	return append(candidate, []byte("*/\n")...)
}
