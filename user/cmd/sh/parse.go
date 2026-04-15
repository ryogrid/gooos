package main

// cmdLine is a parsed shell command line: argv plus optional
// stdin/stdout redirection targets.
type cmdLine struct {
	argv       []string // [0] = command name; [1..] = args
	stdinFile  string   // "" if no '<'
	stdoutFile string   // "" if no '>'/'>>'
	appendOut  bool     // true if '>>'
}

// parseLine tokenises a shell input line and pulls out
// redirection operators. Returns ok=false on syntax error
// (e.g., '>' with no following filename).
func parseLine(line string) (cmdLine, bool) {
	toks := tokenize(line)
	var c cmdLine
	for i := 0; i < len(toks); i++ {
		switch toks[i] {
		case ">":
			if i+1 >= len(toks) {
				return c, false
			}
			c.stdoutFile = toks[i+1]
			c.appendOut = false
			i++
		case ">>":
			if i+1 >= len(toks) {
				return c, false
			}
			c.stdoutFile = toks[i+1]
			c.appendOut = true
			i++
		case "<":
			if i+1 >= len(toks) {
				return c, false
			}
			c.stdinFile = toks[i+1]
			i++
		default:
			c.argv = append(c.argv, toks[i])
		}
	}
	if len(c.argv) == 0 {
		return c, false
	}
	return c, true
}

// tokenize splits line on whitespace AND breaks out '<', '>',
// '>>' as standalone tokens even when not whitespace-separated
// (so `cat>out` parses as ["cat", ">", "out"]).
func tokenize(line string) []string {
	var toks []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			toks = append(toks, string(cur))
			cur = cur[:0]
		}
	}
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch ch {
		case ' ', '\t':
			flush()
		case '<':
			flush()
			toks = append(toks, "<")
		case '>':
			flush()
			// Look ahead for '>>'.
			if i+1 < len(line) && line[i+1] == '>' {
				toks = append(toks, ">>")
				i++
			} else {
				toks = append(toks, ">")
			}
		default:
			cur = append(cur, ch)
		}
	}
	flush()
	return toks
}

// joinArgs concatenates argv[1:] with single spaces, matching
// the existing kernel ABI which takes a single args string.
func joinArgs(argv []string) string {
	if len(argv) <= 1 {
		return ""
	}
	out := argv[1]
	for i := 2; i < len(argv); i++ {
		out += " " + argv[i]
	}
	return out
}
