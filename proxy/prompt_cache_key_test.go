package proxy

import (
	"bytes"
	"testing"
)

func TestResponsesPromptCacheKeyCanonicalObjects(t *testing.T) {
	first := []byte(`{"instructions":"help","tools":[{"type":"function","parameters":{"b":2,"a":1}}],"input":"first"}`)
	second := []byte(`{ "input":"next", "tools":[{ "parameters": {"a":1, "b":2}, "type":"function" }], "instructions":"help" }`)
	_, a, err := ensureResponsesPromptCacheKey(first, "Model")
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := ensureResponsesPromptCacheKey(second, "Model")
	if err != nil || a != b {
		t.Fatalf("equivalent stable fields produced different keys: %q %q, %v", a, b, err)
	}
}

func TestResponsesPromptCacheKeyRetainsSemanticDifferences(t *testing.T) {
	for _, pair := range [][2]string{
		{`{"tools":[{"n":9007199254740992}]}`, `{"tools":[{"n":9007199254740993}]}`},
		{`{"tools":[{"n":0.12345678901234567890}]}`, `{"tools":[{"n":0.12345678901234567891}]}`},
		{`{"tools":["a","b"]}`, `{"tools":["b","a"]}`},
		{`{"instructions":"a"}`, `{"instructions":"b"}`},
		{`{"tools":null}`, `{}`},
		{`{"tools":[]}`, `{"prompt_cache_options":[]}`},
	} {
		_, a, err := ensureResponsesPromptCacheKey([]byte(pair[0]), "model")
		if err != nil {
			t.Fatal(err)
		}
		_, b, err := ensureResponsesPromptCacheKey([]byte(pair[1]), "model")
		if err != nil || a == b {
			t.Fatalf("different stable fields share a key: %s / %s, %v", pair[0], pair[1], err)
		}
	}
}

func TestResponsesPromptCacheKeyPreservesExplicitKeyAndBody(t *testing.T) {
	body := []byte(`{ "prompt_cache_key":" client-key ", "tools":[{"n":9007199254740993}], "input":"hello" }`)
	got, key, err := ensureResponsesPromptCacheKey(body, "model")
	if err != nil || key != " client-key " || !bytes.Equal(got, body) {
		t.Fatalf("explicit cache key/body changed: key=%q body=%s err=%v", key, got, err)
	}
	body = []byte(`{"tools":[{"n":9007199254740993}],"input":"hello"}`)
	got, _, err = ensureResponsesPromptCacheKey(body, "model")
	if err != nil || !bytes.Contains(got, []byte(`9007199254740993`)) || !bytes.Contains(got, []byte(`"input":"hello"`)) {
		t.Fatalf("request content changed: %s, %v", got, err)
	}
}
