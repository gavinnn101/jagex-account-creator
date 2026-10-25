//go:build !windows

package browser

import (
	"os/exec"
	"testing"
)

func TestSetProcGroup(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "true")

	setProcGroup(cmd)

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Errorf("SysProcAttr = %+v, want Setpgid", cmd.SysProcAttr)
	}
}
