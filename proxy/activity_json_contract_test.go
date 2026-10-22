package proxy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestActivityJSONGrammarMatchesStandardDecoder(t *testing.T) {
	for _, body := range []string{
		`{"payload":[],"extra":true}`,
		`{"extra":true,"payload":[]}`,
		`{"payload":[] "extra":true}`,
		`{"payload":[],}`,
		`{"payload":[{},]}`,
		`{"payload":[{}{}]}`,
		`{"payload":[{"sessionId":"sess_a",}]}`,
		`{"extra":[1,],"payload":[]}`,
		`{"extra":[1 2],"payload":[]}`,
		`{"extra":{"x":true,},"payload":[]}`,
		`{"extra":{"x":true "y":false},"payload":[]}`,
		`{"extra":01,"payload":[]}`,
		`{"extra":1.,"payload":[]}`,
		`{"extra":1e,"payload":[]}`,
		`{"extra":-1.25e+2,"payload":[]}`,
		`{"extra":"\\\"\u4e2d","payload":[]}`,
		"{\"extra\":\"unescaped\nnewline\",\"payload\":[]}",
		`{"extra":"\q","payload":[]}`,
		`{"payload":[]} {"payload":[]}`,
		`{"payload":[{"content":{"_meta":{"kiro":{"workflow":{"nodePath":["wf","plan",]}}}}}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			report, err := parseKiroActivityRouting(strings.NewReader(body))
			if json.Valid([]byte(body)) {
				if err != nil && !errors.Is(err, errKiroActivityUnroutable) {
					t.Fatalf("valid JSON rejected: %v", err)
				}
			} else if err == nil || len(report.Entries) != 0 {
				t.Fatalf("invalid JSON accepted or partially committed: entries=%d err=%v", len(report.Entries), err)
			}
		})
	}
}

func FuzzActivityParserRejectsInvalidJSON(f *testing.F) {
	for _, seed := range []string{
		`{"payload":[]}`, `{"extra":true,"payload":[]}`,
		nativeActivityEnvelope("sess_fuzz", 1, "plan"),
		`{"payload":[{},]}`, `{"payload":[] "extra":1}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 64<<10 {
			t.Skip()
		}
		report, err := parseKiroActivityRouting(strings.NewReader(body))
		if !json.Valid([]byte(body)) && (err == nil || len(report.Entries) != 0) {
			t.Fatalf("invalid JSON accepted or partially committed: entries=%d err=%v", len(report.Entries), err)
		}
	})
}
