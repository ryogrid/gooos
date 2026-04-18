# Networking Stack — IPv4, ICMP Echo, and UDP

Detailed design for Phase 3: IPv4 datagram processing, ICMP
echo reply, and UDP send/receive with a per-port bind table.

Parent doc: `net_overview.md`.
Depends on: `net_ethernet_arp.md` (Phase 2).

---

## 1. IPv4 (`src/ipv4.go`)

### 1.1 Header Format

The IPv4 header is 20 bytes minimum (no options):
```
Offset  Size   Field
0       1      Version (4 bits) + IHL (4 bits)
1       1      TOS / DSCP
2       2      Total Length (big-endian)
4       2      Identification
6       2      Flags (3 bits) + Fragment Offset (13 bits)
8       1      TTL
9       1      Protocol
10      2      Header Checksum (big-endian)
12      4      Source IP (big-endian)
16      4      Destination IP (big-endian)
```

### 1.2 Constants

```go
const (
    ipv4HeaderMinSize = 20
    ipProtoICMP       = uint8(1)
    ipProtoUDP        = uint8(17)
)
```

### 1.3 Parsing

```go
type IPv4Header struct {
    VersionIHL  uint8
    TOS         uint8
    TotalLength uint16  // host byte order
    ID          uint16
    FlagsOffset uint16  // raw (flags in top 3 bits)
    TTL         uint8
    Protocol    uint8
    Checksum    uint16
    SrcIP       uint32  // host byte order
    DstIP       uint32  // host byte order
    HeaderLen   int     // IHL * 4 (computed)
}

func ipv4Parse(payload []byte) (IPv4Header, []byte, bool) {
    if len(payload) < ipv4HeaderMinSize {
        return IPv4Header{}, nil, false
    }

    var hdr IPv4Header
    hdr.VersionIHL = payload[0]
    version := hdr.VersionIHL >> 4
    ihl := int(hdr.VersionIHL & 0x0F)

    if version != 4 {
        return IPv4Header{}, nil, false
    }
    if ihl < 5 {
        return IPv4Header{}, nil, false
    }
    hdr.HeaderLen = ihl * 4
    if len(payload) < hdr.HeaderLen {
        return IPv4Header{}, nil, false
    }

    hdr.TOS = payload[1]
    hdr.TotalLength = uint16(payload[2])<<8 | uint16(payload[3])
    hdr.ID = uint16(payload[4])<<8 | uint16(payload[5])
    hdr.FlagsOffset = uint16(payload[6])<<8 | uint16(payload[7])
    hdr.TTL = payload[8]
    hdr.Protocol = payload[9]
    hdr.Checksum = uint16(payload[10])<<8 | uint16(payload[11])
    hdr.SrcIP = uint32(payload[12])<<24 | uint32(payload[13])<<16 |
                uint32(payload[14])<<8 | uint32(payload[15])
    hdr.DstIP = uint32(payload[16])<<24 | uint32(payload[17])<<16 |
                uint32(payload[18])<<8 | uint32(payload[19])

    // Verify checksum
    if ipv4Checksum(payload[:hdr.HeaderLen]) != 0 {
        netStats.ChecksumErr++
        return IPv4Header{}, nil, false
    }

    // Drop fragments (not supported)
    moreFragments := (hdr.FlagsOffset >> 13) & 1
    fragOff := hdr.FlagsOffset & 0x1FFF
    if moreFragments != 0 || fragOff != 0 {
        netStats.RxDropped++
        return IPv4Header{}, nil, false
    }

    // Truncate payload to TotalLength
    totalLen := int(hdr.TotalLength)
    if totalLen > len(payload) {
        totalLen = len(payload)
    }
    data := payload[hdr.HeaderLen:totalLen]

    return hdr, data, true
}
```

### 1.4 IPv4 Checksum

The IPv4 checksum is the one's complement of the one's
complement sum of all 16-bit words in the header:

```go
// ipv4Checksum computes the ones-complement checksum over the
// given byte slice (handles both even and odd lengths). Returns
// 0 for a valid received packet (checksum field included).
// Reused for IPv4 headers AND ICMP messages.
func ipv4Checksum(data []byte) uint16 {
    var sum uint32
    for i := 0; i+1 < len(data); i += 2 {
        sum += uint32(data[i])<<8 | uint32(data[i+1])
    }
    if len(data)%2 != 0 {
        sum += uint32(data[len(data)-1]) << 8
    }
    // Fold 32-bit sum to 16 bits
    for sum > 0xFFFF {
        sum = (sum & 0xFFFF) + (sum >> 16)
    }
    return uint16(^sum)
}
```

### 1.5 Building (for TX)

```go
var ipv4ID uint16 // global monotonic ID counter

// ipv4Build constructs an IPv4 datagram and transmits it as
// an Ethernet frame. Resolves the next-hop MAC via ARP.
func ipv4Send(proto uint8, srcIP, dstIP uint32,
    payload []byte) bool {

    totalLen := ipv4HeaderMinSize + len(payload)
    if totalLen > 1500 { // MTU check
        return false
    }

    // Build IPv4 header
    hdr := make([]byte, ipv4HeaderMinSize)
    hdr[0] = 0x45                     // Version=4, IHL=5
    hdr[1] = 0                        // TOS
    hdr[2] = byte(totalLen >> 8)      // Total Length
    hdr[3] = byte(totalLen)
    ipv4ID++
    hdr[4] = byte(ipv4ID >> 8)       // ID
    hdr[5] = byte(ipv4ID)
    hdr[6] = 0x40                     // Flags: Don't Fragment
    hdr[7] = 0                        // Fragment Offset
    hdr[8] = 64                       // TTL
    hdr[9] = proto                    // Protocol
    // Checksum (bytes 10-11) initially 0
    hdr[12] = byte(srcIP >> 24)       // Source IP
    hdr[13] = byte(srcIP >> 16)
    hdr[14] = byte(srcIP >> 8)
    hdr[15] = byte(srcIP)
    hdr[16] = byte(dstIP >> 24)       // Dest IP
    hdr[17] = byte(dstIP >> 16)
    hdr[18] = byte(dstIP >> 8)
    hdr[19] = byte(dstIP)

    // Compute and fill checksum
    cksum := ipv4Checksum(hdr)
    hdr[10] = byte(cksum >> 8)
    hdr[11] = byte(cksum)

    // Assemble packet
    packet := make([]byte, totalLen)
    copy(packet, hdr)
    copy(packet[ipv4HeaderMinSize:], payload)

    // Resolve next-hop MAC
    nextHop := nextHopIP(dstIP)
    dstMAC, found := arpResolve(nextHop)
    if !found {
        serialPrintln("IPv4: ARP resolve failed for " +
            ipToString(nextHop))
        return false
    }

    // Build and transmit Ethernet frame
    frame := ethernetBuild(dstMAC, e1000MAC,
        etherTypeIPv4, packet)
    return e1000Transmit(frame)
}
```

### 1.6 IPv4 Dispatch

```go
func ipv4Handle(payload []byte) {
    hdr, data, ok := ipv4Parse(payload)
    if !ok {
        return
    }

    // Check if addressed to us
    if hdr.DstIP != ourIP && hdr.DstIP != 0xFFFFFFFF {
        return // not for us (not broadcast either)
    }

    switch hdr.Protocol {
    case ipProtoICMP:
        icmpHandle(hdr, data)
    case ipProtoUDP:
        udpHandle(hdr, data)
    default:
        // Unknown protocol — drop
    }
}
```

### 1.7 LOC Estimate

| Item | LOC |
|---|---|
| Constants + header struct | 20–25 |
| `ipv4Parse` | 50–70 |
| `ipv4Checksum` | 15–20 |
| `ipv4Send` | 50–70 |
| `ipv4Handle` dispatch | 20–30 |
| **Total** | **155–215** |

---

## 2. ICMP Echo (`src/icmp.go`)

### 2.1 ICMP Header Format

```
Offset  Size   Field
0       1      Type
1       1      Code
2       2      Checksum (big-endian)
4       2      Identifier (echo only)
6       2      Sequence Number (echo only)
8       N      Data
```

### 2.2 Constants

```go
const (
    icmpTypeEchoReply   = uint8(0)
    icmpTypeEchoRequest = uint8(8)
    icmpHeaderSize      = 8
)
```

### 2.3 Echo Reply Handler

```go
func icmpHandle(ipHdr IPv4Header, payload []byte) {
    if len(payload) < icmpHeaderSize {
        return
    }

    icmpType := payload[0]
    // icmpCode := payload[1]

    if icmpType == icmpTypeEchoRequest {
        netStats.IcmpEcho++

        // Build echo reply: same data, swap src/dst IP,
        // set type=0, recompute checksum.
        reply := make([]byte, len(payload))
        copy(reply, payload)

        reply[0] = icmpTypeEchoReply // Type = Echo Reply
        reply[1] = 0                 // Code = 0
        reply[2] = 0                 // Clear checksum
        reply[3] = 0

        // Compute ICMP checksum over entire ICMP message
        cksum := ipv4Checksum(reply)
        reply[2] = byte(cksum >> 8)
        reply[3] = byte(cksum)

        // Send reply: swap src/dst
        ipv4Send(ipProtoICMP, ourIP, ipHdr.SrcIP, reply)

        serialPrint("ICMP: echo reply to ")
        serialPrintln(ipToString(ipHdr.SrcIP))
    }
}
```

### 2.4 Checksum Note

The ICMP checksum uses the same ones-complement algorithm as
the IPv4 header checksum, but covers the entire ICMP message
(header + data), not just the header. The existing
`ipv4Checksum` function works for this purpose since it
computes over an arbitrary byte slice.

### 2.5 LOC Estimate

| Item | LOC |
|---|---|
| Constants | 5–10 |
| `icmpHandle` | 35–50 |
| Logging | 5–10 |
| **Total** | **45–70** |

---

## 3. UDP (`src/udp.go`)

### 3.1 Header Format

```
Offset  Size   Field
0       2      Source Port (big-endian)
2       2      Destination Port (big-endian)
4       2      Length (header + data, big-endian)
6       2      Checksum (big-endian, optional for IPv4)
8       N      Data
```

### 3.2 Parsing

```go
const udpHeaderSize = 8

type UDPHeader struct {
    SrcPort uint16  // host byte order
    DstPort uint16
    Length  uint16
    Chksum  uint16
}

func udpParse(payload []byte) (UDPHeader, []byte, bool) {
    if len(payload) < udpHeaderSize {
        return UDPHeader{}, nil, false
    }
    var hdr UDPHeader
    hdr.SrcPort = uint16(payload[0])<<8 | uint16(payload[1])
    hdr.DstPort = uint16(payload[2])<<8 | uint16(payload[3])
    hdr.Length = uint16(payload[4])<<8 | uint16(payload[5])
    hdr.Chksum = uint16(payload[6])<<8 | uint16(payload[7])

    dataLen := int(hdr.Length) - udpHeaderSize
    if dataLen < 0 || udpHeaderSize+dataLen > len(payload) {
        return UDPHeader{}, nil, false
    }

    data := payload[udpHeaderSize : udpHeaderSize+dataLen]
    return hdr, data, true
}
```

### 3.3 UDP Checksum

The UDP checksum includes a pseudo-header:
```
Offset  Size   Field
0       4      Source IP
4       4      Destination IP
8       1      Zero
9       1      Protocol (17)
10      2      UDP Length
```

```go
// udpChecksum computes the UDP checksum with IPv4 pseudo-header.
func udpChecksum(srcIP, dstIP uint32, udpPacket []byte) uint16 {
    var sum uint32

    // Pseudo-header
    sum += uint32(srcIP >> 16)
    sum += uint32(srcIP & 0xFFFF)
    sum += uint32(dstIP >> 16)
    sum += uint32(dstIP & 0xFFFF)
    sum += uint32(ipProtoUDP)
    sum += uint32(len(udpPacket))

    // UDP header + data
    for i := 0; i+1 < len(udpPacket); i += 2 {
        sum += uint32(udpPacket[i])<<8 | uint32(udpPacket[i+1])
    }
    if len(udpPacket)%2 != 0 {
        sum += uint32(udpPacket[len(udpPacket)-1]) << 8
    }

    // Fold
    for sum > 0xFFFF {
        sum = (sum & 0xFFFF) + (sum >> 16)
    }

    result := uint16(^sum)
    if result == 0 {
        result = 0xFFFF // UDP: 0 means "no checksum"
    }
    return result
}

// udpChecksumVerify validates a received UDP packet's checksum.
// Returns true if the checksum is correct. Unlike udpChecksum
// (which substitutes 0→0xFFFF for TX), this returns the raw
// verification result: a valid packet's ones-complement sum
// (including the received checksum field) folds to 0xFFFF.
func udpChecksumVerify(srcIP, dstIP uint32, udpPacket []byte) bool {
    var sum uint32
    sum += uint32(srcIP >> 16)
    sum += uint32(srcIP & 0xFFFF)
    sum += uint32(dstIP >> 16)
    sum += uint32(dstIP & 0xFFFF)
    sum += uint32(ipProtoUDP)
    sum += uint32(len(udpPacket))
    for i := 0; i+1 < len(udpPacket); i += 2 {
        sum += uint32(udpPacket[i])<<8 | uint32(udpPacket[i+1])
    }
    if len(udpPacket)%2 != 0 {
        sum += uint32(udpPacket[len(udpPacket)-1]) << 8
    }
    for sum > 0xFFFF {
        sum = (sum & 0xFFFF) + (sum >> 16)
    }
    return uint16(sum) == 0xFFFF
}
```

### 3.4 Bind Table

A fixed-size array of 8 port bindings, each with a buffered
channel:

```go
const udpMaxBinds = 8

type UDPBinding struct {
    Port   uint16
    Ch     chan UDPDatagram
    Active bool
}

type UDPDatagram struct {
    SrcIP   uint32
    SrcPort uint16
    Data    []byte
}

var (
    udpBindings [udpMaxBinds]UDPBinding
    udpLock     Spinlock  // lock ordering rank 7
)
```

### 3.5 Bind

```go
// udpBind registers a listener on a UDP port. Returns a
// channel that receives incoming datagrams, or nil if the
// bind table is full or the port is already bound.
func udpBind(port uint16) chan UDPDatagram {
    flags := udpLock.Acquire()
    defer udpLock.Release(flags)

    // Check for duplicate
    for i := 0; i < udpMaxBinds; i++ {
        if udpBindings[i].Active && udpBindings[i].Port == port {
            return nil // already bound
        }
    }

    // Find empty slot
    for i := 0; i < udpMaxBinds; i++ {
        if !udpBindings[i].Active {
            ch := make(chan UDPDatagram, 16)
            udpBindings[i] = UDPBinding{
                Port: port, Ch: ch, Active: true,
            }
            return ch
        }
    }
    return nil // table full
}

// udpUnbind removes a port binding.
func udpUnbind(port uint16) {
    flags := udpLock.Acquire()
    defer udpLock.Release(flags)
    for i := 0; i < udpMaxBinds; i++ {
        if udpBindings[i].Active && udpBindings[i].Port == port {
            // Close channel? Depends on consumer contract.
            udpBindings[i].Active = false
            return
        }
    }
}
```

### 3.6 Receive Dispatch

```go
func udpHandle(ipHdr IPv4Header, payload []byte) {
    hdr, data, ok := udpParse(payload)
    if !ok {
        return
    }

    // Optional: verify checksum (skip if checksum == 0).
    // Use udpChecksumVerify (not udpChecksum) because udpChecksum
    // substitutes 0→0xFFFF for TX, which breaks RX verification.
    if hdr.Chksum != 0 {
        if !udpChecksumVerify(ipHdr.SrcIP, ipHdr.DstIP, payload) {
            netStats.ChecksumErr++
            return
        }
    }

    // Look up bind table
    flags := udpLock.Acquire()
    for i := 0; i < udpMaxBinds; i++ {
        if udpBindings[i].Active &&
           udpBindings[i].Port == hdr.DstPort {
            dg := UDPDatagram{
                SrcIP: ipHdr.SrcIP,
                SrcPort: hdr.SrcPort,
                Data: make([]byte, len(data)),
            }
            copy(dg.Data, data)

            // Non-blocking send to channel
            select {
            case udpBindings[i].Ch <- dg:
                netStats.UdpRecv++
            default:
                netStats.RxDropped++
            }
            udpLock.Release(flags)
            return
        }
    }
    udpLock.Release(flags)
    // No binding for this port — drop silently
}
```

### 3.7 Send

```go
// udpSend sends a UDP datagram to dstIP:dstPort from srcPort.
func udpSend(dstIP uint32, dstPort, srcPort uint16,
    data []byte) bool {

    udpLen := udpHeaderSize + len(data)
    packet := make([]byte, udpLen)

    // UDP header
    packet[0] = byte(srcPort >> 8)
    packet[1] = byte(srcPort)
    packet[2] = byte(dstPort >> 8)
    packet[3] = byte(dstPort)
    packet[4] = byte(udpLen >> 8)
    packet[5] = byte(udpLen)
    packet[6] = 0  // checksum initially 0
    packet[7] = 0

    // Data
    copy(packet[udpHeaderSize:], data)

    // Compute checksum
    cksum := udpChecksum(ourIP, dstIP, packet)
    packet[6] = byte(cksum >> 8)
    packet[7] = byte(cksum)

    netStats.UdpSend++

    // Send via IPv4
    return ipv4Send(ipProtoUDP, ourIP, dstIP, packet)
}
```

### 3.8 UDP Echo Server (Built-in, for testing)

```go
// udpEchoServer binds to port 7 and echoes back any received
// datagrams. Runs as a goroutine.
func udpEchoServer() {
    ch := udpBind(7)
    if ch == nil {
        serialPrintln("UDP echo: bind failed")
        return
    }
    serialPrintln("UDP echo: listening on port 7")
    for {
        dg := <-ch
        udpSend(dg.SrcIP, dg.SrcPort, 7, dg.Data)
    }
}
```

### 3.9 LOC Estimate

| Item | LOC |
|---|---|
| Constants + structs | 20–30 |
| `udpParse` | 25–35 |
| `udpChecksum` | 25–35 |
| Bind table + lock | 30–40 |
| `udpBind` / `udpUnbind` | 30–40 |
| `udpHandle` dispatch | 30–45 |
| `udpSend` | 30–40 |
| UDP echo server | 15–20 |
| **Total** | **205–285** |

---

## 4. Static IP Configuration

### 4.1 Globals

```go
var (
    ourIP      uint32
    ourNetmask uint32
    ourGateway uint32
)
```

### 4.2 Default Values

QEMU user-mode networking assigns:
- Guest IP: `10.0.2.15`
- Gateway: `10.0.2.2`
- DNS: `10.0.2.3`
- Netmask: `255.255.255.0`

These are hardcoded defaults. Future DHCP support will
override them dynamically.

### 4.3 TAP Mode Values

When using TAP networking, the IP must be configured to
match the host's TAP setup:
```bash
# Host side:
ip addr add 10.0.0.1/24 dev tap0
ip link set tap0 up
# Kernel side: ourIP = 10.0.0.2, gateway = 10.0.0.1
```

This is set via compile-time constants or a boot parameter
(future work).

---

## 5. Integration with Boot Sequence

### 5.1 `main.go` Additions

After e1000 init:
```go
if e1000Found {
    netInit()
    go udpEchoServer()
    serialPrintln("NET: initialized, UDP echo on port 7")
}
```

### 5.2 Initialization Order

```
1. serialInit()
2. idtInit()
3. picRemap()
4. percpuInitBSPEarly()
5. pitInit()
...
6. smpInit()
7. pciInit()          ← Phase 1 (new)
8. e1000Init()        ← Phase 1 (new)
9. netInit()          ← Phase 2-3 (new)
10. go udpEchoServer() ← Phase 3 (new)
...
11. setupUserspace()
```

---

## 6. Verification Criteria

### 6.1 IPv4

1. QEMU sends an ICMP echo request to kernel's IP.
2. `ipv4Parse` returns valid header with correct src/dst.
3. Checksum verification passes (return 0).
4. Fragment drop: send fragmented packet → counter incremented.

### 6.2 ICMP Echo

1. `ping 10.0.2.15` from QEMU host (TAP mode) or from within
   QEMU user-mode (using `hostfwd`).
2. Serial log: `ICMP: echo reply to X.X.X.X`.
3. Host `ping` shows RTT < 10 ms.
4. pcap dump shows matching request/reply pairs.

### 6.3 UDP

1. Start kernel with `udpEchoServer()` on port 7.
2. From host: `echo "hello" | nc -u 10.0.2.15 7` (TAP mode)
   or use QEMU port forwarding:
   `-netdev user,id=n0,hostfwd=udp::9999-:7`
   then `echo "hello" | nc -u 127.0.0.1 9999`.
3. `nc` receives "hello" back.
4. Serial log: `netStats.UdpRecv` and `netStats.UdpSend`
   both non-zero.

### 6.4 UDP Send (kernel-initiated)

1. On boot, kernel sends "gooos alive" to gateway:9999.
2. Host listens: `nc -lu 9999`.
3. Host receives the message.

### 6.5 Checksum Validation

1. Craft packet with intentionally bad IPv4 checksum.
2. `ipv4Parse` returns `false`.
3. `netStats.ChecksumErr` incremented.
4. Same for UDP checksum.

---

## 7. Risks Specific to Phase 3

| Risk | Mitigation |
|---|---|
| IPv4 checksum off-by-one | Test with known-good pcap data; compare byte-for-byte with reference implementation |
| UDP pseudo-header checksum | Compute incrementally; test with captured UDP packets from Wireshark |
| `make([]byte, ...)` in RX hot path | Acceptable for Phase 3; Phase 4 buffer pool eliminates this |
| Channel backpressure on UDP bind | Buffered channel (cap 16); non-blocking send drops overflow; logged |
| TTL=0 packets | Dropped silently; no ICMP Time Exceeded generated (out of scope) |
| IP options (IHL > 5) | Parsed correctly (skip option bytes); payload starts at IHL×4 |
| MTU exceeded on TX | `ipv4Send` checks `totalLen > 1500`; returns false |
| No routing table | Static gateway; all non-local traffic sent to gateway. Sufficient for QEMU. |
