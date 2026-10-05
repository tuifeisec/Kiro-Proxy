package proxy

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

type kiroProcessCommandResult struct {
	output   []byte
	exitCode int
	err      error
}

type kiroProcessCommandRunner func(name string, args ...string) kiroProcessCommandResult

func checkKiroProcess() (bool, error) {
	return checkKiroProcessWith(runtime.GOOS, runKiroProcessCommand)
}

func checkKiroProcessWith(platform string, run kiroProcessCommandRunner) (bool, error) {
	if run == nil {
		return false, fmt.Errorf("查询 Kiro 进程失败: 未设置命令执行器")
	}
	switch platform {
	case "windows":
		result := run("tasklist", "/FI", "IMAGENAME eq Kiro.exe", "/FO", "CSV", "/NH")
		if err := kiroProcessCommandError("查询 Kiro 进程失败", result); err != nil {
			return false, err
		}
		if result.exitCode != 0 {
			return false, fmt.Errorf("查询 Kiro 进程失败: tasklist 返回退出码 %d", result.exitCode)
		}
		running, err := parseKiroTasklistOutput(result.output)
		if err != nil {
			return false, fmt.Errorf("解析 tasklist 输出失败: %w", err)
		}
		return running, nil
	case "darwin":
		result := run("pgrep", "-f", "/Kiro.app/")
		if result.err != nil {
			if result.exitCode == 1 {
				return false, nil
			}
			return false, kiroProcessCommandError("查询 Kiro 进程失败", result)
		}
		if result.exitCode != 0 {
			return false, fmt.Errorf("查询 Kiro 进程失败: pgrep 返回退出码 %d", result.exitCode)
		}
		fields := strings.Fields(string(result.output))
		if len(fields) == 0 {
			return false, fmt.Errorf("查询 Kiro 进程失败: pgrep 成功但未返回进程号")
		}
		for _, field := range fields {
			if _, err := strconv.Atoi(field); err != nil {
				return false, fmt.Errorf("解析 pgrep 进程号失败: %w", err)
			}
		}
		return true, nil
	default:
		return false, fmt.Errorf("当前系统暂不支持查询 Kiro 进程: %s", platform)
	}
}

func parseKiroTasklistOutput(output []byte) (bool, error) {
	reader := csv.NewReader(strings.NewReader(string(output)))
	reader.FieldsPerRecord = -1
	for {
		record, err := reader.Read()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if len(record) == 0 {
			continue
		}
		imageName := strings.TrimSpace(record[0])
		if strings.HasPrefix(strings.ToUpper(imageName), "INFO:") {
			continue
		}
		if strings.EqualFold(imageName, "Kiro.exe") {
			return true, nil
		}
	}
}

func runKiroProcessCommand(name string, args ...string) kiroProcessCommandResult {
	cmd := hiddenCmd(name, args...)
	output, err := cmd.CombinedOutput()
	result := kiroProcessCommandResult{output: output, err: err}
	if err != nil {
		result.exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.exitCode = exitErr.ExitCode()
		}
	}
	return result
}

func kiroProcessCommandError(context string, result kiroProcessCommandResult) error {
	if result.err == nil {
		return nil
	}
	if detail := strings.TrimSpace(string(result.output)); detail != "" {
		return fmt.Errorf("%s: %w: %s", context, result.err, detail)
	}
	return fmt.Errorf("%s: %w", context, result.err)
}

func stopKiroProcessesWith(platform string, run kiroProcessCommandRunner, logger *TrafficLogger) error {
	if run == nil {
		return fmt.Errorf("停止 Kiro 失败: 未设置命令执行器")
	}
	switch platform {
	case "windows":
		normal := run("taskkill", "/T", "/IM", "Kiro.exe")
		if normal.err == nil {
			return nil
		}
		normalErr := kiroProcessCommandError("正常关闭 Kiro 进程树失败", normal)
		logKiroConfig(logger, normalErr.Error()+"；尝试强制结束")
		force := run("taskkill", "/F", "/T", "/IM", "Kiro.exe")
		if force.err == nil {
			return nil
		}
		forceErr := kiroProcessCommandError("强制结束 Kiro 进程树失败", force)
		return errors.Join(normalErr, forceErr)
	case "darwin":
		var failures []error
		quit := run("osascript", "-e", `tell application "Kiro" to quit`)
		if err := kiroProcessCommandError("请求 Kiro 正常退出失败", quit); err != nil {
			logKiroConfig(logger, err.Error()+"；尝试结束 Kiro 进程")
			failures = append(failures, err)
		}
		for _, command := range []struct {
			args []string
			name string
		}{
			{name: "结束 Kiro 主进程失败", args: []string{"-x", "Kiro"}},
			{name: "结束 Kiro 应用进程树失败", args: []string{"-f", "/Kiro.app/"}},
		} {
			result := run("pkill", command.args...)
			if result.err != nil && result.exitCode != 1 {
				err := kiroProcessCommandError(command.name, result)
				logKiroConfig(logger, err.Error())
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	default:
		return fmt.Errorf("当前系统暂不支持停止 Kiro 进程: %s", platform)
	}
}
