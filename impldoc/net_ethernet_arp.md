# Networking Stack — Ethernet Framing and ARP

Detailed design for Phase 2: Ethernet frame parsing/building,
byte-order utilities, ARP cache management, and the RX dispatch
goroutine.

Parent doc: `net_overview.md`.
Depends on: `net_pci_e1000_driver.md` (Phase 1).

---

## 1. Ethernet Framing (`src/ethernet.go`)

### 1.1 Frame Format

An Ethernet II frame (without 802.1Q VLAN tag):
```
Offset  Size   Field
0       6      Destination MAC
6       6      Source MAC
12      2      EtherType (big-endian)
14      N      Payload (46–1500 bytes)
```

The e1000 strips the trailing 4-byte FCS/CRC when
`RCTL.SECRC` is set (see `net_pci_e1000_driver.md §2.4`).

### 1.2 EtherType Constants

```go
const (
    etherTypeIPv4 = uint16(0x0800)
    etherTypeARP  = uint16(0x0806)
)
```

### 1.3 Parsing

```go
const ethernetHeaderSize = 14

type EthernetHeader struct {
    Dst       [6]byte
    Src       [6]byte
    EtherType uint16 // host byte order (after ntohs)
}

// ethernetParse extracts the Ethernet header and payload from
// a raw frame. Returns the header, payload slice, and ok=true
// if the frame is at least 14 bytes.
func ethernetParse(frame []byte) (EthernetHeader, []byte, bool) {
    if len(frame) < ethernetHeaderSize {
        return EthernetHeader{}, nil, false
    }
    var hdr EthernetHeader
    copy(hdr.Dst[:], frame[0:6])
    copy(hdr.Src[:], frame[6:12])
    hdr.EtherType = uint16(frame[12])<<8 | uint16(frame[13])
    return hdr, frame[ethernetHeaderSize:], true
}
```

Note: `frame[12]` is the high byte of EtherType (big-endian on
wire), so `uint16(frame[12])<<8 | uint16(frame[13])` directly
produces host byte order without needing `ntohs` (since we
manually assemble from individual bytes rather than casting a
multi-byte value).

### 1.4 Building

```go
// ethernetBuild prepends a 14-byte Ethernet header to a payload.
// Returns the complete frame. dst and src are MAC addresses;
// etherType is in host byte order.
func ethernetBuild(dst, src [6]byte, etherType uint16,
    payload []byte) []byte {

    frame := make([]byte, ethernetHeaderSize+len(payload))
    copy(frame[0:6], dst[:])
    copy(frame[6:12], src[:])
    frame[12] = byte(etherType >> 8)   // big-endian on wire
    frame[13] = byte(etherType)
    copy(frame[ethernetHeaderSize:], payload)
    return frame
}
```

### 1.5 Address Checks

```go
var broadcastMAC = [6]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

// isForUs returns true if the frame is addressed to our MAC or
// is a broadcast.
func isForUs(dst [6]byte) bool {
    return dst == e1000MAC || dst == broadcastMAC
}
```

### 1.6 Dispatch Table

The RX dispatch goroutine calls `ethernetParse`, checks
`isForUs`, then dispatches by EtherType:

```go
func ethernetDispatch(frame []byte) {
    hdr, payload, ok := ethernetParse(frame)
    if !ok {
        return
    }
    if !isForUs(hdr.Dst) {
        return // not for us
    }
    switch hdr.EtherType {
    case etherTypeARP:
        arpHandle(hdr.Src, payload)
    case etherTypeIPv4:
        ipv4Handle(payload)
    default:
        // Unknown EtherType — drop silently
        netStats.RxUnknownEtherType++
    }
}
```

### 1.7 LOC Estimate

| Item | LOC |
|---|---|
| Constants + header struct | 15–20 |
| `ethernetParse` | 15–25 |
| `ethernetBuild` | 15–25 |
| Address checks | 10–15 |
| Dispatch | 20–30 |
| **Total** | **75–115** |

---

## 2. ARP (`src/arp.go`)

### 2.1 ARP Packet Format

ARP for IPv4 over Ethernet is a fixed 28-byte payload:
```
Offset  Size   Field
0       2      Hardware Type (0x0001 = Ethernet)
2       2      Protocol Type (0x0800 = IPv4)
4       1      Hardware Addr Length (6)
5       1      Protocol Addr Length (4)
6       2      Operation (1 = Request, 2 = Reply)
8       6      Sender Hardware Address (SHA)
14      4      Sender Protocol Address (SPA)
18      6      Target Hardware Address (THA)
24      4      Target Protocol Address (TPA)
```

All multi-byte fields are big-endian.

### 2.2 Parsing

```go
const arpPacketSize = 28

type ARPPacket struct {
    HWType    uint16
    ProtoType uint16
    HWLen     uint8
    ProtoLen  uint8
    Op        uint16
    SHA       [6]byte  // Sender Hardware Address
    SPA       uint32   // Sender Protocol Address
    THA       [6]byte  // Target Hardware Address
    TPA       uint32   // Target Protocol Address
}

const (
    arpOpRequest = uint16(1)
    arpOpReply   = uint16(2)
)

func arpParse(payload []byte) (ARPPacket, bool) {
    if len(payload) < arpPacketSize {
        return ARPPacket{}, false
    }
    var pkt ARPPacket
    pkt.HWType = uint16(payload[0])<<8 | uint16(payload[1])
    pkt.ProtoType = uint16(payload[2])<<8 | uint16(payload[3])
    pkt.HWLen = payload[4]
    pkt.ProtoLen = payload[5]
    pkt.Op = uint16(payload[6])<<8 | uint16(payload[7])
    copy(pkt.SHA[:], payload[8:14])
    pkt.SPA = uint32(payload[14])<<24 | uint32(payload[15])<<16 |
              uint32(payload[16])<<8 | uint32(payload[17])
    copy(pkt.THA[:], payload[18:24])
    pkt.TPA = uint32(payload[24])<<24 | uint32(payload[25])<<16 |
              uint32(payload[26])<<8 | uint32(payload[27])
    return pkt, true
}
```

### 2.3 Building

```go
func arpBuild(op uint16, sha [6]byte, spa uint32,
    tha [6]byte, tpa uint32) []byte {

    buf := make([]byte, arpPacketSize)
    buf[0] = 0x00; buf[1] = 0x01   // HW type: Ethernet
    buf[2] = 0x08; buf[3] = 0x00   // Proto type: IPv4
    buf[4] = 6                      // HW addr length
    buf[5] = 4                      // Proto addr length
    buf[6] = byte(op >> 8)
    buf[7] = byte(op)
    copy(buf[8:14], sha[:])
    buf[14] = byte(spa >> 24)
    buf[15] = byte(spa >> 16)
    buf[16] = byte(spa >> 8)
    buf[17] = byte(spa)
    copy(buf[18:24], tha[:])
    buf[24] = byte(tpa >> 24)
    buf[25] = byte(tpa >> 16)
    buf[26] = byte(tpa >> 8)
    buf[27] = byte(tpa)
    return buf
}
```

### 2.4 ARP Cache

Fixed-size array with LRU-by-age replacement:

```go
const arpCacheSize = 16

type ARPEntry struct {
    IP   uint32
    MAC  [6]byte
    Age  uint64  // pitTicks at last update
    Used bool
}

var (
    arpCache [arpCacheSize]ARPEntry
    arpLock  Spinlock  // lock ordering rank 6
)
```

**Lookup:**
```go
func arpLookup(ip uint32) ([6]byte, bool) {
    flags := arpLock.Acquire()
    defer arpLock.Release(flags)
    for i := 0; i < arpCacheSize; i++ {
        if arpCache[i].Used && arpCache[i].IP == ip {
            return arpCache[i].MAC, true
        }
    }
    return [6]byte{}, false
}
```

Note: `defer` may not be usable in `//go:nosplit` functions.
For lock acquire/release in non-ISR context, `defer` is
acceptable. In ISR context, use explicit release.

**Learn (insert or update):**
```go
func arpLearn(ip uint32, mac [6]byte) {
    flags := arpLock.Acquire()
    // Update existing entry
    for i := 0; i < arpCacheSize; i++ {
        if arpCache[i].Used && arpCache[i].IP == ip {
            arpCache[i].MAC = mac
            arpCache[i].Age = pitTicks
            arpLock.Release(flags)
            return
        }
    }
    // Find empty slot
    for i := 0; i < arpCacheSize; i++ {
        if !arpCache[i].Used {
            arpCache[i] = ARPEntry{IP: ip, MAC: mac,
                Age: pitTicks, Used: true}
            arpLock.Release(flags)
            return
        }
    }
    // Replace oldest entry
    oldest := 0
    for i := 1; i < arpCacheSize; i++ {
        if arpCache[i].Age < arpCache[oldest].Age {
            oldest = i
        }
    }
    arpCache[oldest] = ARPEntry{IP: ip, MAC: mac,
        Age: pitTicks, Used: true}
    arpLock.Release(flags)
}
```

### 2.5 ARP Request/Reply Handling

```go
func arpHandle(srcMAC [6]byte, payload []byte) {
    pkt, ok := arpParse(payload)
    if !ok || pkt.HWType != 1 || pkt.ProtoType != 0x0800 {
        return
    }

    // Learn sender's MAC-IP mapping
    arpLearn(pkt.SPA, pkt.SHA)
    netStats.ArpHits++

    // If ARP Request for our IP → send Reply
    if pkt.Op == arpOpRequest && pkt.TPA == ourIP {
        reply := arpBuild(arpOpReply, e1000MAC, ourIP,
            pkt.SHA, pkt.SPA)
        frame := ethernetBuild(pkt.SHA, e1000MAC,
            etherTypeARP, reply)
        e1000Transmit(frame)
    }
}
```

### 2.6 ARP Resolve (with timeout)

```go
// arpResolve returns the MAC address for an IP, performing
// an ARP request if not cached. Blocks up to ~2 seconds.
func arpResolve(ip uint32) ([6]byte, bool) {
    // Check cache first
    mac, found := arpLookup(ip)
    if found {
        return mac, true
    }

    // Send ARP Request
    req := arpBuild(arpOpRequest, e1000MAC, ourIP,
        [6]byte{}, ip)
    frame := ethernetBuild(broadcastMAC, e1000MAC,
        etherTypeARP, req)
    e1000Transmit(frame)

    // Wait for reply with timeout (~2 seconds at 100 Hz)
    timeout := afterTicks(200) // 200 ticks = 2 seconds
    for {
        select {
        case <-timeout:
            netStats.ArpMisses++
            return [6]byte{}, false
        default:
            mac, found := arpLookup(ip)
            if found {
                return mac, true
            }
            hlt() // brief sleep
        }
    }
}
```

**Note:** The `afterTicks` channel is already implemented and
tested in gooos. The `select`/`default` pattern allows checking
the cache between timeout checks.

### 2.7 Gratuitous ARP

Sent once during `netInit()` to pre-populate the host's
ARP cache:

```go
func arpSendGratuitous() {
    pkt := arpBuild(arpOpReply, e1000MAC, ourIP,
        broadcastMAC, ourIP)
    frame := ethernetBuild(broadcastMAC, e1000MAC,
        etherTypeARP, pkt)
    e1000Transmit(frame)
    serialPrintln("ARP: sent gratuitous ARP")
}
```

### 2.8 LOC Estimate

| Item | LOC |
|---|---|
| ARP packet parse/build | 60–80 |
| ARP cache + lock | 50–80 |
| Lookup / Learn | 40–60 |
| Request/Reply handling | 30–50 |
| arpResolve with timeout | 30–50 |
| Gratuitous ARP | 10–15 |
| **Total** | **220–335** |

---

## 3. Byte-Order Utilities (`src/netutil.go`)

### 3.1 Conversion Functions

x86-64 is little-endian; network byte order is big-endian.

```go
func htons(v uint16) uint16 {
    return (v << 8) | (v >> 8)
}

func ntohs(v uint16) uint16 {
    return htons(v) // symmetric
}

func htonl(v uint32) uint32 {
    return (v<<24) | ((v<<8)&0x00FF0000) |
           ((v>>8)&0x0000FF00) | (v>>24)
}

func ntohl(v uint32) uint32 {
    return htonl(v) // symmetric
}
```

### 3.2 Formatting Helpers

```go
// macToString formats a MAC address as "XX:XX:XX:XX:XX:XX".
func macToString(mac [6]byte) string {
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

// ipToString formats an IPv4 address as "A.B.C.D".
func ipToString(ip uint32) string {
    return utoa(uint64(ip>>24)) + "." +
           utoa(uint64((ip>>16)&0xFF)) + "." +
           utoa(uint64((ip>>8)&0xFF)) + "." +
           utoa(uint64(ip&0xFF))
}

// parseIPv4 parses "A.B.C.D" into a uint32. Simple manual
// parser (no strconv available in bare-metal).
func parseIPv4(s string) uint32 {
    // Implementation: scan for dots, convert each octet
    // ...
}
```

### 3.3 LOC Estimate

| Item | LOC |
|---|---|
| htons/ntohs/htonl/ntohl | 15–20 |
| macToString | 15–20 |
| ipToString | 10–15 |
| parseIPv4 | 20–30 |
| **Total** | **60–85** |

---

## 4. RX Dispatch Goroutine (`src/net.go`)

### 4.1 Network Initialization

```go
// Static IP configuration
var (
    ourIP      uint32 // e.g., 10.0.2.15 (QEMU user-mode default)
    ourNetmask uint32 // e.g., 255.255.255.0
    ourGateway uint32 // e.g., 10.0.2.2
)

func netInit() {
    // Default QEMU user-mode networking addresses
    ourIP = (10 << 24) | (0 << 16) | (2 << 8) | 15
    ourNetmask = (255 << 24) | (255 << 16) | (255 << 8) | 0
    ourGateway = (10 << 24) | (0 << 16) | (2 << 8) | 2

    serialPrint("NET: IP=")
    serialPrint(ipToString(ourIP))
    serialPrint(" GW=")
    serialPrintln(ipToString(ourGateway))

    // Send gratuitous ARP
    arpSendGratuitous()

    // Start RX dispatch goroutine
    go netRxLoop()
}
```

### 4.2 RX Loop

```go
func netRxLoop() {
    for {
        frame := e1000TryReceive()
        if frame != nil {
            netStats.RxPackets++
            netStats.RxBytes += uint64(len(frame))
            ethernetDispatch(frame)
        } else {
            hlt() // wait for interrupt or next tick
        }
    }
}
```

In Phase 4, this becomes interrupt-driven: the goroutine
blocks on `<-rxSignalCh` instead of polling + `hlt()`.

### 4.3 Routing Helper

For outbound packets, determine if the destination is on the
local subnet or must go through the gateway:

```go
// nextHopIP returns the IP to ARP-resolve for a given
// destination. If dst is on the local subnet, return dst;
// otherwise return the gateway.
func nextHopIP(dst uint32) uint32 {
    if (dst & ourNetmask) == (ourIP & ourNetmask) {
        return dst // local subnet
    }
    return ourGateway
}
```

### 4.4 LOC Estimate

| Item | LOC |
|---|---|
| Static IP config + init | 20–30 |
| netRxLoop | 15–25 |
| nextHopIP | 10–15 |
| **Total** | **45–70** |

---

## 5. Verification Criteria

### 5.1 ARP Reply

1. Boot kernel with `-device e1000,netdev=n0 -netdev
   user,id=n0`.
2. QEMU user-mode sends ARP request for kernel's IP.
3. Serial log shows `ARP: learned 10.0.2.2 = XX:XX:XX:XX:XX:XX`.
4. Kernel sends ARP reply.
5. Verify in pcap dump: ARP request from QEMU, ARP reply from
   kernel.

### 5.2 Gratuitous ARP

1. On boot, serial log shows `ARP: sent gratuitous ARP`.
2. pcap dump contains ARP reply with SPA=TPA=ourIP.

### 5.3 ARP Cache

1. After several ARP exchanges, serial log shows learned
   entries.
2. `arpLookup(gatewayIP)` returns the gateway's MAC.

### 5.4 ARP Timeout

1. Call `arpResolve` for an IP that does not exist on the
   network.
2. After ~2 seconds, returns `false`.
3. `netStats.ArpMisses` incremented.

### 5.5 Ethernet Dispatch

1. Send an IPv4 packet to kernel from QEMU.
2. `ethernetDispatch` routes to `ipv4Handle`.
3. Send an ARP packet → routes to `arpHandle`.
4. Send unknown EtherType → `netStats.RxUnknownEtherType`
   incremented.

---

## 6. Risks Specific to Phase 2

| Risk | Mitigation |
|---|---|
| Big-endian field errors | Byte-level assembly from individual `payload[i]` bytes avoids endianness bugs. No `unsafe` casting of multi-byte fields. |
| ARP cache exhaustion | 16 entries with LRU replacement. Sufficient for QEMU testing. Log warning when replacing oldest. |
| ARP timeout deadlock | `afterTicks` returns a channel; `select` with `default` prevents permanent block. Worst case: 2-second delay. |
| `make([]byte, ...)` allocation in hot path | Acceptable for Phase 2. Phase 4 replaces with buffer pool. |
| EtherType 0x86DD (IPv6) from QEMU | Silently dropped; `RxUnknownEtherType` counter incremented. |
