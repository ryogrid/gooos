# Tiny C Interpreter for gooos — Design Document

This document specifies a tree-walking interpreter for the
**Tiny C** language, implemented in Go and compiled with TinyGo
as a Ring-3 user binary on gooos. The interpreter reads a Tiny C
source file from the in-memory filesystem (or stdin), parses it
into an AST, and executes it directly.

The Tiny C language is based on the specification from
[Tsukuba University's 2006 compiler lecture](https://www.hpcs.cs.tsukuba.ac.jp/~msato/lecture-note/comp2006/tiny-c-note1.html)
by Prof. Mitsuhisa Sato. The original reference implementation
uses C + yacc; this document redesigns it as a single Go binary
with a hand-written recursive-descent parser suitable for
TinyGo's constraints.

## 1. Tiny C Language Specification

### 1.1 Overview

- The only data type is **integer** (`int`, 64-bit signed).
- **One-dimensional arrays** of integers are supported.
- Functions may declare **local variables** with `var`.
- Control flow: `if`/`else`, `while`, `for`.
- Operators: `+`, `-`, `*`, `<`, `>`, `=` (assignment).
- Built-in function: `println(format_string, expr)` — prints
  a formatted integer value. The format string supports `%d`
  as the sole placeholder.
- Entry point is `main()` (no arguments, no return value used).
- No separate compilation, no preprocessor, no pointers,
  no structs, no floating point.

### 1.2 Sample Programs

**Sum of 0..9:**

```c
main()
{
    var i, s;
    s = 0;
    i = 0;
    while (i < 10) {
        s = s + i;
        i = i + 1;
    }
    println("s = %d", s);
}
```

Expected output: `s = 45`

**Array + function call:**

```c
var A[10];

main()
{
    var i;
    for (i = 0; i < 10; i = i + 1) A[i] = i;
    println("s = %d", arraySum(A, 10));
}

arraySum(a, n)
{
    var i, s;
    s = 0;
    for (i = 0; i < n; i = i + 1) s = s + a[i];
    return s;
}
```

Expected output: `s = 45`

### 1.3 BNF Grammar

Notation: lowercase = non-terminal; UPPER = keyword terminal;
`'x'` = literal character; `{...}*` = zero-or-more repetition;
`[...]` = optional.

```
program :=
    { external_definition }*

external_definition :=
      function_name '(' [ parameter { ',' parameter }* ] ')' compound_statement
    | VAR variable_name [ '=' expr ] ';'
    | VAR array_name '[' expr ']' ';'

compound_statement :=
    '{' [ local_variable_declaration ] { statement }* '}'

local_variable_declaration :=
    VAR variable_name [ { ',' variable_name }* ] ';'

statement :=
      expr ';'
    | compound_statement
    | IF '(' expr ')' statement [ ELSE statement ]
    | RETURN [ expr ] ';'
    | WHILE '(' expr ')' statement
    | FOR '(' expr ';' expr ';' expr ')' statement

expr :=
      primary_expr
    | variable_name '=' expr
    | array_name '[' expr ']' '=' expr
    | expr '+' expr
    | expr '-' expr
    | expr '*' expr
    | expr '<' expr
    | expr '>' expr

primary_expr :=
      variable_name
    | NUMBER
    | STRING
    | array_name '[' expr ']'
    | function_name '(' expr [ { ',' expr }* ] ')'
    | function_name '(' ')'
    | '(' expr ')'
    | PRINTLN '(' STRING ',' expr ')'
```

### 1.4 Tokens and Keywords

| Token | Description |
|---|---|
| `NUMBER` | Sequence of decimal digits (`0`-`9`); parsed as int64 |
| `STRING` | `"..."` delimited; only appears in `println` format |
| `IDENT` | `[a-zA-Z_][a-zA-Z0-9_]*`; resolved contextually as variable, array, function, or parameter name |
| Keywords | `var`, `if`, `else`, `return`, `while`, `for`, `println` |
| Operators | `+`, `-`, `*`, `<`, `>`, `=` |
| Delimiters | `(`, `)`, `{`, `}`, `[`, `]`, `,`, `;` |

### 1.5 Operator Precedence (low to high)

| Precedence | Operators | Associativity |
|---|---|---|
| 1 (lowest) | `=` (assignment) | right |
| 2 | `<`, `>` | left |
| 3 | `+`, `-` | left |
| 4 (highest) | `*` | left |

Comparison operators return `1` (true) or `0` (false).

### 1.6 Scoping Rules

- **Global variables / arrays**: declared at the top level with
  `var`; visible to all functions.
- **Local variables**: declared with `var` at the beginning of a
  function's compound statement; shadow globals of the same name
  within that function.
- **Parameters**: behave like local variables; passed by value.
  Arrays passed by name share the underlying storage (reference
  semantics — the callee's `a[i]` writes to the caller's array).
- No nested functions. No closures.

## 2. Architecture

```
Source text
    |
    v
+--------+    token stream    +--------+      AST       +-----------+
| Lexer  | ----------------> | Parser | ------------> | Evaluator |
+--------+                    +--------+               +-----------+
                                                            |
                                                            v
                                                      gooos.Print
                                                      gooos.Exit
```

Three-phase pipeline, all in a single Go binary:

1. **Lexer** (`lexer.go`) — converts source bytes into a stream
   of tokens. Hand-written; no lex/flex dependency.
2. **Parser** (`parser.go`) — recursive-descent parser consuming
   tokens, producing an AST. Hand-written; no yacc dependency.
3. **Evaluator** (`eval.go`) — tree-walking interpreter that
   traverses the AST and executes statements/expressions against
   an environment (variable bindings).

### 2.1 Why Tree-Walking (not bytecode)

- Simplest to implement; smallest binary size.
- Tiny C programs are short (educational examples); execution
  speed is not a concern.
- No additional data-structure surface (no bytecode format, no
  VM loop). Keeps ELF under the 128 KiB `maxFileData` cap.
- A bytecode backend can be added later as a separate design
  round if performance matters.

## 3. AST Node Types

```go
type NodeKind int

const (
    // Declarations
    NdProgram    NodeKind = iota // children: list of external_definition
    NdFuncDef                   // name, params []string, body *Node
    NdVarDecl                   // name, init *Node (nil if no initializer)
    NdArrayDecl                 // name, size *Node

    // Statements
    NdBlock                     // children: []Node (compound_statement)
    NdIf                        // cond, then, else_ *Node
    NdWhile                     // cond, body *Node
    NdFor                       // init, cond, step, body *Node
    NdReturn                    // value *Node (nil for bare return)
    NdExprStmt                  // expr *Node

    // Expressions
    NdAssign                    // target (NdVar or NdIndex), value *Node
    NdBinOp                     // op token, left, right *Node
    NdVar                       // name string
    NdNum                       // value int64
    NdStr                       // value string (println format only)
    NdIndex                     // array name, index *Node
    NdCall                      // function name, args []*Node
    NdPrintln                   // format string, expr *Node
)
```

## 4. Runtime Environment

```go
// Value is the sole runtime type (int64). Arrays are []int64.
type Env struct {
    vars   map[string]int64    // scalar variables
    arrays map[string][]int64  // array variables
    parent *Env                // lexical parent (global)
}
```

- **Global Env**: holds top-level `var` declarations and arrays.
- **Function-call Env**: created per call; `parent` points to
  the global Env. Parameters are copied into `vars`; array
  parameters bind the caller's `[]int64` slice into `arrays`
  (reference semantics).
- **Return value**: propagated via a sentinel mechanism — either
  a dedicated `returnValue` field on the evaluator, or a Go
  panic/recover pair (simpler for TinyGo; no goroutine overhead).

### 4.1 Function Table

```go
type FuncDef struct {
    name   string
    params []string
    body   *Node
}

var funcs = map[string]*FuncDef{}
```

Populated during the top-level walk of `NdProgram`. `main` is
looked up and invoked after all declarations are processed.

## 5. gooos Integration

### 5.1 I/O Mapping

| Tiny C construct | gooos equivalent |
|---|---|
| `println("fmt", expr)` | `gooos.Print(formatted_string)` + `gooos.Print("\n")` |
| Source file input | `gooos.ReadFile(filename)` via `gooos.Args()` to get the filename, OR `gooos.Read(gooos.Stdin, buf)` for stdin pipe |
| Process exit | `gooos.Exit(code)` |
| Error messages | `gooos.Println("tinyc: error: ...")` |

### 5.2 `println` Format Handling

The only format specifier is `%d`. Implementation:

```go
func formatPrintln(format string, value int64) string {
    // Find "%d" in format, replace with strconv.Itoa(int(value))
    // No other specifiers supported.
}
```

Uses `strconv.Itoa` (already proven linkable in `gochan.elf`).

### 5.3 Invocation from the Shell

```
$ tinyc sample.tc
```

The interpreter:
1. Reads `gooos.Args()` to get the filename (`"sample.tc"`).
2. Calls `gooos.ReadFile(filename)` to load the source.
3. If no argument, reads from stdin (`gooos.Read(gooos.Stdin, ...)`
   in a loop until EOF) — enables `cat sample.tc | tinyc`.
4. Lexes → parses → evaluates.
5. Exits with code 0 on success, 1 on error.

### 5.4 Storing Tiny C Source Files

Tiny C source files are plain text stored in the gooos in-memory
filesystem. They can be created from the shell:

```
$ echo "main() { println(\"%d\", 42); }" > hello.tc
$ tinyc hello.tc
42
```

Or pre-loaded as embedded test fixtures alongside the ELF
binaries in `src/main.go`.

## 6. File Layout

```
user/cmd/tinyc/
    main.go       — entry point: read args/stdin, drive pipeline
    token.go      — Token type, TokenKind enum, keyword table
    lexer.go      — Lexer struct, NextToken() method
    ast.go        — Node struct, NodeKind enum
    parser.go     — Parser struct, recursive-descent methods
    eval.go       — Evaluator struct, tree-walk execution
```

All files in package `main`. Single `tinygo build` produces
`tinyc.elf`.

## 7. Constraints and Limits

| Constraint | Value | Rationale |
|---|---|---|
| Max source size | 64 KiB | `gooos.ReadFile` buffer cap; typical Tiny C programs are < 1 KiB |
| Max AST nodes | ~4096 | Heap-allocated; `gc=leaking` means every node is permanent. 4096 nodes x ~64 bytes = 256 KiB heap usage, within the 256 KiB linker-reserved heap |
| Max call depth | 256 | Stack of `Env` frames; prevents unbounded recursion from exhausting the 8 KiB goroutine stack |
| Max array size | 10000 elements | Prevents accidental multi-MB allocations |
| ELF size target | < 128 KiB | Must fit in `maxFileData` (131072). The interpreter is pure Go with no `time` or heavy stdlib imports; expected 60-80 KiB based on similar-complexity user binaries |
| Numeric range | int64 | TinyGo native; no overflow detection |

## 8. Error Handling

- **Lexer errors**: unknown character → print line number +
  character, exit 1.
- **Parser errors**: unexpected token → print line number +
  expected vs. got, exit 1.
- **Runtime errors**: undefined variable, index out of bounds,
  undefined function, division by zero (if `/` is added later),
  call depth exceeded → print error message, exit 1.
- No error recovery; first error is fatal. Acceptable for an
  educational interpreter.

## 9. Extensions Beyond the Base Spec

The base spec omits several features noted as "exercises" in the
lecture notes. The following should be included in v1 to make the
interpreter practically useful:

| Extension | Priority | Notes |
|---|---|---|
| `for` statement | v1 | Listed in grammar but noted as "not implemented" in the reference; trivial with the existing `while` pattern |
| `==` and `!=` operators | v1 | Needed for idiomatic conditionals; compare returns 1/0 |
| `/` and `%` operators | v1 | Division and modulo; common in educational programs |
| Unary `-` | v1 | Negative literals and negation |
| `<=` and `>=` operators | v2 | Sugar; can be expressed with existing ops |
| `&&` and `\|\|` operators | v2 | Short-circuit logical; useful but not essential |
| Multi-value `println` | v2 | `println("a=%d b=%d", a, b)` — multiple `%d` placeholders |

## 10. Verification Plan

### 10.1 Test Programs

Embed 3-4 `.tc` files in the filesystem via `src/main.go` at
boot time (same pattern as `hello.txt`):

| File | Purpose |
|---|---|
| `sum.tc` | Loop summing 0..9, println result (expected: `s = 45`) |
| `array.tc` | Array + function call (expected: `s = 45`) |
| `fib.tc` | Recursive fibonacci(10) (expected: `55`) — tests recursion + return |
| `for.tc` | For-loop with array fill + sum (exercises `for` extension) |

### 10.2 Harness

`tmp/test_tinyc.sh` — QEMU harness matching existing pattern:

1. Boot kernel, wait for shell.
2. `send_line "tinyc sum.tc"` — assert `s = 45` on serial.
3. `send_line "tinyc fib.tc"` — assert `55` on serial.
4. Assert `PF=0` throughout.

### 10.3 Build Integration

- Add `tinyc` to `user/Makefile` CMDS.
- Add `fsCreate("tinyc.elf")` + `fsWrite(...)` to `src/main.go`
  preload block.
- Add `fsCreate("sum.tc")` etc. for the test source files.
- `scripts/embed_elfs.sh` regenerates `src/user_binaries.go`.
- `make build` clean; verify ELF < 128 KiB.

## 11. Recursive-Descent Parser Sketch

Key parsing functions (one per grammar rule):

```
parseProgram()          → loop parseExternalDef until EOF
parseExternalDef()      → peek: VAR → parseVarDecl/parseArrayDecl
                           else → parseFuncDef
parseFuncDef()          → name, '(', params, ')', parseBlock
parseBlock()            → '{', [parseLocalVars], loop parseStmt, '}'
parseLocalVars()        → VAR name {',' name} ';'
parseStmt()             → peek: IF → parseIf
                           peek: WHILE → parseWhile
                           peek: FOR → parseFor
                           peek: RETURN → parseReturn
                           peek: '{' → parseBlock
                           else → parseExpr, ';'
parseExpr()             → parseAssign
parseAssign()           → parseCmp; if '=' follows → NdAssign
parseCmp()              → parseAdd { ('<'|'>') parseAdd }
parseAdd()              → parseMul { ('+'|'-') parseMul }
parseMul()              → parseUnary { '*' parseUnary }
parseUnary()            → ['-'] parsePrimary
parsePrimary()          → NUMBER | STRING | IDENT [...] | '(' expr ')'
                           | PRINTLN '(' STRING ',' expr ')'
```

The `IDENT` case in `parsePrimary` needs lookahead:
- `IDENT '('` → function call
- `IDENT '['` → array index (may be followed by `'=' expr` in
  parseAssign)
- `IDENT` alone → variable reference

## 12. Dependencies

- `strconv` (for `Itoa` in println formatting) — already proven
  in `gochan.elf`.
- `user/gooos` package — `Print`, `Println`, `ReadFile`, `Read`,
  `Args`, `Exit`.
- No other stdlib imports. No `fmt`, no `os`, no `io`, no
  `strings`, no `unicode`.

## 13. Open Questions

1. **Should the interpreter support interactive REPL mode?**
   Useful for demos but adds complexity (incremental parsing,
   prompt loop). Recommendation: defer to v2; v1 is file-only.
2. **String variables?** The base spec only allows strings in
   `println` format position. Adding string variables would
   require a tagged-union value type. Recommendation: defer;
   int-only matches the spec.
3. **Should `println` without a format string be supported?**
   e.g., `println(42)` as sugar for `println("%d", 42)`.
   Recommendation: yes, low cost, improves usability.

## 14. Risk Register

- **ELF size exceeds 128 KiB** — the interpreter is pure Go
  with minimal imports; `gochan.elf` (which imports `strconv` +
  `time`) is 99 KiB. `tinyc` drops `time` but adds more Go
  code (~500-800 lines). Expected ~70-90 KiB. If exceeded,
  bump `maxFileData` to 192 KiB (32 x 192 KiB = 6 MiB .bss).
- **Heap exhaustion under `gc=leaking`** — the AST and Env
  frames are never freed. For small Tiny C programs (< 100
  lines, < 10 function calls) this is fine. Long-running or
  deeply-recursive programs may hit the 256 KiB heap ceiling.
  Mitigation: cap recursion depth at 256; document the limit.
- **Goroutine stack overflow** — the recursive-descent parser
  and tree-walk evaluator both recurse. TinyGo's 8 KiB default
  stack with `automatic-stack-size=true` should handle typical
  depths (< 50 levels). If hit, the `gooosStackOverflow` hook
  fires a clean diagnostic.
