// user/cmd/udpecho — userspace UDP echo server on port 17.
//
// Proves the Phase-5 socket API end-to-end: Socket + Bind listen on
// UDP 17; every received datagram is UDPSendTo'd back to its sender.
// The run-net Makefile target forwards host :19999 → guest :17, so
// `echo hi | nc -u -w2 127.0.0.1 19999` exercises the full round
// trip through Ring-3 userspace code.
//
// Deliberately kept small — no flags, no logging beyond one startup
// message. Exit via Ctrl+C (not wired up) or reboot.

package main

import "github.com/ryogrid/gooos/user/gooos"

const listenPort = 17

func main() {
	gooos.Println("udpecho: starting userspace echo on UDP port 17")

	fd := gooos.Socket()
	if fd < 0 {
		gooos.Println("udpecho: Socket() failed")
		return
	}
	defer gooos.Close(fd)

	if gooos.Bind(fd, listenPort) < 0 {
		gooos.Println("udpecho: Bind(17) failed — port in use?")
		return
	}

	var buf [1500]byte
	for {
		n, info := gooos.UDPRecvFrom(fd, buf[:])
		if n <= 0 {
			continue
		}
		gooos.UDPSendTo(fd, buf[:n], info.SrcIP, info.SrcPort)
	}
}
