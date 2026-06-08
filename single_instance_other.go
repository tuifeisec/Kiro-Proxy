//go:build !windows

package main

import "fmt"

const singleInstanceMessage = "当前已经有运行了，无法重复运行"

func acquireSingleInstanceLock() (func(), bool, error) {
	return func() {}, true, nil
}

func notifySingleInstanceRunning() {
	fmt.Println(singleInstanceMessage)
}
