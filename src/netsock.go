// src/netsock.go -- Socket-style UDP API for userspace (Phase 5).
//
// Exposes six syscalls (22..27) that let Ring-3 programs speak UDP
// through the kernel stack. The only socket type supported is
// AF_INET + SOCK_DGRAM; anything else returns -fdErrBad.
//
//   socketFd is a FileDesc impl holding a per-socket cap=16 recv
//   channel. sys_bind plugs that channel into the kernel udpBindings
//   table via udpBindWithChannel so the normal udpHandle dispatcher
//   delivers packets straight to userspace.
//
//   sys_recvfrom extends the design-doc API (net_socket_api.md §12
//   open question 1) with a timeout_ticks argument in R8. Zero means
//   block indefinitely; non-zero races the recv against afterTicks.
//   The DHCP client uses this to avoid hanging if no DHCP server
//   responds within 4 seconds.
//
//   sys_sendto_bcast bypasses ARP and forces source IP 0.0.0.0, used
//   by the DHCP client's DISCOVER/REQUEST broadcasts before it has an
//   IP or a learned gateway MAC.
//
// Concurrency: socketFd fields (bound, localPort, recvCh) are mutated
// without an internal lock. The file relies on gooos's current
// single-BSP cooperative-yield scheduling — handlers never preempt
// each other, and Close is guaranteed not to run concurrently with
// Read / sys_recvfrom because sockets are NOT inherited on spawn
// (see process.go's fd-inheritance loop, which drops *socketFd
// slots). If gooos ever schedules goroutines truly in parallel on
// multiple CPUs, socketFd needs a per-instance Spinlock.

package main

import "unsafe"

// Socket family / type / protocol values that the syscall layer
// accepts. Anything else is rejected up front.
const (
	sockAFInet    = 2 // AF_INET
	sockSockDgram = 2 // SOCK_DGRAM
)

// sys_net_config operation codes (RDI). Keep in sync with
// user/gooos/net.go.
const (
	netConfigGetIP      = 0
	netConfigSetIP      = 1
	netConfigGetNetmask = 2
	netConfigSetNetmask = 3
	netConfigGetGateway = 4
	netConfigSetGateway = 5
	netConfigGetMAC     = 6
	netConfigApply      = 7
	netConfigGetDNS     = 8
	netConfigSetDNS     = 9
)

// userAddrMin / userAddrMax bracket the range a user-supplied pointer
// must lie inside. Lower bound: the user code / data / heap base set
// by user/linker_user.ld (0x40000000). Upper bound: just above the
// user stack top (0x7FFF2000 in linker_user.ld), rounded up to 2 GiB
// so a future stack bump has headroom without revisiting this file.
// Pointers outside this window would dereference kernel memory or
// unmapped pages and panic the kernel during the syscall copy loop.
const (
	userAddrMin = uintptr(0x40000000)
	userAddrMax = uintptr(0x80000000)
)

// userBufInRange returns true when the half-open byte range
// [ptr, ptr+length) lies entirely within the user virtual address
// window, with overflow guarded. Zero-length ranges are always valid.
func userBufInRange(ptr, length uintptr) bool {
	if length == 0 {
		return true
	}
	if ptr < userAddrMin {
		return false
	}
	end := ptr + length
	if end < ptr { // wrap
		return false
	}
	return end <= userAddrMax
}

// sockKind* discriminates UDP from TCP socketFds. Default zero
// value (sockKindUDP) preserves Phase-5 UDP semantics for every
// existing call site that allocates `&socketFd{...}` without
// setting kind.
const (
	sockKindUDP         uint8 = 0
	sockKindTCPIdle     uint8 = 1 // post-Socket, pre-listen/connect
	sockKindTCPListener uint8 = 2 // after sys_listen
	sockKindTCPConn     uint8 = 3 // after sys_connect or sys_accept
)

// socketFd is the FileDesc implementation behind every Ring-3 socket.
//
// For UDP (kind == sockKindUDP), recvCh is the bound receive queue
// owned by the socket; udpBindWithChannel is given the same channel
// at bind time so when the socket closes, unbind removes the kernel
// reference and the channel becomes garbage.
//
// For TCP sockets (kind == sockKindTCP*), tcpTCB points at the TCB
// in the kernel-wide pool; tcpListener points at the listener
// entry for sockKindTCPListener sockets. The recvCh/localPort UDP
// fields are unused.
type socketFd struct {
	kind uint8 // discriminant; see sockKind* constants

	// UDP fields (valid when kind == sockKindUDP).
	localPort uint16
	bound     bool
	recvCh    chan UDPDatagram

	// TCP fields (valid when kind ∈ {sockKindTCPIdle,
	// sockKindTCPListener, sockKindTCPConn}).
	tcpListener *tcpListener
	tcpTCB      *TCB
}

// Read is defined so socketFd satisfies FileDesc. The real receive
// path is sys_recvfrom; Read only delivers the payload bytes (drops
// the source address — callers who need it must use sys_recvfrom).
func (s *socketFd) Read(buf []byte) (int, fdErr) {
	if !s.bound {
		return 0, fdErrBad
	}
	dg := <-s.recvCh
	n := len(dg.Data)
	if n > len(buf) {
		n = len(buf)
	}
	copy(buf[:n], dg.Data[:n])
	return n, fdErrOK
}

// Write is not supported on sockets — users must call sys_sendto to
// include a destination address.
func (s *socketFd) Write(buf []byte) (int, fdErr) {
	return 0, fdErrBad
}

// Close unbinds the socket from the kernel UDP table and drains any
// datagrams still queued on recvCh so the goroutine GC can reclaim
// them.
func (s *socketFd) Close() fdErr {
	if s.bound {
		udpUnbind(s.localPort)
		s.bound = false
	}
	for {
		select {
		case <-s.recvCh:
		default:
			return fdErrOK
		}
	}
}

// --- Syscall handlers ----------------------------------------------------

// sys_socket (22): RDI=domain, RSI=type, RDX=protocol → fd or -err.
func sysSocketHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	if frame.RDI != sockAFInet || frame.RSI != sockSockDgram {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	sock := &socketFd{recvCh: make(chan UDPDatagram, 16)}
	fd, err := procAllocFD(proc, sock)
	if err != fdErrOK {
		frame.RAX = sysFail(err)
		return
	}
	frame.RAX = uintptr(fd)
}

// sys_bind (23): RDI=fd, RSI=port → 0 or -err.
func sysBindHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	fd := int(frame.RDI)
	port := uint16(frame.RSI)

	desc := procGetFD(proc, fd)
	sock, ok := desc.(*socketFd)
	if !ok || sock == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	if sock.bound {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	if !udpBindWithChannel(port, sock.recvCh) {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	sock.localPort = port
	sock.bound = true
	frame.RAX = 0
}

// sys_sendto (24): RDI=fd, RSI=buf_ptr, RDX=buf_len, R10=dst_ip,
// R8=dst_port → bytes sent or -err.
func sysSendtoHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	fd := int(frame.RDI)
	desc := procGetFD(proc, fd)
	sock, ok := desc.(*socketFd)
	if !ok || sock == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}

	bufPtr := frame.RSI
	bufLen := frame.RDX
	dstIP := uint32(frame.R10)
	dstPort := uint16(frame.R8)

	if !userBufInRange(bufPtr, bufLen) {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	// Hard ceiling matches ipv4MaxPayload - udpHeaderSize.
	if bufLen > uintptr(ipv4MaxPayload-udpHeaderSize) {
		frame.RAX = sysFail(fdErrBad)
		return
	}

	data := make([]byte, bufLen)
	for i := uintptr(0); i < bufLen; i++ {
		data[i] = *(*byte)(unsafe.Pointer(bufPtr + i))
	}

	if !udpSend(dstIP, dstPort, sock.localPort, data) {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	frame.RAX = bufLen
}

// sys_recvfrom (25): RDI=fd, RSI=buf_ptr, RDX=buf_max, R10=info_ptr,
// R8=timeout_ticks (0 = block forever).
//
// info_ptr, when non-zero, receives 8 bytes:
//     [0:4]  srcIP  (uint32, host byte order)
//     [4:6]  srcPort (uint16)
//     [6:8]  padding
func sysRecvfromHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	fd := int(frame.RDI)
	desc := procGetFD(proc, fd)
	sock, ok := desc.(*socketFd)
	if !ok || sock == nil || !sock.bound {
		frame.RAX = sysFail(fdErrBad)
		return
	}

	bufPtr := frame.RSI
	bufMax := int(frame.RDX)
	infoPtr := frame.R10
	timeoutTicks := uint64(frame.R8)

	if bufMax < 0 || !userBufInRange(bufPtr, uintptr(bufMax)) {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	if infoPtr != 0 && !userBufInRange(infoPtr, 8) {
		frame.RAX = sysFail(fdErrBad)
		return
	}

	var dg UDPDatagram
	received := false
	if timeoutTicks == 0 {
		dg = <-sock.recvCh
		received = true
	} else {
		timeoutCh := afterTicks(timeoutTicks)
		select {
		case d := <-sock.recvCh:
			dg = d
			received = true
		case <-timeoutCh:
		}
	}
	if !received {
		frame.RAX = sysFail(fdErrBad)
		return
	}

	n := len(dg.Data)
	if n > bufMax {
		n = bufMax
	}
	for i := 0; i < n; i++ {
		*(*byte)(unsafe.Pointer(bufPtr + uintptr(i))) = dg.Data[i]
	}

	if infoPtr != 0 {
		*(*uint32)(unsafe.Pointer(infoPtr)) = dg.SrcIP
		*(*uint16)(unsafe.Pointer(infoPtr + 4)) = dg.SrcPort
		*(*uint16)(unsafe.Pointer(infoPtr + 6)) = 0
	}
	frame.RAX = uintptr(n)
}

// sys_net_config (26): RDI=op, RSI=a1, RDX=a2, R10=a3.
func sysNetConfigHandler(frame *SyscallFrame) {
	// currentProc() is not strictly needed for the SET/GET ops today,
	// but we call it for contract symmetry with the other handlers
	// and so that a future proc-scoped op (e.g. per-process overrides)
	// fails cleanly when called from a context without a Process.
	if currentProc() == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	switch frame.RDI {
	case netConfigGetIP:
		frame.RAX = uintptr(ourIP)
	case netConfigSetIP:
		ourIP = uint32(frame.RSI)
		frame.RAX = 0
	case netConfigGetNetmask:
		frame.RAX = uintptr(ourNetmask)
	case netConfigSetNetmask:
		ourNetmask = uint32(frame.RSI)
		frame.RAX = 0
	case netConfigGetGateway:
		frame.RAX = uintptr(ourGateway)
	case netConfigSetGateway:
		ourGateway = uint32(frame.RSI)
		frame.RAX = 0
	case netConfigGetMAC:
		ptr := frame.RSI
		if !userBufInRange(ptr, 6) {
			frame.RAX = sysFail(fdErrBad)
			return
		}
		for i := 0; i < 6; i++ {
			*(*byte)(unsafe.Pointer(ptr + uintptr(i))) = e1000MAC[i]
		}
		frame.RAX = 0
	case netConfigApply:
		arpSendGratuitous()
		frame.RAX = 0
	case netConfigGetDNS:
		frame.RAX = uintptr(ourDNS)
	case netConfigSetDNS:
		ourDNS = uint32(frame.RSI)
		frame.RAX = 0
	default:
		frame.RAX = sysFail(fdErrBad)
	}
}

// sys_sendto_bcast (27): RDI=fd, RSI=buf_ptr, RDX=buf_len, R10=dst_port.
// Source IP is forced to 0.0.0.0, destination IP to 255.255.255.255,
// destination MAC to broadcast — bypasses ARP. Used by DHCP before it
// has an IP.
func sysSendtoBcastHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	fd := int(frame.RDI)
	desc := procGetFD(proc, fd)
	sock, ok := desc.(*socketFd)
	if !ok || sock == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}

	bufPtr := frame.RSI
	bufLen := frame.RDX
	dstPort := uint16(frame.R10)

	if !userBufInRange(bufPtr, bufLen) {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	if bufLen > uintptr(ipv4MaxPayload-udpHeaderSize) {
		frame.RAX = sysFail(fdErrBad)
		return
	}

	data := make([]byte, bufLen)
	for i := uintptr(0); i < bufLen; i++ {
		data[i] = *(*byte)(unsafe.Pointer(bufPtr + i))
	}

	if !udpSendRaw(0, 0xFFFFFFFF, sock.localPort, dstPort, data) {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	frame.RAX = bufLen
}
