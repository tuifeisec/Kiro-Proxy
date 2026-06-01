//go:build desktop && darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func useDesktopDataDir() error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("获取用户配置目录失败: %w", err)
	}

	dataDir := filepath.Join(configDir, "Kiro Proxy")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}
	if err := os.Chdir(dataDir); err != nil {
		return fmt.Errorf("切换数据目录失败: %w", err)
	}
	return nil
}
