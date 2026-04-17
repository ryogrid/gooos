# Networking Stack — PCI Enumeration and e1000 NIC Driver

Detailed design for Phase 1: PCI bus scan, e1000 register
abstraction, TX/RX descriptor rings, and interrupt handling.

Parent doc: `net_overview.md`.

---

## 1. PCI Bus Enumeration (`src/pci.go`)

### 1.1 Background

The Intel e1000 (82540EM) is a PCI device. QEMU exposes it on
PCI bus 0. The kernel must scan the PCI configuration space to
discover the device, read its BAR0 (MMIO base address), and
enable bus-master DMA.

PCI configuration space is accessed via I/O ports using
mechanism 1:
- **0xCF8** (CONFIG_ADDRESS): 32-bit write selects
  bus/device/function/register.
- **0xCFC** (CONFIG_DATA): 32-bit read/write accesses the
  selected register.

### 1.2 Prerequisite: 32-bit I/O Port Stubs

The existing `outb`/`inb` in `src/stubs.S` handle only 8-bit
I/O. PCI mechanism 1 requires 32-bit I/O.

**New assembly stubs in `src/stubs.S`:**

```
    /* outl(port uint16, val uint32) */
    /* SysV ABI: di = port, esi = val */
    .global outl
outl:
    movw    %di, %dx
    movl    %esi, %eax
    outl    %eax, %dx
    ret

    /* inl(port uint16) uint32 */
    /* SysV ABI: di = port; returns eax */
    .global inl
inl:
    movw    %di, %dx
    inl     %dx, %eax
    ret
```

**Go declarations in `src/pci.go`:**

```go
//go:linkname outl outl
func outl(port uint16, val uint32)

//go:linkname inl inl
func inl(port uint16) uint32
```

### 1.3 PCI Config Space Access

```go
const (
    pciConfigAddr = uint16(0xCF8)
    pciConfigData = uint16(0xCFC)
)

// pciConfigRead32 reads a 32-bit value from PCI config space.
func pciConfigRead32(bus, device, function, offset uint8) uint32 {
    addr := uint32(1<<31) |              // enable bit
        uint32(bus)<<16 |
        uint32(device)<<11 |
        uint32(function)<<8 |
        uint32(offset&0xFC)               // 4-byte aligned
    outl(pciConfigAddr, addr)
    return inl(pciConfigData)
}

// pciConfigWrite32 writes a 32-bit value to PCI config space.
func pciConfigWrite32(bus, device, function, offset uint8, val uint32) {
    addr := uint32(1<<31) |
        uint32(bus)<<16 |
        uint32(device)<<11 |
        uint32(function)<<8 |
        uint32(offset&0xFC)
    outl(pciConfigAddr, addr)
    outl(pciConfigData, val)
}
```

### 1.4 Bus Scan

Brute-force scan of bus 0, devices 0–31, functions 0–7:

```go
const (
    e1000VendorID = uint16(0x8086)
    e1000DeviceID = uint16(0x100E) // 82540EM
)

type PCIDevice struct {
    Bus      uint8
    Device   uint8
    Function uint8
    VendorID uint16
    DeviceID uint16
    BAR0     uint32
    IRQLine  uint8
}

var e1000PCI PCIDevice
var e1000Found bool
```

The scan reads offset 0x00 (vendor/device), checks for
match, then reads:
- Offset 0x10: BAR0 (MMIO base; mask low 4 bits)
- Offset 0x04: Command register (set bit 2 for bus-master)
- Offset 0x3C: Interrupt Line (legacy IRQ number)

### 1.5 BAR0 Decode

BAR0 for e1000 is a 32-bit memory-mapped BAR:
- Bit 0 = 0 → memory space
- Bits 2:1 = type (00 = 32-bit)
- Bits 31:4 = base address

```go
bar0Raw := pciConfigRead32(bus, dev, fn, 0x10)
bar0Addr := uintptr(bar0Raw & 0xFFFFFFF0)
```

### 1.6 Bus-Master Enable

```go
cmd := pciConfigRead32(bus, dev, fn, 0x04)
cmd |= (1 << 2)     // bus-master enable
cmd |= (1 << 1)     // memory space enable
pciConfigWrite32(bus, dev, fn, 0x04, cmd)
```

### 1.7 MMIO Mapping

Map BAR0 region (128 KiB for e1000) using `mapPage` with
PCD+PWT flags for uncacheable MMIO:

```go
const e1000MmioSize = 128 * 1024  // 0x20000

for off := uintptr(0); off < e1000MmioSize; off += pageSize {
    mapPage(bar0Addr+off, bar0Addr+off,
        pagePresent|pageWrite|pagePCD|pagePWT)
}
```

This identity-maps the MMIO region, consistent with IOAPIC
mapping in `src/ioapic.go:70`.

### 1.8 Serial Logging

Log discovery results:
```
PCI: found e1000 at 00:03.0 BAR0=0xFEBC0000 IRQ=11
```

### 1.9 LOC Estimate

| Item | LOC |
|---|---|
| `outl`/`inl` stubs (stubs.S) | 15–20 |
| PCI config read/write | 25–35 |
| Bus scan + device struct | 50–70 |
| BAR0 decode + bus-master | 20–30 |
| MMIO mapping | 15–20 |
| Serial logging | 10–15 |
| **Total (`pci.go` + stubs)** | **135–190** |

---

## 2. e1000 Register Abstraction (`src/e1000_regs.go`)

### 2.1 Register Map

All registers are 32-bit MMIO at offsets from BAR0.

```go
const (
    // Device control and status
    e1000CTRL   = 0x00000 // Device Control
    e1000STATUS = 0x00008 // Device Status

    // Interrupt registers
    e1000ICR    = 0x000C0 // Interrupt Cause Read
    e1000ICS    = 0x000C8 // Interrupt Cause Set
    e1000IMS    = 0x000D0 // Interrupt Mask Set
    e1000IMC    = 0x000D8 // Interrupt Mask Clear

    // Receive registers
    e1000RCTL   = 0x00100 // Receive Control
    e1000RDBAL  = 0x02800 // RX Descriptor Base Low
    e1000RDBAH  = 0x02804 // RX Descriptor Base High
    e1000RDLEN  = 0x02808 // RX Descriptor Length
    e1000RDH    = 0x02810 // RX Descriptor Head
    e1000RDT    = 0x02818 // RX Descriptor Tail

    // Transmit registers
    e1000TCTL   = 0x00400 // Transmit Control
    e1000TDBAL  = 0x03800 // TX Descriptor Base Low
    e1000TDBAH  = 0x03804 // TX Descriptor Base High
    e1000TDLEN  = 0x03808 // TX Descriptor Length
    e1000TDH    = 0x03810 // TX Descriptor Head
    e1000TDT    = 0x03818 // TX Descriptor Tail

    // Receive address (MAC)
    e1000RAL0   = 0x05400 // Receive Address Low (MAC bytes 0-3)
    e1000RAH0   = 0x05404 // Receive Address High (MAC bytes 4-5 + flags)

    // Multicast Table Array (MTA) — 128 entries
    e1000MTA    = 0x05200
)
```

### 2.2 CTRL Register Bits

```go
const (
    e1000CTRLReset = 1 << 26 // RST — device reset
    e1000CTRLSLU   = 1 << 6  // Set Link Up
    e1000CTRLASDE  = 1 << 5  // Auto-Speed Detection Enable
)
```

### 2.3 STATUS Register Bits

```go
const (
    e1000StatusLU = 1 << 1  // Link Up
)
```

### 2.4 RCTL Register Bits

```go
const (
    e1000RCTLEN    = 1 << 1  // Receiver Enable
    e1000RCTLSBP   = 1 << 2  // Store Bad Packets (debug)
    e1000RCTLUPE   = 1 << 3  // Unicast Promiscuous Enable
    e1000RCTLMPE   = 1 << 4  // Multicast Promiscuous Enable
    e1000RCTLLPE   = 1 << 5  // Long Packet Reception Enable
    e1000RCTLBAM   = 1 << 15 // Broadcast Accept Mode
    e1000RCTLBSECRC = 1 << 26 // Strip Ethernet CRC
    // BSIZE: bits 17:16 (00=2048, 01=1024, 10=512, 11=256)
    e1000RCTLBSIZE2048 = 0 << 16
)
```

### 2.5 TCTL Register Bits

```go
const (
    e1000TCTLEN   = 1 << 1  // Transmit Enable
    e1000TCTLPSP  = 1 << 3  // Pad Short Packets
    e1000TCTLCT   = 0x10 << 4  // Collision Threshold (default 0x10)
    e1000TCTLCOLD = 0x40 << 12 // Collision Distance (full duplex)
)
```

### 2.6 ICR/IMS Interrupt Bits

```go
const (
    e1000ICRTXDW  = 1 << 0  // TX Descriptor Written Back
    e1000ICRTXQE  = 1 << 1  // TX Queue Empty
    e1000ICRLSC   = 1 << 2  // Link Status Change
    e1000ICRRXDMT0 = 1 << 4  // RX Descriptor Minimum Threshold
    e1000ICRRXT0  = 1 << 7  // RX Timer Interrupt
)
```

### 2.7 MMIO Read/Write Helpers

```go
var e1000Base uintptr // set during pciInit

func e1000Read(reg uint32) uint32 {
    return *(*uint32)(unsafe.Pointer(e1000Base + uintptr(reg)))
}

func e1000Write(reg uint32, val uint32) {
    *(*uint32)(unsafe.Pointer(e1000Base + uintptr(reg))) = val
}
```

### 2.8 LOC Estimate

| Item | LOC |
|---|---|
| Register constants | 50–70 |
| Bit flag constants | 20–30 |
| Read/write helpers | 10–15 |
| **Total** | **80–115** |

---

## 3. TX/RX Descriptor Rings (`src/e1000.go`)

### 3.1 Descriptor Layout

The e1000 legacy descriptor is 16 bytes:

**RX Descriptor (legacy):**
```
Offset  Size  Field
0       8     Buffer Address (physical)
8       2     Length
10      2     Checksum
12      1     Status
13      1     Errors
14      2     Special
```

**TX Descriptor (legacy):**
```
Offset  Size  Field
0       8     Buffer Address (physical)
8       2     Length
10      1     CSO (Checksum Offset)
11      1     CMD
12      1     Status (DD bit)
13      1     CSS (Checksum Start)
14      2     Special
```

### 3.2 Descriptor Implementation

To avoid TinyGo struct padding issues, use raw byte arrays
with manual field access:

```go
const (
    e1000RxDescSize = 16
    e1000TxDescSize = 16
    e1000NumRxDesc  = 64    // must be power of 2
    e1000NumTxDesc  = 32    // must be power of 2
    e1000BufSize    = 2048  // matches RCTL BSIZE
)

// Raw descriptor ring storage — contiguous physical pages.
// Allocated via allocPagesContig to guarantee physical
// contiguity and identity-mapped virtual addresses.
var (
    rxDescRing uintptr // physical addr of RX descriptor array
    txDescRing uintptr // physical addr of TX descriptor array
    rxBufs     uintptr // physical addr of RX buffer pool
    txBufs     uintptr // physical addr of TX buffer pool
    rxTail     uint32  // software RX tail tracker
    txTail     uint32  // software TX tail tracker
)
```

### 3.3 Descriptor Field Access

```go
// rxDescAddr returns the buffer-address field of RX descriptor i.
func rxDescAddr(i uint32) uint64 {
    p := rxDescRing + uintptr(i)*e1000RxDescSize
    return *(*uint64)(unsafe.Pointer(p))
}

// rxDescSetAddr sets the buffer-address field of RX descriptor i.
func rxDescSetAddr(i uint32, addr uint64) {
    p := rxDescRing + uintptr(i)*e1000RxDescSize
    *(*uint64)(unsafe.Pointer(p)) = addr
}

// rxDescStatus returns the status byte of RX descriptor i.
func rxDescStatus(i uint32) uint8 {
    p := rxDescRing + uintptr(i)*e1000RxDescSize + 12
    return *(*uint8)(unsafe.Pointer(p))
}

// rxDescLength returns the received packet length.
func rxDescLength(i uint32) uint16 {
    p := rxDescRing + uintptr(i)*e1000RxDescSize + 8
    return *(*uint16)(unsafe.Pointer(p))
}

// rxDescClear zeroes the status/errors fields (bytes 12-15).
func rxDescClear(i uint32) {
    p := rxDescRing + uintptr(i)*e1000RxDescSize + 12
    *(*uint32)(unsafe.Pointer(p)) = 0
}
```

Similar helpers for TX descriptors (set length, CMD, read
status DD bit).

### 3.4 Ring Allocation

```go
func e1000AllocRings() {
    // RX descriptor ring: 64 * 16 = 1024 bytes = 1 page
    rxDescRing = allocPagesContig(1)

    // TX descriptor ring: 32 * 16 = 512 bytes = 1 page
    txDescRing = allocPagesContig(1)

    // RX buffers: 64 * 2048 = 131072 bytes = 32 pages
    rxBufs = allocPagesContig(32)

    // TX buffers: 32 * 2048 = 65536 bytes = 16 pages
    txBufs = allocPagesContig(16)
}
```

### 3.5 Ring Initialization

For each RX descriptor: set buffer address, clear status.
Set RDBAL/RDBAH to ring physical address, RDLEN to ring size,
RDH=0, RDT=NumRxDesc-1.

For TX: set TDBAL/TDBAH, TDLEN, TDH=0, TDT=0 (empty ring).

---

## 4. e1000 Initialization Sequence

### 4.1 Reset

```go
func e1000Init() {
    // 1. Reset the device
    e1000Write(e1000CTRL, e1000Read(e1000CTRL) | e1000CTRLReset)
    // Wait ~1 ms for reset to complete (spin on PIT ticks)
    resetStart := pitTicks
    for pitTicks - resetStart < 2 { /* ~20ms at 100Hz */ }

    // 2. Disable interrupts during init
    e1000Write(e1000IMC, 0xFFFFFFFF)

    // 3. Read MAC address from RAL0/RAH0
    e1000ReadMAC()

    // 4. Allocate and init descriptor rings
    e1000AllocRings()
    e1000InitRxRing()
    e1000InitTxRing()

    // 5. Configure receive control
    e1000Write(e1000RCTL,
        e1000RCTLEN | e1000RCTLBAM | e1000RCTLBSECRC |
        e1000RCTLBSIZE2048)

    // 6. Configure transmit control
    e1000Write(e1000TCTL,
        e1000TCTLEN | e1000TCTLPSP |
        e1000TCTLCT | e1000TCTLCOLD)

    // 7. Set link up
    e1000Write(e1000CTRL,
        e1000Read(e1000CTRL) | e1000CTRLSLU | e1000CTRLASDE)

    // 8. Clear MTA (multicast table)
    for i := uint32(0); i < 128; i++ {
        e1000Write(e1000MTA + i*4, 0)
    }

    // 9. Enable interrupts
    e1000Write(e1000IMS,
        e1000ICRRXT0 | e1000ICRLSC)

    // 10. Wait for link up
    e1000WaitLinkUp()
}
```

### 4.2 MAC Address Read

```go
var e1000MAC [6]byte

func e1000ReadMAC() {
    ral := e1000Read(e1000RAL0)
    rah := e1000Read(e1000RAH0)
    e1000MAC[0] = byte(ral)
    e1000MAC[1] = byte(ral >> 8)
    e1000MAC[2] = byte(ral >> 16)
    e1000MAC[3] = byte(ral >> 24)
    e1000MAC[4] = byte(rah)
    e1000MAC[5] = byte(rah >> 8)
}
```

### 4.3 Link-Up Wait

```go
func e1000WaitLinkUp() {
    start := pitTicks
    for {
        if e1000Read(e1000STATUS) & e1000StatusLU != 0 {
            serialPrintln("e1000: link up")
            return
        }
        if pitTicks - start > 500 { // 5 second timeout
            serialPrintln("e1000: link up timeout")
            return
        }
        hlt() // wait for next tick
    }
}
```

---

## 5. Transmit Path

### 5.1 `e1000Transmit(frame []byte) bool`

```
1. Check frame length (14 ≤ len ≤ 1518)
2. Read current txTail
3. Copy frame data into TX buffer at txBufs + txTail * e1000BufSize
4. Set TX descriptor: buffer addr, length, CMD (EOP | IFCS | RS)
5. Advance txTail = (txTail + 1) % e1000NumTxDesc
6. Write txTail to TDT register
7. Optionally poll for DD (transmit complete) — or let it be async
```

**CMD byte flags:**
```go
const (
    e1000TxCmdEOP  = 1 << 0 // End of Packet
    e1000TxCmdIFCS = 1 << 1 // Insert FCS (CRC)
    e1000TxCmdRS   = 1 << 3 // Report Status (sets DD)
)
```

### 5.2 TX Completion

For simplicity, poll the DD (Descriptor Done) bit in the
status byte of the descriptor:

```go
func e1000TxDone(i uint32) bool {
    p := txDescRing + uintptr(i)*e1000TxDescSize + 12
    return *(*uint8)(unsafe.Pointer(p)) & 1 != 0
}
```

In Phase 4, TX completion is driven by TXDW interrupt.

---

## 6. Receive Path

### 6.1 Polling Mode (Phase 1)

```
1. Check rxTail: next = (rxTail + 1) % e1000NumRxDesc
2. Read status byte of descriptor at next
3. If DD bit set:
   a. Read packet length from descriptor
   b. Copy data from rxBufs + next * e1000BufSize
   c. Clear descriptor status
   d. Re-arm: set buffer address back
   e. rxTail = next
   f. Write rxTail to RDT register
   g. Process packet (pass to Ethernet layer)
4. If DD not set: no packet available
```

### 6.2 RX Dispatch Goroutine

```go
var rxPacketCh chan []byte

func e1000RxPollLoop() {
    rxPacketCh = make(chan []byte, 16)
    for {
        frame := e1000TryReceive()
        if frame != nil {
            rxPacketCh <- frame
        } else {
            hlt() // sleep until next interrupt
        }
    }
}
```

In Phase 4, `hlt()` is replaced by channel wait on an
interrupt signal.

---

## 7. IRQ Handler (`src/e1000_irq.go`)

### 7.1 Handler Registration

The e1000's PCI Interrupt Line register reports the legacy
IRQ number (typically 11 on QEMU). The vector is
`picMasterOffset + irqLine` (32 + 11 = 43) when using PIC
pass-through.

```go
func e1000RegisterIRQ() {
    irq := e1000PCI.IRQLine
    vector := int(32 + irq)
    registerHandler(vector, handleE1000IRQ)
    serialPrint("e1000: IRQ registered at vector ")
    serialPrintln(utoa(uint64(vector)))
}
```

### 7.2 IRQ Handler

```go
//go:nosplit
func handleE1000IRQ(vector uint64) {
    // Read and acknowledge interrupt cause
    icr := e1000Read(e1000ICR)

    if icr & e1000ICRRXT0 != 0 {
        // Signal RX goroutine (non-blocking send)
        select {
        case rxSignalCh <- struct{}{}:
        default:
        }
    }

    if icr & e1000ICRLSC != 0 {
        // Link status change — log it
        status := e1000Read(e1000STATUS)
        if status & e1000StatusLU != 0 {
            serialPrintln("e1000: link up")
        } else {
            serialPrintln("e1000: link down")
        }
    }

    // Send EOI
    if ioapicActive {
        lapicSendEOI()
    } else {
        picSendEOI(uint8(vector - 32))
    }
}
```

### 7.3 ISR Safety

The handler is `//go:nosplit` to avoid stack growth checks.
It does NOT allocate memory — the `select` on a buffered
channel is allocation-free in TinyGo. Work is deferred to
the RX goroutine.

### 7.4 IRQ 11 and PIC

QEMU's e1000 typically reports IRQ 11. On the PIC, IRQ 11
is on the slave PIC (IRQ 8-15 → vectors 40-47), so vector
= 40 + (11 - 8) = 43. The `handleDefaultIRQ` registered for
vectors 32-47 in `main.go:145-147` already sends EOI for
unhandled IRQs, so the specific handler must be registered
before the first interrupt fires.

**Important**: In the PIC remap (`src/pic.go`), slave IRQs
8-15 map to vectors 40-47. So IRQ 11 → vector 43. The
handler must replace the default handler at vector 43.

---

## 8. Boot Integration (`src/main.go` Changes)

### 8.1 Insertion Point

After SMP init and before userspace setup, add:

```go
// PCI bus scan and e1000 NIC init.
pciInit()
if e1000Found {
    e1000Init()
    go e1000RxPollLoop()
    serialPrintln("e1000: NIC initialized")
} else {
    serialPrintln("e1000: not found on PCI bus")
}
```

### 8.2 Makefile `run-net` Target

```makefile
run-net: $(KERNEL_ISO) check-multiboot
    $(QEMU) -cdrom $(KERNEL_ISO) -serial stdio -no-reboot -no-shutdown \
        -device e1000,netdev=n0 \
        -netdev user,id=n0
```

---

## 9. Risk Mitigations Specific to Phase 1

| Risk | Mitigation |
|---|---|
| TinyGo struct padding | Raw `[16]byte` arrays, manual pack/unpack. Verify `unsafe.Sizeof` at init. |
| DMA address above 4 GiB | `allocPagesContig` returns within identity map (< 1 GiB). Assert at runtime. |
| QEMU e1000 variant mismatch | Pin to device ID 0x100E (82540EM). Log vendor/device if mismatch. |
| Bus-master not enabled | Explicitly set PCI Command bit 2. Log Command register after write. |
| IRQ vector collision | Replace default handler for the specific vector. Log replacement. |
| IOAPIC disabled | Use PIC pass-through (current default). Phase 4 adds IOAPIC option. |

---

## 10. Verification Criteria

1. **PCI scan**: Serial log shows `PCI: found e1000 at
   XX:YY.Z BAR0=0x... IRQ=N`.
2. **MMIO mapping**: `e1000Read(e1000STATUS)` returns non-zero
   value (link status bits).
3. **MAC address**: Serial log shows `e1000: MAC=XX:XX:XX:XX:XX:XX`.
4. **Link up**: Serial log shows `e1000: link up` within 5 seconds.
5. **TX test**: Craft a broadcast Ethernet frame (FF:FF:FF:FF:FF:FF),
   transmit via `e1000Transmit`. Capture with
   `-netdev user,id=n0,dump=file:tx.pcap`. Open in Wireshark and
   verify frame contents.
6. **RX test**: QEMU user-mode networking sends periodic ARP
   requests. `e1000TryReceive` returns non-nil. Log first 32
   bytes to serial in hex.
7. **IRQ test**: After enabling IMS with RXT0, receive generates
   an interrupt. Serial log shows IRQ handler entry.
