package main

import (
	"strconv"

	"github.com/ryogrid/gooos/user/gooos"
)

// Env holds variable/array bindings for a scope.
type Env struct {
	vars   map[string]int64
	arrays map[string][]int64
	parent *Env
}

func newEnv(parent *Env) *Env {
	return &Env{
		vars:   make(map[string]int64),
		arrays: make(map[string][]int64),
		parent: parent,
	}
}

// lookupVar searches the scope chain for a scalar variable.
func (e *Env) lookupVar(name string) (int64, bool) {
	if v, ok := e.vars[name]; ok {
		return v, true
	}
	if e.parent != nil {
		return e.parent.lookupVar(name)
	}
	return 0, false
}

// setVar sets a variable in the nearest scope that owns it,
// or in the current scope if not found.
func (e *Env) setVar(name string, val int64) {
	if _, ok := e.vars[name]; ok {
		e.vars[name] = val
		return
	}
	if e.parent != nil {
		if _, ok := e.parent.lookupVar(name); ok {
			e.parent.setVar(name, val)
			return
		}
	}
	e.vars[name] = val
}

// lookupArray searches the scope chain for an array.
func (e *Env) lookupArray(name string) ([]int64, bool) {
	if a, ok := e.arrays[name]; ok {
		return a, true
	}
	if e.parent != nil {
		return e.parent.lookupArray(name)
	}
	return nil, false
}

// FuncDef holds a parsed function definition.
type FuncDef struct {
	params []string
	body   *Node
}

// Evaluator is the tree-walk interpreter state.
type Evaluator struct {
	globals   *Env
	funcs     map[string]*FuncDef
	callDepth int
	returned  bool
	retVal    int64
}

const maxCallDepth = 256

// NewEvaluator creates an evaluator.
func NewEvaluator() *Evaluator {
	return &Evaluator{
		globals: newEnv(nil),
		funcs:   make(map[string]*FuncDef),
	}
}

// Run executes a parsed program.
func (ev *Evaluator) Run(prog *Node) {
	// First pass: register global vars/arrays and functions.
	for _, child := range prog.Children {
		switch child.Kind {
		case NdVarDecl:
			val := int64(0)
			if child.Init != nil {
				val = ev.evalExpr(child.Init, ev.globals)
			}
			ev.globals.vars[child.Name] = val
		case NdArrayDecl:
			size := ev.evalExpr(child.Left, ev.globals)
			if size <= 0 || size > 10000 {
				gooos.Println("tinyc: error: invalid array size for '" + child.Name + "'")
				gooos.Exit(1)
			}
			ev.globals.arrays[child.Name] = make([]int64, size)
		case NdFuncDef:
			ev.funcs[child.Name] = &FuncDef{params: child.Params, body: child.Body}
		}
	}
	// Look up and call main().
	mainFn, ok := ev.funcs["main"]
	if !ok {
		gooos.Println("tinyc: error: no main() function")
		gooos.Exit(1)
	}
	ev.callFunc("main", mainFn, nil, ev.globals)
}

func (ev *Evaluator) callFunc(name string, fn *FuncDef, args []*Node, callerEnv *Env) int64 {
	if ev.callDepth >= maxCallDepth {
		gooos.Println("tinyc: error: call depth exceeded (max " + itoa(maxCallDepth) + ")")
		gooos.Exit(1)
	}
	ev.callDepth++
	local := newEnv(ev.globals)
	// Bind parameters
	for i, pname := range fn.params {
		if i < len(args) {
			argVal := ev.evalExpr(args[i], callerEnv)
			local.vars[pname] = argVal
		}
	}
	// Check if any argument is an array name: bind by reference
	for i, pname := range fn.params {
		if i < len(args) && args[i].Kind == NdVar {
			argName := args[i].Name
			if arr, ok := callerEnv.lookupArray(argName); ok {
				local.arrays[pname] = arr
				delete(local.vars, pname)
			}
		}
	}
	oldReturned := ev.returned
	oldRetVal := ev.retVal
	ev.returned = false
	ev.retVal = 0
	ev.execBlock(fn.body, local)
	result := ev.retVal
	ev.returned = oldReturned
	ev.retVal = oldRetVal
	ev.callDepth--
	return result
}

func (ev *Evaluator) execBlock(block *Node, env *Env) {
	for _, child := range block.Children {
		if ev.returned {
			return
		}
		ev.execStmt(child, env)
	}
}

func (ev *Evaluator) execStmt(node *Node, env *Env) {
	if ev.returned {
		return
	}
	switch node.Kind {
	case NdBlock:
		ev.execBlock(node, env)
	case NdVarDecl:
		val := int64(0)
		if node.Init != nil {
			val = ev.evalExpr(node.Init, env)
		}
		env.vars[node.Name] = val
	case NdExprStmt:
		ev.evalExpr(node.Left, env)
	case NdIf:
		cond := ev.evalExpr(node.Cond, env)
		if cond != 0 {
			ev.execStmt(node.Then, env)
		} else if node.Else != nil {
			ev.execStmt(node.Else, env)
		}
	case NdWhile:
		for !ev.returned {
			cond := ev.evalExpr(node.Cond, env)
			if cond == 0 {
				break
			}
			ev.execStmt(node.Body, env)
		}
	case NdFor:
		ev.execStmt(node.Init, env)
		for !ev.returned {
			cond := ev.evalExpr(node.Cond, env)
			if cond == 0 {
				break
			}
			ev.execStmt(node.Body, env)
			ev.execStmt(node.Step, env)
		}
	case NdReturn:
		if node.Left != nil {
			ev.retVal = ev.evalExpr(node.Left, env)
		}
		ev.returned = true
	case NdPrintln:
		ev.execPrintln(node, env)
	default:
		gooos.Println("tinyc: error: unexpected statement kind")
		gooos.Exit(1)
	}
}

func (ev *Evaluator) evalExpr(node *Node, env *Env) int64 {
	switch node.Kind {
	case NdNum:
		return node.NumVal
	case NdVar:
		val, ok := env.lookupVar(node.Name)
		if !ok {
			gooos.Println("tinyc: error: undefined variable '" + node.Name + "'")
			gooos.Exit(1)
		}
		return val
	case NdUnaryNeg:
		return -ev.evalExpr(node.Left, env)
	case NdBinOp:
		left := ev.evalExpr(node.Left, env)
		right := ev.evalExpr(node.Right, env)
		return ev.evalBinOp(node.Op, left, right)
	case NdAssign:
		val := ev.evalExpr(node.Right, env)
		if node.Left != nil && node.Left.Kind == NdIndex {
			// Array element assignment
			idx := ev.evalExpr(node.Left.Left, env)
			arr, ok := env.lookupArray(node.Left.Name)
			if !ok {
				gooos.Println("tinyc: error: undefined array '" + node.Left.Name + "'")
				gooos.Exit(1)
			}
			if idx < 0 || idx >= int64(len(arr)) {
				gooos.Println("tinyc: error: index out of bounds for '" + node.Left.Name + "'")
				gooos.Exit(1)
			}
			arr[idx] = val
		} else {
			// Scalar assignment
			env.setVar(node.Name, val)
		}
		return val
	case NdIndex:
		arr, ok := env.lookupArray(node.Name)
		if !ok {
			gooos.Println("tinyc: error: undefined array '" + node.Name + "'")
			gooos.Exit(1)
		}
		idx := ev.evalExpr(node.Left, env)
		if idx < 0 || idx >= int64(len(arr)) {
			gooos.Println("tinyc: error: index out of bounds for '" + node.Name + "'")
			gooos.Exit(1)
		}
		return arr[idx]
	case NdCall:
		fn, ok := ev.funcs[node.Name]
		if !ok {
			gooos.Println("tinyc: error: undefined function '" + node.Name + "'")
			gooos.Exit(1)
		}
		return ev.callFunc(node.Name, fn, node.Args, env)
	case NdPrintln:
		ev.execPrintln(node, env)
		return 0
	default:
		gooos.Println("tinyc: error: unexpected expression kind")
		gooos.Exit(1)
		return 0
	}
}

func (ev *Evaluator) evalBinOp(op TokenKind, left, right int64) int64 {
	switch op {
	case TkPlus:
		return left + right
	case TkMinus:
		return left - right
	case TkStar:
		return left * right
	case TkSlash:
		if right == 0 {
			gooos.Println("tinyc: error: division by zero")
			gooos.Exit(1)
		}
		return left / right
	case TkPercent:
		if right == 0 {
			gooos.Println("tinyc: error: modulo by zero")
			gooos.Exit(1)
		}
		return left % right
	case TkLt:
		return boolToInt(left < right)
	case TkGt:
		return boolToInt(left > right)
	case TkLe:
		return boolToInt(left <= right)
	case TkGe:
		return boolToInt(left >= right)
	case TkEqEq:
		return boolToInt(left == right)
	case TkNeq:
		return boolToInt(left != right)
	default:
		gooos.Println("tinyc: error: unknown operator")
		gooos.Exit(1)
		return 0
	}
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func (ev *Evaluator) execPrintln(node *Node, env *Env) {
	format := node.StrVal
	// Replace each %d with the corresponding argument value.
	argIdx := 0
	result := make([]byte, 0, len(format)+32)
	i := 0
	for i < len(format) {
		if i+1 < len(format) && format[i] == '%' && format[i+1] == 'd' {
			if argIdx < len(node.Args) {
				val := ev.evalExpr(node.Args[argIdx], env)
				s := strconv.Itoa(int(val))
				for j := 0; j < len(s); j++ {
					result = append(result, s[j])
				}
				argIdx++
			}
			i += 2
			continue
		}
		// Handle \n escape in format string
		if i+1 < len(format) && format[i] == '\\' && format[i+1] == 'n' {
			result = append(result, '\n')
			i += 2
			continue
		}
		result = append(result, format[i])
		i++
	}
	gooos.Print(string(result))
	gooos.Print("\n")
}
