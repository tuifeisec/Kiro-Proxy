package api

import (
	"net/http"
	"testing"

	"kiro-proxy/store"
)

func TestClassifierSelectionPutDisablesTypesafeAndPreservesConfig(t *testing.T) {
	db, server := newTypesafeSettingsTestServer(t)
	initialConfig := `{"enabled":true,"base_url":"https://typesafe.example/v1","api_key":"existing-key","model":"custom-model","timeout_seconds":30,"input_price_per_million":0.5,"proxy_url":"http://127.0.0.1:7890"}`
	if err := db.SetSetting(store.TypesafeClassifierSetting, initialConfig); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("classifier_model_id", "legacy-model"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("unrelated_setting", "keep me"); err != nil {
		t.Fatal(err)
	}

	configWithMask := `{"enabled":false,"base_url":"https://typesafe.example/v1","api_key":"****","model":"custom-model","timeout_seconds":30,"input_price_per_million":0.5,"proxy_url":"http://127.0.0.1:7890"}`
	status := putTypesafeSettings(t, server, configWithMask, map[string]string{
		"classifier_model_id": "ordinary-model",
	})
	if status != http.StatusOK {
		t.Fatalf("selection PUT status=%d", status)
	}

	storedRaw, err := db.GetSetting(store.TypesafeClassifierSetting)
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.ParseTypesafeClassifierConfig(storedRaw)
	if err != nil {
		t.Fatalf("parse stored Typesafe config: %v", err)
	}
	if config.Enabled || config.APIKey != "existing-key" || config.BaseURL != "https://typesafe.example/v1" || config.Model != "custom-model" || config.TimeoutSeconds != 30 || config.InputPricePerMillion != 0.5 || config.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("stored Typesafe config = %#v", config)
	}
	for key, want := range map[string]string{
		"classifier_model_id": "ordinary-model",
		"unrelated_setting":   "keep me",
	} {
		got, err := db.GetSetting(key)
		if err != nil || got != want {
			t.Errorf("GetSetting(%q) = %q, error=%v; want %q", key, got, err, want)
		}
	}
}

func TestClassifierSelectionCannotEnableTypesafeWithoutKey(t *testing.T) {
	db, server := newTypesafeSettingsTestServer(t)
	if err := db.SetSetting("classifier_model_id", "legacy-model"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("unrelated_setting", "before"); err != nil {
		t.Fatal(err)
	}

	status := putTypesafeSettings(t, server, `{"enabled":true,"api_key":""}`, map[string]string{
		"classifier_model_id": "must-not-save",
		"unrelated_setting":   "after",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("selection PUT status=%d, want %d", status, http.StatusBadRequest)
	}
	for key, want := range map[string]string{
		"classifier_model_id": "legacy-model",
		"unrelated_setting":   "before",
	} {
		got, err := db.GetSetting(key)
		if err != nil || got != want {
			t.Errorf("GetSetting(%q) = %q, error=%v; want %q", key, got, err, want)
		}
	}
	configRaw, err := db.GetSetting(store.TypesafeClassifierSetting)
	if err != nil || configRaw != "" {
		t.Errorf("Typesafe config = %q, error=%v; want no stored config", configRaw, err)
	}
}
