package store

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net/url"
	"strings"

	"kiro-proxy/upstreamproxy"

	json "github.com/goccy/go-json"
)

const TypesafeClassifierSetting = "typesafe_classifier_config"
const TypesafeAPIKeyMask = "****"

const (
	defaultTypesafeBaseURL          = "https://api.typesafe.ai"
	defaultTypesafeModel            = "jev-1.13.0"
	defaultTypesafeTimeoutSeconds   = 10
	defaultTypesafeInputPricePerMil = 0.042
)

// TypesafeClassifierConfig 保存 Typesafe 意图分类器配置。
type TypesafeClassifierConfig struct {
	Enabled              bool    `json:"enabled"`
	BaseURL              string  `json:"base_url"`
	APIKey               string  `json:"api_key"`
	Model                string  `json:"model"`
	TimeoutSeconds       int     `json:"timeout_seconds"`
	InputPricePerMillion float64 `json:"input_price_per_million"`
	ProxyURL             string  `json:"proxy_url"`
}

// ParseTypesafeClassifierConfig 解析并规范化持久化配置对象。
func ParseTypesafeClassifierConfig(raw string) (TypesafeClassifierConfig, error) {
	config := TypesafeClassifierConfig{
		BaseURL:              defaultTypesafeBaseURL,
		Model:                defaultTypesafeModel,
		TimeoutSeconds:       defaultTypesafeTimeoutSeconds,
		InputPricePerMillion: defaultTypesafeInputPricePerMil,
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return config, nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return TypesafeClassifierConfig{}, fmt.Errorf("configuration must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return TypesafeClassifierConfig{}, fmt.Errorf("invalid configuration: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return TypesafeClassifierConfig{}, fmt.Errorf("configuration must contain only one JSON value")
		}
		return TypesafeClassifierConfig{}, fmt.Errorf("invalid trailing data: %w", err)
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		config.BaseURL = defaultTypesafeBaseURL
	}
	if strings.TrimSpace(config.Model) == "" {
		config.Model = defaultTypesafeModel
	}
	if config.TimeoutSeconds == 0 {
		config.TimeoutSeconds = defaultTypesafeTimeoutSeconds
	}
	if config.InputPricePerMillion == 0 {
		config.InputPricePerMillion = defaultTypesafeInputPricePerMil
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.Model = strings.TrimSpace(config.Model)
	config.ProxyURL = strings.TrimSpace(config.ProxyURL)
	if err := ValidateTypesafeClassifierConfig(config); err != nil {
		return TypesafeClassifierConfig{}, err
	}
	return config, nil
}

// ValidateTypesafeClassifierConfig 校验规范化后的 Typesafe 配置。
func ValidateTypesafeClassifierConfig(config TypesafeClassifierConfig) error {
	if config.APIKey == TypesafeAPIKeyMask {
		return fmt.Errorf("api_key must not be the masked placeholder")
	}
	if config.Enabled && config.APIKey == "" {
		return fmt.Errorf("api_key is required when Typesafe classifier is enabled")
	}
	if config.BaseURL == "" {
		return fmt.Errorf("base_url is required")
	}
	baseURL, err := url.Parse(config.BaseURL)
	if err != nil {
		return fmt.Errorf("invalid base_url: %w", err)
	}
	if (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" || baseURL.Opaque != "" {
		return fmt.Errorf("base_url must be an absolute HTTP or HTTPS URL")
	}
	if baseURL.User != nil || baseURL.RawQuery != "" || baseURL.ForceQuery || strings.Contains(config.BaseURL, "#") {
		return fmt.Errorf("base_url must not contain user info, query, or fragment")
	}
	if config.Model == "" {
		return fmt.Errorf("model is required")
	}
	if config.TimeoutSeconds < 1 || config.TimeoutSeconds > 120 {
		return fmt.Errorf("timeout_seconds must be between 1 and 120")
	}
	if config.InputPricePerMillion <= 0 || math.IsNaN(config.InputPricePerMillion) || math.IsInf(config.InputPricePerMillion, 0) {
		return fmt.Errorf("input_price_per_million must be a finite positive number")
	}
	if _, err := upstreamproxy.ParseProxyURL(config.ProxyURL); err != nil {
		return fmt.Errorf("invalid proxy_url: %w", err)
	}
	return nil
}
