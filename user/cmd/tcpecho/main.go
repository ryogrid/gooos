// user/cmd/tcpecho — userspace TCP echo server on port 8081.
//
// Phase TCP-5 demo: accept incoming TCP connections on 8081, echo
// each chunk of bytes back to the peer, and close cleanly on
// peer-FIN (TCPRecv returns 0 = EOF). The run-net Makefile target
// forwards host :10081 → guest :8081, so:
//
//     echo hi | nc -w 3 127.0.0.1 10081
//
// exercises the full 3WHS → data → close sequence through Ring-3
// user code. Mirrors the scope of udpecho.elf.
//
// Connections are handled **serially** — one at a time in the
// main goroutine. A `go handleConn(cfd)` pattern would be more
// idiomatic but deadlocks against gooos's accepted v1
// limitation that a blocking syscall parks the entire user
// process's ring3Wrapper goroutine, freezing every user
// goroutine in the process (see
// `impldoc/userspace_scheduler_integration.md` §4). Since
// `TCPAccept` is blocking, spawning `handleConn` in a
// goroutine means it would never get scheduled until accept
// returns — which never happens while accept is blocked. §4.3
// explicitly prescribes this "blocking I/O inline" pattern;
// `udpecho.elf` follows it.

package main

import "github.com/ryogrid/gooos/user/gooos"

const listenPort = 8081

func main() {
	gooos.Println("tcpecho: starting userspace echo on TCP port 8081")

	fd := gooos.TCPSocket()
	if fd < 0 {
		gooos.Println("tcpecho: TCPSocket() failed")
		return
	}
	defer gooos.Close(fd)

	if gooos.Bind(fd, listenPort) < 0 {
		gooos.Println("tcpecho: Bind(8081) failed")
		return
	}
	if gooos.TCPListen(fd, 8) < 0 {
		gooos.Println("tcpecho: TCPListen failed")
		return
	}

	var buf [1500]byte
	for {
		cfd, _ := gooos.TCPAccept(fd, 0 /* block forever */)
		if cfd < 0 {
			continue
		}
		for {
			n := gooos.TCPRecv(cfd, buf[:], 0)
			if n <= 0 {
				break // 0 = peer EOF; <0 = error
			}
			if gooos.TCPSendAll(cfd, buf[:n]) <= 0 {
				break
			}
		}
		gooos.Close(cfd)
	}
}
