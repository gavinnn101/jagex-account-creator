package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/page"
)

// debugScreenshotDir is the directory under the working directory that holds the debug screenshots.
const debugScreenshotDir = "debug_screenshots"

// screenshot captures a PNG of the page and saves it to a new file named
// <name>_<random>.png in debugScreenshotDir. It returns the file path. The
// capture runs first, so a failed capture leaves no directory and no file.
func screenshot(ctx context.Context, c *cdp.Client, name string) (string, error) {
	args := page.NewCaptureScreenshotArgs().SetFormat("png")
	reply, err := c.Page.CaptureScreenshot(ctx, args)
	if err != nil {
		return "", fmt.Errorf("capturing screenshot: %w", err)
	}

	if err := os.MkdirAll(debugScreenshotDir, 0755); err != nil {
		return "", fmt.Errorf("creating screenshot directory: %w", err)
	}

	// Each capture gets its own file, so the captures of parallel attempts never overwrite each other.
	f, err := os.CreateTemp(debugScreenshotDir, name+"_*.png")
	if err != nil {
		return "", fmt.Errorf("creating screenshot file: %w", err)
	}
	_, writeErr := f.Write(reply.Data)
	return f.Name(), errors.Join(writeErr, f.Close())
}

// SaveDebugScreenshot saves a screenshot of the page when debug is on. name
// starts the file name. The DEBUG line gives the file path, so the operator
// can match the file to the attempt that logged it.
func SaveDebugScreenshot(ctx context.Context, c *cdp.Client, logger *slog.Logger, debug bool, name string) {
	if !debug {
		return
	}
	file, err := screenshot(ctx, c, name)
	if err != nil {
		logger.Warn("screenshot failed", "name", name, "error", err)
		return
	}
	logger.Debug("screenshot saved", "file", file)
}
