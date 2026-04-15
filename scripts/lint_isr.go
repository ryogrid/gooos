// scripts/lint_isr.go — Static linter for ISR-reachable functions.
//
// Enforces the ISR-safety rules from impldoc/goroutine_design_channels_and_isr.md
// §3.1. From each handler registered via registerHandler(N, fn), walk the
// static call graph (name-based, depth <= 4) and flag forbidden constructs:
//
//   1. String concatenation where at least one operand is a string literal.
//   2. Channel ops: make(chan ...), send (ch <- v), receive (<-ch).
//   3. `go` statements.
//   4. Map/slice/interface literal heap allocations.
//
// Exits 0 on clean tree, 1 on any violation.
//
// Usage: lint_isr [src_dir]   (default: src/)
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxDepth = 4

// safelist of helpers proven ISR-safe by review.
var safelist = map[string]bool{
	"serialPutChar":    true,
	"outb":             true,
	"inb":              true,
	"hlt":              true,
	"cli":              true,
	"sti":              true,
	"readFlags":        true,
	"restoreFlags":     true,
	"picSendEOI":       true,
	"keyboardIRQSend":  true,
	"keyboardIRQRecv":  true,
	"interruptIn":      true,
	"pitTicks":         true,
	"lastErrorCode":    true,
	"lastFramePtr":     true,
	"readCR2":          true,
	"vgaWriteLine":     true,
	"serialPrintBytes": true,
	"appendStr":        true,
	"appendHex":        true,
	"bytesToString":    true,
	"panicHexBuf":      true,
}

type violation struct {
	pos    token.Position
	reason string
	chain  []string
	root   string
}

type linter struct {
	fset      *token.FileSet
	funcs     map[string]*ast.FuncDecl // name -> decl (single-package)
	nosplit   map[string]bool
	roots     []string
	violas    []violation
}

// hasNosplitPragma returns true when the decl's doc comment contains
// //go:nosplit (TinyGo/Go pragma).
func hasNosplitPragma(d *ast.FuncDecl) bool {
	if d.Doc == nil {
		return false
	}
	for _, c := range d.Doc.List {
		if strings.TrimSpace(c.Text) == "//go:nosplit" {
			return true
		}
	}
	return false
}

func (l *linter) loadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)

	l.fset = token.NewFileSet()
	l.funcs = make(map[string]*ast.FuncDecl)
	l.nosplit = make(map[string]bool)

	for _, path := range files {
		f, err := parser.ParseFile(l.fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fd.Recv != nil {
				continue // methods: skip (none of the ISR helpers use them)
			}
			name := fd.Name.Name
			l.funcs[name] = fd
			if hasNosplitPragma(fd) {
				l.nosplit[name] = true
			}
		}
		// Collect ISR roots: registerHandler(N, fnIdent)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "registerHandler" {
				return true
			}
			if len(call.Args) < 2 {
				return true
			}
			if fn, ok := call.Args[1].(*ast.Ident); ok {
				l.roots = append(l.roots, fn.Name)
			}
			return true
		})
	}
	// Dedupe + sort roots for determinism.
	seen := make(map[string]bool)
	var uniq []string
	for _, r := range l.roots {
		if !seen[r] {
			seen[r] = true
			uniq = append(uniq, r)
		}
	}
	sort.Strings(uniq)
	l.roots = uniq
	return nil
}

func (l *linter) addViolation(pos token.Pos, reason string, chain []string, root string) {
	cp := make([]string, len(chain))
	copy(cp, chain)
	l.violas = append(l.violas, violation{
		pos:    l.fset.Position(pos),
		reason: reason,
		chain:  cp,
		root:   root,
	})
}

func isStringLit(e ast.Expr) bool {
	// Direct literal.
	if bl, ok := e.(*ast.BasicLit); ok {
		return bl.Kind == token.STRING
	}
	// Parenthesised literal.
	if pe, ok := e.(*ast.ParenExpr); ok {
		return isStringLit(pe.X)
	}
	// Concatenated chain: "a" + "b" + x — if either side contains a
	// string literal, the whole chain is string-typed.
	if be, ok := e.(*ast.BinaryExpr); ok && be.Op == token.ADD {
		return isStringLit(be.X) || isStringLit(be.Y)
	}
	return false
}

// walk traverses a function body, flagging forbidden constructs and
// recursing into callee definitions found in the same package.
func (l *linter) walk(name string, depth int, chain []string, visited map[string]bool, root string) {
	if depth > maxDepth {
		return
	}
	if safelist[name] || visited[name] {
		return
	}
	// Only apply nosplit exemption to non-root callees. A nosplit root
	// is still fully inspected (the ISR itself is the subject of the
	// lint).
	if depth > 0 && l.nosplit[name] {
		return
	}
	fd, ok := l.funcs[name]
	if !ok || fd.Body == nil {
		return
	}
	visited[name] = true
	chain = append(chain, name)

	// Single-pass AST walk: flag forbidden nodes, collect callees.
	var callees []string
	seenCallee := make(map[string]bool)

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.GoStmt:
			l.addViolation(x.Pos(), "go statement", chain, root)
		case *ast.SendStmt:
			l.addViolation(x.Pos(), "channel send", chain, root)
		case *ast.UnaryExpr:
			if x.Op == token.ARROW {
				l.addViolation(x.Pos(), "channel receive", chain, root)
			}
		case *ast.BinaryExpr:
			if x.Op == token.ADD && (isStringLit(x.X) || isStringLit(x.Y)) {
				l.addViolation(x.Pos(), "string concat", chain, root)
			}
		case *ast.CompositeLit:
			switch t := x.Type.(type) {
			case *ast.ArrayType:
				if t.Len == nil {
					l.addViolation(x.Pos(), "slice literal", chain, root)
				}
			case *ast.MapType:
				l.addViolation(x.Pos(), "map literal", chain, root)
			default:
				_ = t
			}
		case *ast.CallExpr:
			// make(chan ...) detection.
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "make" && len(x.Args) >= 1 {
				if _, isChan := x.Args[0].(*ast.ChanType); isChan {
					l.addViolation(x.Pos(), "make(chan)", chain, root)
				}
			}
			// interface{}(x) boxing shaped as CallExpr with InterfaceType fun.
			if _, ok := x.Fun.(*ast.InterfaceType); ok {
				l.addViolation(x.Pos(), "interface boxing", chain, root)
			}
			// Record callee for recursion.
			if id, ok := x.Fun.(*ast.Ident); ok {
				if !seenCallee[id.Name] {
					seenCallee[id.Name] = true
					callees = append(callees, id.Name)
				}
			}
		}
		return true
	})

	sort.Strings(callees)
	for _, c := range callees {
		l.walk(c, depth+1, chain, visited, root)
	}
	// Allow the same callee to be revisited via a different root; clear
	// only the per-root visited entry when unwinding.
	delete(visited, name)
}

func main() {
	dir := "src"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	l := &linter{}
	if err := l.loadDir(dir); err != nil {
		fmt.Fprintln(os.Stderr, "ISR-LINT: load error:", err)
		os.Exit(2)
	}
	for _, root := range l.roots {
		visited := make(map[string]bool)
		l.walk(root, 0, nil, visited, root)
	}
	// Deterministic output: sort by file/line/col/reason.
	sort.Slice(l.violas, func(i, j int) bool {
		a, b := l.violas[i].pos, l.violas[j].pos
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return l.violas[i].reason < l.violas[j].reason
	})
	// Dedupe exact duplicates.
	seenKey := make(map[string]bool)
	exit := 0
	for _, v := range l.violas {
		key := fmt.Sprintf("%s:%d:%d:%s:%s", v.pos.Filename, v.pos.Line, v.pos.Column, v.reason, v.root)
		if seenKey[key] {
			continue
		}
		seenKey[key] = true
		chain := strings.Join(v.chain, "->")
		fmt.Fprintf(os.Stderr, "ISR-LINT: %s:%d:%d: %s in %s (root=%s)\n",
			v.pos.Filename, v.pos.Line, v.pos.Column, v.reason, chain, v.root)
		exit = 1
	}
	os.Exit(exit)
}
