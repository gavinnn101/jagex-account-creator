package browser

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// CacheManager owns a pool of persistent disk cache dirs for Chrome. Each Chrome
// uses one dir in place through --disk-cache-dir, so the next Chrome on that dir
// reads its hits with no copy.
// Limitation: a second creator process shares the dirs. Its Chrome runs cold on a
// shared dir, and a reset in one process can remove the dir under the other's Chrome.
type CacheManager struct {
	baseDir string
	mu      sync.Mutex
	busy    []bool // busy[i] is true while a Chrome holds dir i
	logger  *slog.Logger
}

// cacheLease is the cache dir that one Chrome holds until release.
type cacheLease struct {
	index int
	dir   string
}

// maxCacheBytes is the size past which release resets a dir. Chrome does not trim
// its HTTP cache, even under --disk-cache-size, so a dir grows on each run.
const maxCacheBytes = 64 << 20

// NewCacheManager creates a CacheManager rooted at cacheDir.
// If cacheDir is empty, a default under os.UserCacheDir is used.
func NewCacheManager(cacheDir string, logger *slog.Logger) (*CacheManager, error) {
	if cacheDir == "" {
		dir, err := appCacheDir("browser-cache")
		if err != nil {
			return nil, err
		}
		cacheDir = dir
	}
	return &CacheManager{
		baseDir: cacheDir,
		logger:  logger.With("component", "cache"),
	}, nil
}

// acquire takes the lowest free dir of the pool and creates it. Dir 0 is the
// base dir, and dir i is the base dir with the suffix -i.
func (cm *CacheManager) acquire() (cacheLease, error) {
	cm.mu.Lock()
	i := slices.Index(cm.busy, false)
	if i < 0 {
		i = len(cm.busy)
		cm.busy = append(cm.busy, true)
	} else {
		cm.busy[i] = true
	}
	cm.mu.Unlock()

	dir := cm.baseDir
	if i > 0 {
		dir = fmt.Sprintf("%s-%d", cm.baseDir, i)
	}
	// acquire creates the dir, because release can remove it.
	if err := os.MkdirAll(dir, 0755); err != nil {
		cm.free(i)
		return cacheLease{}, fmt.Errorf("creating cache dir: %w", err)
	}
	return cacheLease{index: i, dir: dir}, nil
}

// release resets the dir of lease when it is over maxCacheBytes, then frees its
// index. The index stays busy during the reset, so the reset needs no lock.
func (cm *CacheManager) release(lease cacheLease) {
	if size := dirSize(lease.dir); size > maxCacheBytes {
		if err := os.RemoveAll(lease.dir); err != nil {
			cm.logger.Warn("failed to reset browser cache dir", "index", lease.index, "error", err)
		} else {
			cm.logger.Info("browser cache dir was over the size limit, removed it", "index", lease.index, "bytes", size)
		}
	}
	cm.free(lease.index)
}

// free marks index i of the pool as free.
func (cm *CacheManager) free(i int) {
	cm.mu.Lock()
	cm.busy[i] = false
	cm.mu.Unlock()
}

// chromeTempBase returns the dir for Chrome profiles, under the user cache dir and not os.TempDir.
// Snap-confined Chromium cannot reach /tmp, but it can reach $HOME/.cache.
func chromeTempBase() (string, error) {
	dir, err := appCacheDir("tmp")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return dir, nil
}

// appDirName is the directory of this tool under the user cache dir.
const appDirName = "jagex-creator"

// appCacheDir returns the path of sub under the app directory in the user cache dir.
func appCacheDir(sub string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("finding user cache dir: %w", err)
	}
	return filepath.Join(base, appDirName, sub), nil
}

// dirSize returns the total file size under dir.
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // The size is best effort. The walk skips an unreadable entry.
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // The size is best effort. The walk skips a file that it cannot stat.
		}
		total += info.Size()
		return nil
	})
	return total
}
