package waitcheck

import (
	"fmt"
	"go/ast"
	"go/token"
	"slices"
	"strconv"
	"time"
)

// minGuard is the shortest guard that a real-clock wait may have.
const minGuard = 2 * time.Second

// maxDepth bounds the recursion through constants, names and helpers.
const maxDepth = 8

// body is the own code of one func: its body without the nested closures.
type body struct {
	block  *ast.BlockStmt
	comms  map[*ast.UnaryExpr]bool // receives that are select cases
	chans  map[string]bool         // local channels and channel parameters
	dialer bool                    // the func returns a conn with no deadline, so its callers hold the conn rule
}

// newBody returns the own code of fn, or nil when fn has no body.
func (s *scanner) newBody(fn ast.Node) *body {
	b := &body{comms: map[*ast.UnaryExpr]bool{}, chans: map[string]bool{}}
	var params *ast.FieldList
	switch f := fn.(type) {
	case *ast.FuncDecl:
		b.block, params = f.Body, f.Type.Params
		b.dialer = s.dialers[f.Name.Name]
	case *ast.FuncLit:
		b.block, params = f.Body, f.Type.Params
	}
	if b.block == nil {
		return nil
	}
	for _, f := range params.List {
		if _, ok := f.Type.(*ast.ChanType); ok {
			for _, n := range f.Names {
				b.chans[n.Name] = true
			}
		}
	}
	walkOwn(b.block, b.indexNode)
	return b
}

// walkOwn visits the nodes of block, but not the nodes of a nested closure.
func walkOwn(block *ast.BlockStmt, visit func(ast.Node)) {
	ast.Inspect(block, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		visit(n)
		return true
	})
}

func (b *body) indexNode(n ast.Node) {
	switch x := n.(type) {
	case *ast.CommClause:
		if x.Comm == nil || recvOperand(x.Comm) == nil {
			return
		}
		ast.Inspect(x.Comm, func(m ast.Node) bool {
			if u, ok := m.(*ast.UnaryExpr); ok && u.Op == token.ARROW {
				b.comms[u] = true
			}
			return true
		})
	case *ast.AssignStmt:
		for i, r := range x.Rhs {
			if id, ok := lhsIdent(x, i); ok && isMakeChan(r) {
				b.chans[id.Name] = true
			}
		}
	}
}

func lhsIdent(as *ast.AssignStmt, i int) (*ast.Ident, bool) {
	if i >= len(as.Lhs) {
		return nil, false
	}
	id, ok := as.Lhs[i].(*ast.Ident)
	return id, ok
}

func isMakeChan(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}
	if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "make" {
		return false
	}
	_, ok = call.Args[0].(*ast.ChanType)
	return ok
}

// check runs the rules on the own code of a live func.
func (s *scanner) check(fn ast.Node) {
	b := s.newBody(fn)
	if b == nil {
		return
	}
	walkOwn(b.block, func(n ast.Node) {
		switch x := n.(type) {
		case *ast.CallExpr:
			s.checkCall(x, b.block)
		case *ast.UnaryExpr:
			if x.Op == token.ARROW && !b.comms[x] {
				s.add(x, "recv", "a channel receive with no guard")
			}
		case *ast.RangeStmt:
			if id, ok := x.X.(*ast.Ident); ok && b.chans[id.Name] {
				s.add(x, "range", "a range over a channel with no guard")
			}
		case *ast.SelectStmt:
			s.checkSelect(x, b.block)
		case *ast.SelectorExpr:
			s.checkPipeValue(x)
		case *ast.AssignStmt:
			if !b.dialer {
				s.checkConn(x, b.block)
			}
		}
	})
}

// checkGoroutine runs the receive rules on the own code of a closure that a live func starts.
func (s *scanner) checkGoroutine(fn ast.Node) {
	b := s.newBody(fn)
	if b == nil {
		return
	}
	walkOwn(b.block, func(n ast.Node) {
		switch x := n.(type) {
		case *ast.UnaryExpr:
			if x.Op == token.ARROW && !b.comms[x] {
				s.add(x, "go-recv", "a goroutine of the test receives with no guard outside a bubble")
			}
		case *ast.SelectStmt:
			s.checkSelect(x, b.block)
		}
	})
}

func (s *scanner) checkCall(call *ast.CallExpr, scope ast.Node) {
	if s.checkPipeCall(call) || s.checkClock(call) || s.checkContext(call) {
		return
	}
	s.checkDeadline(call)
	s.checkAccept(call, scope)
	s.checkGroupWait(call)
}

// indexGroups records the names that a declaration in the dir gives the type sync.WaitGroup.
func (s *scanner) indexGroups(n ast.Node) {
	switch d := n.(type) {
	case *ast.ValueSpec:
		if isGroupType(d.Type) {
			for _, name := range d.Names {
				s.groups[name.Name] = true
			}
		}
	case *ast.Field:
		if isGroupType(d.Type) {
			for _, name := range d.Names {
				s.groups[name.Name] = true
			}
		}
	case *ast.AssignStmt:
		for i, r := range d.Rhs {
			if id, ok := lhsIdent(d, i); ok && isGroupValue(r) {
				s.groups[id.Name] = true
			}
		}
	}
}

// isGroupType reports whether e is sync.WaitGroup or *sync.WaitGroup.
func isGroupType(e ast.Expr) bool {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "WaitGroup" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "sync"
}

// isGroupValue reports whether e is sync.WaitGroup{}, &sync.WaitGroup{} or new(sync.WaitGroup).
func isGroupValue(e ast.Expr) bool {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	switch x := e.(type) {
	case *ast.CompositeLit:
		return isGroupType(x.Type)
	case *ast.CallExpr:
		id, ok := x.Fun.(*ast.Ident)
		return ok && id.Name == "new" && len(x.Args) == 1 && isGroupType(x.Args[0])
	}
	return false
}

// checkGroupWait reports a Wait call on a name that the dir declares as a sync.WaitGroup.
func (s *scanner) checkGroupWait(call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Wait" || len(call.Args) != 0 {
		return
	}
	name := ""
	switch x := sel.X.(type) {
	case *ast.Ident:
		name = x.Name
	case *ast.SelectorExpr:
		name = x.Sel.Name
	}
	if s.groups[name] {
		s.add(call, "wait", "a sync.WaitGroup Wait with no guard")
	}
}

// pkgFunc returns name when call calls pkg.name.
func pkgFunc(call *ast.CallExpr, pkg string) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
		return sel.Sel.Name
	}
	return ""
}

// calleeName returns the name of the func or the method that call calls.
func calleeName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// checkPipeCall reports a net.Pipe and a call of a pipe helper. It returns true for a net.Pipe.
func (s *scanner) checkPipeCall(call *ast.CallExpr) bool {
	if pkgFunc(call, "net") == "Pipe" {
		// The callers of a pipe helper get the finding.
		if top := s.topDecl(call); top == nil || !s.pipes[top.Name.Name] {
			s.add(call, "pipe", "a net.Pipe outside a bubble")
		}
		return true
	}
	if name := calleeName(call); s.pipes[name] {
		s.add(call, "pipe", "calls "+name+", which opens a net.Pipe, outside a bubble")
	}
	return false
}

// checkPipeValue reports a pipe helper that goes as a value to other code.
func (s *scanner) checkPipeValue(sel *ast.SelectorExpr) {
	if call, ok := s.parent[sel].(*ast.CallExpr); ok && call.Fun == sel {
		return
	}
	if s.pipes[sel.Sel.Name] {
		s.add(sel, "pipe", "passes "+sel.Sel.Name+", which opens a net.Pipe, outside a bubble")
	}
}

func (s *scanner) checkClock(call *ast.CallExpr) bool {
	switch name := pkgFunc(call, "time"); name {
	case "Sleep":
		s.add(call, "sleep", "time.Sleep on the real clock")
		return true
	case "AfterFunc", "NewTimer", "Tick", "NewTicker":
		if len(call.Args) > 0 {
			s.add(call, "timer", "time."+name+" on the real clock")
			return true
		}
	}
	return false
}

func (s *scanner) checkContext(call *ast.CallExpr) bool {
	name := pkgFunc(call, "context")
	if len(call.Args) < 2 {
		return false
	}
	arg := call.Args[1]
	switch name {
	case "WithTimeout", "WithTimeoutCause":
	case "WithDeadline", "WithDeadlineCause":
		d, ok := nowAdd(arg)
		if !ok {
			return true
		}
		arg = d
	default:
		return false
	}
	if d, ok := s.dur(arg, 0); ok && d < minGuard {
		s.add(call, "short-ctx", fmt.Sprintf("context.%s of %v on the real clock, under the 2s guard", name, d))
	}
	return true
}

func (s *scanner) checkDeadline(call *ast.CallExpr) {
	name := calleeName(call)
	if (name != "SetDeadline" && name != "SetReadDeadline") || len(call.Args) != 1 {
		return
	}
	a, ok := nowAdd(call.Args[0])
	if !ok {
		return
	}
	if d, ok := s.dur(a, 0); ok && d < minGuard {
		s.add(call, "short-deadline", fmt.Sprintf("%s of %v, under the 2s guard", name, d))
	}
}

func (s *scanner) checkAccept(call *ast.CallExpr, scope ast.Node) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Accept" || len(call.Args) != 0 {
		return
	}
	if id, ok := sel.X.(*ast.Ident); ok {
		if d := firstDeadline(scope, id.Name); d == token.NoPos || d > call.Pos() {
			s.add(call, "accept", "an Accept with no listener deadline")
		}
	}
}

func (s *scanner) checkSelect(sel *ast.SelectStmt, scope ast.Node) {
	guarded := false
	for _, st := range sel.Body.List {
		cc, ok := st.(*ast.CommClause)
		if !ok {
			continue
		}
		if cc.Comm == nil {
			return // a default case: the select does not wait
		}
		op := recvOperand(cc.Comm)
		if op == nil {
			continue
		}
		if d, known, ok := s.guardOf(op, scope, 0); ok {
			guarded = true
			if known && d < minGuard {
				s.add(cc, "short-guard", fmt.Sprintf("a select guard of %v, under 2s", d))
			}
		}
	}
	if !guarded {
		s.add(sel, "select", "a select with no guard case")
	}
}

// guardOf returns the duration of a select case that is a guard.
func (s *scanner) guardOf(op ast.Expr, scope ast.Node, depth int) (d time.Duration, known, guard bool) {
	if depth > maxDepth {
		return 0, false, false
	}
	if call, ok := isCall(op, "time", "After"); ok && len(call.Args) == 1 {
		d, known = s.dur(call.Args[0], 0)
		return d, known, true
	}
	switch x := op.(type) {
	case *ast.SelectorExpr:
		if x.Sel.Name == "C" {
			return 0, false, true
		}
	case *ast.Ident:
		if e := assigned(scope, x.Name); e != nil {
			return s.guardOf(e, scope, depth+1)
		}
	}
	return s.doneGuard(op, scope)
}

// doneGuard returns the timeout of ctx.Done() when ctx comes from context.WithTimeout.
func (s *scanner) doneGuard(op ast.Expr, scope ast.Node) (d time.Duration, known, guard bool) {
	call, ok := isCall(op, "", "Done")
	if !ok {
		return 0, false, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return 0, false, false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return 0, false, false
	}
	c, ok := isCall(assigned(scope, id.Name), "context", "WithTimeout")
	if !ok || len(c.Args) != 2 {
		return 0, false, false
	}
	d, known = s.dur(c.Args[1], 0)
	return d, known, true
}

// assigned returns the last expression assigned to name in scope.
func assigned(scope ast.Node, name string) ast.Expr {
	var out ast.Expr
	ast.Inspect(scope, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i := range as.Lhs {
			id, ok := lhsIdent(as, i)
			if !ok || id.Name != name {
				continue
			}
			if len(as.Rhs) == len(as.Lhs) {
				out = as.Rhs[i]
			} else if len(as.Rhs) == 1 {
				out = as.Rhs[0]
			}
		}
		return true
	})
	return out
}

func recvOperand(st ast.Stmt) ast.Expr {
	var e ast.Expr
	switch c := st.(type) {
	case *ast.ExprStmt:
		e = c.X
	case *ast.AssignStmt:
		if len(c.Rhs) == 1 {
			e = c.Rhs[0]
		}
	}
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.ARROW {
		return u.X
	}
	return nil
}

// nowAdd returns d of time.Now().Add(d).
func nowAdd(e ast.Expr) (ast.Expr, bool) {
	call, ok := isCall(e, "", "Add")
	if !ok || len(call.Args) != 1 {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	if _, ok := isCall(sel.X, "time", "Now"); !ok {
		return nil, false
	}
	return call.Args[0], true
}

// dur evaluates a duration expression. ok is false when it cannot.
func (s *scanner) dur(e ast.Expr, depth int) (time.Duration, bool) {
	if depth > maxDepth {
		return 0, false
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.INT {
			v, err := strconv.ParseInt(x.Value, 0, 64)
			return time.Duration(v), err == nil
		}
	case *ast.ParenExpr:
		return s.dur(x.X, depth+1)
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && id.Name == "time" {
			return timeUnit(x.Sel.Name)
		}
	case *ast.Ident:
		if v, ok := s.consts[x.Name]; ok {
			return s.dur(v, depth+1)
		}
	case *ast.BinaryExpr:
		return s.durBinary(x, depth)
	case *ast.CallExpr:
		if calleeName(x) == "Duration" && len(x.Args) == 1 {
			return s.dur(x.Args[0], depth+1)
		}
	}
	return 0, false
}

func (s *scanner) durBinary(x *ast.BinaryExpr, depth int) (time.Duration, bool) {
	a, ok := s.dur(x.X, depth+1)
	if !ok {
		return 0, false
	}
	b, ok := s.dur(x.Y, depth+1)
	if !ok {
		return 0, false
	}
	// A literal reads as a count of nanoseconds, so 5*time.Second is the product of two durations.
	switch x.Op {
	case token.MUL:
		return time.Duration(int64(a) * int64(b)), true
	case token.ADD:
		return a + b, true
	case token.SUB:
		return a - b, true
	case token.QUO:
		if b == 0 {
			return 0, false
		}
		return a / b, true
	default:
		return 0, false
	}
}

func timeUnit(name string) (time.Duration, bool) {
	switch name {
	case "Nanosecond":
		return time.Nanosecond, true
	case "Microsecond":
		return time.Microsecond, true
	case "Millisecond":
		return time.Millisecond, true
	case "Second":
		return time.Second, true
	case "Minute":
		return time.Minute, true
	case "Hour":
		return time.Hour, true
	}
	return 0, false
}

// markPipes finds the helpers that open a net.Pipe, by name.
func (s *scanner) markPipes() {
	for changed := true; changed; {
		changed = false
		for name, decls := range s.funcs {
			if !s.pipes[name] && slices.ContainsFunc(decls, s.opensPipe) {
				s.pipes[name] = true
				changed = true
			}
		}
	}
}

func (s *scanner) opensPipe(fd *ast.FuncDecl) bool {
	if fd.Body == nil || isTestFunc(fd) {
		return false
	}
	hit := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			hit = hit || pkgFunc(call, "net") == "Pipe" || s.pipes[calleeName(call)]
		}
		return !hit
	})
	return hit
}

// markDialers finds the helpers that return a conn with no deadline.
func (s *scanner) markDialers() {
	for changed := true; changed; {
		changed = false
		for name, decls := range s.funcs {
			if !s.dialers[name] && slices.ContainsFunc(decls, s.returnsUnguardedConn) {
				s.dialers[name] = true
				changed = true
			}
		}
	}
}

func (s *scanner) returnsUnguardedConn(fd *ast.FuncDecl) bool {
	if fd.Body == nil || fd.Type.Results == nil {
		return false
	}
	conns := map[string]bool{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if id, isID := lhsIdent(as, 0); ok && isID && id.Name != "_" && s.dialSite(call) {
			conns[id.Name] = true
		}
		return true
	})
	found := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.ReturnStmt); !ok {
			return !found
		}
		ast.Inspect(n, func(m ast.Node) bool {
			id, ok := m.(*ast.Ident)
			found = found || (ok && conns[id.Name] && firstDeadline(fd.Body, id.Name) == token.NoPos)
			return !found
		})
		return false
	})
	return found
}

// dialSite reports whether call opens a conn: net.Dial, x.Accept() or a dial helper.
func (s *scanner) dialSite(call *ast.CallExpr) bool {
	switch name := pkgFunc(call, "net"); name {
	case "Dial", "DialTimeout":
		return true
	}
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name == "Accept" && len(call.Args) == 0
	case *ast.Ident:
		return s.dialers[f.Name]
	}
	return false
}

// firstDeadline returns the position of the first SetDeadline or SetReadDeadline on name in scope.
func firstDeadline(scope ast.Node, name string) token.Pos {
	pos := token.NoPos
	ast.Inspect(scope, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "SetDeadline" && sel.Sel.Name != "SetReadDeadline") {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == name && (pos == token.NoPos || call.Pos() < pos) {
			pos = call.Pos()
		}
		return true
	})
	return pos
}

// checkConn reports a conn from a dial or an accept that the scope reads before any deadline.
func (s *scanner) checkConn(as *ast.AssignStmt, scope ast.Node) {
	if len(as.Rhs) != 1 {
		return
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok || !s.dialSite(call) {
		return
	}
	for i := range as.Lhs {
		id, ok := lhsIdent(as, i)
		if !ok || id.Name == "_" || id.Name == "err" {
			continue
		}
		if readsBeforeDeadline(s.firstRead(scope, id.Name, 0), firstDeadline(scope, id.Name)) {
			s.add(as, "conn", "a conn read with no deadline")
			return
		}
	}
}

// firstRead returns the position of the first read from name in scope. A read
// is a read method, a stdlib read func, or a helper that reads its parameter
// with no deadline.
func (s *scanner) firstRead(scope ast.Node, name string, depth int) token.Pos {
	pos := token.NoPos
	ast.Inspect(scope, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if ok && (pos == token.NoPos || call.Pos() < pos) && s.reads(call, name, depth) {
			pos = call.Pos()
		}
		return true
	})
	return pos
}

// reads reports whether call reads from name.
func (s *scanner) reads(call *ast.CallExpr, name string, depth int) bool {
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		id, ok := f.X.(*ast.Ident)
		if !ok {
			return false
		}
		if id.Name == name {
			return isReadMethod(f.Sel.Name)
		}
		return readsArg(call, id.Name+"."+f.Sel.Name, name)
	case *ast.Ident:
		return depth < maxDepth && s.helperReads(call, f.Name, name, depth)
	}
	return false
}

func isReadMethod(m string) bool {
	switch m {
	case "Read", "ReadString", "ReadBytes", "ReadLine", "ReadByte", "Peek", "ReadRune":
		return true
	}
	return false
}

// readsArg reports whether the stdlib func fn reads the argument name.
func readsArg(call *ast.CallExpr, fn, name string) bool {
	switch fn {
	case "bufio.NewReader", "http.ReadRequest", "http.ReadResponse", "io.ReadAll", "io.ReadFull":
		return argIs(call, 0, name)
	case "io.Copy":
		return argIs(call, 1, name)
	}
	return false
}

func argIs(call *ast.CallExpr, i int, name string) bool {
	if i >= len(call.Args) {
		return false
	}
	id, ok := call.Args[i].(*ast.Ident)
	return ok && id.Name == name
}

// helperReads reports whether the helper fn reads the parameter that call passes name to, before a deadline on it.
func (s *scanner) helperReads(call *ast.CallExpr, fn, name string, depth int) bool {
	for _, fd := range s.funcs[fn] {
		if fd.Body == nil || fd.Type.Params == nil {
			continue
		}
		i := 0
		for _, field := range fd.Type.Params.List {
			if len(field.Names) == 0 {
				i++
				continue
			}
			for _, pn := range field.Names {
				if argIs(call, i, name) && readsBeforeDeadline(s.firstRead(fd.Body, pn.Name, depth+1), firstDeadline(fd.Body, pn.Name)) {
					return true
				}
				i++
			}
		}
	}
	return false
}

func readsBeforeDeadline(read, deadline token.Pos) bool {
	return read != token.NoPos && (deadline == token.NoPos || deadline > read)
}
