//go:build !windows

package proxy

import "fmt"

func replaceKiroStageFile(_, _ string) error {
	return fmt.Errorf("当前系统不支持安装 Windows Kiro 阶段桥接")
}
