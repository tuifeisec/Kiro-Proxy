//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const singleInstanceMessage = "当前已经有运行了，无法重复运行"

var (
	singleInstanceUser32 = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW      = singleInstanceUser32.NewProc("MessageBoxW")
	singleInstanceName   = windows.StringToUTF16Ptr(`Local\KiroProxySingleInstance`)
)

func acquireSingleInstanceLock() (func(), bool, error) {
	handle, err := windows.CreateMutex(nil, false, singleInstanceName)
	if handle == 0 {
		return nil, false, fmt.Errorf("创建单实例锁失败: %w", err)
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(handle)
		return nil, false, nil
	}
	return func() { windows.CloseHandle(handle) }, true, nil
}

func notifySingleInstanceRunning() {
	fmt.Println(singleInstanceMessage)
	title := windows.StringToUTF16Ptr("Kiro Proxy")
	message := windows.StringToUTF16Ptr(singleInstanceMessage)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x00000030)
}
