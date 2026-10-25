//go:build !windows

package browser

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// readChromeVersion runs "<chromePath> --version" and returns the trimmed
// output. The run works through wrapper scripts and snap indirection.
func readChromeVersion(ctx context.Context, chromePath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, chromePath, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("running %s --version: %w", chromePath, err)
	}
	return strings.TrimSpace(string(out)), nil
}
