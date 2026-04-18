# Networking Stack — TCP Socket Syscall API

Detailed design for extending the Phase 5 socket syscall surface
(`impldoc/net_socket_api.md`) to `SOCK_STREAM`. Adds six new
syscalls (28-33) and extends `socketFd` with a TCP discriminant.

Parent doc: [`net_tcp_overview.md`](net_tcp_overview.md).
Depends on: [`net_tcp_state_machine.md`](net_tcp_state_machine.md)
(TCB struct, listener table), [`net_tcp_buffers.md`](net_tcp_buffers.md)
(per-TCB ring buffers).

---

## 1. Goals

1. Let Ring-3 programs create, listen, accept, connect, send,
   receive, and close TCP connections.
2. Preserve the existing Phase 5 socket ABI
   (`impldoc/net_socket_api.md`) bit-for-bit — syscalls 22-27
   continue to work against UDP sockets unchanged.
3. Match the register convention of Phase 5 exactly — `int 0x80`,
   RAX=number, RDI/RSI/RDX/R10/R8/R9=args, return in RAX; errno
   in the existing `fdErr` space (`src/fd.go:32-35`).
4. All user-memory pointer accesses go through the existing
   `userBufInRange` at `src/netsock.go:72-84`. No new validation
   primitive is introduced.
5. Userspace SDK in `user/gooos/net.go` mirrors the UDP surface.

### 1.1 Non-goals

- `select` / `poll` / `epoll` multiplexing. `sys_tcp_recv` is
  blocking with optional timeout.
- Non-blocking sockets / `O_NONBLOCK`. Deferred.
- `getsockopt` / `setsockopt` — no TCP-level options exposed in
  v1. `SO_REUSEADDR`, `TCP_NODELAY`, etc. are post-v1.
- `shutdown(SHUT_RD)` half-close. Only `SHUT_WR` and full close.
- `getpeername` / `getsockname` — not needed for v1 demos.

---

## 2. Syscall Additions

Six new syscall numbers extend the Phase 5 table from
`impldoc/net_socket_api.md §2`. **All** arguments follow the
existing convention; no new register usage is introduced.

| Nr | Name | RDI | RSI | RDX | R10 | R8 | Returns | Description |
|---|---|---|---|---|---|---|---|---|
| 28 | `sys_listen` | fd | backlog | — | — | — | 0 or -err | Mark socket as passive listener on port bound earlier by `sys_bind` |
| 29 | `sys_accept` | fd | info_ptr | timeout_ticks | — | — | new fd or -err | Block for incoming connection; return new fd; write peer address to `info_ptr` |
| 30 | `sys_connect` | fd | dst_ip | dst_port | timeout_ticks | — | 0 or -err | Actively open connection to `dst_ip:dst_port` |
| 31 | `sys_tcp_send` | fd | buf_ptr | buf_len | — | — | bytes sent or -err | Write up to `buf_len` bytes into the send buffer; blocks when full |
| 32 | `sys_tcp_recv` | fd | buf_ptr | buf_max | timeout_ticks | — | bytes received, 0 for EOF, or -err | Read up to `buf_max` bytes; blocks when empty (0 = block forever) |
| 33 | `sys_shutdown` | fd | how | — | — | — | 0 or -err | `how = 1` (SHUT_WR) sends FIN; `how = 2` (SHUT_RDWR) sends FIN and drops rx buffer |

### 2.1 Constants

```go
const (
    // Extending net_socket_api.md §2.2 — unchanged:
    AF_INET     = 2
    // New:
    SOCK_STREAM = 1
    IPPROTO_TCP = 6

    // Shutdown `how` values.
    shutdownRead  = 0 // not supported in v1 — returns -fdErrBad
    shutdownWrite = 1
    shutdownBoth  = 2
)
```

The existing `SOCK_DGRAM = 2` constant in
`impldoc/net_socket_api.md §2.2` is **not** changed.
`sys_socket` now accepts `(AF_INET, SOCK_STREAM, 0)` in
addition to the existing `(AF_INET, SOCK_DGRAM, 0)`.

### 2.2 Peer-address info format (sys_accept, sys_tcp_recv)

Identical layout to `UDPInfo` from `net_socket_api.md §2` to
let the user-side SDK share the helper type:

```
Offset 0  uint32  peer IP
Offset 4  uint16  peer port
Offset 6  uint16  padding (must be zero-written by kernel)
```

---

## 3. `socketFd` Extension

Current struct (`src/netsock.go:90-94`):

```go
type socketFd struct {
    localPort uint16
    bound     bool
    recvCh    chan UDPDatagram
}
```

Extended in Phase TCP-5 (single discriminant field; no existing
fields change type):

```go
type socketFd struct {
    // Discriminant. Sockets default to UDP (kind == sockKindUDP)
    // so all existing Phase 5 call sites continue to work.
    kind uint8

    // UDP fields (valid when kind == sockKindUDP). Unchanged.
    localPort uint16
    bound     bool
    recvCh    chan UDPDatagram

    // TCP fields (valid when kind == sockKindTCPIdle,
    // sockKindTCPListener, or sockKindTCPConn).
    tcpListener *tcpListener // non-nil when kind == sockKindTCPListener
    tcpTCB      *TCB         // non-nil when kind == sockKindTCPConn
}

const (
    sockKindUDP          uint8 = 0 // default — preserves existing call sites
    sockKindTCPIdle      uint8 = 1 // created by Socket(SOCK_STREAM), before listen/connect
    sockKindTCPListener  uint8 = 2 // after sys_listen
    sockKindTCPConn      uint8 = 3 // after sys_connect or returned from sys_accept
)
```

### 3.1 `FileDesc` methods for TCP kinds

```go
func (s *socketFd) Read(buf []byte) (int, fdErr) {
    switch s.kind {
    case sockKindUDP:
        // Unchanged: see src/netsock.go:99-110.
    case sockKindTCPConn:
        return tcpRead(s.tcpTCB, buf, 0 /* no timeout */)
    default:
        return 0, fdErrBad
    }
}

func (s *socketFd) Write(buf []byte) (int, fdErr) {
    switch s.kind {
    case sockKindTCPConn:
        return tcpWrite(s.tcpTCB, buf)
    default:
        return 0, fdErrBad // UDP returned fdErrBad already
    }
}

func (s *socketFd) Close() fdErr {
    switch s.kind {
    case sockKindUDP:
        // Unchanged: see src/netsock.go:121-133.
    case sockKindTCPListener:
        return tcpListenerClose(s.tcpListener)
    case sockKindTCPConn:
        return tcpConnClose(s.tcpTCB)
    case sockKindTCPIdle:
        return fdErrOK // nothing to tear down
    }
    return fdErrOK
}
```

`tcpRead` / `tcpWrite` / `tcpListenerClose` / `tcpConnClose`
are new helpers; see §4.

### 3.2 Process-inherit on exec

`socketFd` is dropped across `sys_exec` / `sys_spawn` — the
same guardrail added in `pasttodos/TODO_NET2.md` MAJOR finding
#1 for UDP sockets applies to TCP equally. The fd-inheritance
loop (`src/process.go`) must drop every `*socketFd` slot
regardless of kind. No new code needed if the existing loop
already type-asserts on `*socketFd` — verify during Phase TCP-5
and document.

---

## 4. Syscall Handler Implementations

All handlers follow the same skeleton as `src/netsock.go:138-389`:

1. Resolve `proc := currentProc()`; bail on nil.
2. Resolve `sock, ok := desc.(*socketFd)` from `fd`.
3. Validate `kind` matches the operation.
4. Acquire `tcbTableLock` (rank 9) when touching TCP state.
5. Perform the work; write `frame.RAX = result`.
6. Release the lock and return.

The UDP handlers at `src/netsock.go:138-389` remain unchanged
except `sysSocketHandler` (§4.0 below).

### 4.0 `sys_socket` extension

`sys_socket` at `src/netsock.go:138-155` currently requires
`(AF_INET, SOCK_DGRAM)`. Relax to accept `SOCK_STREAM` too:

```go
func sysSocketHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }

    if frame.RDI != sockAFInet {
        frame.RAX = sysFail(fdErrBad); return
    }
    switch frame.RSI {
    case sockSockDgram:
        sock := &socketFd{kind: sockKindUDP,
            recvCh: make(chan UDPDatagram, 16)}
        fd, err := procAllocFD(proc, sock)
        if err != fdErrOK { frame.RAX = sysFail(err); return }
        frame.RAX = uintptr(fd)
    case sockSockStream:
        sock := &socketFd{kind: sockKindTCPIdle}
        fd, err := procAllocFD(proc, sock)
        if err != fdErrOK { frame.RAX = sysFail(err); return }
        frame.RAX = uintptr(fd)
    default:
        frame.RAX = sysFail(fdErrBad)
    }
}

const sockSockStream = 1 // new constant alongside sockSockDgram
```

### 4.1 `sys_bind` (23) — TCP path

`sys_bind` is unchanged in signature but gains a TCP branch:

```go
case sockKindTCPIdle:
    // Reserve the local port in a new tcpBoundPorts[] table
    // (separate from udpBindings because TCP binds full
    // 4-tuples at listen/connect time — bind-only reservations
    // need their own book-keeping).
    if !tcpReservePort(port, proc.pid) {
        frame.RAX = sysFail(fdErrBad); return
    }
    sock.localPort = port
    sock.bound = true
    frame.RAX = 0
```

`tcpReservePort(port, pid)` is a new helper that takes
`tcpListenLock` (rank 10) and claims a port in a new small
reservation table (`tcpMaxListeners` entries — §6 below).

### 4.2 `sys_listen` (28)

```go
// sys_listen (28): RDI=fd, RSI=backlog → 0 or -err.
func sysListenHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }
    fd := int(frame.RDI)
    backlog := int(frame.RSI)
    if backlog <= 0 { backlog = 1 }
    if backlog > tcpAcceptQueueDepth { backlog = tcpAcceptQueueDepth }

    desc := procGetFD(proc, fd)
    sock, ok := desc.(*socketFd)
    if !ok || sock == nil || sock.kind != sockKindTCPIdle || !sock.bound {
        frame.RAX = sysFail(fdErrBad); return
    }

    flags := tcpListenLock.Acquire()
    defer tcpListenLock.Release(flags)

    listener := tcpListenerAlloc(sock.localPort, proc.pid)
    if listener == nil {
        frame.RAX = sysFail(fdErrBad); return
    }

    sock.kind = sockKindTCPListener
    sock.tcpListener = listener
    frame.RAX = 0
}
```

### 4.3 `sys_accept` (29)

```go
// sys_accept (29): RDI=fd, RSI=info_ptr, RDX=timeout_ticks
//   → new fd or -err.
func sysAcceptHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }
    fd := int(frame.RDI)
    infoPtr := frame.RSI
    timeout := uint64(frame.RDX)

    desc := procGetFD(proc, fd)
    sock, ok := desc.(*socketFd)
    if !ok || sock == nil || sock.kind != sockKindTCPListener {
        frame.RAX = sysFail(fdErrBad); return
    }
    if infoPtr != 0 && !userBufInRange(infoPtr, 8) {
        frame.RAX = sysFail(fdErrBad); return
    }

    tcb := tcpAcceptWait(sock.tcpListener, timeout)
    if tcb == nil {
        frame.RAX = sysFail(fdErrBad); return
    }

    // Wrap the TCB in a new socketFd and allocate an fd.
    newSock := &socketFd{
        kind:   sockKindTCPConn,
        tcpTCB: tcb,
    }
    newFd, ferr := procAllocFD(proc, newSock)
    if ferr != fdErrOK {
        tcpConnClose(tcb) // release back into TCB pool
        frame.RAX = sysFail(ferr); return
    }
    if infoPtr != 0 {
        *(*uint32)(unsafe.Pointer(infoPtr))     = tcb.remoteIP
        *(*uint16)(unsafe.Pointer(infoPtr + 4)) = tcb.remotePort
        *(*uint16)(unsafe.Pointer(infoPtr + 6)) = 0
    }
    frame.RAX = uintptr(newFd)
}
```

`tcpAcceptWait(listener, timeoutTicks)` blocks on
`listener.acceptWake` with the supplied timeout (0 = forever,
matching `sys_recvfrom`'s convention in
`impldoc/net_socket_api.md §12 Q1`). Returns the front-of-queue
TCB or `nil` on timeout.

### 4.4 `sys_connect` (30)

```go
// sys_connect (30): RDI=fd, RSI=dst_ip, RDX=dst_port,
//   R10=timeout_ticks → 0 or -err.
func sysConnectHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }
    fd := int(frame.RDI)
    dstIP := uint32(frame.RSI)
    dstPort := uint16(frame.RDX)
    timeout := uint64(frame.R10)

    desc := procGetFD(proc, fd)
    sock, ok := desc.(*socketFd)
    if !ok || sock == nil || sock.kind != sockKindTCPIdle {
        frame.RAX = sysFail(fdErrBad); return
    }

    // Allocate an ephemeral local port if not yet bound.
    if !sock.bound {
        p := tcpEphemeralPort()
        if p == 0 { frame.RAX = sysFail(fdErrBad); return }
        sock.localPort = p
        sock.bound = true
    }

    tcb := tcpActiveConnect(sock.localPort, dstIP, dstPort, proc.pid, timeout)
    if tcb == nil {
        frame.RAX = sysFail(fdErrBad); return
    }
    sock.kind = sockKindTCPConn
    sock.tcpTCB = tcb
    frame.RAX = 0
}
```

`tcpActiveConnect` drives the machine through SYN_SENT →
ESTABLISHED (or fails). On failure it frees the TCB and returns
`nil`; the socketFd remains in `sockKindTCPIdle` so the user
may retry with a different destination.

### 4.5 `sys_tcp_send` (31)

```go
// sys_tcp_send (31): RDI=fd, RSI=buf_ptr, RDX=buf_len
//   → bytes sent or -err. Blocks when send buffer is full.
func sysTcpSendHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }
    fd := int(frame.RDI)
    bufPtr := frame.RSI
    bufLen := frame.RDX

    desc := procGetFD(proc, fd)
    sock, ok := desc.(*socketFd)
    if !ok || sock == nil || sock.kind != sockKindTCPConn {
        frame.RAX = sysFail(fdErrBad); return
    }
    if bufLen == 0 {
        frame.RAX = 0; return
    }
    if !userBufInRange(bufPtr, bufLen) {
        frame.RAX = sysFail(fdErrBad); return
    }

    // Copy from user memory into a stack buffer (cap mssEff to
    // avoid a 64 KiB stack frame).
    n, err := tcpWriteFromUser(sock.tcpTCB, bufPtr, bufLen)
    if err != fdErrOK {
        frame.RAX = sysFail(err); return
    }
    frame.RAX = uintptr(n)
}
```

`tcpWriteFromUser` loops: copy up to `min(mssEff, space)` bytes
per iteration into `t.txBuf`, signal the TX goroutine, and
either return with the copied count (short-write on first fill)
or block on `t.txWake` to continue. v1 chooses to do a short
write on the first time the buffer fills — simpler and
prevents lock-holding indefinitely. The user-side SDK
(`TCPSend`) loops until all bytes are sent.

### 4.6 `sys_tcp_recv` (32)

```go
// sys_tcp_recv (32): RDI=fd, RSI=buf_ptr, RDX=buf_max,
//   R10=timeout_ticks → bytes received, 0 for EOF, or -err.
func sysTcpRecvHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }
    fd := int(frame.RDI)
    bufPtr := frame.RSI
    bufMax := frame.RDX
    timeout := uint64(frame.R10)

    desc := procGetFD(proc, fd)
    sock, ok := desc.(*socketFd)
    if !ok || sock == nil || sock.kind != sockKindTCPConn {
        frame.RAX = sysFail(fdErrBad); return
    }
    if bufMax == 0 {
        frame.RAX = 0; return
    }
    if !userBufInRange(bufPtr, bufMax) {
        frame.RAX = sysFail(fdErrBad); return
    }

    n, err := tcpReadIntoUser(sock.tcpTCB, bufPtr, bufMax, timeout)
    if err != fdErrOK {
        frame.RAX = sysFail(err); return
    }
    frame.RAX = uintptr(n) // 0 = EOF (peer FIN); >0 = data
}
```

`tcpReadIntoUser` returns:
- `n > 0, fdErrOK` — data delivered.
- `0, fdErrOK` — peer has closed (FIN received + rxBuf empty).
- `0, fdErrBad` — timeout expired, or connection reset.

### 4.7 `sys_shutdown` (33)

```go
// sys_shutdown (33): RDI=fd, RSI=how → 0 or -err.
func sysShutdownHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }
    fd := int(frame.RDI)
    how := int(frame.RSI)

    desc := procGetFD(proc, fd)
    sock, ok := desc.(*socketFd)
    if !ok || sock == nil || sock.kind != sockKindTCPConn {
        frame.RAX = sysFail(fdErrBad); return
    }

    switch how {
    case shutdownWrite:
        tcpShutdownWrite(sock.tcpTCB)
        frame.RAX = 0
    case shutdownBoth:
        tcpShutdownBoth(sock.tcpTCB)
        frame.RAX = 0
    default:
        // SHUT_RD deferred (see §1.1).
        frame.RAX = sysFail(fdErrBad)
    }
}
```

`tcpShutdownWrite(t)` enqueues a FIN at `sndNxt`, transitioning
ESTABLISHED → FIN_WAIT_1 or CLOSE_WAIT → LAST_ACK per the state
machine (`net_tcp_state_machine.md §3.2`). `tcpShutdownBoth(t)`
additionally drops any remaining bytes from `t.rxBuf` so
`sys_tcp_recv` returns 0 (EOF) immediately.

---

## 5. Dispatch Table Update

In `src/userspace.go`, extend the constants and the dispatch
switch from `impldoc/net_socket_api.md §5.2`:

```go
const (
    // ... existing 22-27 unchanged ...
    sysListen    = 28
    sysAccept    = 29
    sysConnect   = 30
    sysTcpSend   = 31
    sysTcpRecv   = 32
    sysShutdown  = 33
)

// In syscallDispatch, add:
case sysListen:   sysListenHandler(frame)
case sysAccept:   sysAcceptHandler(frame)
case sysConnect:  sysConnectHandler(frame)
case sysTcpSend:  sysTcpSendHandler(frame)
case sysTcpRecv:  sysTcpRecvHandler(frame)
case sysShutdown: sysShutdownHandler(frame)
```

---

## 6. Port Reservation for TCP

`sys_bind` on a TCP socket needs to reserve a local port even
before the socket transitions to listener. A lightweight
reservation table lives alongside `tcpListeners`:

```go
type tcpPortReservation struct {
    port   uint16
    pid    int
    active bool
}

var tcpPortReservations [tcpMaxListeners]tcpPortReservation
```

Protected by `tcpListenLock` (rank 10). `tcpReservePort(port,
pid)` fails if:
- The port is already in a reservation entry or listener entry.
- The table is full (all four slots active).

On `sockKindTCPListener → sockKindTCPIdle` transition (never
happens in v1) or on socket close, the reservation is released.
UDP binds (`udpBindings`) are a **separate** table; the two
protocols share no port reservation. This matches Linux
behaviour — TCP port 7 and UDP port 7 can coexist.

**Ephemeral ports** for `sys_connect` come from the range
`49152–49167` (cap 16). Allocated linearly; if all 16 are in
use, `tcpEphemeralPort` returns 0 and `sys_connect` fails. A
freed TCB releases its ephemeral port on `tcbFree`.

---

## 7. Userspace SDK (`user/gooos/net.go`)

Extend the existing file with TCP helpers, mirroring
`net_socket_api.md §6`:

```go
// --- TCP API (new) ---

const (
    SOCK_STREAM = 1

    SHUT_RD   = 0
    SHUT_WR   = 1
    SHUT_RDWR = 2
)

// TCPSocket creates a TCP stream socket.
func TCPSocket() int {
    r := syscall3(sysSocket, AF_INET, SOCK_STREAM, 0)
    return int(int64(r))
}

// TCPListen marks the socket as a passive listener. `backlog`
// is the pending-accept queue depth (capped at 8 in v1).
func TCPListen(fd, backlog int) int {
    r := syscall2(sysListen, uintptr(fd), uintptr(backlog))
    return int(int64(r))
}

// TCPAccept returns the new-connection fd and the peer
// address. `timeoutTicks` of 0 blocks forever.
func TCPAccept(fd int, timeoutTicks uint64) (int, UDPInfo) {
    var info UDPInfo
    r := syscall3(sysAccept,
        uintptr(fd),
        uintptr(unsafe.Pointer(&info)),
        uintptr(timeoutTicks),
    )
    return int(int64(r)), info
}

// TCPConnect actively opens a connection. `timeoutTicks` of 0
// uses the kernel's default connect retry schedule (1s, 3s, 7s).
func TCPConnect(fd int, dstIP uint32, dstPort uint16,
    timeoutTicks uint64) int {
    r := syscall4(sysConnect,
        uintptr(fd),
        uintptr(dstIP),
        uintptr(dstPort),
        uintptr(timeoutTicks),
    )
    return int(int64(r))
}

// TCPSend writes a chunk of bytes; may return fewer than
// len(data) — callers should loop for full writes.
func TCPSend(fd int, data []byte) int {
    if len(data) == 0 {
        return 0
    }
    r := syscall3(sysTcpSend,
        uintptr(fd),
        uintptr(unsafe.Pointer(&data[0])),
        uintptr(len(data)),
    )
    return int(int64(r))
}

// TCPSendAll loops TCPSend until all bytes are sent or an
// error occurs. Returns bytes sent on success.
func TCPSendAll(fd int, data []byte) int {
    total := 0
    for total < len(data) {
        n := TCPSend(fd, data[total:])
        if n <= 0 {
            if total > 0 { return total }
            return n
        }
        total += n
    }
    return total
}

// TCPRecv blocks for up to timeoutTicks; 0 = block forever;
// returns 0 on EOF (peer closed).
func TCPRecv(fd int, buf []byte, timeoutTicks uint64) int {
    if len(buf) == 0 {
        return 0
    }
    r := syscall4(sysTcpRecv,
        uintptr(fd),
        uintptr(unsafe.Pointer(&buf[0])),
        uintptr(len(buf)),
        uintptr(timeoutTicks),
    )
    return int(int64(r))
}

// TCPShutdown sends FIN (SHUT_WR) or sends FIN and drops rx
// buffer (SHUT_RDWR). SHUT_RD is not supported in v1.
func TCPShutdown(fd int, how int) int {
    r := syscall2(sysShutdown, uintptr(fd), uintptr(how))
    return int(int64(r))
}

// Extend the syscall-number block near the top of the file:
const (
    // ... existing 22-27 ...
    sysListen   = 28
    sysAccept   = 29
    sysConnect  = 30
    sysTcpSend  = 31
    sysTcpRecv  = 32
    sysShutdown = 33
)
```

No new syscall stub is required — `syscall3` and `syscall4`
from `user/rt0.S` already cover all six signatures. The
existing `syscall5` (added in Phase 5) remains unused by TCP.

---

## 8. Lock Ordering Interactions

- Every handler in §4 acquires `tcbTableLock` (rank 9) when it
  reads or mutates TCB state.
- Handlers that touch the listener table additionally acquire
  `tcpListenLock` (rank 10) — always after rank 9.
- `sys_bind`'s TCP path takes rank 10 directly (without rank 9
  first) because it does not touch any TCB.
- No handler takes `udpLock` (rank 7); TCP and UDP paths are
  disjoint past the initial `sysSocketHandler` switch.
- `procLock` (rank 2) is acquired and released inside
  `currentProc()` / `procGetFD` / `procAllocFD` before any TCP
  lock is taken — the existing UDP `sysBindHandler` at
  `src/netsock.go:158-184` is the precedent for this ordering
  (resolve `proc`, resolve `desc`, then take the protocol
  lock).

---

## 9. LOC Estimate

| Component | Min | Max | File |
|---|---|---|---|
| `socketFd` extension | 20 | 40 | (edit `src/netsock.go`) |
| `sys_socket` branch for SOCK_STREAM | 15 | 25 | (edit `src/netsock.go`) |
| `sys_bind` TCP branch | 25 | 40 | (edit `src/netsock.go`) |
| `sys_listen` / `sys_accept` / `sys_connect` handlers | 140 | 210 | (edit `src/netsock.go`) |
| `sys_tcp_send` / `sys_tcp_recv` handlers | 80 | 140 | (edit `src/netsock.go`) |
| `sys_shutdown` handler | 30 | 50 | (edit `src/netsock.go`) |
| `tcpReservePort` / `tcpEphemeralPort` / `tcpListenerAlloc` | 60 | 100 | `src/tcp.go` |
| Dispatch table + syscall constants | 12 | 15 | (edit `src/userspace.go`) |
| User SDK | 150 | 220 | (edit `user/gooos/net.go`) |
| **Total** | **532** | **840** | — |

---

## 10. Verification Criteria

1. **`TCPSocket` + `TCPListen`**: returns fd ≥ 3; second
   `TCPListen` on the same fd fails.
2. **`TCPAccept` blocks**: with no incoming connection, call
   blocks until timeout or connection arrives.
3. **`TCPAccept` delivers peer address**: `info.SrcIP` /
   `info.SrcPort` are the host's connecting IP/port.
4. **`TCPConnect` succeeds**: against `nc -l 10.0.2.2 9999`
   (TAP mode), `TCPConnect(fd, IPv4(10,0,2,2), 9999, 0)`
   returns 0 within 100 ms.
5. **`TCPConnect` timeouts**: against a closed host port,
   retries per schedule then returns error.
6. **`TCPSend` + `TCPRecv`**: round-trip "hello" through a
   userspace echo server.
7. **Short write**: `TCPSend` of 100 KiB returns partial
   counts; `TCPSendAll` completes the full transfer.
8. **Peer FIN**: `TCPRecv` returns 0 when the peer closes.
9. **`TCPShutdown(SHUT_WR)`**: sends FIN; subsequent `TCPSend`
   returns error; `TCPRecv` still works until peer also FINs.
10. **Kind safety**: calling `sys_tcp_send` on a UDP socket
    returns `-fdErrBad`. Calling `sys_sendto` on a TCP socket
    returns `-fdErrBad`.
11. **Regression**: all Phase 5 UDP tests
    (`pasttodos/TODO_NET2.md` Part C) still pass.

---

## 11. Open Questions

1. **`sys_tcp_send` short-write semantics**: v1 returns after
   the first block; the SDK loops (`TCPSendAll`). Alternative:
   kernel blocks until all bytes queued, returning only on
   error. Recommendation: keep short-write — avoids kernel-side
   long-held locks, user SDK loop is trivial.
2. **`sys_tcp_recv` partial reads**: v1 returns as soon as any
   bytes arrive. Alternative: fill-buffer semantics. Recommendation:
   partial — matches Linux default and is simpler.
3. **`SO_REUSEADDR`-equivalent**: v1 refuses re-bind of a
   TIME_WAIT port. Alternative: always allow re-bind after close.
   Recommendation: refuse; makes TIME_WAIT observable in tests.
4. **`TCPAccept` fairness**: the accept queue is FIFO. Fine for
   v1; no prioritization needed.
5. **Listener port on close**: closing the listener fd aborts
   any pending SYN_RECEIVED TCBs (RST to peer). Recommendation:
   accept; matches Linux.

---

## 12. Relationship to Other Documents

- **`impldoc/net_socket_api.md`**: Phase 5 socket ABI this
  extends. Every register convention, errno mapping, and
  `userBufInRange` call is reused verbatim.
- **`impldoc/busybox_syscall_abi.md`**: central syscall-number
  registry — add 28-33 to its table during Phase TCP-5.
- **`net_tcp_state_machine.md §6-§7`**: listener and accept
  queue that the handlers in §4 drive.
- **`net_tcp_buffers.md`**: `tcpRead` / `tcpWrite` underlying
  ring buffers.
- **`src/netsock.go:72-84`**: `userBufInRange` — every user-
  memory pointer access flows through it.
- **`impldoc/shell_io_fd_table.md §5.1`**: canonical syscall
  table that must be extended.
