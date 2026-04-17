// user/cmd/dhcp — userspace DHCP client (RFC 2131 DORA, IPv4 only).
//
// Performs the four-message Discover → Offer → Request → Ack exchange
// against whatever DHCP server the LAN offers (QEMU's slirp user-mode
// networking has a built-in server at 10.0.2.2 that hands out
// 10.0.2.15 / 255.255.255.0 / gw 10.0.2.2 / dns 10.0.2.3). On a
// successful ACK the client:
//
//   1. Pushes the lease into the kernel stack via sys_net_config:
//      SetIP / SetNetmask / SetGateway / SetDNS (if offered) + a
//      final ApplyNetConfig so the kernel gratuitously ARPs for the
//      new IP.
//   2. Writes /network.conf into the in-memory FS so other shells /
//      programs can `cat network.conf` and see the result across
//      process invocations (lost on reboot like the rest of the FS).
//
// The broadcast flag (BOOTP flags = 0x8000) is set in both DISCOVER
// and REQUEST so the server replies to the broadcast MAC/IP. The
// gooos kernel's ipv4Handle drops frames where DstIP != ourIP, and
// at DISCOVER time ourIP is still the static 10.0.2.15 default —
// broadcast replies sidestep that filter.
//
// Timeouts: 4 seconds (400 PIT ticks at 100 Hz) on each recvfrom.
// There is no retry / exponential backoff yet; on timeout the
// program prints an error and exits 1. See impldoc/net_dhcp_client.md
// §14 for deferred enhancements (lease renewal, RELEASE, retries).

package main

import "github.com/ryogrid/gooos/user/gooos"

const (
	dhcpDiscover = 1
	dhcpOffer    = 2
	dhcpRequest  = 3
	dhcpAck      = 5
	dhcpNak      = 6

	dhcpServerPort = 67
	dhcpClientPort = 68

	// 4 seconds at 100 Hz PIT.
	dhcpTimeoutTicks = 400
)

// result captures the fields we care about from an OFFER or ACK.
type result struct {
	yourIP    uint32
	serverIP  uint32
	netmask   uint32
	gateway   uint32
	dns       uint32
	leaseTime uint32
	msgType   byte
}

func main() {
	gooos.Println("dhcp: starting DHCP client")

	mac := gooos.GetMAC()
	gooos.Println("dhcp: MAC = " + gooos.FormatMAC(mac))
	xid := xidFromMAC(mac)

	fd := gooos.Socket()
	if fd < 0 {
		gooos.Println("dhcp: Socket() failed")
		gooos.Exit(1)
	}

	if gooos.Bind(fd, dhcpClientPort) < 0 {
		gooos.Println("dhcp: Bind(68) failed — port already in use?")
		gooos.Close(fd)
		gooos.Exit(1)
	}

	// --- DISCOVER ------------------------------------------------------
	discover := buildDiscover(mac, xid)
	if gooos.UDPSendBroadcast(fd, discover, dhcpServerPort) < 0 {
		gooos.Println("dhcp: failed to send DISCOVER")
		gooos.Close(fd)
		gooos.Exit(1)
	}
	gooos.Println("dhcp: DISCOVER sent, waiting for OFFER...")

	offer, ok := recvDHCP(fd, xid, dhcpOffer)
	if !ok {
		gooos.Println("dhcp: no valid OFFER received")
		gooos.Close(fd)
		gooos.Exit(1)
	}
	gooos.Println("dhcp: OFFER received: IP = " + gooos.FormatIP(offer.yourIP))

	// --- REQUEST -------------------------------------------------------
	request := buildRequest(mac, xid, offer.yourIP, offer.serverIP)
	if gooos.UDPSendBroadcast(fd, request, dhcpServerPort) < 0 {
		gooos.Println("dhcp: failed to send REQUEST")
		gooos.Close(fd)
		gooos.Exit(1)
	}
	gooos.Println("dhcp: REQUEST sent, waiting for ACK...")

	ack, ok := recvDHCP(fd, xid, dhcpAck)
	if !ok {
		gooos.Println("dhcp: no valid ACK received")
		gooos.Close(fd)
		gooos.Exit(1)
	}

	// --- Apply + persist ----------------------------------------------
	gooos.SetIP(ack.yourIP)
	gooos.SetNetmask(ack.netmask)
	gooos.SetGateway(ack.gateway)
	if ack.dns != 0 {
		gooos.SetDNS(ack.dns)
	}
	gooos.ApplyNetConfig()

	writeConfigFile(ack)

	gooos.Println("")
	gooos.Println("dhcp: network configured:")
	gooos.Println("  IP      = " + gooos.FormatIP(ack.yourIP))
	gooos.Println("  Netmask = " + gooos.FormatIP(ack.netmask))
	gooos.Println("  Gateway = " + gooos.FormatIP(ack.gateway))
	if ack.dns != 0 {
		gooos.Println("  DNS     = " + gooos.FormatIP(ack.dns))
	}
	gooos.Println("  Lease   = " + uitoa(ack.leaseTime) + " seconds")
	gooos.Println("  Server  = " + gooos.FormatIP(ack.serverIP))

	gooos.Close(fd)
}

// recvDHCP reads up to one packet with the 4-second timeout, parses it
// as DHCP, and verifies XID + expected message type. Returns (result,
// false) on timeout or validation failure.
func recvDHCP(fd int, wantXID uint32, wantType byte) (result, bool) {
	var buf [1500]byte
	n, _ := gooos.UDPRecvFromTimeout(fd, buf[:], dhcpTimeoutTicks)
	if n <= 0 {
		return result{}, false
	}
	r, parsed := parseDHCP(buf[:n])
	if !parsed {
		return result{}, false
	}
	if r.msgType == dhcpNak {
		gooos.Println("dhcp: server returned NAK")
		return result{}, false
	}
	if r.msgType != wantType {
		return result{}, false
	}
	rxXID := uint32(buf[4])<<24 | uint32(buf[5])<<16 |
		uint32(buf[6])<<8 | uint32(buf[7])
	if rxXID != wantXID {
		return result{}, false
	}
	return r, true
}

// xidFromMAC generates a 32-bit transaction ID from the MAC (plus a
// fixed salt). Not cryptographically random but sufficient to
// distinguish our exchange from any other client on the same segment
// — the server matches replies by XID.
func xidFromMAC(mac [6]byte) uint32 {
	x := uint32(mac[0])<<24 | uint32(mac[1])<<16 |
		uint32(mac[2])<<8 | uint32(mac[3])
	x ^= uint32(mac[4])<<8 | uint32(mac[5])
	return x ^ 0xDEADBEEF
}

// buildDiscover constructs a 300-byte DHCPDISCOVER packet with the
// broadcast-reply flag set and a "Parameter Request List" option
// asking the server for netmask / router / dns / lease time.
func buildDiscover(mac [6]byte, xid uint32) []byte {
	pkt := make([]byte, 300)
	fillBootpHeader(pkt, mac, xid)

	off := 240
	// Option 53: DHCP Message Type = DISCOVER.
	pkt[off] = 53
	pkt[off+1] = 1
	pkt[off+2] = dhcpDiscover
	off += 3
	// Option 55: Parameter Request List — subnet, router, dns, lease.
	pkt[off] = 55
	pkt[off+1] = 4
	pkt[off+2] = 1
	pkt[off+3] = 3
	pkt[off+4] = 6
	pkt[off+5] = 51
	off += 6
	// Option 255: End.
	pkt[off] = 255
	off++

	return pkt[:off]
}

// buildRequest constructs the DHCPREQUEST that confirms the offered
// IP back to the specific server (options 50 + 54).
func buildRequest(mac [6]byte, xid, requestedIP, serverIP uint32) []byte {
	pkt := make([]byte, 300)
	fillBootpHeader(pkt, mac, xid)

	off := 240
	// Option 53: REQUEST.
	pkt[off] = 53
	pkt[off+1] = 1
	pkt[off+2] = dhcpRequest
	off += 3
	// Option 50: Requested IP.
	pkt[off] = 50
	pkt[off+1] = 4
	pkt[off+2] = byte(requestedIP >> 24)
	pkt[off+3] = byte(requestedIP >> 16)
	pkt[off+4] = byte(requestedIP >> 8)
	pkt[off+5] = byte(requestedIP)
	off += 6
	// Option 54: Server Identifier.
	pkt[off] = 54
	pkt[off+1] = 4
	pkt[off+2] = byte(serverIP >> 24)
	pkt[off+3] = byte(serverIP >> 16)
	pkt[off+4] = byte(serverIP >> 8)
	pkt[off+5] = byte(serverIP)
	off += 6
	// Option 55: Parameter Request List.
	pkt[off] = 55
	pkt[off+1] = 4
	pkt[off+2] = 1
	pkt[off+3] = 3
	pkt[off+4] = 6
	pkt[off+5] = 51
	off += 6
	// End.
	pkt[off] = 255
	off++

	return pkt[:off]
}

// fillBootpHeader writes the 240-byte fixed BOOTP header: op, htype,
// hlen, xid, broadcast flag, client MAC, and magic cookie.
func fillBootpHeader(pkt []byte, mac [6]byte, xid uint32) {
	pkt[0] = 1 // BOOTREQUEST
	pkt[1] = 1 // Ethernet
	pkt[2] = 6 // MAC length
	pkt[4] = byte(xid >> 24)
	pkt[5] = byte(xid >> 16)
	pkt[6] = byte(xid >> 8)
	pkt[7] = byte(xid)
	// flags: broadcast (bit 15 set).
	pkt[10] = 0x80
	pkt[11] = 0
	// chaddr[0..5] = MAC.
	copy(pkt[28:34], mac[:])
	// Magic cookie at offset 236.
	pkt[236] = 0x63
	pkt[237] = 0x82
	pkt[238] = 0x53
	pkt[239] = 0x63
}

// parseDHCP extracts the fields needed to apply a lease from a
// server reply. Rejects packets that fail the BOOTREPLY / magic-
// cookie check.
func parseDHCP(pkt []byte) (result, bool) {
	var r result
	if len(pkt) < 241 {
		return r, false
	}
	if pkt[0] != 2 {
		return r, false
	}
	if pkt[236] != 0x63 || pkt[237] != 0x82 ||
		pkt[238] != 0x53 || pkt[239] != 0x63 {
		return r, false
	}
	r.yourIP = ip4(pkt[16], pkt[17], pkt[18], pkt[19])
	r.serverIP = ip4(pkt[20], pkt[21], pkt[22], pkt[23])

	off := 240
	for off < len(pkt) {
		tag := pkt[off]
		if tag == 255 {
			break
		}
		if tag == 0 {
			off++
			continue
		}
		if off+1 >= len(pkt) {
			break
		}
		length := int(pkt[off+1])
		off += 2
		if off+length > len(pkt) {
			break
		}
		switch tag {
		case 53:
			if length >= 1 {
				r.msgType = pkt[off]
			}
		case 1:
			if length >= 4 {
				r.netmask = ip4(pkt[off], pkt[off+1], pkt[off+2], pkt[off+3])
			}
		case 3:
			if length >= 4 {
				r.gateway = ip4(pkt[off], pkt[off+1], pkt[off+2], pkt[off+3])
			}
		case 6:
			if length >= 4 {
				r.dns = ip4(pkt[off], pkt[off+1], pkt[off+2], pkt[off+3])
			}
		case 51:
			if length >= 4 {
				r.leaseTime = uint32(pkt[off])<<24 |
					uint32(pkt[off+1])<<16 |
					uint32(pkt[off+2])<<8 |
					uint32(pkt[off+3])
			}
		case 54:
			if length >= 4 {
				r.serverIP = ip4(pkt[off], pkt[off+1], pkt[off+2], pkt[off+3])
			}
		}
		off += length
	}
	if r.msgType == 0 {
		return r, false
	}
	return r, true
}

// writeConfigFile persists the lease to /network.conf so other
// programs can read it without re-running DHCP.
func writeConfigFile(r result) {
	s := "# Network configuration (DHCP)\n" +
		"ip=" + gooos.FormatIP(r.yourIP) + "\n" +
		"netmask=" + gooos.FormatIP(r.netmask) + "\n" +
		"gateway=" + gooos.FormatIP(r.gateway) + "\n" +
		"dns=" + gooos.FormatIP(r.dns) + "\n" +
		"lease=" + uitoa(r.leaseTime) + "\n" +
		"server=" + gooos.FormatIP(r.serverIP) + "\n"
	if !gooos.WriteFile("network.conf", []byte(s)) {
		gooos.Println("dhcp: warning — failed to write network.conf")
	}
}

func ip4(a, b, c, d byte) uint32 {
	return uint32(a)<<24 | uint32(b)<<16 | uint32(c)<<8 | uint32(d)
}

// uitoa converts a uint32 to decimal without importing strconv.
func uitoa(n uint32) string {
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
