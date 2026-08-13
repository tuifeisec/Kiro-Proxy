package rules

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Proxy ProxyConfig `yaml:"proxy"`
}

type ProxyConfig struct {
	Port       int    `yaml:"port"`
	Verbose    bool   `yaml:"verbose"`
	LogFile    string `yaml:"log_file"`
	LogDetail  string `yaml:"log_detail"`   // summary | full
	LogMaxBody int    `yaml:"log_max_body"` // summary 模式下 detail 最大字节数
	// ForwardProxyURL 仅用于经过 MITM 放行的认证、遥测等请求。
	ForwardProxyURL string `yaml:"forward_proxy_url"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := *DefaultConfig()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	cfg.Normalize()
	return &cfg, nil
}

func (c *Config) Normalize() {
	if c.Proxy.Port == 0 {
		c.Proxy.Port = 18081
	}
	if c.Proxy.LogDetail != "full" {
		c.Proxy.LogDetail = "summary"
	}
	if c.Proxy.LogMaxBody <= 0 {
		c.Proxy.LogMaxBody = 64 * 1024
	}
}

func DefaultConfig() *Config {
	return &Config{
		Proxy: ProxyConfig{
			Port:       18081,
			Verbose:    true,
			LogFile:    "kiro_traffic.log",
			LogDetail:  "summary",
			LogMaxBody: 64 * 1024,
		},
	}
}
