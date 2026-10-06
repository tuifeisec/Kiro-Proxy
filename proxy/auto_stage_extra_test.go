package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kiro-proxy/store"
)

func TestStageRawManualPassthroughClearsAutoToolLock(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })

	env := newAutoStageIntegrationEnv(t)
	var passthroughCalls int
	rawStageProxy(t, env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passthroughCalls++
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "passthrough")
	}))
	conversationID := "raw-manual-clears-lock"

	first := serveRawStage(t, env, rawStageRequest(t, "auto", conversationID, "plan", 0))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "response.completed") {
		t.Fatalf("initial stage request failed: %d %s", first.Code, first.Body.String())
	}
	if got := <-env.requests; got.Model != "upstream-shared-responses" {
		t.Fatalf("initial stage target = %q, want shared-responses: %+v", got.Model, got)
	}

	manual := serveRawStage(t, env, rawStageRequest(t, "manual", conversationID, "run", 0))
	if manual.Code != http.StatusOK || manual.Body.String() != "passthrough" || passthroughCalls != 1 {
		t.Fatalf("manual raw request did not pass through: status=%d body=%q calls=%d", manual.Code, manual.Body.String(), passthroughCalls)
	}

	continued := serveRawStage(t, env, rawStageRequest(t, "auto", conversationID, "implement", 1))
	if continued.Code != http.StatusOK || !strings.Contains(continued.Body.String(), "response.completed") {
		t.Fatalf("tool-result Auto request failed: %d %s", continued.Code, continued.Body.String())
	}
	if got := <-env.requests; got.Model != "upstream-implement" {
		t.Fatalf("tool-result Auto reused stale manual-era stage lock: %+v", got)
	}
	if passthroughCalls != 1 {
		t.Fatalf("Auto continuation unexpectedly passed through: calls=%d", passthroughCalls)
	}
}

func TestStageRawAutoRejectsLegacyAndDisabledWithoutLock(t *testing.T) {
	oldMode := GetProxyMode()
	SetProxyMode(ModeForward)
	t.Cleanup(func() { SetProxyMode(oldMode) })

	for _, tc := range []struct {
		name      string
		raw       string
		wantError string
	}{
		{
			name:      "legacy_mode",
			raw:       `{"mode":"task_rules","enabled":true,"default_model_id":"default"}`,
			wantError: "legacy",
		},
		{
			name:      "legacy_missing_mode",
			raw:       `{"enabled":true,"default_model_id":"default"}`,
			wantError: "legacy",
		},
		{
			name:      "disabled",
			raw:       `{"mode":"stage_fixed","enabled":false,"default_model_id":"default","stage_models":{"run":"","plan":"","implement":"","review":"","finalize":""}}`,
			wantError: "raw Kiro Responses Auto routing requires enabled stage_fixed mode",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newAutoStageIntegrationEnv(t)
			passthroughCalls := 0
			rawStageProxy(t, env, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				passthroughCalls++
				_, _ = io.WriteString(w, "unexpected passthrough")
			}))
			if err := env.db.SetSetting(store.AutoRoutingSetting, tc.raw); err != nil {
				t.Fatal(err)
			}
			env.server.router.Reload()

			resp := serveRawStage(t, env, rawStageRequest(t, "auto", "raw-no-stage-lock-"+tc.name, "plan", 0))
			if resp.Code != http.StatusServiceUnavailable || !strings.Contains(resp.Body.String(), tc.wantError) {
				t.Fatalf("valid-header Auto request was not rejected: status=%d body=%q", resp.Code, resp.Body.String())
			}
			if passthroughCalls != 0 {
				t.Fatalf("rejected request reached passthrough: %d calls", passthroughCalls)
			}
			select {
			case got := <-env.requests:
				t.Fatalf("rejected request reached model upstream: %+v", got)
			default:
			}
		})
	}
}

func TestStageAutoModelListOmitsUnknownTokenLimitsAndReasoningSchema(t *testing.T) {
	env := newAutoStageIntegrationEnv(t)
	resp := env.server.buildModelsResponse(httptest.NewRequest(http.MethodPost, "/ListAvailableModels", nil))
	defer resp.Body.Close()
	var body struct {
		Models []map[string]interface{} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, entry := range body.Models {
		if entry["modelId"] != "auto" {
			continue
		}
		if _, exists := entry["tokenLimits"]; exists {
			t.Fatalf("unknown stage Auto capacity was advertised on wire: %#v", entry["tokenLimits"])
		}
		if _, exists := entry["additionalModelRequestFieldsSchema"]; exists {
			t.Fatalf("stage Auto fabricated a reasoning schema: %#v", entry["additionalModelRequestFieldsSchema"])
		}
		return
	}
	t.Fatal("model response omitted the stage Auto entry")
}

func TestStageAutoModelListOmitsOnlyUnknownTokenLimit(t *testing.T) {
	for _, tc := range []struct {
		name             string
		contextWindow    int
		maxTokens        int
		wantAdvertised   string
		wantAdvertisedAt int
		wantOmitted      string
	}{
		{name: "unknown input capacity", contextWindow: 0, maxTokens: 5000, wantAdvertised: "maxOutputTokens", wantAdvertisedAt: 5000, wantOmitted: "maxInputTokens"},
		{name: "unknown output capacity", contextWindow: 64000, maxTokens: 0, wantAdvertised: "maxInputTokens", wantAdvertisedAt: 64000, wantOmitted: "maxOutputTokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newAutoStageIntegrationEnv(t)
			for _, id := range []string{"default", "run", "shared-responses", "implement", "finalize"} {
				model := env.models[id]
				model.ContextWindow = tc.contextWindow
				model.MaxTokens = tc.maxTokens
				if err := env.db.UpdateModel(model); err != nil {
					t.Fatal(err)
				}
			}
			env.server.router.Reload()

			resp := env.server.buildModelsResponse(httptest.NewRequest(http.MethodPost, "/ListAvailableModels", nil))
			defer resp.Body.Close()
			var body struct {
				Models []map[string]interface{} `json:"models"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			for _, entry := range body.Models {
				if entry["modelId"] != "auto" {
					continue
				}
				limits, ok := entry["tokenLimits"].(map[string]interface{})
				if !ok {
					t.Fatalf("known capacity missing from tokenLimits: %#v", entry["tokenLimits"])
				}
				if _, exists := limits[tc.wantOmitted]; exists {
					t.Fatalf("unknown capacity was advertised: %#v", limits)
				}
				if got, ok := limits[tc.wantAdvertised].(float64); !ok || int(got) != tc.wantAdvertisedAt {
					t.Fatalf("known capacity %s = %#v, want %d", tc.wantAdvertised, limits[tc.wantAdvertised], tc.wantAdvertisedAt)
				}
				if _, exists := entry["additionalModelRequestFieldsSchema"]; exists {
					t.Fatalf("stage Auto fabricated a reasoning schema: %#v", entry["additionalModelRequestFieldsSchema"])
				}
				return
			}
			t.Fatal("model response omitted the stage Auto entry")
		})
	}
}

func TestStageRoutingEnabledGatesBridgeOnValidConfiguration(t *testing.T) {
	env := newAutoStageIntegrationEnv(t)
	if !env.server.router.StageRoutingEnabled() {
		t.Fatal("valid enabled stage_fixed configuration did not enable bridge gating")
	}

	for _, tc := range []struct {
		name       string
		raw        string
		valid      bool
		parseError bool
	}{
		{name: "legacy_mode", raw: `{"mode":"task_rules","enabled":true,"default_model_id":"default"}`, parseError: true},
		{name: "legacy_missing_mode", raw: `{"enabled":true,"default_model_id":"default"}`, parseError: true},
		{name: "disabled", raw: `{"mode":"stage_fixed","enabled":false,"default_model_id":"default","stage_models":{"run":"","plan":"","implement":"","review":"","finalize":""}}`},
		{name: "invalid", raw: `{"mode":"unknown","enabled":true}`, parseError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := store.ParseAutoRoutingConfig(tc.raw)
			if err != nil {
				if !tc.parseError {
					t.Fatalf("expected valid %s configuration: %v", tc.name, err)
				}
			} else if tc.parseError {
				t.Fatal("legacy or invalid configuration unexpectedly parsed")
			} else {
				models, err := env.db.GetModels()
				if err != nil {
					t.Fatal(err)
				}
				if err := store.ValidateAutoRoutingConfig(parsed, models); err != nil {
					t.Fatalf("expected valid %s configuration: %v", tc.name, err)
				}
			}
			if err := env.db.SetSetting(store.AutoRoutingSetting, tc.raw); err != nil {
				t.Fatal(err)
			}
			env.server.router.Reload()
			if got := env.server.router.StageRoutingEnabled(); got != tc.valid {
				t.Fatalf("StageRoutingEnabled() = %t, want %t", got, tc.valid)
			}
		})
	}
}
