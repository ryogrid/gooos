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

import (
	"strconv"

	"github.com/ryogrid/gooos/user/gooos"
)

func main() {
	argline := gooos.Args()
	tokens := splitSpace(argline)
	if len(tokens) < 1 {
		gooos.Println("usage: wget <url>")
		return
	}
	url := tokens[0]
	ip, port, path, filename, errMsg := parseURL(url)
	if errMsg != "" {
		gooos.Println(errMsg)
		return
	}
	_ = filename // used in step 5 (file output)

	// Build the HTTP/1.0 GET request. Connection: close lets
	// the server signal end-of-body by closing the socket
	// (RFC 1945 §7.2.2), so we don't need Content-Length or
	// chunked-decoding logic.
	hostport := formatIP(ip)
	if port != 80 {
		hostport += ":" + strconv.Itoa(int(port))
	}
	req := "GET " + path + " HTTP/1.0\r\n" +
		"Host: " + hostport + "\r\n" +
		"User-Agent: gooos-wget/0.1\r\n" +
		"Connection: close\r\n" +
		"\r\n"

	fd := gooos.TCPSocket()
	if fd < 0 {
		gooos.Println("wget: TCPSocket failed")
		return
	}
	defer gooos.Close(fd)

	if gooos.TCPConnect(fd, ip, port, 0) < 0 {
		gooos.Println("wget: TCPConnect failed")
		return
	}

	reqBytes := []byte(req)
	if gooos.TCPSendAll(fd, reqBytes) != len(reqBytes) {
		gooos.Println("wget: TCPSendAll short/failed")
		return
	}

	// Read up to one 4 KiB buffer of response. Step 4 will
	// replace this with readHeaders() proper.
	var buf [4096]byte
	n := gooos.TCPRecv(fd, buf[:], 0)
	if n < 0 {
		gooos.Println("wget: TCPRecv error " + strconv.Itoa(n))
		return
	}
	gooos.Println("wget: response (" + strconv.Itoa(n) + " bytes):")
	gooos.Println(string(buf[:n]))
	gooos.TCPShutdown(fd, gooos.SHUT_WR)
}

// parseURL splits an HTTP URL of the form
//
//	http://<IPv4>[:port]/<path>
//
// into its components. Returns errMsg = "" on success and
// non-empty errMsg on rejection. Reject cases:
//   - HTTPS or no scheme        → "wget: only http:// supported"
//   - non-IPv4-literal host     → "wget: hostname not supported (no DNS); use IP literal"
//   - host == 0.0.0.0           → "wget: 0.0.0.0 is not a valid target"
//   - bad port                  → "wget: bad port"
//   - empty basename (URL ends  → "wget: URL has no basename"
//     in /, or path is "" / ".")
//
// parseURL distinguishes "0.0.0.0" from malformed IPv4
// input via parseIPOK so the user sees the right error.
func parseURL(s string) (ip uint32, port uint16, path string, filename string, errMsg string) {
	const httpsPrefix = "https://"
	if hasPrefix(s, httpsPrefix) {
		errMsg = "wget: only http:// supported"
		return
	}
	const httpPrefix = "http://"
	if !hasPrefix(s, httpPrefix) {
		errMsg = "wget: only http:// supported"
		return
	}
	rest := s[len(httpPrefix):]

	// Split off the path at the first '/'.
	pathStart := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			pathStart = i
			break
		}
	}
	var authority string
	if pathStart < 0 {
		authority = rest
		path = ""
	} else {
		authority = rest[:pathStart]
		path = rest[pathStart:]
	}

	// Split authority on ':'.
	host := authority
	port = 80
	for i := 0; i < len(authority); i++ {
		if authority[i] == ':' {
			host = authority[:i]
			p := parseInt(authority[i+1:])
			if p <= 0 || p > 65535 {
				errMsg = "wget: bad port"
				return
			}
			port = uint16(p)
			break
		}
	}

	if len(host) == 0 {
		errMsg = "wget: missing host"
		return
	}
	parsedIP, ok := parseIPOK(host)
	if !ok {
		errMsg = "wget: hostname not supported (no DNS); use IP literal"
		return
	}
	if parsedIP == 0 {
		errMsg = "wget: 0.0.0.0 is not a valid target"
		return
	}
	ip = parsedIP

	// Filename = last '/'-delimited segment of path. Reject "",
	// "/", and "." / "/.".
	if path == "" || path == "/" {
		errMsg = "wget: URL has no basename"
		return
	}
	last := -1
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			last = i
		}
	}
	if last == len(path)-1 {
		errMsg = "wget: URL has no basename"
		return
	}
	filename = path[last+1:]
	if filename == "." || filename == ".." {
		errMsg = "wget: URL has no basename"
		return
	}
	return
}

// hasPrefix reports whether s begins with prefix.
func hasPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

// formatIP renders a host-order IPv4 uint32 as "a.b.c.d".
func formatIP(ip uint32) string {
	a := int((ip >> 24) & 0xff)
	b := int((ip >> 16) & 0xff)
	c := int((ip >> 8) & 0xff)
	d := int(ip & 0xff)
	return strconv.Itoa(a) + "." + strconv.Itoa(b) + "." +
		strconv.Itoa(c) + "." + strconv.Itoa(d)
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

// parseIPOK is derived from user/cmd/tcpcli/main.go:84–116
// but returns an explicit ok bool so callers can
// distinguish the literal "0.0.0.0" from malformed input.
// Returns (0, false) on parse failure; (ip, true) on
// success (including ip == 0 for "0.0.0.0").
func parseIPOK(s string) (uint32, bool) {
	var parts [4]uint32
	idx := 0
	cur := uint32(0)
	have := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if !have || idx == 3 {
				return 0, false
			}
			parts[idx] = cur
			idx++
			cur = 0
			have = false
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		cur = cur*10 + uint32(c-'0')
		if cur > 255 {
			return 0, false
		}
		have = true
	}
	if !have || idx != 3 {
		return 0, false
	}
	parts[3] = cur
	return parts[0]<<24 | parts[1]<<16 | parts[2]<<8 | parts[3], true
}

// parseInt — copied from user/cmd/tcpcli/main.go:120–133.
// Decimal string → int; returns 0 on malformed input.
func parseInt(s string) int {
	if len(s) == 0 {
		return 0
	}
	v := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0
		}
		v = v*10 + int(c-'0')
	}
	return v
}
