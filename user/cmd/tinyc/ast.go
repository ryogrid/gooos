package main

// NodeKind classifies an AST node.
type NodeKind int

const (
	NdProgram   NodeKind = iota // top-level: Children = external defs
	NdFuncDef                   // Name, Params, Body
	NdVarDecl                   // Name, Init (may be nil)
	NdArrayDecl                 // Name, Left (size expr)
	NdBlock                     // Children = statements
	NdIf                        // Cond, Then, Else (may be nil)
	NdWhile                     // Cond, Body
	NdFor                       // Init, Cond, Step, Body
	NdReturn                    // Left (may be nil for bare return)
	NdExprStmt                  // Left (expression)
	NdAssign                    // Name or Left=NdIndex target, Right=value
	NdBinOp                     // Op, Left, Right
	NdUnaryNeg                  // Left (operand)
	NdVar                       // Name
	NdNum                       // NumVal
	NdStr                       // StrVal
	NdIndex                     // Name (array), Left (index expr)
	NdCall                      // Name (function), Args
	NdPrintln                   // StrVal (format), Args (expressions)
)

// Node is a generic AST node. Fields are used selectively
// depending on Kind.
type Node struct {
	Kind    NodeKind
	Name    string
	NumVal  int64
	StrVal  string
	Params  []string
	Op      TokenKind
	Left    *Node
	Right   *Node
	Cond    *Node
	Then    *Node
	Else    *Node
	Init    *Node
	Step    *Node
	Body    *Node
	Args    []*Node
	Children []*Node
}
