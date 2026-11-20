//go:build windows

package download

import (
	"os/exec"
	"strconv"
	"syscall"
)

func setupCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Cancel = func() error {
		kill := exec.Command("taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := kill.Run(); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
