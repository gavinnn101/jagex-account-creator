package waitcheck

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestFixtures runs the check on each file in testdata. A file holds one positive or one
// negative case of a rule or of a path into a bubble. A "// want <rule>" comment marks each expected finding.
func TestFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixture files in testdata")
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".go")
		t.Run(name, func(t *testing.T) {
			fset := token.NewFileSet()
			files := parseFiles(t, fset, []string{path}, parser.ParseComments)
			findings := scanDir(fset, files)
			got := make([]string, 0, len(findings))
			for _, fd := range findings {
				got = append(got, fmt.Sprintf("%d: %s", fd.pos.Line, fd.rule))
			}
			want := wants(fset, files[0])
			if !slices.Equal(got, want) {
				t.Errorf("findings %q, want %q", got, want)
			}
			switch {
			case strings.HasSuffix(name, "_positive"):
				if len(want) == 0 {
					t.Error("a _positive file marks no finding")
				}
			case strings.HasSuffix(name, "_negative"):
				if len(want) != 0 {
					t.Errorf("a _negative file marks %d findings", len(want))
				}
			default:
				t.Error("a fixture file name ends in _positive or _negative")
			}
		})
	}
}

// wants returns the findings that the "// want" comments of f mark, in line order.
func wants(fset *token.FileSet, f *ast.File) []string {
	var out []string
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			rules, ok := strings.CutPrefix(c.Text, "// want ")
			if !ok {
				continue
			}
			for rule := range strings.FieldsSeq(rules) {
				out = append(out, fmt.Sprintf("%d: %s", fset.Position(c.Pos()).Line, rule))
			}
		}
	}
	return out
}

func TestTree(t *testing.T) {
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := moduleRoot(t, pkgDir)
	dirs := testFilesByDir(t, root)
	// The scan of a wrong root finds no files and passes, so the scan must find this package.
	if _, ok := dirs[pkgDir]; !ok {
		t.Fatalf("the scan of %s found no test file in %s", root, pkgDir)
	}
	findings, helpers := scanTree(t, root, modulePath(t, root), dirs)
	// A wrong module path or import rule reads no helper and passes, so some scan must read one.
	if len(helpers) == 0 {
		t.Fatalf("no scan of %s read a helper package", root)
	}
	for _, fd := range findings {
		t.Errorf("%s:%d: %s: %s", relPath(t, root, fd.pos.Filename), fd.pos.Line, fd.rule, fd.msg)
	}
}

// TestHelperTree runs the tree scan on the fixture module in testdata/helpertree.
// A "// want <rule>" comment marks each expected finding, in a test file or a helper file.
func TestHelperTree(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("testdata", "helpertree"))
	if err != nil {
		t.Fatal(err)
	}
	findings, helpers := scanTree(t, root, "fixture", testFilesByDir(t, root))
	got := make([]string, 0, len(findings))
	for _, fd := range findings {
		got = append(got, fmt.Sprintf("%s:%d: %s", relPath(t, root, fd.pos.Filename), fd.pos.Line, fd.rule))
	}
	want := treeWants(t, root)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("findings %q, want %q", got, want)
	}
	gotHelpers := make([]string, 0, len(helpers))
	for dir := range helpers {
		gotHelpers = append(gotHelpers, relPath(t, root, dir))
	}
	slices.Sort(gotHelpers)
	if wantHelpers := []string{"closertest", "drainertest", "selftest"}; !slices.Equal(gotHelpers, wantHelpers) {
		t.Errorf("helper dirs read %q, want %q", gotHelpers, wantHelpers)
	}
}

// treeWants returns the findings that the "// want" comments of the .go files under root mark.
func treeWants(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		fset := token.NewFileSet()
		for _, w := range wants(fset, parseFiles(t, fset, []string{path}, parser.ParseComments)[0]) {
			out = append(out, relPath(t, root, path)+":"+w)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// scanTree scans each dir of dirs together with the helper packages that it reads.
// It returns each finding once, in file and line order, and the helper dirs that the scans read.
func scanTree(t *testing.T, root, module string, dirs map[string][]string) ([]finding, map[string]bool) {
	t.Helper()
	helpers := map[string]bool{}
	seen := map[string]bool{}
	var out []finding
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		fset := token.NewFileSet()
		files := parseFiles(t, fset, dirs[dir], 0)
		files = append(files, readHelpers(t, fset, root, module, dir, files, helpers)...)
		for _, fd := range scanDir(fset, files) {
			// One caller can bubble a helper wait and another not, so the key holds no scanning dir.
			key := fmt.Sprintf("%s:%d:%s", fd.pos.Filename, fd.pos.Line, fd.rule)
			if !seen[key] {
				seen[key] = true
				out = append(out, fd)
			}
		}
	}
	slices.SortFunc(out, compareFindings)
	return out, helpers
}

// readHelpers parses the non-test files of each helper package that files import, also
// through another helper package, and of dir when dir is a helper. It adds each helper dir with files to helpers.
func readHelpers(t *testing.T, fset *token.FileSet, root, module, dir string, files []*ast.File, helpers map[string]bool) []*ast.File {
	t.Helper()
	queue := helperImports(root, module, files)
	if strings.HasSuffix(importPath(t, root, module, dir), "test") {
		queue = append(queue, dir)
	}
	done := map[string]bool{}
	var out []*ast.File
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if done[h] {
			continue
		}
		done[h] = true
		hf := parseFiles(t, fset, nonTestFiles(t, h), 0)
		if len(hf) > 0 {
			helpers[h] = true
		}
		out = append(out, hf...)
		queue = append(queue, helperImports(root, module, hf)...)
	}
	return out
}

// helperImports returns the dirs of the module packages that files import whose import path ends in "test".
func helperImports(root, module string, files []*ast.File) []string {
	var out []string
	for _, f := range files {
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			rel, ok := strings.CutPrefix(path, module+"/")
			if err != nil || !ok || !strings.HasSuffix(rel, "test") {
				continue
			}
			out = append(out, filepath.Join(root, filepath.FromSlash(rel)))
		}
	}
	return out
}

// importPath returns the import path of the package in dir.
func importPath(t *testing.T, root, module, dir string) string {
	t.Helper()
	rel := relPath(t, root, dir)
	if rel == "." {
		return module
	}
	return module + "/" + rel
}

func nonTestFiles(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(paths, func(p string) bool { return strings.HasSuffix(p, "_test.go") })
}

// relPath returns path relative to root, with forward slashes.
func relPath(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel)
}

// modulePath returns the module path that the go.mod in root declares.
func modulePath(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(data)) {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(path)
		}
	}
	t.Fatal("no module line in go.mod")
	return ""
}

func parseFiles(t *testing.T, fset *token.FileSet, paths []string, mode parser.Mode) []*ast.File {
	t.Helper()
	files := make([]*ast.File, 0, len(paths))
	for _, path := range paths {
		f, err := parser.ParseFile(fset, path, nil, mode|parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

// moduleRoot walks up from dir to the dir that holds go.mod.
// The coverage gate runs the tests in another worktree, so the root comes from the running tree.
func moduleRoot(t *testing.T, dir string) string {
	t.Helper()
	for !hasGoMod(t, dir) {
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package dir")
		}
		dir = parent
	}
	return dir
}

func hasGoMod(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// testFilesByDir returns the _test.go files of the module under root, by dir.
// It skips the dirs that the go tool skips and each nested module.
func testFilesByDir(t *testing.T, root string) map[string][]string {
	t.Helper()
	dirs := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && skipDir(t, path, d.Name()) {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, "_test.go") {
			dirs[filepath.Dir(path)] = append(dirs[filepath.Dir(path)], path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}

func skipDir(t *testing.T, path, name string) bool {
	t.Helper()
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" {
		return true
	}
	return hasGoMod(t, path)
}
