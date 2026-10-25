package browser

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mafredri/cdp"

	"jagex_account_creator/internal/browser/cdptest"
	"jagex_account_creator/internal/logtest"
)

func TestSaveDebugScreenshot(t *testing.T) {
	const warn = `level=WARN msg="screenshot failed" `
	tests := []struct {
		name     string
		debug    bool
		shot     string // the name argument
		shotErr  error
		dirFile  bool   // whether debug_screenshots exists as a file before the call
		wantWarn string // the prefix of the one WARN line, or "" for none
		wantDir  bool   // whether debug_screenshots is a directory after the call
	}{
		{name: "debug off", shot: "load"},
		{name: "saved", debug: true, shot: "load", wantDir: true},
		{name: "capture fails", debug: true, shot: "load", shotErr: errFake, wantWarn: warn + `name=load error="capturing screenshot: fake failure"` + "\n"},
		{name: "the directory is a file", debug: true, shot: "load", dirFile: true, wantWarn: warn + `name=load error="creating screenshot directory: `},
		{name: "a name with a path separator", debug: true, shot: "a/b", wantWarn: warn + `name=a/b error="creating screenshot file: `, wantDir: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if tt.dirFile {
				if err := os.WriteFile(debugScreenshotDir, nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
			c := &cdp.Client{Page: &cdptest.Page{Screenshot: []byte("png"), ScreenshotErr: tt.shotErr}}
			var buf bytes.Buffer

			SaveDebugScreenshot(t.Context(), c, logtest.New(&buf), tt.debug, tt.shot)

			files, err := filepath.Glob(filepath.Join(debugScreenshotDir, "*"))
			if err != nil {
				t.Fatal(err)
			}
			log := buf.String()
			if tt.debug && tt.wantWarn == "" {
				checkSaved(t, files, tt.shot, log)
			} else if len(files) != 0 {
				t.Errorf("files = %q, want none", files)
			}
			if tt.wantWarn == "" && !tt.debug && log != "" {
				t.Errorf("log = %q, want none", log)
			}
			if tt.wantWarn != "" && (!strings.HasPrefix(log, tt.wantWarn) || strings.Count(log, "\n") != 1) {
				t.Errorf("log = %q, want one line that starts %q", log, tt.wantWarn)
			}
			info, err := os.Stat(debugScreenshotDir)
			if isDir := err == nil && info.IsDir(); isDir != tt.wantDir {
				t.Errorf("%s is a directory = %v, want %v", debugScreenshotDir, isDir, tt.wantDir)
			}
		})
	}
}

// checkSaved checks that files holds one capture whose name starts with shot
// and that log is the one DEBUG line that names it.
func checkSaved(t *testing.T, files []string, shot, log string) {
	t.Helper()
	if len(files) != 1 || !strings.HasPrefix(filepath.Base(files[0]), shot+"_") || filepath.Ext(files[0]) != ".png" {
		t.Fatalf("files = %q, want one %s_*.png", files, shot)
	}
	if data, err := os.ReadFile(files[0]); err != nil || string(data) != "png" {
		t.Errorf("file = %q, %v, want the capture", data, err)
	}
	if want := `level=DEBUG msg="screenshot saved" file=` + files[0] + "\n"; log != want {
		t.Errorf("log = %q, want %q", log, want)
	}
}

func TestScreenshot_UniqueFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	c := &cdp.Client{Page: &cdptest.Page{Screenshot: []byte("png")}}

	first, firstErr := screenshot(t.Context(), c, "same")
	second, secondErr := screenshot(t.Context(), c, "same")

	if firstErr != nil || secondErr != nil || first == second {
		t.Fatalf("screenshot() = %q, %v and %q, %v, want two files", first, firstErr, second, secondErr)
	}
	files, err := filepath.Glob(filepath.Join(debugScreenshotDir, "same_*.png"))
	if err != nil || len(files) != 2 {
		t.Errorf("files = %q, %v, want %q and %q", files, err, first, second)
	}
}
