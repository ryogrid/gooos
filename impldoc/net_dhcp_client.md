# Networking Stack — Userspace DHCP Client

Design for a manually-executed userspace DHCP client that
obtains an IP address, subnet mask, gateway, and DNS server
from a DHCP server, records the configuration in the kernel's
network stack, and enables network connectivity.

Parent doc: `net_overview.md`.
Depends on: Socket API (`net_socket_api.md`), kernel UDP stack
(`net_ipv4_icmp_udp.md`), NIC driver (`net_pci_e1000_driver.md`).

---

## 1. Goals

1. Implement a userspace DHCP client as a normal gooos command
   (`user/cmd/dhcp/main.go`) that is executed manually by the
   user from the shell.
2. The client performs a full DHCP DORA (Discover → Offer →
   Request → Ack) exchange over UDP.
3. Upon receiving an ACK, the client applies the obtained
   configuration to the kernel (IP, netmask, gateway, DNS)
   via `sys_net_config`.
4. The client also records the configuration into an in-memory
   filesystem file (`/etc/network.conf`) so that it can be
   inspected by other programs.
5. The client exits after configuration; it is not a daemon
   and does not handle lease renewal.

### 1.1 Non-Goals

- Lease renewal / rebinding timers (long-running daemon).
- DHCP INFORM or RELEASE messages.
- DHCPv6 / IPv6.
- Multiple NIC support (only the first e1000 NIC is used).
- Persistent configuration across reboots (in-memory FS is
  volatile).

---

## 2. DHCP Protocol Overview

DHCP operates over UDP. The client sends on port 68 (source)
to port 67 (destination). Before the client has an IP, it uses
`0.0.0.0` as source and `255.255.255.255` as destination
(broadcast). The server responds to port 68.

### 2.1 DORA Sequence

```
Client → Server:  DHCPDISCOVER  (broadcast, UDP 68→67)
Server → Client:  DHCPOFFER     (broadcast or unicast, UDP 67→68)
Client → Server:  DHCPREQUEST   (broadcast, UDP 68→67)
Server → Client:  DHCPACK       (broadcast or unicast, UDP 67→68)
```

### 2.2 DHCP Packet Format

All DHCP packets use the BOOTP message format (RFC 2131):

```
Offset  Size  Field
0       1     op      (1=BOOTREQUEST, 2=BOOTREPLY)
1       1     htype   (1=Ethernet)
2       1     hlen    (6)
3       1     hops    (0)
4       4     xid     (transaction ID, random)
8       2     secs    (0)
10      2     flags   (0x8000 = broadcast flag)
12      4     ciaddr  (client IP, 0.0.0.0 if unknown)
16      4     yiaddr  (your IP, assigned by server)
20      4     siaddr  (server IP)
24      4     giaddr  (relay agent IP, 0)
28      16    chaddr  (client MAC, padded to 16 bytes)
44      64    sname   (server hostname, unused)
108     128   file    (boot filename, unused)
236     4     magic cookie (0x63825363)
240     var   options (TLV format)
```

Total fixed header: 236 bytes + 4-byte magic cookie = 240 bytes
minimum before options.

### 2.3 Required DHCP Options

| Option | Tag | Length | Description |
|---|---|---|---|
| Message Type | 53 | 1 | 1=Discover, 2=Offer, 3=Request, 5=Ack |
| Subnet Mask | 1 | 4 | Network mask |
| Router | 3 | 4+ | Default gateway IP (first entry) |
| DNS Server | 6 | 4+ | DNS server IP (first entry) |
| IP Address Lease Time | 51 | 4 | Seconds |
| Server Identifier | 54 | 4 | DHCP server's IP |
| Requested IP | 50 | 4 | In REQUEST: the offered IP |
| End | 255 | 0 | Marks end of options |

---

## 3. Implementation Design

### 3.1 Directory Layout

```
user/cmd/dhcp/
└── main.go     # DHCP client program
```

### 3.2 Program Flow

```
main():
    1. Print "DHCP client starting..."
    2. Get MAC address via gooos.GetMAC()
    3. Generate random XID (using tick counter or MAC-based seed)
    4. Create UDP socket: fd = gooos.Socket()
    5. Bind to port 68: gooos.Bind(fd, 68)
    6. Build DHCPDISCOVER packet
    7. Send via broadcast: gooos.UDPSendBroadcast(fd, pkt, 67)
    8. Print "DHCPDISCOVER sent, waiting for offer..."
    9. Receive DHCPOFFER: n, _ = gooos.UDPRecvFrom(fd, buf)
   10. Parse offer: extract yiaddr, serverIP, subnet, gateway, DNS
   11. Print "DHCPOFFER received: IP=x.x.x.x"
   12. Build DHCPREQUEST packet (with requested IP and server ID)
   13. Send via broadcast: gooos.UDPSendBroadcast(fd, pkt, 67)
   14. Print "DHCPREQUEST sent, waiting for ACK..."
   15. Receive DHCPACK: n, _ = gooos.UDPRecvFrom(fd, buf)
   16. Parse ACK: verify message type = 5
   17. Apply configuration:
       a. gooos.SetIP(assignedIP)
       b. gooos.SetNetmask(subnetMask)
       c. gooos.SetGateway(gatewayIP)
       d. gooos.SetDNS(dnsServer)
       e. gooos.ApplyNetConfig()  // gratuitous ARP
   18. Write config to filesystem:
       WriteConfigFile(ip, mask, gw, dns, lease)
   19. Print summary:
       "Network configured:"
       "  IP:      10.0.2.15"
       "  Netmask: 255.255.255.0"
       "  Gateway: 10.0.2.2"
       "  DNS:     10.0.2.3"
       "  Lease:   86400 seconds"
   20. gooos.Close(fd)
   21. Exit
```

### 3.3 Timeout Handling

If no DHCPOFFER arrives within 4 seconds, retry the DISCOVER
up to 3 times. Each retry doubles the timeout (4s, 8s, 16s).

Implementation using `sys_recvfrom` with timeout (see
`net_socket_api.md` §12 open question 1):

```go
// recvWithTimeout receives a UDP datagram with a timeout.
// Returns 0 bytes if timeout expires.
func recvWithTimeout(fd int, buf []byte, timeoutTicks uint64) (int, gooos.UDPInfo) {
    // Use sys_recvfrom with timeout argument in R8
    // (extended ABI per net_socket_api.md §12)
    ...
}
```

If the `sys_recvfrom` timeout extension is not yet implemented,
the simpler fallback is a busy-poll approach:

```go
// Fallback: try recvfrom in a loop with sys_sleep between
// attempts. Works because gooos userspace has scheduler=none
// (single-threaded) — but sys_sleep yields to kernel
// goroutines so the recv channel gets fed.
func recvWithRetry(fd int, buf []byte, maxRetries int) (int, gooos.UDPInfo) {
    for i := 0; i < maxRetries*10; i++ {
        // Check if data available (non-blocking variant needed)
        // ... OR just use blocking recvfrom and accept the
        //     timeout-less design for v1.
    }
}
```

**Recommended v1**: Use blocking `sys_recvfrom` with no timeout.
If no DHCP server responds, the program blocks forever (gooos
has no signal/Ctrl+C support; reboot is the only recovery).
**Run only with QEMU user-mode networking** which always has a
built-in DHCP server (assigns 10.0.2.15). Running without a
DHCP server requires reboot.

### 3.4 Transaction ID Generation

DHCP requires a random 32-bit transaction ID to match requests
with responses. Without a true random source, use a simple
hash of the MAC address and a counter:

```go
func generateXID(mac [6]byte) uint32 {
    xid := uint32(mac[0])<<24 | uint32(mac[1])<<16 |
           uint32(mac[2])<<8 | uint32(mac[3])
    xid ^= uint32(mac[4])<<8 | uint32(mac[5])
    xid ^= 0xDEADBEEF  // salt
    return xid
}
```

A real implementation could use the PIT tick counter as
additional entropy if a `sys_getticks` syscall were available.

---

## 4. DHCP Packet Construction

### 4.1 `buildDiscover`

```go
func buildDiscover(mac [6]byte, xid uint32) []byte {
    pkt := make([]byte, 300) // fixed header + options

    // BOOTP header
    pkt[0] = 1       // op: BOOTREQUEST
    pkt[1] = 1       // htype: Ethernet
    pkt[2] = 6       // hlen: MAC length
    pkt[3] = 0       // hops
    // XID (bytes 4-7)
    pkt[4] = byte(xid >> 24)
    pkt[5] = byte(xid >> 16)
    pkt[6] = byte(xid >> 8)
    pkt[7] = byte(xid)
    // secs = 0 (bytes 8-9)
    // flags: broadcast (bytes 10-11)
    pkt[10] = 0x80
    pkt[11] = 0x00
    // ciaddr, yiaddr, siaddr, giaddr = 0.0.0.0 (bytes 12-27)
    // chaddr: MAC address (bytes 28-33), rest padded to 16
    copy(pkt[28:34], mac[:])
    // sname, file: zero (bytes 44-235)

    // Magic cookie (bytes 236-239)
    pkt[236] = 0x63
    pkt[237] = 0x82
    pkt[238] = 0x53
    pkt[239] = 0x63

    // DHCP options (starting at byte 240)
    off := 240
    // Option 53: DHCP Message Type = DISCOVER (1)
    pkt[off] = 53; pkt[off+1] = 1; pkt[off+2] = 1
    off += 3
    // Option 55: Parameter Request List
    pkt[off] = 55; pkt[off+1] = 4
    pkt[off+2] = 1   // Subnet Mask
    pkt[off+3] = 3   // Router
    pkt[off+4] = 6   // DNS Server
    pkt[off+5] = 51  // Lease Time
    off += 6
    // Option 255: End
    pkt[off] = 255
    off++

    return pkt[:off]
}
```

### 4.2 `buildRequest`

```go
func buildRequest(mac [6]byte, xid uint32,
    requestedIP, serverIP uint32) []byte {
    pkt := make([]byte, 300)

    pkt[0] = 1       // BOOTREQUEST
    pkt[1] = 1       // Ethernet
    pkt[2] = 6       // MAC length
    pkt[4] = byte(xid >> 24)
    pkt[5] = byte(xid >> 16)
    pkt[6] = byte(xid >> 8)
    pkt[7] = byte(xid)
    pkt[10] = 0x80   // broadcast flag
    copy(pkt[28:34], mac[:])

    // Magic cookie
    pkt[236] = 0x63
    pkt[237] = 0x82
    pkt[238] = 0x53
    pkt[239] = 0x63

    off := 240
    // Option 53: DHCP Message Type = REQUEST (3)
    pkt[off] = 53; pkt[off+1] = 1; pkt[off+2] = 3
    off += 3
    // Option 50: Requested IP Address
    pkt[off] = 50; pkt[off+1] = 4
    pkt[off+2] = byte(requestedIP >> 24)
    pkt[off+3] = byte(requestedIP >> 16)
    pkt[off+4] = byte(requestedIP >> 8)
    pkt[off+5] = byte(requestedIP)
    off += 6
    // Option 54: Server Identifier
    pkt[off] = 54; pkt[off+1] = 4
    pkt[off+2] = byte(serverIP >> 24)
    pkt[off+3] = byte(serverIP >> 16)
    pkt[off+4] = byte(serverIP >> 8)
    pkt[off+5] = byte(serverIP)
    off += 6
    // Option 55: Parameter Request List
    pkt[off] = 55; pkt[off+1] = 4
    pkt[off+2] = 1; pkt[off+3] = 3
    pkt[off+4] = 6; pkt[off+5] = 51
    off += 6
    // Option 255: End
    pkt[off] = 255
    off++

    return pkt[:off]
}
```

### 4.3 `parseOffer` / `parseAck`

Both OFFER and ACK share the same parsing logic:

```go
type DHCPResult struct {
    YourIP    uint32  // yiaddr
    ServerIP  uint32  // siaddr or option 54
    Netmask   uint32  // option 1
    Gateway   uint32  // option 3
    DNS       uint32  // option 6
    LeaseTime uint32  // option 51
    MsgType   byte    // option 53
}

func parseDHCP(pkt []byte) (DHCPResult, bool) {
    if len(pkt) < 241 {
        return DHCPResult{}, false
    }
    // Check op = BOOTREPLY
    if pkt[0] != 2 {
        return DHCPResult{}, false
    }
    // Check magic cookie
    if pkt[236] != 0x63 || pkt[237] != 0x82 ||
       pkt[238] != 0x53 || pkt[239] != 0x63 {
        return DHCPResult{}, false
    }

    result := DHCPResult{
        YourIP:   ip4(pkt[16], pkt[17], pkt[18], pkt[19]),
        ServerIP: ip4(pkt[20], pkt[21], pkt[22], pkt[23]),
    }

    // Parse options starting at byte 240
    off := 240
    for off < len(pkt) {
        tag := pkt[off]
        if tag == 255 { break } // End
        if tag == 0 { off++; continue } // Padding
        if off+1 >= len(pkt) { break }
        length := int(pkt[off+1])
        off += 2
        if off+length > len(pkt) { break }

        switch tag {
        case 53: // Message Type
            if length >= 1 { result.MsgType = pkt[off] }
        case 1: // Subnet Mask
            if length >= 4 {
                result.Netmask = ip4(pkt[off], pkt[off+1],
                    pkt[off+2], pkt[off+3])
            }
        case 3: // Router
            if length >= 4 {
                result.Gateway = ip4(pkt[off], pkt[off+1],
                    pkt[off+2], pkt[off+3])
            }
        case 6: // DNS Server
            if length >= 4 {
                result.DNS = ip4(pkt[off], pkt[off+1],
                    pkt[off+2], pkt[off+3])
            }
        case 51: // Lease Time
            if length >= 4 {
                result.LeaseTime = uint32(pkt[off])<<24 |
                    uint32(pkt[off+1])<<16 |
                    uint32(pkt[off+2])<<8 |
                    uint32(pkt[off+3])
            }
        case 54: // Server Identifier (overrides siaddr)
            if length >= 4 {
                result.ServerIP = ip4(pkt[off], pkt[off+1],
                    pkt[off+2], pkt[off+3])
            }
        }
        off += length
    }

    return result, result.MsgType != 0
}

func ip4(a, b, c, d byte) uint32 {
    return uint32(a)<<24 | uint32(b)<<16 |
           uint32(c)<<8 | uint32(d)
}
```

---

## 5. Configuration Recording

### 5.1 Apply to Kernel

After a successful DHCPACK, the client calls the `gooos/net`
configuration functions:

```go
gooos.SetIP(result.YourIP)
gooos.SetNetmask(result.Netmask)
gooos.SetGateway(result.Gateway)
gooos.SetDNS(result.DNS)
gooos.ApplyNetConfig() // sends gratuitous ARP
```

This modifies the kernel globals (`ourIP`, `ourNetmask`,
`ourGateway`, `ourDNS`) that the IPv4 stack uses for all
subsequent outbound packets and ARP resolution.

### 5.2 Record to Filesystem

The client also writes a human-readable configuration file
to the in-memory filesystem:

```go
func writeConfigFile(r DHCPResult) {
    config := "# Network configuration (DHCP)\n" +
        "ip=" + gooos.FormatIP(r.YourIP) + "\n" +
        "netmask=" + gooos.FormatIP(r.Netmask) + "\n" +
        "gateway=" + gooos.FormatIP(r.Gateway) + "\n" +
        "dns=" + gooos.FormatIP(r.DNS) + "\n" +
        "lease=" + uitoa(r.LeaseTime) + "\n" +
        "server=" + gooos.FormatIP(r.ServerIP) + "\n"

    // Write to filesystem via sys_fs_write
    nameBytes := []byte("network.conf")
    dataBytes := []byte(config)
    gooos.WriteFile("network.conf", dataBytes)
}
```

Other programs can read this file with `cat network.conf`
to view the current network configuration. The file is
ephemeral (in-memory FS) and is lost on reboot.

### 5.3 `WriteFile` Helper

Add to `user/gooos/fs.go`:

```go
// WriteFile writes data to a named file in the in-memory FS.
// Creates the file if it does not exist. Returns true on success.
func WriteFile(name string, data []byte) bool {
    nameBytes := []byte(name)
    r := syscall4(sysFsWrite,
        uintptr(unsafe.Pointer(&nameBytes[0])),
        uintptr(len(name)),
        uintptr(unsafe.Pointer(&data[0])),
        uintptr(len(data)),
    )
    return r != 0xFFFFFFFFFFFFFFFF
}
```

---

## 6. Complete `main.go`

```go
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
)

func main() {
    gooos.Println("DHCP client starting...")

    // Get MAC address
    mac := gooos.GetMAC()
    gooos.Print("MAC: " + gooos.FormatMAC(mac) + "\n")

    // Generate transaction ID
    xid := generateXID(mac)

    // Create and bind socket
    fd := gooos.Socket()
    if fd < 0 {
        gooos.Println("Error: failed to create socket")
        gooos.Exit(1)
    }
    if gooos.Bind(fd, dhcpClientPort) < 0 {
        gooos.Println("Error: failed to bind port 68")
        gooos.Close(fd)
        gooos.Exit(1)
    }

    // --- DISCOVER ---
    discover := buildDiscover(mac, xid)
    gooos.Println("Sending DHCPDISCOVER...")
    n := gooos.UDPSendBroadcast(fd, discover, dhcpServerPort)
    if n < 0 {
        gooos.Println("Error: failed to send DISCOVER")
        gooos.Close(fd)
        gooos.Exit(1)
    }

    // --- Wait for OFFER ---
    var buf [1500]byte
    gooos.Println("Waiting for DHCPOFFER...")
    recvN, _ := gooos.UDPRecvFrom(fd, buf[:])
    if recvN <= 0 {
        gooos.Println("Error: no response received")
        gooos.Close(fd)
        gooos.Exit(1)
    }

    offer, ok := parseDHCP(buf[:recvN])
    if !ok || offer.MsgType != dhcpOffer {
        gooos.Println("Error: invalid DHCPOFFER")
        gooos.Close(fd)
        gooos.Exit(1)
    }

    // Verify XID matches
    rxXID := uint32(buf[4])<<24 | uint32(buf[5])<<16 |
             uint32(buf[6])<<8 | uint32(buf[7])
    if rxXID != xid {
        gooos.Println("Error: XID mismatch in OFFER")
        gooos.Close(fd)
        gooos.Exit(1)
    }

    gooos.Println("DHCPOFFER: IP=" + gooos.FormatIP(offer.YourIP))

    // --- REQUEST ---
    request := buildRequest(mac, xid, offer.YourIP, offer.ServerIP)
    gooos.Println("Sending DHCPREQUEST...")
    n = gooos.UDPSendBroadcast(fd, request, dhcpServerPort)
    if n < 0 {
        gooos.Println("Error: failed to send REQUEST")
        gooos.Close(fd)
        gooos.Exit(1)
    }

    // --- Wait for ACK ---
    gooos.Println("Waiting for DHCPACK...")
    recvN, _ = gooos.UDPRecvFrom(fd, buf[:])
    if recvN <= 0 {
        gooos.Println("Error: no ACK received")
        gooos.Close(fd)
        gooos.Exit(1)
    }

    ack, ok := parseDHCP(buf[:recvN])
    if !ok {
        gooos.Println("Error: invalid DHCPACK")
        gooos.Close(fd)
        gooos.Exit(1)
    }
    if ack.MsgType == dhcpNak {
        gooos.Println("Error: received DHCPNAK")
        gooos.Close(fd)
        gooos.Exit(1)
    }
    if ack.MsgType != dhcpAck {
        gooos.Println("Error: unexpected message type")
        gooos.Close(fd)
        gooos.Exit(1)
    }

    // Verify XID
    rxXID = uint32(buf[4])<<24 | uint32(buf[5])<<16 |
            uint32(buf[6])<<8 | uint32(buf[7])
    if rxXID != xid {
        gooos.Println("Error: XID mismatch in ACK")
        gooos.Close(fd)
        gooos.Exit(1)
    }

    // --- Apply configuration ---
    gooos.Println("Applying network configuration...")
    gooos.SetIP(ack.YourIP)
    gooos.SetNetmask(ack.Netmask)
    gooos.SetGateway(ack.Gateway)
    if ack.DNS != 0 {
        gooos.SetDNS(ack.DNS)
    }
    gooos.ApplyNetConfig()

    // Record to filesystem
    writeConfigFile(ack)

    // Print summary
    gooos.Println("")
    gooos.Println("Network configured successfully:")
    gooos.Println("  IP:      " + gooos.FormatIP(ack.YourIP))
    gooos.Println("  Netmask: " + gooos.FormatIP(ack.Netmask))
    gooos.Println("  Gateway: " + gooos.FormatIP(ack.Gateway))
    if ack.DNS != 0 {
        gooos.Println("  DNS:     " + gooos.FormatIP(ack.DNS))
    }
    gooos.Println("  Lease:   " + uitoa(ack.LeaseTime) + " seconds")
    gooos.Println("  Server:  " + gooos.FormatIP(ack.ServerIP))

    gooos.Close(fd)
}

// uitoa converts a uint32 to decimal string.
func uitoa(n uint32) string {
    if n == 0 { return "0" }
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

(The `buildDiscover`, `buildRequest`, `parseDHCP`, `ip4`,
`generateXID`, `writeConfigFile` functions are defined in §4
and §5 above.)

---

## 7. Build Integration

### 7.1 `user/Makefile` Update

Add `dhcp` to the `CMDS` list:

```makefile
CMDS := sh echo cat ls wc hello edit fdprobe gochan \
        goprobe smpprobe tinyc dhcp
```

### 7.2 Kernel Embed

`scripts/embed_elfs.sh` automatically picks up all `.elf` files
in `user/build/`. No changes needed unless the script
explicitly lists filenames.

---

## 8. Usage Example

### 8.1 Running the DHCP Client

After booting gooos in QEMU with network support:

```
gooos> dhcp
DHCP client starting...
MAC: 52:54:00:12:34:56
Sending DHCPDISCOVER...
Waiting for DHCPOFFER...
DHCPOFFER: IP=10.0.2.15
Sending DHCPREQUEST...
Waiting for DHCPACK...
Applying network configuration...

Network configured successfully:
  IP:      10.0.2.15
  Netmask: 255.255.255.0
  Gateway: 10.0.2.2
  DNS:     10.0.2.3
  Lease:   86400 seconds
  Server:  10.0.2.2
gooos>
```

### 8.2 Verifying Configuration

```
gooos> cat network.conf
# Network configuration (DHCP)
ip=10.0.2.15
netmask=255.255.255.0
gateway=10.0.2.2
dns=10.0.2.3
lease=86400
server=10.0.2.2
gooos>
```

### 8.3 Testing Connectivity After DHCP

```
gooos> ping 10.0.2.2
PING 10.0.2.2: 64 bytes, seq=1, ttl=64, time=1ms
PING 10.0.2.2: 64 bytes, seq=2, ttl=64, time=0ms
```

(Assumes `ping` command exists or will be implemented.)

---

## 9. QEMU Configuration

### 9.1 Default QEMU User-Mode Network

QEMU's default user-mode network (`-netdev user`) includes a
built-in DHCP server:

```
-netdev user,id=net0 -device e1000,netdev=net0
```

The built-in DHCP server assigns:
- IP: `10.0.2.15`
- Netmask: `255.255.255.0`
- Gateway: `10.0.2.2`
- DNS: `10.0.2.3`

### 9.2 TAP Network with External DHCP

For testing with a real DHCP server (e.g., dnsmasq):

```bash
# Create TAP device
sudo ip tuntap add dev tap0 mode tap user $USER
sudo ip addr add 10.0.0.1/24 dev tap0
sudo ip link set tap0 up

# Start dnsmasq as DHCP server
sudo dnsmasq --interface=tap0 \
    --dhcp-range=10.0.0.100,10.0.0.200,255.255.255.0,12h \
    --no-daemon

# Run QEMU with TAP
qemu-system-x86_64 ... \
    -netdev tap,id=net0,ifname=tap0,script=no,downscript=no \
    -device e1000,netdev=net0
```

---

## 10. Error Handling

| Condition | Behavior |
|---|---|
| Socket creation fails | Print error, exit 1 |
| Port 68 already bound | Print error, exit 1 |
| Broadcast send fails | Print error, exit 1 |
| No DHCPOFFER received (v1) | Block forever (see §3.3) |
| XID mismatch | Print error, exit 1 |
| Received DHCPNAK | Print error, exit 1 |
| Invalid packet format | Print error, exit 1 |
| `sys_net_config SET` fails | Print warning, continue |
| FS write fails | Print warning (non-fatal), continue |

---

## 11. Code Size Estimate

| Component | LOC |
|---|---|
| `user/cmd/dhcp/main.go` — main + DORA flow | ~120 |
| `user/cmd/dhcp/main.go` — packet build/parse | ~150 |
| `user/cmd/dhcp/main.go` — config recording | ~30 |
| `user/gooos/fs.go` — `WriteFile` addition | ~15 |
| Total | ~315 |

---

## 12. Dependencies

- **Socket API** (`net_socket_api.md`): `sys_socket`,
  `sys_bind`, `sys_recvfrom`, `sys_sendto_bcast`,
  `sys_net_config`.
- **Kernel UDP stack** (`net_ipv4_icmp_udp.md`): `udpSend`,
  `udpBind`, `udpHandle`.
- **NIC driver** (`net_pci_e1000_driver.md`): `e1000Transmit`,
  `e1000MAC`.
- **Userspace SDK** (`busybox_userland_sdk.md`): `gooos`
  package, `rt0.S`, `target.json`.

Dependency chain:
```
NIC Driver → IPv4/UDP Stack → Socket Syscalls → DHCP Client
```

---

## 13. Verification Criteria

### 13.1 QEMU User-Mode Network

1. Boot gooos with `-netdev user,id=net0 -device e1000,netdev=net0`.
2. Run `dhcp` from the shell.
3. Observe DORA exchange in serial output.
4. Verify `cat network.conf` shows correct values.
5. Verify `ping 10.0.2.2` works (if ping exists).

### 13.2 Packet Capture

1. Run QEMU with `-object filter-dump,id=dump,netdev=net0,file=dhcp.pcap`.
2. Open `dhcp.pcap` in Wireshark.
3. Verify:
   - DISCOVER: src=0.0.0.0:68, dst=255.255.255.255:67
   - OFFER: contains yiaddr + options
   - REQUEST: contains option 50 (requested IP) + option 54
   - ACK: contains yiaddr + options matching OFFER

### 13.3 Error Cases

1. Run `dhcp` without a DHCP server → blocks (v1 behavior).
2. Run `dhcp` twice → second run fails at bind (port 68 in use)
   if first dhcp process socket is not properly closed.
   Verify that after first dhcp exits cleanly, second run
   succeeds.

### 13.4 Regression

1. All existing commands work after DHCP runs.
2. `make lint` passes.

---

## 14. Future Enhancements

1. **Lease renewal**: Run as a background process that wakes
   at T1/T2 intervals to renew the lease.
2. **`sys_recvfrom` timeout**: Add timeout support to avoid
   blocking forever when no DHCP server exists.
3. **DHCP RELEASE**: Send RELEASE on graceful shutdown.
4. **Multiple NICs**: Accept a NIC name argument.
5. **Static IP fallback**: If DHCP fails after N retries, use
   a link-local address (169.254.x.x).
6. **DNS client**: Use the DNS server address for name
   resolution (requires a DNS resolver implementation).

---

## 15. Relationship to Other Documents

- **`net_overview.md`**: Lists DHCP client as a future
  userspace application. This document provides the design.
- **`net_socket_api.md`**: Defines the socket syscalls that
  the DHCP client uses. Must be implemented first.
- **`net_ipv4_icmp_udp.md`**: Defines the kernel UDP stack.
  Extended by `net_socket_api.md` with `udpBindWithChannel`.
- **`net_pci_e1000_driver.md`**: Provides the NIC driver for
  packet transmission/reception.
- **`busybox_userland_sdk.md`**: Defines the userspace SDK
  build system and `gooos` package structure.
