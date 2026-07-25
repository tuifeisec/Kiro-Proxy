//go:build dev

package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kiro-proxy/license"
)

func TestDevelopmentLicenseAllowsRegisteredMachine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/license/check" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(license.CheckResult{Registered: true})
	}))
	defer server.Close()

	result, err := license.NewClient(server.URL).Check()
	if err != nil {
		t.Fatalf("check license: %v", err)
	}
	if !result.DevelopmentMode || !result.Authorized || !result.VersionValid || result.Tampered || !result.TrafficLogs {
		t.Fatalf("unexpected development result: %+v", result)
	}
}

func TestDevelopmentLicenseRejectsUnregisteredMachine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(license.CheckResult{})
	}))
	defer server.Close()

	result, err := license.NewClient(server.URL).Check()
	if err != nil {
		t.Fatalf("check license: %v", err)
	}
	if !result.DevelopmentMode || result.Authorized {
		t.Fatalf("unexpected unregistered result: %+v", result)
	}
}
