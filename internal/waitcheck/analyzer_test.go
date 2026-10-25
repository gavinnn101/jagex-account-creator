// Package waitcheck fails a test that waits on the real clock with no guard.
//
// TestTree reads each _test.go file of the module, dir by dir. It also reads the
// non-test files of the helper packages. A helper package is a module package
// whose import path ends in "test". A dir reads each helper package that its
// files import, also through another helper package. A helper dir also reads
// its own non-test files. TestTree fails when no dir reads a helper package.
//
// TestTree reports each wait that runs on a test goroutine outside a
// synctest.Test bubble. It reports each wait once, also when more than one
// scan reads its file. The rules:
//
//   - sleep: a call of time.Sleep.
//   - timer: a call of time.AfterFunc, time.NewTimer, time.Tick or time.NewTicker.
//   - recv: a channel receive that is not a select case, for example <-time.After(d).
//   - range: a range over a channel.
//   - select: a select with no guard case and no default case.
//   - short-guard: a select guard under 2s.
//   - short-ctx: a context.WithTimeout or a context.WithDeadline under 2s.
//   - short-deadline: a SetDeadline or a SetReadDeadline under 2s.
//   - accept: an Accept with no listener deadline before it.
//   - conn: a read from a conn of net.Dial, Accept or a dial helper before a deadline on that conn.
//   - pipe: a net.Pipe, or a helper that opens one. A guard does not satisfy this rule.
//     A pipe test runs in a bubble, where the pipe deadlines and the guards use the fake clock.
//   - go-recv: a goroutine that the test starts receives with no guard.
//   - wait: a Wait call on a sync.WaitGroup. waittest.Wait waits under a guard.
//
// The code on a test goroutine starts at each TestXxx func. The check follows
// the calls of named funcs, closures with a local name, table fields that a
// call x.field(...) runs, and t.Run closures. It stops at these points:
//
//   - an argument of synctest.Test: a closure or a named func.
//   - a closure that goes to a helper that calls synctest.Test.
//   - a closure that a go statement, a Go call or an AfterFunc call starts.
//     In such a goroutine, only the go-recv and select rules apply.
//
// A guard is time.After, the C field of a timer, or the Done channel of a
// context from context.WithTimeout.
//
// Approximations. Each one fails toward a missed wait, unless it says otherwise:
//
//   - The check reads test files and helper files only. It does not see a wait in other
//     production code that a test calls.
//   - A production package whose import path ends in "test" reads as a helper.
//     This causes extra findings.
//   - Names resolve in one scan, with no types. A scan is one dir and the helper files that it reads.
//     A helper func gets the bubble context of its caller. A name clash can also cause an extra finding.
//   - The check does not read a closure that a func returns or that a package-level var holds.
//   - In a closure that the test starts as a goroutine, only the go-recv and select rules apply.
//     A time.Sleep there passes.
//   - A go statement that calls a named func makes that func live, so its receives report
//     as recv. This causes an extra finding.
//   - The pipe rule sees a pipe helper that goes as a value only as a selector x.name, not as a bare name.
//   - The wait rule knows a sync.WaitGroup only by the name that a var, a field, a parameter
//     or an assignment in the scan gives it. Another value of that name causes an extra finding.
//     The check does not read a Wait call of an errgroup or an exec.Cmd.
//   - The range rule sees only a local make(chan ...) or a channel parameter.
//   - A duration comes only from literals, time units and constants. An unknown duration is not short.
//   - Each C field is a guard. The check does not look for a t.Fatal in the guard case.
//   - A deadline counts only on the same name, before the first read in source order.
//     A deadline through another name causes an extra finding.
//
// The check has no waiver marker.
package waitcheck

import (
	"cmp"
	"go/ast"
	"go/token"
	"slices"
	"strings"
)

// finding is one wait that a rule reports.
type finding struct {
	pos  token.Position
	rule string
	msg  string
}

// litKind tells where a closure runs.
type litKind int

const (
	litOther     litKind = iota // returned or stored: the check does not follow it
	litCalled                   // called on the goroutine of its caller
	litBubble                   // run in a synctest bubble
	litGoroutine                // started on its own goroutine
	litBound                    // bound to a local name
	litField                    // the value of a table field
)

// scanner holds the parsed files of one scan.
type scanner struct {
	fset     *token.FileSet
	files    []*ast.File
	consts   map[string]ast.Expr
	funcs    map[string][]*ast.FuncDecl
	parent   map[ast.Node]ast.Node
	lits     []*ast.FuncLit
	live     map[ast.Node]bool // a func that runs on a test goroutine outside a bubble
	liveGo   map[ast.Node]bool // a closure that live code starts on its own goroutine
	dialers  map[string]bool   // a helper that returns a conn with no deadline
	pipes    map[string]bool   // a helper that opens a net.Pipe
	groups   map[string]bool   // a var, a field or a parameter of type sync.WaitGroup
	findings []finding
}

// scanDir returns the findings in the files of one scan, in file and line order.
func scanDir(fset *token.FileSet, files []*ast.File) []finding {
	s := &scanner{
		fset:    fset,
		files:   files,
		consts:  map[string]ast.Expr{},
		funcs:   map[string][]*ast.FuncDecl{},
		parent:  map[ast.Node]ast.Node{},
		live:    map[ast.Node]bool{},
		liveGo:  map[ast.Node]bool{},
		dialers: map[string]bool{},
		pipes:   map[string]bool{},
		groups:  map[string]bool{},
	}
	s.index()
	s.markDialers()
	s.markPipes()
	s.markLive()
	for fn := range s.live {
		s.check(fn)
	}
	for fn := range s.liveGo {
		s.checkGoroutine(fn)
	}
	slices.SortFunc(s.findings, compareFindings)
	return s.findings
}

// compareFindings orders findings by file, line, column and rule.
func compareFindings(a, b finding) int {
	return cmp.Or(
		strings.Compare(a.pos.Filename, b.pos.Filename),
		cmp.Compare(a.pos.Line, b.pos.Line),
		cmp.Compare(a.pos.Column, b.pos.Column),
		strings.Compare(a.rule, b.rule),
	)
}

func (s *scanner) add(n ast.Node, rule, msg string) {
	s.findings = append(s.findings, finding{pos: s.fset.Position(n.Pos()), rule: rule, msg: msg})
}

// index records the parent of each node, the funcs, the closures and the constants.
func (s *scanner) index() {
	for _, f := range s.files {
		var stack []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return false
			}
			if len(stack) > 0 {
				s.parent[n] = stack[len(stack)-1]
			}
			stack = append(stack, n)
			s.indexNode(n)
			s.indexGroups(n)
			return true
		})
	}
}

func (s *scanner) indexNode(n ast.Node) {
	switch d := n.(type) {
	case *ast.FuncDecl:
		s.funcs[d.Name.Name] = append(s.funcs[d.Name.Name], d)
	case *ast.FuncLit:
		s.lits = append(s.lits, d)
	case *ast.GenDecl:
		if d.Tok == token.CONST {
			s.indexConsts(d)
		}
	}
}

func (s *scanner) indexConsts(d *ast.GenDecl) {
	for _, spec := range d.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range vs.Names {
			if i < len(vs.Values) {
				s.consts[name.Name] = vs.Values[i]
			}
		}
	}
}

// isCall reports whether n calls pkg.name, or any x.name when pkg is "".
func isCall(n ast.Node, pkg, name string) (*ast.CallExpr, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return nil, false
	}
	if pkg == "" {
		return call, true
	}
	id, ok := sel.X.(*ast.Ident)
	return call, ok && id.Name == pkg
}

// enclosing returns the innermost FuncLit or FuncDecl around n.
func (s *scanner) enclosing(n ast.Node) ast.Node {
	for p := s.parent[n]; p != nil; p = s.parent[p] {
		switch p.(type) {
		case *ast.FuncLit, *ast.FuncDecl:
			return p
		}
	}
	return nil
}

// topDecl returns the FuncDecl around n.
func (s *scanner) topDecl(n ast.Node) *ast.FuncDecl {
	for p := n; p != nil; p = s.parent[p] {
		if fd, ok := p.(*ast.FuncDecl); ok {
			return fd
		}
	}
	return nil
}

func (s *scanner) isLive(n ast.Node) bool { return s.live[s.enclosing(n)] }

func isTestFunc(fd *ast.FuncDecl) bool {
	if fd.Recv != nil || fd.Name.Name == "TestMain" || !strings.HasPrefix(fd.Name.Name, "Test") {
		return false
	}
	return fd.Type.Params != nil && len(fd.Type.Params.List) == 1
}

// markLive finds the code that runs on a test goroutine outside a bubble.
func (s *scanner) markLive() {
	for _, decls := range s.funcs {
		for _, fd := range decls {
			if isTestFunc(fd) {
				s.live[fd] = true
			}
		}
	}
	for changed := true; changed; {
		funcs := s.markLiveFuncs()
		lits := s.markLiveLits()
		changed = funcs || lits
	}
	for _, lit := range s.lits {
		if s.kindOf(lit) == litGoroutine && s.isLive(lit) {
			s.liveGo[lit] = true
		}
	}
}

func (s *scanner) markLiveFuncs() bool {
	changed := false
	for name, decls := range s.funcs {
		for _, fd := range decls {
			if s.live[fd] || fd.Body == nil {
				continue
			}
			if slices.ContainsFunc(s.nameUses(name), s.usesLive) {
				s.live[fd] = true
				changed = true
			}
		}
	}
	return changed
}

func (s *scanner) markLiveLits() bool {
	changed := false
	for _, lit := range s.lits {
		if !s.live[lit] && s.litRunsLive(lit) {
			s.live[lit] = true
			changed = true
		}
	}
	return changed
}

func (s *scanner) litRunsLive(lit *ast.FuncLit) bool {
	switch s.kindOf(lit) {
	case litCalled:
		return s.isLive(lit)
	case litBound:
		scope := s.enclosing(lit)
		return scope != nil && slices.ContainsFunc(localUses(scope, s.boundName(lit), lit), s.usesLive)
	case litField:
		return slices.ContainsFunc(s.selectorCalls(s.boundName(lit)), s.isLive)
	case litOther, litBubble, litGoroutine:
	}
	return false
}

// kindOf tells where lit runs.
func (s *scanner) kindOf(lit *ast.FuncLit) litKind {
	switch p := s.parent[lit].(type) {
	case *ast.CallExpr:
		return s.argKind(lit, p)
	case *ast.AssignStmt, *ast.ValueSpec:
		return litBound
	case *ast.KeyValueExpr:
		return litField
	}
	return litOther
}

// argKind tells where lit runs when call calls it or takes it as an argument.
func (s *scanner) argKind(lit *ast.FuncLit, call *ast.CallExpr) litKind {
	if call.Fun == lit {
		if _, ok := s.parent[call].(*ast.GoStmt); ok {
			return litGoroutine
		}
		return litCalled
	}
	if _, ok := isCall(call, "synctest", "Test"); ok {
		return litBubble
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if fun.Sel.Name == "Go" || fun.Sel.Name == "AfterFunc" {
			return litGoroutine
		}
	case *ast.Ident:
		if slices.ContainsFunc(s.funcs[fun.Name], containsBubble) {
			return litBubble
		}
	}
	return litCalled
}

func containsBubble(fd *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fd, func(n ast.Node) bool {
		if _, ok := isCall(n, "synctest", "Test"); ok {
			found = true
		}
		return !found
	})
	return found
}

// boundName returns the local name or the table field that holds lit.
func (s *scanner) boundName(lit *ast.FuncLit) string {
	switch p := s.parent[lit].(type) {
	case *ast.AssignStmt:
		for i, r := range p.Rhs {
			if r == lit && i < len(p.Lhs) {
				if id, ok := p.Lhs[i].(*ast.Ident); ok {
					return id.Name
				}
			}
		}
	case *ast.ValueSpec:
		for i, r := range p.Values {
			if r == lit && i < len(p.Names) {
				return p.Names[i].Name
			}
		}
	case *ast.KeyValueExpr:
		if id, ok := p.Key.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// usesLive reports whether the use u of a func value runs on a test goroutine.
// A use inside a table value runs where the calls of that table field run.
func (s *scanner) usesLive(u ast.Node) bool {
	if call, ok := s.parent[u].(*ast.CallExpr); ok && call.Fun != u {
		if _, ok := isCall(call, "synctest", "Test"); ok {
			return false
		}
	}
	if field, ok := s.tableField(u); ok {
		// With no call of the field, the value goes to production code, which the check does not read.
		return slices.ContainsFunc(s.selectorCalls(field), s.isLive)
	}
	return s.isLive(u)
}

// tableField returns the key of the table value that holds u in its own func.
func (s *scanner) tableField(u ast.Node) (string, bool) {
	for p := s.parent[u]; p != nil; p = s.parent[p] {
		switch x := p.(type) {
		case *ast.KeyValueExpr:
			key, ok := x.Key.(*ast.Ident)
			if ok && u.Pos() >= x.Value.Pos() && u.End() <= x.Value.End() {
				return key.Name, true
			}
		case *ast.FuncLit, *ast.FuncDecl:
			return "", false
		}
	}
	return "", false
}

// nameUses returns the uses of name: an ident, or a selector x.name. A declared
// name, a parameter name and a composite literal key are not uses.
func (s *scanner) nameUses(name string) []ast.Node {
	var out []ast.Node
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Body != nil {
				ast.Inspect(x.Body, visit)
			}
			return false
		case *ast.Field:
			ast.Inspect(x.Type, visit)
			return false
		case *ast.KeyValueExpr:
			ast.Inspect(x.Value, visit)
			return false
		case *ast.SelectorExpr:
			if x.Sel.Name == name {
				out = append(out, x)
				return false
			}
		case *ast.Ident:
			if x.Name == name {
				out = append(out, x)
			}
		}
		return true
	}
	for _, f := range s.files {
		ast.Inspect(f, visit)
	}
	return out
}

// localUses returns the uses of the local name in scope, outside lit.
func localUses(scope ast.Node, name string, lit ast.Node) []ast.Node {
	var out []ast.Node
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return x != lit
		case *ast.AssignStmt:
			for _, r := range x.Rhs {
				ast.Inspect(r, visit)
			}
			return false
		case *ast.KeyValueExpr:
			ast.Inspect(x.Value, visit)
			return false
		case *ast.SelectorExpr:
			ast.Inspect(x.X, visit)
			return false
		case *ast.Ident:
			if x.Name == name {
				out = append(out, x)
			}
		}
		return true
	}
	ast.Inspect(scope, visit)
	return out
}

// selectorCalls returns the calls x.name(...): the calls of a table field.
func (s *scanner) selectorCalls(name string) []ast.Node {
	var out []ast.Node
	for _, f := range s.files {
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := isCall(n, "", name); ok {
				out = append(out, call)
			}
			return true
		})
	}
	return out
}
