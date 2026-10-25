//go:build windows

package browser

import "os/exec"

// setProcGroup does nothing on Windows.
func setProcGroup(_ *exec.Cmd) {}

// killGroup ends only the main Chrome process, because setProcGroup sets no
// group on Windows. Process.Kill calls TerminateProcess.
func killGroup(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}
