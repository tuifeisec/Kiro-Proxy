//go:build !dev

package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kiro-proxy/license"
)

func TestReleaseLicensePreservesRemoteDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/license/check" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(license.CheckResult{
			Registered:   true,
			VersionValid: false,
		})
	}))
	defer server.Close()

	result, err := license.NewClient(server.URL).Check()
	if err != nil {
		t.Fatalf("check license: %v", err)
	}
	if result.DevelopmentMode || result.Authorized || result.VersionValid || result.TrafficLogs {
		t.Fatalf("release policy changed remote decision: %+v", result)
	}
}
