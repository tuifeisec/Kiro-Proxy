package codex

import (
	"context"
	"io"
	"kiro-proxy/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchUsageUsesExistingTokenWithoutRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer existing-access" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/codex/usage" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"rate_limit":{"ok":true}}`)
	}))
	defer server.Close()
	m := &store.Model{Provider: "codex", BaseURL: server.URL, APIKey: "existing-access", CodexAccountID: "acct"}
	usage, err := FetchUsage(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if usage == nil || !strings.Contains(string(usage.Raw), "rate_limit") {
		t.Fatalf("unexpected usage response: %#v", usage)
	}
}
