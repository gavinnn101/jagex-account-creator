package browser

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"jagex_account_creator/internal/logtest"
	"jagex_account_creator/internal/waittest"
)

func TestChromeTempBase(t *testing.T) {
	base := t.TempDir()
	setCacheEnv(t, base)

	// The second call finds the dir that the first call created.
	var first string
	for i := range 2 {
		dir, err := chromeTempBase()
		if err != nil {
			t.Fatalf("call %d: chromeTempBase() error: %v", i+1, err)
		}
		if i == 0 {
			first = dir
		} else if dir != first {
			t.Fatalf("call 2: chromeTempBase() = %q, call 1 = %q", dir, first)
		}
		if !strings.HasPrefix(dir, base) || !strings.HasSuffix(dir, filepath.Join(appDirName, "tmp")) {
			t.Fatalf("call %d: chromeTempBase() = %q, want a path under %q that ends in %s", i+1, dir, base, filepath.Join(appDirName, "tmp"))
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("call %d: Stat(%q) = %v, want a dir", i+1, dir, err)
		}
	}
}

func TestAppCacheDirWithoutUserCacheDir(t *testing.T) {
	tests := []struct {
		name string
		call func() error
	}{
		{name: "uaCachePath", call: func() error { _, err := uaCachePath(); return err }},
		{name: "chromeTempBase", call: func() error { _, err := chromeTempBase(); return err }},
		{name: "NewCacheManager", call: func() error { _, err := NewCacheManager("", slog.Default()); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setCacheEnv(t, "")

			if err := tt.call(); err == nil || !strings.HasPrefix(err.Error(), "finding user cache dir") {
				t.Errorf("%s error = %v, want a \"finding user cache dir\" error", tt.name, err)
			}
		})
	}
}

// newTestCacheManager returns a CacheManager whose base dir is under a temp dir.
func newTestCacheManager(t *testing.T) *CacheManager {
	t.Helper()
	cm, err := NewCacheManager(filepath.Join(t.TempDir(), "browser-cache"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return cm
}

// mustAcquire acquires a lease and checks that its dir exists.
func mustAcquire(t *testing.T, cm *CacheManager) cacheLease {
	t.Helper()
	lease, err := cm.acquire()
	if err != nil {
		t.Fatalf("acquire() error: %v", err)
	}
	if info, err := os.Stat(lease.dir); err != nil || !info.IsDir() {
		t.Fatalf("Stat(%q) = %v, want a dir", lease.dir, err)
	}
	return lease
}

// poolDir returns the path that dir i of the pool of cm must have.
func poolDir(cm *CacheManager, i int) string {
	if i == 0 {
		return cm.baseDir
	}
	return cm.baseDir + "-" + strconv.Itoa(i)
}

// wantLease fails the test when got is not index i of the pool of cm.
func wantLease(t *testing.T, cm *CacheManager, got cacheLease, i int) {
	t.Helper()
	if want := (cacheLease{index: i, dir: poolDir(cm, i)}); got != want {
		t.Fatalf("lease = %+v, want %+v", got, want)
	}
}

func TestCacheManagerAcquire(t *testing.T) {
	cm := newTestCacheManager(t)

	wantLease(t, cm, mustAcquire(t, cm), 0)
	wantLease(t, cm, mustAcquire(t, cm), 1)
}

func TestCacheManagerAcquireReusesLowestFree(t *testing.T) {
	cm := newTestCacheManager(t)
	leases := []cacheLease{mustAcquire(t, cm), mustAcquire(t, cm), mustAcquire(t, cm)}

	cm.release(leases[2])
	cm.release(leases[0])

	wantLease(t, cm, mustAcquire(t, cm), 0)
	wantLease(t, cm, mustAcquire(t, cm), 2)
	wantLease(t, cm, mustAcquire(t, cm), 3)
}

// writeSparseFile writes a file of the given logical size under dir.
func writeSparseFile(t *testing.T, dir string, size int64) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "data_0"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

func TestCacheManagerReleaseResetsOverLimit(t *testing.T) {
	tests := []struct {
		name     string
		size     int64
		wantKeep bool
		wantLog  string
	}{
		{name: "at the limit", size: maxCacheBytes, wantKeep: true},
		{
			name:     "over the limit",
			size:     maxCacheBytes + 1,
			wantKeep: false,
			wantLog:  fmt.Sprintf("level=INFO msg=\"browser cache dir was over the size limit, removed it\" component=cache index=0 bytes=%d\n", maxCacheBytes+1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log strings.Builder
			cm, err := NewCacheManager(filepath.Join(t.TempDir(), "browser-cache"), logtest.New(&log))
			if err != nil {
				t.Fatal(err)
			}
			lease := mustAcquire(t, cm)
			writeSparseFile(t, lease.dir, tt.size)

			cm.release(lease)

			_, err = os.Stat(filepath.Join(lease.dir, "data_0"))
			if kept := err == nil; kept != tt.wantKeep {
				t.Errorf("file kept = %v (Stat error %v), want %v", kept, err, tt.wantKeep)
			}
			// The test compares the whole log, so a WARN line in place of the INFO line fails.
			if got := log.String(); got != tt.wantLog {
				t.Errorf("log = %q, want %q", got, tt.wantLog)
			}
			// The index is free, and acquire creates the dir again after a reset.
			wantLease(t, cm, mustAcquire(t, cm), 0)
		})
	}
}

func TestCacheManagerReleaseMissingDir(t *testing.T) {
	cm := newTestCacheManager(t)
	lease := mustAcquire(t, cm)
	if err := os.RemoveAll(lease.dir); err != nil {
		t.Fatal(err)
	}

	cm.release(lease)

	wantLease(t, cm, mustAcquire(t, cm), 0)
}

func TestCacheManagerAcquireFailureFreesIndex(t *testing.T) {
	// A file in the place of a parent dir makes MkdirAll fail.
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, nil, 0644); err != nil {
		t.Fatal(err)
	}
	cm, err := NewCacheManager(filepath.Join(parent, "browser-cache"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	if lease, err := cm.acquire(); err == nil {
		t.Fatalf("acquire() = %+v, want an error", lease)
	}
	if slices.Contains(cm.busy, true) {
		t.Errorf("busy = %v after a failed acquire, want no busy index", cm.busy)
	}
}

func TestCacheManagerConcurrentAcquire(t *testing.T) {
	cm := newTestCacheManager(t)
	const n = 16
	leases := make([]cacheLease, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { leases[i], errs[i] = cm.acquire() })
	}
	// acquire creates dirs on disk, so the wait is on the real clock under a guard.
	waittest.Wait(t, &wg)

	seen := make(map[string]bool, n)
	for i, lease := range leases {
		if errs[i] != nil {
			t.Fatalf("acquire() error: %v", errs[i])
		}
		if seen[lease.dir] {
			t.Fatalf("dir %q acquired two times", lease.dir)
		}
		seen[lease.dir] = true
	}
	for i := range n {
		if dir := poolDir(cm, i); !seen[dir] {
			t.Errorf("no lease holds dir %q", dir)
		}
	}
}
