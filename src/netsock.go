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
	sockAFInet     = 2 // AF_INET
	sockSockDgram  = 2 // SOCK_DGRAM
	sockSockStream = 1 // SOCK_STREAM (TCP)
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
// For UDP (kind == sockKindUDP), recvQ is the bound receive queue
// owned by the socket; udpBindWithQueue is given the same queue
// at bind time so when the socket closes, unbind removes the kernel
// reference and the queue becomes garbage. M4.2.b: was recvCh
// chan UDPDatagram pre-Route-C.
//
// For TCP sockets (kind == sockKindTCP*), tcpTCB points at the TCB
// in the kernel-wide pool; tcpListener points at the listener
// entry for sockKindTCPListener sockets. The recvQ/localPort UDP
// fields are unused.
type socketFd struct {
	kind uint8 // discriminant; see sockKind* constants

	// UDP fields (valid when kind == sockKindUDP).
	localPort uint16
	bound     bool
	recvQ     *udpDgramQueue

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
	dg := s.recvQ.Pop() // M4.2.b: was <-s.recvCh
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

// Close releases the kernel resources backing this socket. The
// cleanup depends on the socket's kind.
func (s *socketFd) Close() fdErr {
	switch s.kind {
	case sockKindUDP:
		if s.bound {
			udpUnbind(s.localPort)
			s.bound = false
		}
		// Drain the receive queue. M4.2.b: was a `select { case <-
		// s.recvCh: default: }` loop. TryPop returns false on empty.
		if s.recvQ != nil {
			for {
				if _, ok := s.recvQ.TryPop(); !ok {
					break
				}
			}
		}
		return fdErrOK
	case sockKindTCPIdle:
		// Nothing in the kernel yet — just drop the fd.
		return fdErrOK
	case sockKindTCPListener:
		if s.tcpListener != nil {
			// Snapshot the queued TCBs under the listener lock,
			// then tear them down outside any TCP lock (tcpClose
			// descends into tcpSendSegment → ipv4Send which
			// eventually takes netBufLock at rank 5).
			lflags := tcpListenLock.Acquire()
			var drained [tcpAcceptQueueDepth * 2]*TCB
			dn := 0
			for i := 0; i < s.tcpListener.nPending; i++ {
				drained[dn] = s.tcpListener.pending[i]
				s.tcpListener.pending[i] = nil
				dn++
			}
			s.tcpListener.nPending = 0
			for i := 0; i < s.tcpListener.nAccept; i++ {
				drained[dn] = s.tcpListener.accept[i]
				s.tcpListener.accept[i] = nil
				dn++
			}
			s.tcpListener.nAccept = 0
			s.tcpListener.active = false
			tcpListenLock.Release(lflags)

			for i := 0; i < dn; i++ {
				t := drained[i]
				if t == nil {
					continue
				}
				// Clear the backpointer so any late segment
				// handling doesn't deref a freed slot.
				tflags := tcbTableLock.Acquire()
				t.listener = nil
				tcbTableLock.Release(tflags)
				tcpClose(t) // send FIN if ESTABLISHED-ish
				// Orphaned SYN_RECEIVED TCBs won't get their ACK
				// anyway; free them outright.
				if t.state == tcpStateSynReceived {
					tcbFree(t)
				}
			}
			s.tcpListener = nil
		}
		return fdErrOK
	case sockKindTCPConn:
		if s.tcpTCB != nil {
			// Graceful close — initiate FIN handshake; the
			// state machine + scanner free the TCB later.
			tcpClose(s.tcpTCB)
			s.tcpTCB = nil
		}
		return fdErrOK
	}
	return fdErrOK
}

// --- Syscall handlers ----------------------------------------------------

// sys_socket (22): RDI=domain, RSI=type, RDX=protocol → fd or -err.
// Domain must be AF_INET. Type distinguishes UDP / TCP.
func sysSocketHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	if frame.RDI != sockAFInet {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	var sock *socketFd
	switch frame.RSI {
	case sockSockDgram:
		sock = &socketFd{
			kind:  sockKindUDP,
			recvQ: newUdpDgramQueue(),
		}
	case sockSockStream:
		sock = &socketFd{kind: sockKindTCPIdle}
	default:
		frame.RAX = sysFail(fdErrBad)
		return
	}
	fd, err := procAllocFD(proc, sock)
	if err != fdErrOK {
		frame.RAX = sysFail(err)
		return
	}
	frame.RAX = uintptr(fd)
}

// sys_bind (23): RDI=fd, RSI=port → 0 or -err. Branches on
// socket kind — UDP binds into udpBindings, TCP reserves the
// port in the listener-port space for a later sys_listen.
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
	switch sock.kind {
	case sockKindUDP:
		if !udpBindWithQueue(port, sock.recvQ) {
			frame.RAX = sysFail(fdErrBad)
			return
		}
		sock.localPort = port
		sock.bound = true
		frame.RAX = 0

	case sockKindTCPIdle:
		// TCP uses a separate port space (TCP/UDP can share
		// port numbers per the design). We note the port on
		// the socket and let sys_listen allocate the listener
		// entry in tcpListeners.
		sock.localPort = port
		sock.bound = true
		frame.RAX = 0

	default:
		frame.RAX = sysFail(fdErrBad)
	}
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

	// M4.2.b: was `<-sock.recvCh` blocking + `select { ch /
	// timeoutCh }` for timeout. Both are H-01 hazards from kthread
	// context. Replace with direct queue Pop / TryPop bounded-poll.
	var dg UDPDatagram
	received := false
	if timeoutTicks == 0 {
		dg = sock.recvQ.Pop()
		received = true
	} else {
		deadline := pitTicks + timeoutTicks
		for {
			if v, ok := sock.recvQ.TryPop(); ok {
				dg = v
				received = true
				break
			}
			if pitTicks >= deadline {
				break
			}
			// 50 ms sleep between polls; matches M4.3 TCP-poll
			// pattern.
			if kschedRunning[cpuID()] != nil {
				kschedTimedPark(5)
			} else {
				<-afterTicks(5)
			}
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

// --- Phase TCP-5 syscalls ----------------------------------------

// requireTCPConn / requireTCPIdle / requireTCPListener resolve an fd
// to a socketFd of the expected TCP kind. Returns (nil, nil) and
// writes -fdErrBad to frame on any mismatch — caller must return
// early in that case.
func requireTCP(frame *SyscallFrame, wantKind uint8) (*Process, *socketFd) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return nil, nil
	}
	fd := int(frame.RDI)
	desc := procGetFD(proc, fd)
	sock, ok := desc.(*socketFd)
	if !ok || sock == nil || sock.kind != wantKind {
		frame.RAX = sysFail(fdErrBad)
		return nil, nil
	}
	return proc, sock
}

// sys_listen (28): RDI=fd, RSI=backlog → 0 or -err.
func sysListenHandler(frame *SyscallFrame) {
	proc, sock := requireTCP(frame, sockKindTCPIdle)
	if sock == nil {
		return
	}
	if !sock.bound {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	_ = proc // pid for listener.owner
	flags := tcpListenLock.Acquire()
	l := tcpListenerAllocLocked(sock.localPort, -1)
	tcpListenLock.Release(flags)
	if l == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	sock.kind = sockKindTCPListener
	sock.tcpListener = l
	frame.RAX = 0
}

// sys_accept (29): RDI=fd, RSI=info_ptr, RDX=timeout_ticks →
// new fd or -err. info_ptr (if nonzero) receives the peer
// {srcIP uint32, srcPort uint16, padding uint16}.
func sysAcceptHandler(frame *SyscallFrame) {
	proc, sock := requireTCP(frame, sockKindTCPListener)
	if sock == nil {
		return
	}
	infoPtr := frame.RSI
	timeout := uint64(frame.RDX)
	if infoPtr != 0 && !userBufInRange(infoPtr, 8) {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	l := sock.tcpListener

	// Poll the listener's accept queue. For v1 we spin with
	// afterTicks-based cooperative yield — the state machine
	// splices TCBs from pending to accept on handshake completion.
	var tcb *TCB
	deadline := uint64(0)
	if timeout > 0 {
		deadline = pitTicks + timeout
	}
	for {
		lflags := tcpListenLock.Acquire()
		if l.nAccept > 0 {
			tcb = l.accept[0]
			for i := 0; i < l.nAccept-1; i++ {
				l.accept[i] = l.accept[i+1]
			}
			l.accept[l.nAccept-1] = nil
			l.nAccept--
		}
		tcpListenLock.Release(lflags)
		if tcb != nil {
			break
		}
		if timeout > 0 && pitTicks >= deadline {
			frame.RAX = sysFail(fdErrBad)
			return
		}
		// M4.3: kthread-hosted callers must use kschedTimedPark
		// to avoid the H-01 chan-recv hazard.
		if kschedRunning[cpuID()] != nil {
			kschedTimedPark(5)
		} else {
			<-afterTicks(5) // 50 ms poll
		}
	}

	// Wrap the TCB in a fresh socketFd and allocate a new fd.
	newSock := &socketFd{kind: sockKindTCPConn, tcpTCB: tcb}
	newFd, ferr := procAllocFD(proc, newSock)
	if ferr != fdErrOK {
		tcpClose(tcb)
		frame.RAX = sysFail(ferr)
		return
	}
	if infoPtr != 0 {
		*(*uint32)(unsafe.Pointer(infoPtr)) = tcb.remoteIP
		*(*uint16)(unsafe.Pointer(infoPtr + 4)) = tcb.remotePort
		*(*uint16)(unsafe.Pointer(infoPtr + 6)) = 0
	}
	frame.RAX = uintptr(newFd)
}

// sys_connect (30): RDI=fd, RSI=dst_ip, RDX=dst_port,
// R10=timeout_ticks → 0 or -err.
func sysConnectHandler(frame *SyscallFrame) {
	_, sock := requireTCP(frame, sockKindTCPIdle)
	if sock == nil {
		return
	}
	dstIP := uint32(frame.RSI)
	dstPort := uint16(frame.RDX)
	timeout := uint64(frame.R10)
	if timeout == 0 {
		timeout = 1200 // 12 s overall envelope
	}
	tcb := tcpActiveConnect(dstIP, dstPort)
	if tcb == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	// Wait for ESTABLISHED, with timeout.
	deadline := pitTicks + timeout
	for {
		tflags := tcbTableLock.Acquire()
		st := tcb.state
		tcbTableLock.Release(tflags)
		if st == tcpStateEstablished {
			break
		}
		if st == tcpStateClosed || !tcb.active {
			frame.RAX = sysFail(fdErrBad)
			return
		}
		if pitTicks >= deadline {
			tcpClose(tcb)
			frame.RAX = sysFail(fdErrBad)
			return
		}
		// M4.3: kthread-hosted callers must use kschedTimedPark.
		if kschedRunning[cpuID()] != nil {
			kschedTimedPark(5)
		} else {
			<-afterTicks(5)
		}
	}
	sock.kind = sockKindTCPConn
	sock.tcpTCB = tcb
	frame.RAX = 0
}

// sys_tcp_send (31): RDI=fd, RSI=buf_ptr, RDX=buf_len →
// bytes sent or -err. Writes up to buf_len bytes into the
// kernel's txBuf; data is transmitted by a per-TCB TX pass
// scheduled by the kernel. Short-write semantics — the caller
// loops on remaining bytes.
func sysTcpSendHandler(frame *SyscallFrame) {
	_, sock := requireTCP(frame, sockKindTCPConn)
	if sock == nil {
		return
	}
	bufPtr := frame.RSI
	bufLen := frame.RDX
	if bufLen == 0 {
		frame.RAX = 0
		return
	}
	if !userBufInRange(bufPtr, bufLen) {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	tcb := sock.tcpTCB
	if tcb == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}

	// Copy user bytes into txBuf under tcbTableLock, subject to
	// the ring's free space. Short-write if the ring fills.
	tflags := tcbTableLock.Acquire()
	if !tcb.active || tcb.state != tcpStateEstablished {
		tcbTableLock.Release(tflags)
		frame.RAX = sysFail(fdErrBad)
		return
	}
	free := uint32(tcb.txBuf.rbFree())
	n := uint32(bufLen)
	if n > free {
		n = free
	}
	// Copy byte-by-byte from user memory into txBuf.
	for i := uint32(0); i < n; i++ {
		b := *(*byte)(unsafe.Pointer(bufPtr + uintptr(i)))
		var one [1]byte
		one[0] = b
		tcb.txBuf.rbWrite(one[:])
	}
	tcbTableLock.Release(tflags)

	// Drain: while the peer has window, emit up to mssEff-sized
	// data segments drawn from txBuf at sndNxt. This runs from
	// the syscall goroutine (outside tcbTableLock for the TX).
	tcpTCBDrainTX(tcb)

	frame.RAX = uintptr(n)
}

// sys_tcp_recv (32): RDI=fd, RSI=buf_ptr, RDX=buf_max,
// R10=timeout_ticks → bytes received (0 = EOF) or -err.
func sysTcpRecvHandler(frame *SyscallFrame) {
	_, sock := requireTCP(frame, sockKindTCPConn)
	if sock == nil {
		return
	}
	bufPtr := frame.RSI
	bufMax := frame.RDX
	timeout := uint64(frame.R10)
	if bufMax == 0 {
		frame.RAX = 0
		return
	}
	if !userBufInRange(bufPtr, bufMax) {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	tcb := sock.tcpTCB
	if tcb == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	deadline := uint64(0)
	if timeout > 0 {
		deadline = pitTicks + timeout
	}
	for {
		tflags := tcbTableLock.Acquire()
		if !tcb.active {
			tcbTableLock.Release(tflags)
			frame.RAX = sysFail(fdErrBad)
			return
		}
		n := uint32(tcb.rxBuf.rbLen())
		if n > 0 {
			if n > uint32(bufMax) {
				n = uint32(bufMax)
			}
			// Read into a stack buffer to avoid touching user
			// memory while holding the lock.
			var scratch [1500]byte
			if n > uint32(len(scratch)) {
				n = uint32(len(scratch))
			}
			got := tcb.rxBuf.rbRead(scratch[:n])
			// Let rcvWnd recover — we just drained rxBuf.
			tcb.rcvWnd += uint32(got)
			if tcb.rcvWnd > uint32(tcpRxBufSize) {
				tcb.rcvWnd = uint32(tcpRxBufSize)
			}
			tcbTableLock.Release(tflags)
			// Copy scratch into user memory without the lock.
			for i := 0; i < got; i++ {
				*(*byte)(unsafe.Pointer(bufPtr + uintptr(i))) = scratch[i]
			}
			frame.RAX = uintptr(got)
			return
		}
		// No bytes buffered. EOF if peer has closed (state past
		// ESTABLISHED and rxBuf empty).
		eof := tcb.state == tcpStateCloseWait ||
			tcb.state == tcpStateLastAck ||
			tcb.state == tcpStateClosed
		tcbTableLock.Release(tflags)
		if eof {
			frame.RAX = 0
			return
		}
		if timeout > 0 && pitTicks >= deadline {
			frame.RAX = sysFail(fdErrBad)
			return
		}
		// M4.3: kthread-hosted callers must use kschedTimedPark.
		if kschedRunning[cpuID()] != nil {
			kschedTimedPark(5)
		} else {
			<-afterTicks(5)
		}
	}
}

// sys_shutdown (33): RDI=fd, RSI=how → 0 or -err.
// how = 1 (SHUT_WR) or 2 (SHUT_RDWR).
func sysShutdownHandler(frame *SyscallFrame) {
	_, sock := requireTCP(frame, sockKindTCPConn)
	if sock == nil {
		return
	}
	how := int(frame.RSI)
	tcb := sock.tcpTCB
	if tcb == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	switch how {
	case 1, 2: // SHUT_WR / SHUT_RDWR
		tcpClose(tcb)
		if how == 2 {
			tflags := tcbTableLock.Acquire()
			tcb.rxBuf.rbReset()
			tcbTableLock.Release(tflags)
		}
		frame.RAX = 0
	default:
		frame.RAX = sysFail(fdErrBad)
	}
}

// tcpTCBDrainTX emits data segments from t.txBuf up to the
// peer's advertised window and our cwnd. Called by
// sys_tcp_send after queuing user bytes and by the RTO /
// window-update paths (future wiring). Caller does NOT hold
// tcbTableLock.
func tcpTCBDrainTX(t *TCB) {
	for {
		tflags := tcbTableLock.Acquire()
		if !t.active || t.state != tcpStateEstablished {
			tcbTableLock.Release(tflags)
			return
		}
		// Bytes we can send = min(cwnd, sndWnd) - in-flight.
		flight := t.sndNxt - t.sndUna
		window := t.sndWnd
		if t.cwnd < window {
			window = t.cwnd
		}
		if flight >= window {
			tcbTableLock.Release(tflags)
			return
		}
		canSend := window - flight
		// Bytes already queued but not yet sent = txBuf.len -
		// flight.
		queued := uint32(t.txBuf.rbLen()) - flight
		if queued == 0 {
			tcbTableLock.Release(tflags)
			return
		}
		n := queued
		if n > canSend {
			n = canSend
		}
		mss := uint32(t.mssEff)
		if mss > 0 && n > mss {
			n = mss
		}
		// Peek n bytes starting at (flight) offset — those are
		// the bytes at sndNxt..sndNxt+n.
		var buf [1500]byte
		if n > uint32(len(buf)) {
			n = uint32(len(buf))
		}
		t.txBuf.rbPeek(flight, n, buf[:n])
		// Capture seq for retx descriptor before sendmove.
		seq := t.sndNxt
		tcbTableLock.Release(tflags)

		ok := tcpSendSegment(t, tcpFlagACK|tcpFlagPSH, nil, buf[:n])
		if !ok {
			return
		}

		tflags = tcbTableLock.Acquire()
		t.sndNxt += n
		retxPush(t, tcpRetxEntry{
			seq:       seq,
			endSeq:    seq + n,
			flags:     tcpFlagACK | tcpFlagPSH,
			bufOff:    flight, // offset within txBuf at send time
			bufLen:    uint16(n),
			sentTicks: pitTicks,
		})
		tcpArmRTO(t)
		tcbTableLock.Release(tflags)
	}
}
