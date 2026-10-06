//go:build windows

package proxy

import "golang.org/x/sys/windows"

func replaceKiroStageFile(candidatePath, targetPath string) error {
	from, err := windows.UTF16PtrFromString(candidatePath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
