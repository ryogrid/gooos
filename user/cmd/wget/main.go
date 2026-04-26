// user/cmd/wget — minimal userspace HTTP/1.0 downloader.
//
// Shell usage:
//     wget http://<IPv4>[:port]/<path>
//
// Downloads the URL body and writes it to a file in the
// current FS namespace (flat, global) under the URL path's
// basename. HTTP only; no HTTPS, no DNS (IP literal required),
// no redirects, no resume. See design_docs/01_simple-wget_overview.md
// for the full design and Known Limitations.

package main

import "github.com/ryogrid/gooos/user/gooos"

func main() {
	argline := gooos.Args()
	tokens := splitSpace(argline)
	if len(tokens) < 1 {
		gooos.Println("usage: wget <url>")
		return
	}
	gooos.Println("wget: url=" + tokens[0])
}

// splitSpace — copied from user/cmd/tcpcli/main.go:58–82.
// The 2-tokens-then-tail behavior is irrelevant for wget's
// single-arg input; we use only tokens[0].
func splitSpace(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		if len(out) >= 2 {
			out = append(out, s[i:])
			return out
		}
		j := i
		for j < len(s) && s[j] != ' ' && s[j] != '\t' {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}
