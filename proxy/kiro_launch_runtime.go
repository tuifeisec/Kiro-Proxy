package proxy

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

type kiroRestartDriver struct {
	running func() (bool, error)
	stop    func(*TrafficLogger) error
	wait    func()
	start   func(kiroLaunchPlan) error
}

func launchPreparedKiro(plan kiroLaunchPlan, logger *TrafficLogger) error {
	return executeKiroRestart(plan, logger, kiroRestartDriver{
		running: checkKiroProcess,
		stop:    stopKiroProcesses,
		wait:    func() { time.Sleep(2 * time.Second) },
		start:   startKiroProcess,
	})
}

func executeKiroRestart(plan kiroLaunchPlan, logger *TrafficLogger, driver kiroRestartDriver) error {
	if driver.running == nil {
		return fmt.Errorf("检查 Kiro 进程状态失败: 未设置状态检查器")
	}
	if driver.start == nil {
		return fmt.Errorf("启动 Kiro 失败: 未设置进程启动器")
	}
	logKiroConfig(logger, "正在重启 Kiro...")
	running, err := driver.running()
	if err != nil {
		return fmt.Errorf("检查 Kiro 进程状态失败: %w", err)
	}
	if running {
		if driver.stop == nil {
			return fmt.Errorf("停止 Kiro 失败: 未设置进程停止器")
		}
		stopErr := driver.stop(logger)
		if driver.wait != nil {
			driver.wait()
		}
		stillRunning, err := driver.running()
		if err != nil {
			if stopErr != nil {
				return fmt.Errorf("停止 Kiro 失败: %v；停止后检查进程状态失败: %w", stopErr, err)
			}
			return fmt.Errorf("停止后检查 Kiro 进程状态失败: %w", err)
		}
		if stillRunning {
			if stopErr != nil {
				return fmt.Errorf("停止 Kiro 失败: %w；旧进程仍在运行", stopErr)
			}
			return fmt.Errorf("旧 Kiro 进程仍在运行，取消新实例启动")
		}
		if stopErr != nil {
			logKiroConfig(logger, "停止命令失败，但已确认旧 Kiro 进程退出: "+stopErr.Error())
		}
	}
	if err := driver.start(plan); err != nil {
		return fmt.Errorf("启动 Kiro 失败: %w", err)
	}
	logKiroConfig(logger, "Kiro 启动命令已提交")
	return nil
}

func stopKiroProcesses(logger *TrafficLogger) error {
	return stopKiroProcessesWith(runtime.GOOS, runKiroProcessCommand, logger)
}

func startKiroProcess(plan kiroLaunchPlan) error {
	cmd := hiddenCmd(plan.executable, plan.args...)
	cmd.Env = plan.env
	if plan.platform == "darwin" {
		output, err := cmd.CombinedOutput()
		if err != nil {
			if detail := strings.TrimSpace(string(output)); detail != "" {
				return fmt.Errorf("%w: %s", err, detail)
			}
			return err
		}
		return nil
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
