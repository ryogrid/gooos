package main

// cmdLine is a parsed shell command line: argv plus optional
// stdin/stdout redirection targets.
type cmdLine struct {
	argv       []string // [0] = command name; [1..] = args
	stdinFile  string   // "" if no '<'
	stdoutFile string   // "" if no '>'/'>>'
	appendOut  bool     // true if '>>'
}

// pipeline is one or more cmdLines connected by '|'. A line
// with no '|' parses to a one-element pipeline.
//
// background is set when the pipeline ends with a bare '&' token.
// POSIX semantics: the whole pipeline is backgrounded (not just
// the tail stage). See impldoc/shell_background_jobs.md §2.2.
type pipeline struct {
	stages     []cmdLine
	background bool
}

// parsePipeline tokenises and splits the line on '|' into
// per-stage cmdLines. Returns ok=false on syntax error.
//
// A trailing '&' token (not preceded by another '&') sets
// p.background. Internal '&' tokens, '&&' lookahead tokens, and
// bare '&' with no stages are syntax errors.
func parsePipeline(line string) (pipeline, bool) {
	toks := tokenize(line)
	var p pipeline
	// Strip a trailing '&' token and record background.
	if len(toks) > 0 && toks[len(toks)-1] == "&" {
		toks = toks[:len(toks)-1]
		p.background = true
	}
	var stageToks []string
	flushStage := func() bool {
		if len(stageToks) == 0 {
			return false
		}
		c, ok := parseStage(stageToks)
		if !ok {
			return false
		}
		p.stages = append(p.stages, c)
		stageToks = stageToks[:0]
		return true
	}
	for _, t := range toks {
		if t == "|" {
			if !flushStage() {
				return p, false
			}
			continue
		}
		// Any residual '&' or '&&' past the trailing-strip is a
		// syntax error — no mid-pipeline backgrounding, and &&
		// (logical-and) is reserved for a future feature.
		if t == "&" || t == "&&" {
			return p, false
		}
		stageToks = append(stageToks, t)
	}
	if !flushStage() {
		return p, false
	}
	return p, true
}

// parseStage extracts argv + redirection from a single
// pipeline stage's token list.
func parseStage(toks []string) (cmdLine, bool) {
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
// '>>', '|' as standalone tokens even when not whitespace-
// separated (so `cat|wc` parses as ["cat", "|", "wc"]).
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
		case '|':
			flush()
			toks = append(toks, "|")
		case '>':
			flush()
			// Look ahead for '>>'.
			if i+1 < len(line) && line[i+1] == '>' {
				toks = append(toks, ">>")
				i++
			} else {
				toks = append(toks, ">")
			}
		case '&':
			flush()
			// Look ahead for '&&' — rejected at parseStage / parsePipeline.
			if i+1 < len(line) && line[i+1] == '&' {
				toks = append(toks, "&&")
				i++
			} else {
				toks = append(toks, "&")
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
