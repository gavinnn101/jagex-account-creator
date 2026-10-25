//go:build !windows

package browser

import (
	"os/exec"
	"syscall"
)

// setProcGroup puts the process into its own process group, so killGroup can
// end the entire group at once.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the entire process group rooted at cmd.
func killGroup(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
