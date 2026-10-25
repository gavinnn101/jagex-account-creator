package browser

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func TestKillEndsTheProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv(fakeChildPIDEnv, pidFile)
	l := NewLauncher(slog.Default(), nil)
	defer l.Kill()
	if err := launchFake(t.Context(), t, l, "group"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	// A pidfd reads as ready when its process exits, so the wait needs no poll loop.
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()

	l.Kill()

	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if n, err := unix.Poll(fds, 2000); err != nil || n != 1 {
		t.Fatalf("Poll = %d, %v: the child of the fake Chrome outlived Kill", n, err)
	}
}
