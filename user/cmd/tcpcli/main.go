// user/cmd/tcpcli — userspace TCP client.
//
// Shell usage:
//     tcpcli <ip> <port> <message>
// Connects to <ip>:<port>, sends <message>, reads up to 1500 bytes
// of response, prints it, and closes cleanly. Intended for
// exercising the Phase TCP-5 active-open (sys_connect) + data TX
// + close path.

package main

import "github.com/ryogrid/gooos/user/gooos"

func main() {
	argline := gooos.Args()
	tokens := splitSpace(argline)
	if len(tokens) < 3 {
		gooos.Println("usage: tcpcli <ip> <port> <message>")
		return
	}
	ip := parseIP(tokens[0])
	port := uint16(parseInt(tokens[1]))
	msg := tokens[2]
	if ip == 0 || port == 0 {
		gooos.Println("tcpcli: bad ip or port")
		return
	}

	fd := gooos.TCPSocket()
	if fd < 0 {
		gooos.Println("tcpcli: TCPSocket failed")
		return
	}
	defer gooos.Close(fd)

	if gooos.TCPConnect(fd, ip, port, 0 /* default timeout */) < 0 {
		gooos.Println("tcpcli: TCPConnect failed")
		return
	}

	if gooos.TCPSendAll(fd, []byte(msg)) != len(msg) {
		gooos.Println("tcpcli: TCPSendAll short/failed")
		return
	}

	var buf [1500]byte
	n := gooos.TCPRecv(fd, buf[:], 200 /* 2 s */)
	if n > 0 {
		gooos.Println("tcpcli: <- " + string(buf[:n]))
	}

	gooos.TCPShutdown(fd, gooos.SHUT_WR)
}

// splitSpace splits on runs of ASCII space / tab, preserving the
// remainder of the string verbatim after 3 tokens (so a multi-
// word message passes through as a single token).
func splitSpace(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		// Skip spaces.
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		if len(out) >= 2 {
			// Remainder becomes the last token verbatim.
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

// parseIP parses an "a.b.c.d" string into a host-order uint32.
func parseIP(s string) uint32 {
	var parts [4]uint32
	idx := 0
	cur := uint32(0)
	have := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if !have || idx == 3 {
				return 0
			}
			parts[idx] = cur
			idx++
			cur = 0
			have = false
			continue
		}
		if c < '0' || c > '9' {
			return 0
		}
		cur = cur*10 + uint32(c-'0')
		if cur > 255 {
			return 0
		}
		have = true
	}
	if !have || idx != 3 {
		return 0
	}
	parts[3] = cur
	return parts[0]<<24 | parts[1]<<16 | parts[2]<<8 | parts[3]
}

// parseInt parses a non-negative decimal string. Returns 0 on
// malformed input.
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
