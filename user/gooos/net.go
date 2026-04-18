// Package gooos — userspace bindings for the Phase-5 socket API.
//
// Exposes three families of calls:
//
//   * Socket / Bind / UDPSendTo / UDPRecvFrom / UDPSendBroadcast —
//     POSIX-style UDP I/O. Only AF_INET + SOCK_DGRAM is supported.
//
//   * GetIP / SetIP / GetNetmask / SetNetmask / GetGateway /
//     SetGateway / GetMAC / GetDNS / SetDNS / ApplyNetConfig — the
//     sys_net_config multiplexer the DHCP client uses to push its
//     DORA result into the kernel's stack.
//
//   * IPv4 / FormatIP / FormatMAC — host-order address construction
//     and pretty-printing helpers.

package gooos

import "unsafe"

// Phase-5 syscall numbers (must match src/userspace.go / netsock.go).
const (
	sysSocket      = 22
	sysBind        = 23
	sysSendto      = 24
	sysRecvfrom    = 25
	sysNetConfig   = 26
	sysSendtoBcast = 27
)

// Address family + socket type. The kernel accepts only the pair
// (AF_INET, SOCK_DGRAM) today.
const (
	AF_INET    = 2
	SOCK_DGRAM = 2
)

// sys_net_config operation codes. Kept in sync with
// src/netsock.go's netConfig* constants.
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

// UDPInfo carries the source address of a received datagram. Layout
// matches the 8-byte block that the kernel writes at info_ptr (SrcIP
// uint32 + SrcPort uint16 + 2 bytes padding).
type UDPInfo struct {
	SrcIP   uint32
	SrcPort uint16
	_pad    uint16
}

// Socket creates a UDP socket. Returns fd >= 0 on success, negative
// errno on failure.
func Socket() int {
	r := syscall3(sysSocket, AF_INET, SOCK_DGRAM, 0)
	return int(int64(r))
}

// Bind associates a socket fd with a local UDP port. Returns 0 on
// success, negative errno on failure.
func Bind(fd int, port uint16) int {
	r := syscall2(sysBind, uintptr(fd), uintptr(port))
	return int(int64(r))
}

// UDPSendTo transmits a UDP datagram to dstIP:dstPort. The source
// port is whatever the socket is bound to, or 0 if unbound. Returns
// bytes sent (== len(data)) on success, negative errno on failure.
func UDPSendTo(fd int, data []byte, dstIP uint32, dstPort uint16) int {
	var p uintptr
	if len(data) > 0 {
		p = uintptr(unsafe.Pointer(&data[0]))
	}
	r := syscall5(sysSendto,
		uintptr(fd),
		p,
		uintptr(len(data)),
		uintptr(dstIP),
		uintptr(dstPort),
	)
	return int(int64(r))
}

// UDPRecvFrom blocks until a datagram arrives on the socket, copies
// up to len(buf) bytes into buf (truncating the rest), and returns
// the copied length + the sender's address.
func UDPRecvFrom(fd int, buf []byte) (int, UDPInfo) {
	return UDPRecvFromTimeout(fd, buf, 0)
}

// UDPRecvFromTimeout is UDPRecvFrom with a timeout expressed in PIT
// ticks (100 Hz — 100 ticks = 1 second). Passing 0 blocks forever.
// If the timeout fires before a datagram arrives, returns (0, zero
// UDPInfo) — the kernel returns a negative errno which we clamp to 0
// here to keep the caller path simple. Callers that need to
// distinguish timeout from "zero-byte datagram" should check the
// result against 0 and inspect UDPInfo.
func UDPRecvFromTimeout(fd int, buf []byte, timeoutTicks uint64) (int, UDPInfo) {
	var info UDPInfo
	var p uintptr
	if len(buf) > 0 {
		p = uintptr(unsafe.Pointer(&buf[0]))
	}
	r := syscall5(sysRecvfrom,
		uintptr(fd),
		p,
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&info)),
		uintptr(timeoutTicks),
	)
	n := int(int64(r))
	if n < 0 {
		return 0, UDPInfo{}
	}
	return n, info
}

// UDPSendBroadcast sends a UDP datagram to 255.255.255.255:dstPort
// from source IP 0.0.0.0, bypassing ARP. Used by the DHCP client for
// DISCOVER / REQUEST before it has an IP.
func UDPSendBroadcast(fd int, data []byte, dstPort uint16) int {
	var p uintptr
	if len(data) > 0 {
		p = uintptr(unsafe.Pointer(&data[0]))
	}
	r := syscall4(sysSendtoBcast,
		uintptr(fd),
		p,
		uintptr(len(data)),
		uintptr(dstPort),
	)
	return int(int64(r))
}

// --- TCP socket API (Phase TCP-5) ----------------------------------------

// TCP syscall numbers — see impldoc/net_tcp_socket_api.md §2.
const (
	sysListen   = 28
	sysAccept   = 29
	sysConnect  = 30
	sysTcpSend  = 31
	sysTcpRecv  = 32
	sysShutdown = 33
)

// SOCK_STREAM and shutdown how values.
const (
	SOCK_STREAM = 1
	SHUT_RD     = 0
	SHUT_WR     = 1
	SHUT_RDWR   = 2
)

// TCPSocket creates a TCP stream socket.
func TCPSocket() int {
	r := syscall3(sysSocket, AF_INET, SOCK_STREAM, 0)
	return int(int64(r))
}

// TCPListen marks a bound TCP socket as passive. backlog is
// advisory; the kernel uses a fixed 8-entry accept queue.
func TCPListen(fd, backlog int) int {
	r := syscall2(sysListen, uintptr(fd), uintptr(backlog))
	return int(int64(r))
}

// TCPAccept pops the next completed connection from the listener.
// timeoutTicks = 0 blocks forever. The returned UDPInfo carries
// the peer's address (field names kept UDP-flavoured since the
// wire layout is identical to the Phase-5 struct).
func TCPAccept(fd int, timeoutTicks uint64) (int, UDPInfo) {
	var info UDPInfo
	r := syscall3(sysAccept,
		uintptr(fd),
		uintptr(unsafe.Pointer(&info)),
		uintptr(timeoutTicks),
	)
	return int(int64(r)), info
}

// TCPConnect initiates an active open to dstIP:dstPort.
// timeoutTicks = 0 uses the kernel's 12 s default envelope.
func TCPConnect(fd int, dstIP uint32, dstPort uint16, timeoutTicks uint64) int {
	r := syscall4(sysConnect,
		uintptr(fd),
		uintptr(dstIP),
		uintptr(dstPort),
		uintptr(timeoutTicks),
	)
	return int(int64(r))
}

// TCPSend writes up to len(data) bytes and returns the actual
// count written (short writes possible when the kernel's send
// buffer fills). Callers that need a full write should use
// TCPSendAll.
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

// TCPSendAll loops TCPSend until every byte lands or an error
// occurs. Returns the total bytes sent (== len(data) on success).
func TCPSendAll(fd int, data []byte) int {
	total := 0
	for total < len(data) {
		n := TCPSend(fd, data[total:])
		if n <= 0 {
			if total > 0 {
				return total
			}
			return n
		}
		total += n
	}
	return total
}

// TCPRecv reads up to len(buf) bytes. Returns the number read;
// 0 means EOF (peer has closed and all buffered bytes consumed).
// timeoutTicks = 0 blocks forever.
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

// TCPShutdown sends a FIN (SHUT_WR) or FIN + rx-drop (SHUT_RDWR).
// SHUT_RD is not supported in v1.
func TCPShutdown(fd, how int) int {
	r := syscall2(sysShutdown, uintptr(fd), uintptr(how))
	return int(int64(r))
}

// --- Network Configuration -----------------------------------------------

// GetIP returns the kernel's current IP address as a host-order
// uint32 (MSB = first octet).
func GetIP() uint32 { return uint32(syscall1(sysNetConfig, ncGetIP)) }

// SetIP installs a new IP address in the kernel stack.
func SetIP(ip uint32) { syscall2(sysNetConfig, ncSetIP, uintptr(ip)) }

// GetNetmask returns the kernel's current netmask.
func GetNetmask() uint32 { return uint32(syscall1(sysNetConfig, ncGetNetmask)) }

// SetNetmask installs a new netmask.
func SetNetmask(mask uint32) { syscall2(sysNetConfig, ncSetNetmask, uintptr(mask)) }

// GetGateway returns the kernel's current default gateway IP.
func GetGateway() uint32 { return uint32(syscall1(sysNetConfig, ncGetGateway)) }

// SetGateway installs a new default gateway IP.
func SetGateway(gw uint32) { syscall2(sysNetConfig, ncSetGateway, uintptr(gw)) }

// GetDNS returns the kernel's current DNS server IP.
func GetDNS() uint32 { return uint32(syscall1(sysNetConfig, ncGetDNS)) }

// SetDNS installs a new DNS server IP.
func SetDNS(dns uint32) { syscall2(sysNetConfig, ncSetDNS, uintptr(dns)) }

// GetMAC returns the NIC's station MAC address.
func GetMAC() [6]byte {
	var mac [6]byte
	syscall2(sysNetConfig, ncGetMAC, uintptr(unsafe.Pointer(&mac[0])))
	return mac
}

// ApplyNetConfig asks the kernel to announce the currently-configured
// IP on the local segment via gratuitous ARP. The DHCP client calls
// this after updating IP/netmask/gateway/DNS.
func ApplyNetConfig() { syscall1(sysNetConfig, ncApply) }

// --- Helpers -------------------------------------------------------------

// IPv4 builds a host-order uint32 from four octets (MSB = a).
func IPv4(a, b, c, d byte) uint32 {
	return uint32(a)<<24 | uint32(b)<<16 | uint32(c)<<8 | uint32(d)
}

// FormatIP renders an IPv4 address as "A.B.C.D".
func FormatIP(ip uint32) string {
	return itoaByte(byte(ip>>24)) + "." +
		itoaByte(byte(ip>>16)) + "." +
		itoaByte(byte(ip>>8)) + "." +
		itoaByte(byte(ip))
}

// FormatMAC renders a MAC address as "XX:XX:XX:XX:XX:XX".
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

// itoaByte converts 0-255 to a decimal string without any imports.
func itoaByte(n byte) string {
	if n == 0 {
		return "0"
	}
	var buf [3]byte
	i := 3
	v := uint16(n)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
