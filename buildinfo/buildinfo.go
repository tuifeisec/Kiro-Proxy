// Package buildinfo 从当前可执行文件读取构建身份，不把工作区 HEAD 当成运行版本。
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

type Info struct {
	Revision  string `json:"revision"`
	Time      string `json:"time,omitempty"`
	Modified  *bool  `json:"modified"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func Current() Info {
	result := Info{Revision: "unknown", GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				result.Revision = setting.Value
			case "vcs.time":
				result.Time = setting.Value
			case "vcs.modified":
				modified := setting.Value == "true"
				result.Modified = &modified
			}
		}
	}
	return result
}
