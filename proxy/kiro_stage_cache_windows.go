//go:build windows

package proxy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformKiroStageCacheProtection() (func([]byte) ([]byte, error), func([]byte) ([]byte, error), error) {
	return protectKiroStageCacheData, unprotectKiroStageCacheData, nil
}

// DPAPI 使用当前 Windows 用户范围；凭据不与本可执行文件绑定，也不能阻止同一用户下的其他进程调用 DPAPI。
func protectKiroStageCacheData(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("DPAPI 输入不能为空")
	}
	input := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var output windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		return nil, err
	}
	if output.Data == nil || output.Size == 0 {
		return nil, errors.New("DPAPI 加密成功但没有返回数据")
	}
	defer func() {
		_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	}()
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}

func unprotectKiroStageCacheData(protected []byte) ([]byte, error) {
	if len(protected) == 0 {
		return nil, errors.New("DPAPI 输入不能为空")
	}
	input := windows.DataBlob{Size: uint32(len(protected)), Data: &protected[0]}
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		return nil, err
	}
	if output.Data == nil || output.Size == 0 {
		return nil, errors.New("DPAPI 解密成功但没有返回数据")
	}
	defer func() {
		_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	}()
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}

func atomicKiroStageCachePublish(from, to string) error {
	fromPointer, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return fmt.Errorf("编码 Kiro 阶段验证凭据临时路径失败: %w", err)
	}
	toPointer, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return fmt.Errorf("编码 Kiro 阶段验证凭据目标路径失败: %w", err)
	}
	return windows.MoveFileEx(fromPointer, toPointer, windows.MOVEFILE_WRITE_THROUGH)
}

func readKiroStageCacheFile(path string) ([]byte, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: errors.New("打开 Kiro 阶段验证凭据句柄失败")}
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return nil, err
		}
		return nil, &os.PathError{Op: "read", Path: path, Err: err}
	}
	return data, nil
}
