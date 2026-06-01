//go:build desktop && !darwin

package main

func useDesktopDataDir() error {
	return nil
}
