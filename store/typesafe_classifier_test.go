package store

import (
	"math"
	"strings"
	"testing"
)

func TestParseTypesafeClassifierConfigDefaultsAndNormalization(t *testing.T) {
	config, err := ParseTypesafeClassifierConfig("")
	if err != nil {
		t.Fatal(err)
	}
	want := TypesafeClassifierConfig{
		BaseURL:              "https://api.typesafe.ai",
		Model:                "jev-1.13.0",
		TimeoutSeconds:       10,
		InputPricePerMillion: 0.042,
	}
	if config != want {
		t.Fatalf("default config = %#v, want %#v", config, want)
	}

	config, err = ParseTypesafeClassifierConfig(`{"enabled":false,"base_url":" https://typesafe.example/v1/ ","api_key":" key ","model":" custom ","timeout_seconds":0,"input_price_per_million":0,"proxy_url":"  ","extra":null}`)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v, want strict field rejection", err)
	}
	config, err = ParseTypesafeClassifierConfig(`{"base_url":" https://typesafe.example/v1/ ","api_key":" key ","model":" custom ","timeout_seconds":0,"input_price_per_million":0,"proxy_url":"  "}`)
	if err != nil {
		t.Fatal(err)
	}
	if config.BaseURL != "https://typesafe.example/v1" || config.APIKey != "key" || config.Model != "custom" || config.TimeoutSeconds != 10 || config.InputPricePerMillion != 0.042 || config.ProxyURL != "" {
		t.Fatalf("normalized config = %#v", config)
	}
}

func TestParseTypesafeClassifierConfigRejectsInvalidInput(t *testing.T) {
	invalid := []struct {
		name string
		raw  string
	}{
		{"non-object", `[]`},
		{"unknown-field", `{"unexpected":true}`},
		{"trailing-value", `{} {}`},
		{"bad-scheme", `{"base_url":"ftp://typesafe.example"}`},
		{"missing-host", `{"base_url":"https://"}`},
		{"userinfo", `{"base_url":"https://user:pass@typesafe.example"}`},
		{"query", `{"base_url":"https://typesafe.example?x=1"}`},
		{"fragment", `{"base_url":"https://typesafe.example/#part"}`},
		{"timeout-low", `{"timeout_seconds":-1}`},
		{"timeout-high", `{"timeout_seconds":121}`},
		{"price-negative", `{"input_price_per_million":-0.01}`},
		{"proxy-invalid", `{"proxy_url":"file:///tmp/proxy"}`},
		{"enabled-without-key", `{"enabled":true}`},
		{"mask-is-not-key", `{"api_key":"****"}`},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseTypesafeClassifierConfig(test.raw); err == nil {
				t.Fatalf("ParseTypesafeClassifierConfig(%s) succeeded, want error", test.raw)
			}
		})
	}

	config, err := ParseTypesafeClassifierConfig(`{"api_key":"test"}`)
	if err != nil {
		t.Fatal(err)
	}
	config.InputPricePerMillion = math.NaN()
	if err := ValidateTypesafeClassifierConfig(config); err == nil {
		t.Fatal("NaN input price passed validation")
	}
}
