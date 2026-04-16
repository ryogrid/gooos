// tinyc — Tiny C tree-walking interpreter for gooos.
//
// Usage from the gooos shell:
//
//	$ tinyc program.tc
//
// Reads a Tiny C source file from the in-memory filesystem,
// parses it into an AST, and executes it. Supports int-only
// types, 1D arrays, functions, if/else/while/for, and the
// println built-in. See impldoc/tinyc_interpreter.md.

package main

import "github.com/ryogrid/gooos/user/gooos"

func main() {
	arg := gooos.Args()
	if len(arg) == 0 {
		// No filename — try reading from stdin.
		var src []byte
		buf := make([]byte, 256)
		for {
			n := gooos.Read(gooos.Stdin, buf)
			if n <= 0 {
				break
			}
			src = append(src, buf[:n]...)
		}
		if len(src) == 0 {
			gooos.Println("usage: tinyc <file.tc>")
			gooos.Exit(1)
		}
		run(src)
		return
	}
	src := gooos.ReadFile(arg)
	if src == nil {
		gooos.Println("tinyc: file not found: " + arg)
		gooos.Exit(1)
	}
	run(src)
}

func run(src []byte) {
	lex := NewLexer(src)
	parser := NewParser(lex)
	prog := parser.Parse()
	ev := NewEvaluator()
	ev.Run(prog)
}

// itoa converts an int to its decimal string representation.
// Avoids importing strconv in every file; strconv.Itoa is
// only used in eval.go for println formatting.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
