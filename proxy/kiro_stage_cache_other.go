//go:build !windows

package proxy

import (
	"os"
)

func platformKiroStageCacheProtection() (func([]byte) ([]byte, error), func([]byte) ([]byte, error), error) {
	return nil, nil, errKiroStageVerificationUnsupported
}

// 阶段桥接仅支持 Windows。此处使用禁止替换的硬链接让注入式缓存测试可在其他平台运行，生产保护路径仍保持不支持。
func atomicKiroStageCachePublish(from, to string) error {
	if err := os.Link(from, to); err != nil {
		return err
	}
	return nil
}

func readKiroStageCacheFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
