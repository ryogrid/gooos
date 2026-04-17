# Networking Stack — Socket Syscall API for Userspace

Detailed design for the socket system call layer that enables
Ring-3 userspace TinyGo programs to perform network I/O.
Builds on the kernel UDP/IPv4 stack described in
`net_ipv4_icmp_udp.md` and integrates with the per-process
file descriptor table defined in `shell_io_fd_table.md`.

Parent doc: `net_overview.md`.
Depends on: Phase 3 (UDP), per-process fd table (`src/fd.go`).

---

## 1. Goals

1. Provide POSIX-inspired socket syscalls so userspace programs
   can send and receive UDP datagrams.
2. Integrate sockets into the existing `FileDesc` interface and
   per-process fd table (`Process.fds`).
3. Expose a high-level `gooos/net` package in the userspace SDK
   so TinyGo programs can call `net.UDPSend`, `net.UDPRecv`,
   etc. without raw syscall assembly.
4. Support the userspace DHCP client described in
   `net_dhcp_client.md` as the first real consumer.
5. Allow a userspace program to configure the kernel's network
   stack (IP/netmask/gateway) via a dedicated syscall.

### 1.1 Non-Goals (Deferred)

- TCP sockets (`SOCK_STREAM`).
- `connect()` / `listen()` / `accept()` semantics.
- `select()` / `poll()` / `epoll()` multiplexing.
- Non-blocking sockets / `O_NONBLOCK`.
- Raw sockets (`SOCK_RAW`).
- IPv6 / `AF_INET6`.
- DNS resolution in the kernel.

---

## 2. Syscall Additions

Six new syscall numbers are added to the dispatch table in
`src/userspace.go`. They follow the existing register ABI
(`int 0x80`, RAX=nr, RDI/RSI/RDX/R10/R8/R9=args, return in
RAX).

| Nr | Name | RDI | RSI | RDX | R10 | R8 | Returns | Description |
|---|---|---|---|---|---|---|---|---|
| 22 | `sys_socket` | domain | type | protocol | — | — | fd or -err | Create a socket descriptor |
| 23 | `sys_bind` | fd | port | — | — | — | 0 or -err | Bind socket to a local UDP port |
| 24 | `sys_sendto` | fd | buf_ptr | buf_len | dst_ip | dst_port | bytes sent or -err | Send UDP datagram |
| 25 | `sys_recvfrom` | fd | buf_ptr | buf_max | info_ptr | — | bytes received or -err | Receive UDP datagram (blocking) |
| 26 | `sys_net_config` | op | a1 | a2 | a3 | — | 0 or -err | Get/set network configuration |
| 27 | `sys_sendto_bcast` | fd | buf_ptr | buf_len | dst_port | — | bytes sent or -err | Send UDP datagram to broadcast (255.255.255.255) from 0.0.0.0 |

### 2.1 Rationale for Separate `sys_sendto_bcast`

DHCP requires sending from `0.0.0.0:68` to `255.255.255.255:67`
before the host has an IP address. The normal `sys_sendto` uses
`ourIP` as the source address. `sys_sendto_bcast` forces source
IP to `0.0.0.0` and destination to the broadcast MAC/IP,
bypassing ARP resolution. This is a DHCP-specific path; general
broadcast can use `sys_sendto` with `255.255.255.255` once the
host has an IP.

### 2.2 Domain / Type / Protocol Constants

```go
const (
    AF_INET     = 2   // IPv4
    SOCK_DGRAM  = 2   // UDP datagram
    IPPROTO_UDP = 17
)
```

Only `AF_INET` + `SOCK_DGRAM` is supported. All other
combinations return `-fdErrBad`.

---

## 3. Kernel-Side Socket Object (`src/netsock.go`)

### 3.1 `socketFd` Struct

A new `FileDesc` implementation representing a UDP socket:

```go
type socketFd struct {
    localPort uint16          // 0 = unbound
    bound     bool
    recvCh    chan UDPDatagram // buffered, capacity 16
}
```

`socketFd` implements the `FileDesc` interface:
- `Read(buf)` → reads from `recvCh` (blocks if empty); copies
  `SrcIP`, `SrcPort`, and `Data` into buf. Returns data length.
- `Write(buf)` → returns `fdErrBad` (use `sys_sendto` instead).
- `Close()` → calls `udpUnbind(localPort)` if bound, drains
  `recvCh`.

### 3.2 Integration with UDP Bind Table

When `sys_bind(fd, port)` is called:
1. Look up `socketFd` in `proc.fds[fd]`.
2. Call `udpBindWithChannel(port, sock.recvCh)` — a new variant
   of `udpBind` that accepts an external channel instead of
   creating one internally.
3. Set `sock.localPort = port`, `sock.bound = true`.
4. Incoming UDP packets to that port are delivered to
   `sock.recvCh` by the kernel's `udpHandle` dispatch.

### 3.3 `socketFd.Read` / `socketFd.Write`

`Read` and `Write` on the `FileDesc` interface are provided for
compatibility with the fd table but are not the primary API:

```go
func (s *socketFd) Read(buf []byte) (int, fdErr) {
    if !s.bound {
        return 0, fdErrBad
    }
    dg := <-s.recvCh
    // Copy data
    n := len(dg.Data)
    if n > len(buf) {
        n = len(buf)
    }
    copy(buf[:n], dg.Data[:n])
    return n, fdErrOK
}

func (s *socketFd) Write(buf []byte) (int, fdErr) {
    return 0, fdErrBad // use sys_sendto
}

func (s *socketFd) Close() fdErr {
    if s.bound {
        udpUnbind(s.localPort)
        s.bound = false
    }
    // Drain remaining datagrams
    for {
        select {
        case <-s.recvCh:
        default:
            return fdErrOK
        }
    }
}
```

---

## 4. Syscall Handler Implementations

### 4.1 `sys_socket` (22)

```go
func sysSocketHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil {
        frame.RAX = sysFail(fdErrBad)
        return
    }
    domain := frame.RDI   // must be AF_INET
    sockType := frame.RSI // must be SOCK_DGRAM
    if domain != AF_INET || sockType != SOCK_DGRAM {
        frame.RAX = sysFail(fdErrBad)
        return
    }
    sock := &socketFd{
        recvCh: make(chan UDPDatagram, 16),
    }
    fd, err := procAllocFD(proc, sock)
    if err != fdErrOK {
        frame.RAX = sysFail(err)
        return
    }
    frame.RAX = uintptr(fd)
}
```

### 4.2 `sys_bind` (23)

```go
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
        frame.RAX = sysFail(fdErrBad) // already bound
        return
    }
    if !udpBindWithChannel(port, sock.recvCh) {
        frame.RAX = sysFail(fdErrBad) // port in use or table full
        return
    }
    sock.localPort = port
    sock.bound = true
    frame.RAX = 0
}
```

### 4.3 `sys_sendto` (24)

```
RDI = fd
RSI = buf_ptr (user memory)
RDX = buf_len
R10 = dst_ip (uint32, host byte order)
R8  = dst_port (uint16)
```

```go
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

    // Validate user pointer is within user address range.
    if bufPtr < 0x40000000 {
        frame.RAX = sysFail(fdErrBad)
        return
    }

    if bufLen > 1472 { // max UDP payload without fragmentation
        frame.RAX = sysFail(fdErrBad)
        return
    }

    // Copy data from user memory
    data := make([]byte, bufLen)
    for i := uintptr(0); i < bufLen; i++ {
        data[i] = *(*byte)(unsafe.Pointer(bufPtr + i))
    }

    // Determine source port: use bound port if available,
    // otherwise ephemeral 0 (kernel will use 0 which is
    // acceptable for one-shot sends like DHCP).
    srcPort := sock.localPort

    if !udpSend(dstIP, dstPort, srcPort, data) {
        frame.RAX = sysFail(fdErrBad)
        return
    }
    frame.RAX = bufLen
}
```

### 4.4 `sys_recvfrom` (25)

```
RDI = fd
RSI = buf_ptr (user memory, receives payload)
RDX = buf_max
R10 = info_ptr (user memory, optional: receives 8 bytes
      [srcIP uint32, srcPort uint16, padding uint16])
```

```go
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

    // Validate user buffer pointer.
    bufPtr := frame.RSI
    if bufPtr < 0x40000000 {
        frame.RAX = sysFail(fdErrBad)
        return
    }

    // Block until a datagram arrives.
    // **v1 note**: blocks indefinitely. A timeout mechanism
    // (via afterTicks or a deadline argument) is deferred to v2.
    dg := <-sock.recvCh

    // Copy payload to user buffer
    n := len(dg.Data)
    bufMax := int(frame.RDX)
    if n > bufMax {
        n = bufMax // truncate (POSIX MSG_TRUNC not supported)
    }
    bufPtr := frame.RSI
    for i := 0; i < n; i++ {
        *(*byte)(unsafe.Pointer(bufPtr + uintptr(i))) = dg.Data[i]
    }

    // Write sender info if info_ptr is non-zero
    infoPtr := frame.R10
    if infoPtr != 0 {
        *(*uint32)(unsafe.Pointer(infoPtr))     = dg.SrcIP
        *(*uint16)(unsafe.Pointer(infoPtr + 4)) = dg.SrcPort
        *(*uint16)(unsafe.Pointer(infoPtr + 6)) = 0 // padding
    }

    frame.RAX = uintptr(n)
}
```

### 4.5 `sys_net_config` (26)

A multi-purpose syscall for reading and writing the kernel's
network configuration:

```
RDI = op:
    0 = NET_CONFIG_GET_IP      → returns ourIP in RAX
    1 = NET_CONFIG_SET_IP      → RSI = new IP
    2 = NET_CONFIG_GET_NETMASK → returns ourNetmask in RAX
    3 = NET_CONFIG_SET_NETMASK → RSI = new netmask
    4 = NET_CONFIG_GET_GATEWAY → returns ourGateway in RAX
    5 = NET_CONFIG_SET_GATEWAY → RSI = new gateway
    6 = NET_CONFIG_GET_MAC     → writes 6 bytes to RSI ptr
    7 = NET_CONFIG_APPLY       → send gratuitous ARP with new config
    8 = NET_CONFIG_GET_DNS     → returns ourDNS in RAX
    9 = NET_CONFIG_SET_DNS     → RSI = new DNS server IP
```

```go
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

func sysNetConfigHandler(frame *SyscallFrame) {
    op := frame.RDI
    switch op {
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
```

New global variable:
```go
var ourDNS uint32 // DNS server IP (set by DHCP or static)
```

### 4.6 `sys_sendto_bcast` (27)

DHCP-specific broadcast send with source IP `0.0.0.0`:

```go
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

    if bufLen > 1472 {
        frame.RAX = sysFail(fdErrBad)
        return
    }

    data := make([]byte, bufLen)
    for i := uintptr(0); i < bufLen; i++ {
        data[i] = *(*byte)(unsafe.Pointer(bufPtr + i))
    }

    srcPort := sock.localPort

    // Build UDP packet with source IP 0.0.0.0
    // Destination IP 255.255.255.255
    // Destination MAC FF:FF:FF:FF:FF:FF (broadcast)
    if !udpSendRaw(0x00000000, 0xFFFFFFFF, srcPort, dstPort,
        data) {
        frame.RAX = sysFail(fdErrBad)
        return
    }
    frame.RAX = bufLen
}
```

This requires a new kernel function `udpSendRaw` that accepts
explicit source and destination IPs and bypasses ARP:

```go
func udpSendRaw(srcIP, dstIP uint32, srcPort, dstPort uint16,
    data []byte) bool {
    // Build UDP header + data
    udpLen := udpHeaderSize + len(data)
    packet := make([]byte, udpLen)
    packet[0] = byte(srcPort >> 8)
    packet[1] = byte(srcPort)
    packet[2] = byte(dstPort >> 8)
    packet[3] = byte(dstPort)
    packet[4] = byte(udpLen >> 8)
    packet[5] = byte(udpLen)
    copy(packet[udpHeaderSize:], data)
    cksum := udpChecksum(srcIP, dstIP, packet)
    packet[6] = byte(cksum >> 8)
    packet[7] = byte(cksum)

    // Build IPv4 header with explicit src/dst
    // (same as ipv4Send but with custom srcIP and no ARP)
    totalLen := ipv4HeaderMinSize + udpLen
    hdr := make([]byte, ipv4HeaderMinSize)
    hdr[0] = 0x45
    hdr[2] = byte(totalLen >> 8)
    hdr[3] = byte(totalLen)
    ipv4ID++
    hdr[4] = byte(ipv4ID >> 8)
    hdr[5] = byte(ipv4ID)
    hdr[8] = 64 // TTL
    hdr[9] = ipProtoUDP
    hdr[12] = byte(srcIP >> 24)
    hdr[13] = byte(srcIP >> 16)
    hdr[14] = byte(srcIP >> 8)
    hdr[15] = byte(srcIP)
    hdr[16] = byte(dstIP >> 24)
    hdr[17] = byte(dstIP >> 16)
    hdr[18] = byte(dstIP >> 8)
    hdr[19] = byte(dstIP)
    ckHdr := ipv4Checksum(hdr)
    hdr[10] = byte(ckHdr >> 8)
    hdr[11] = byte(ckHdr)

    ipPacket := make([]byte, totalLen)
    copy(ipPacket, hdr)
    copy(ipPacket[ipv4HeaderMinSize:], packet)

    // Broadcast: use FF:FF:FF:FF:FF:FF destination MAC
    frame := ethernetBuild(broadcastMAC, e1000MAC,
        etherTypeIPv4, ipPacket)
    return e1000Transmit(frame)
}
```

---

## 5. Changes to Existing Kernel Code

### 5.1 UDP Bind Table Extension

The current `udpBind` in `net_ipv4_icmp_udp.md` creates its
own channel. A new variant is needed:

```go
// udpBindWithChannel binds a port using an externally provided
// channel. Returns true on success (port not already in use
// and table not full).
func udpBindWithChannel(port uint16, ch chan UDPDatagram) bool {
    flags := udpLock.Acquire()
    defer udpLock.Release(flags)
    for i := 0; i < udpMaxBinds; i++ {
        if udpBindings[i].Active && udpBindings[i].Port == port {
            return false
        }
    }
    for i := 0; i < udpMaxBinds; i++ {
        if !udpBindings[i].Active {
            udpBindings[i] = UDPBinding{
                Port: port, Ch: ch, Active: true,
            }
            return true
        }
    }
    return false
}
```

### 5.2 Dispatch Table Update

In `src/userspace.go`, add new constants and `case` entries:

```go
const (
    // ... existing constants ...
    sysSocket       = 22
    sysBind         = 23
    sysSendto       = 24
    sysRecvfrom     = 25
    sysNetConfig    = 26
    sysSendtoBcast  = 27
)

// In syscallDispatch:
case sysSocket:
    sysSocketHandler(frame)
case sysBind:
    sysBindHandler(frame)
case sysSendto:
    sysSendtoHandler(frame)
case sysRecvfrom:
    sysRecvfromHandler(frame)
case sysNetConfig:
    sysNetConfigHandler(frame)
case sysSendtoBcast:
    sysSendtoBcastHandler(frame)
```

### 5.3 `src/netsock.go` — New File

All socket-related kernel code (the `socketFd` struct and its
methods, the six syscall handlers) lives in a new file
`src/netsock.go`, following the pattern of `src/fd.go` for the
fd infrastructure and `src/pipe.go` for pipe objects.

### 5.4 `src/net.go` — Extension

Add `ourDNS` global variable alongside `ourIP`, `ourNetmask`,
`ourGateway`:

```go
var ourDNS uint32
```

---

## 6. Userspace SDK (`user/gooos/net.go`)

A new file in the userspace SDK package providing high-level
network functions for TinyGo programs:

```go
package gooos

import "unsafe"

// Socket syscall numbers
const (
    sysSocket       = 22
    sysBind         = 23
    sysSendto       = 24
    sysRecvfrom     = 25
    sysNetConfig    = 26
    sysSendtoBcast  = 27
)

// Socket constants
const (
    AF_INET    = 2
    SOCK_DGRAM = 2
)

// UDPInfo holds the source address of a received datagram.
type UDPInfo struct {
    SrcIP   uint32
    SrcPort uint16
    _pad    uint16
}

// Socket creates a UDP socket. Returns fd >= 0 on success.
func Socket() int {
    r := syscall3(sysSocket, AF_INET, SOCK_DGRAM, 0)
    return int(int64(r))
}

// Bind binds a socket to a local UDP port.
func Bind(fd int, port uint16) int {
    r := syscall2(sysBind, uintptr(fd), uintptr(port))
    return int(int64(r))
}

// UDPSendTo sends a UDP datagram to dstIP:dstPort via the
// given socket fd.
func UDPSendTo(fd int, data []byte, dstIP uint32,
    dstPort uint16) int {
    if len(data) == 0 {
        return 0
    }
    r := syscall5(sysSendto,
        uintptr(fd),
        uintptr(unsafe.Pointer(&data[0])),
        uintptr(len(data)),
        uintptr(dstIP),
        uintptr(dstPort),
    )
    return int(int64(r))
}

// UDPRecvFrom receives a UDP datagram from the given socket fd.
// Blocks until a datagram arrives. Returns (data, srcIP, srcPort).
func UDPRecvFrom(fd int, buf []byte) (int, UDPInfo) {
    var info UDPInfo
    r := syscall4(sysRecvfrom,
        uintptr(fd),
        uintptr(unsafe.Pointer(&buf[0])),
        uintptr(len(buf)),
        uintptr(unsafe.Pointer(&info)),
    )
    return int(int64(r)), info
}

// UDPSendBroadcast sends a UDP datagram to broadcast from
// 0.0.0.0. Used for DHCP before an IP is assigned.
func UDPSendBroadcast(fd int, data []byte,
    dstPort uint16) int {
    if len(data) == 0 {
        return 0
    }
    r := syscall4(sysSendtoBcast,
        uintptr(fd),
        uintptr(unsafe.Pointer(&data[0])),
        uintptr(len(data)),
        uintptr(dstPort),
    )
    return int(int64(r))
}

// --- Network Configuration ---

// Net config operation codes
const (
    ncGetIP      = 0
    ncSetIP      = 1
    ncGetNetmask = 2
    ncSetNetmask = 3
    ncGetGateway = 4
    ncSetGateway = 5
    ncGetMAC     = 6
    ncApply      = 7
    ncGetDNS     = 8
    ncSetDNS     = 9
)

// GetIP returns the kernel's current IP address.
func GetIP() uint32 {
    return uint32(syscall1(sysNetConfig, ncGetIP))
}

// SetIP sets the kernel's IP address.
func SetIP(ip uint32) {
    syscall2(sysNetConfig, ncSetIP, uintptr(ip))
}

// GetNetmask returns the kernel's current netmask.
func GetNetmask() uint32 {
    return uint32(syscall1(sysNetConfig, ncGetNetmask))
}

// SetNetmask sets the kernel's netmask.
func SetNetmask(mask uint32) {
    syscall2(sysNetConfig, ncSetNetmask, uintptr(mask))
}

// GetGateway returns the kernel's current gateway IP.
func GetGateway() uint32 {
    return uint32(syscall1(sysNetConfig, ncGetGateway))
}

// SetGateway sets the kernel's gateway IP.
func SetGateway(gw uint32) {
    syscall2(sysNetConfig, ncSetGateway, uintptr(gw))
}

// GetMAC returns the NIC's MAC address.
func GetMAC() [6]byte {
    var mac [6]byte
    syscall2(sysNetConfig, ncGetMAC,
        uintptr(unsafe.Pointer(&mac[0])))
    return mac
}

// ApplyNetConfig sends a gratuitous ARP to announce the
// current IP/MAC to the network.
func ApplyNetConfig() {
    syscall1(sysNetConfig, ncApply)
}

// GetDNS returns the kernel's current DNS server IP.
func GetDNS() uint32 {
    return uint32(syscall1(sysNetConfig, ncGetDNS))
}

// SetDNS sets the kernel's DNS server IP.
func SetDNS(dns uint32) {
    syscall2(sysNetConfig, ncSetDNS, uintptr(dns))
}

// --- Helper Functions ---

// IPv4 constructs a uint32 IP from four octets.
func IPv4(a, b, c, d byte) uint32 {
    return uint32(a)<<24 | uint32(b)<<16 |
           uint32(c)<<8 | uint32(d)
}

// FormatIP formats a uint32 IP as "A.B.C.D".
func FormatIP(ip uint32) string {
    return itoa(int(ip>>24)) + "." +
           itoa(int((ip>>16)&0xFF)) + "." +
           itoa(int((ip>>8)&0xFF)) + "." +
           itoa(int(ip&0xFF))
}

// FormatMAC formats a MAC address as "XX:XX:XX:XX:XX:XX".
func FormatMAC(mac [6]byte) string {
    const hex = "0123456789ABCDEF"
    var buf [17]byte
    for i := 0; i < 6; i++ {
        if i > 0 {
            buf[i*3-1] = ':'
        }
        buf[i*3] = hex[mac[i]>>4]
        buf[i*3+1] = hex[mac[i]&0xF]
    }
    return string(buf[:])
}

// itoa converts a non-negative int to decimal string.
func itoa(n int) string {
    if n == 0 {
        return "0"
    }
    var buf [10]byte
    i := len(buf)
    for n > 0 {
        i--
        buf[i] = byte('0' + n%10)
        n /= 10
    }
    return string(buf[i:])
}
```

### 6.1 `syscall5` Stub

The existing assembly in `user/rt0.S` provides `syscall0`
through `syscall4`. `sys_sendto` needs 5 arguments, so a new
`syscall5` stub is required:

```asm
.global syscall5
syscall5:
    movq %rdi, %rax       # nr
    movq %rsi, %rdi       # a1
    movq %rdx, %rsi       # a2
    movq %rcx, %rdx       # a3
    movq %r8,  %r10       # a4
    movq %r9,  %r8        # a5
    int  $0x80
    ret
```

Go declaration in `user/gooos/syscall.go`:
```go
//go:linkname syscall5 syscall5
func syscall5(nr, a1, a2, a3, a4, a5 uintptr) uintptr
```

---

## 7. Security Considerations

### 7.1 User Memory Validation

All syscall handlers that copy data from user memory must
validate that pointers are within the user address range
(`>= 0x40000000`). This matches the existing pattern in
`sysWriteHandler` and `sysReadHandler`.

### 7.2 Port Binding Restrictions

Currently there are no port-number restrictions (any port
0–65535 can be bound). A future enhancement could restrict
well-known ports (< 1024) to privileged processes if a
privilege model is added.

### 7.3 Network Configuration Access

`sys_net_config` SET operations modify global kernel state.
Any userspace process can call them. In a multi-user OS this
would be a security concern, but gooos has a single-user
model with no privilege separation beyond Ring 0/Ring 3.

### 7.4 Buffer Overflow Prevention

- `sys_sendto` caps `bufLen` at 1472 bytes (max UDP payload).
- `sys_recvfrom` truncates data to `buf_max` (data beyond
  capacity is silently discarded).
- `sys_net_config` MAC read writes exactly 6 bytes.

---

## 8. Lock Ordering

No new locks are introduced. The socket syscalls use the
existing `udpLock` (rank 7) for bind-table operations and
`procLock` (rank 2) for `currentProc()`.

---

## 9. New Files Summary

| File | Purpose | LOC Estimate |
|---|---|---|
| `src/netsock.go` | `socketFd` struct, 6 syscall handlers, `udpSendRaw` | 250–400 |
| `user/gooos/net.go` | Userspace socket + config API package | 150–220 |
| `user/rt0.S` (edit) | Add `syscall5` assembly stub | ~10 |
| `user/gooos/syscall.go` (edit) | Add `syscall5` declaration, new syscall constants | ~10 |
| `src/userspace.go` (edit) | Add 6 `case` entries + constants | ~20 |
| `src/udp.go` (edit) | Add `udpBindWithChannel`, `udpSendRaw` | ~60 |
| `src/net.go` (edit) | Add `ourDNS` global | ~5 |

**Total new code: ~505–725 LOC**

---

## 10. Dependency DAG

```
Phase 3 (IPv4 + ICMP + UDP) complete
    │
    └──► [S1] src/netsock.go (socketFd struct)
              │
              ├──► [S2] src/netsock.go (sys_socket handler)
              │
              ├──► [S3] src/netsock.go (sys_bind handler)
              │         │
              │         └──► [S4] src/udp.go (udpBindWithChannel)
              │
              ├──► [S5] src/netsock.go (sys_sendto handler)
              │
              ├──► [S6] src/netsock.go (sys_recvfrom handler)
              │
              ├──► [S7] src/netsock.go (sys_net_config handler)
              │
              └──► [S8] src/netsock.go (sys_sendto_bcast)
                        │
                        └──► [S9] src/udp.go (udpSendRaw)

    [S10] user/gooos/net.go (userspace SDK)
          │
          └──► [S11] user/rt0.S (syscall5 stub)
```

S1–S9 are kernel-side; S10–S11 are userspace-side. Both tracks
can proceed in parallel after Phase 3 is complete.

---

## 11. Verification Criteria

### 11.1 `sys_socket` + `sys_bind`

1. Userspace program calls `Socket()` → returns fd ≥ 3.
2. Calls `Bind(fd, 7)` → returns 0.
3. Calling `Bind(fd, 7)` again → returns error (already bound).

### 11.2 `sys_sendto` + `sys_recvfrom`

1. Start a userspace echo server on port 7:
   ```go
   fd := gooos.Socket()
   gooos.Bind(fd, 7)
   var buf [1500]byte
   for {
       n, info := gooos.UDPRecvFrom(fd, buf[:])
       gooos.UDPSendTo(fd, buf[:n], info.SrcIP, info.SrcPort)
   }
   ```
2. From host: `echo "hello" | nc -u 10.0.0.2 7` → receives
   "hello" back.

### 11.3 `sys_net_config`

1. Userspace calls `GetIP()` → returns current IP.
2. Calls `SetIP(newIP)` + `SetNetmask(mask)` + `SetGateway(gw)`.
3. Calls `ApplyNetConfig()` → gratuitous ARP sent.
4. Calls `GetIP()` → returns new IP.

### 11.4 `sys_sendto_bcast` (DHCP path)

1. Before IP is assigned, userspace calls `UDPSendBroadcast`.
2. pcap dump shows UDP packet with src=0.0.0.0:68,
   dst=255.255.255.255:67.
3. DHCP server response arrives on port 68.

### 11.5 Socket Close Cleanup

1. Bind port 7, close socket, re-bind port 7 → succeeds.
2. After close, no packets delivered to old channel.

### 11.6 Regression

1. All existing shell commands (`ls`, `cat`, `echo`, etc.)
   continue to work.
2. `make lint` passes.
3. Existing network tests (ping, UDP echo) pass.

---

## 12. Open Questions

1. **Timeout on `sys_recvfrom`**: Currently blocking forever.
   DHCP needs a timeout (typically 4 seconds). Options:
   a. Add a timeout argument to `sys_recvfrom`.
   b. Use `sys_sleep` in a separate goroutine (not available
      in `scheduler: "none"` userspace).
   c. Add a `sys_recvfrom_timeout` variant (recommended).
   Decision: Add timeout_ticks as R8 argument to `sys_recvfrom`.
   Zero means block forever.

2. **Ephemeral port allocation**: When `sys_sendto` is called
   on an unbound socket, should the kernel auto-assign a port?
   Recommendation: yes, allocate from range 49152–65535 on
   first send. Track in `socketFd.localPort`.

3. **Multiple sockets on same port**: Not supported. First
   binder wins.

4. **Socket fd inheritance on `sys_exec`**: Sockets are
   `FileDesc` implementations, so they are shallow-copied on
   exec like pipes. The child inherits the bound port. The
   parent and child share the same receive channel. This may
   cause confusion; recommendation: document as "undefined
   behavior" for now, or close socket fds before exec.

---

## 13. Relationship to Other Documents

- **`net_overview.md` §13**: Lists "Socket syscall API" as
  ~300–600 LOC future extension. This document provides the
  detailed design.
- **`net_ipv4_icmp_udp.md` §3**: Defines the kernel-side UDP
  bind table and `udpSend`/`udpBind` functions. Extended here
  with `udpBindWithChannel` and `udpSendRaw`.
- **`net_dhcp_client.md`**: The DHCP client is the first
  userspace consumer of this socket API.
- **`busybox_syscall_abi.md`**: Defines the syscall ABI
  convention. Extended here with syscalls 22–27.
- **`shell_io_fd_table.md`**: Defines the `FileDesc` interface
  and `procAllocFD`. `socketFd` is a new implementation.
