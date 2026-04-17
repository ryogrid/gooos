// src/e1000.go -- Intel 82540EM (e1000) NIC driver.
//
// Implements the minimum set of features needed for UDP/IP over a single
// emulated e1000 NIC under QEMU:
//   * BAR0 MMIO mapping (identity-mapped, PCD+PWT)
//   * Device reset + basic configuration
//   * MAC address read from RAL0/RAH0
//   * Legacy RX/TX descriptor rings (64/32 entries, 2048-byte buffers)
//   * Bus-master DMA via physically-contiguous buffer pools
//   * Polled RX (Phase 1-3); Phase 4 rewires to interrupt-driven
//
// Design: impldoc/net_pci_e1000_driver.md.
//
// Descriptor layout: we deliberately use raw [16]byte slots accessed via
// unsafe.Pointer field offsets instead of Go structs, because TinyGo does
// not guarantee the 16-byte no-padding layout the NIC hardware requires
// for descriptor DMA (see D3 / R1 in the design doc).

package main

import "unsafe"

// ---------------------------------------------------------------------
// Register offsets (relative to BAR0 MMIO base)
// ---------------------------------------------------------------------

const (
	e1000CTRL   = uint32(0x00000) // Device Control
	e1000STATUS = uint32(0x00008) // Device Status

	e1000ICR = uint32(0x000C0) // Interrupt Cause Read (read-to-clear)
	e1000ICS = uint32(0x000C8) // Interrupt Cause Set
	e1000IMS = uint32(0x000D0) // Interrupt Mask Set
	e1000IMC = uint32(0x000D8) // Interrupt Mask Clear

	e1000RCTL  = uint32(0x00100) // Receive Control
	e1000RDBAL = uint32(0x02800) // RX Descriptor Base Low
	e1000RDBAH = uint32(0x02804) // RX Descriptor Base High
	e1000RDLEN = uint32(0x02808) // RX Descriptor Length
	e1000RDH   = uint32(0x02810) // RX Descriptor Head
	e1000RDT   = uint32(0x02818) // RX Descriptor Tail

	e1000TCTL  = uint32(0x00400) // Transmit Control
	e1000TDBAL = uint32(0x03800) // TX Descriptor Base Low
	e1000TDBAH = uint32(0x03804) // TX Descriptor Base High
	e1000TDLEN = uint32(0x03808) // TX Descriptor Length
	e1000TDH   = uint32(0x03810) // TX Descriptor Head
	e1000TDT   = uint32(0x03818) // TX Descriptor Tail

	e1000RAL0 = uint32(0x05400) // Receive Address Low (MAC[0..3])
	e1000RAH0 = uint32(0x05404) // Receive Address High (MAC[4..5] + AV)

	e1000MTA = uint32(0x05200) // Multicast Table Array base (128 entries)
)

// CTRL bits
const (
	e1000CTRLReset = uint32(1 << 26) // RST
	e1000CTRLSLU   = uint32(1 << 6)  // Set Link Up
	e1000CTRLASDE  = uint32(1 << 5)  // Auto-Speed Detection Enable
)

// STATUS bits
const (
	e1000StatusLU = uint32(1 << 1) // Link Up
)

// RCTL bits
const (
	e1000RCTLEN       = uint32(1 << 1)  // Receiver Enable
	e1000RCTLBAM      = uint32(1 << 15) // Broadcast Accept Mode
	e1000RCTLBSECRC   = uint32(1 << 26) // Strip Ethernet CRC
	e1000RCTLBSIZE2048 = uint32(0 << 16) // BSIZE = 2048 when BSEX=0
)

// TCTL bits
const (
	e1000TCTLEN   = uint32(1 << 1)     // Transmit Enable
	e1000TCTLPSP  = uint32(1 << 3)     // Pad Short Packets
	e1000TCTLCT   = uint32(0x10 << 4)  // Collision Threshold
	e1000TCTLCOLD = uint32(0x40 << 12) // Collision Distance (half-duplex)
)

// ICR / IMS bits
const (
	e1000ICRTXDW   = uint32(1 << 0) // TX Descriptor Written Back
	e1000ICRTXQE   = uint32(1 << 1) // TX Queue Empty
	e1000ICRLSC    = uint32(1 << 2) // Link Status Change
	e1000ICRRXDMT0 = uint32(1 << 4) // RX Descriptor Minimum Threshold
	e1000ICRRXT0   = uint32(1 << 7) // RX Timer Interrupt
)

// TX descriptor CMD byte bits
const (
	e1000TxCmdEOP  = uint8(1 << 0) // End of Packet
	e1000TxCmdIFCS = uint8(1 << 1) // Insert FCS
	e1000TxCmdRS   = uint8(1 << 3) // Report Status (sets DD on completion)
)

// Descriptor / ring sizing
const (
	e1000RxDescSize = uintptr(16)
	e1000TxDescSize = uintptr(16)
	e1000NumRxDesc  = uint32(64) // power of two
	e1000NumTxDesc  = uint32(32) // power of two
	e1000BufSize    = uintptr(2048)
	e1000MmioSize   = uintptr(128 * 1024) // 0x20000
)

// ---------------------------------------------------------------------
// State
// ---------------------------------------------------------------------

var (
	// e1000Base is the identity-mapped MMIO virtual address of BAR0.
	// Equal to the physical BAR0 base for our identity-map discipline.
	e1000Base uintptr

	// Descriptor ring and buffer pool physical addresses (also used
	// directly as virtual addresses thanks to the boot identity map,
	// since allocPagesContig returns pages < 0x40000000).
	rxDescRing uintptr
	txDescRing uintptr
	rxBufs     uintptr
	txBufs     uintptr

	// Software tail trackers (mirror the RDT/TDT registers).
	rxTail uint32
	txTail uint32

	// MAC address read from RAL0/RAH0.
	e1000MAC [6]byte

	// rxPacketCh delivers received frames to the RX dispatch goroutine.
	// Phase 2+ (net.go netRxLoop) drains this channel; Phase 1 may leave
	// it unread — the buffered channel absorbs spurious RX packets.
	rxPacketCh chan []byte
)

// ---------------------------------------------------------------------
// MMIO helpers
// ---------------------------------------------------------------------

//go:nosplit
func e1000Read(reg uint32) uint32 {
	return *(*uint32)(unsafe.Pointer(e1000Base + uintptr(reg)))
}

//go:nosplit
func e1000Write(reg uint32, val uint32) {
	*(*uint32)(unsafe.Pointer(e1000Base + uintptr(reg))) = val
}

// ---------------------------------------------------------------------
// Descriptor field accessors (manual byte-level layout)
// ---------------------------------------------------------------------

//go:nosplit
func rxDescSetAddr(i uint32, addr uint64) {
	p := rxDescRing + uintptr(i)*e1000RxDescSize
	*(*uint64)(unsafe.Pointer(p)) = addr
}

//go:nosplit
func rxDescStatus(i uint32) uint8 {
	p := rxDescRing + uintptr(i)*e1000RxDescSize + 12
	return *(*uint8)(unsafe.Pointer(p))
}

//go:nosplit
func rxDescLength(i uint32) uint16 {
	p := rxDescRing + uintptr(i)*e1000RxDescSize + 8
	return *(*uint16)(unsafe.Pointer(p))
}

//go:nosplit
func rxDescClear(i uint32) {
	// Zero status + errors + special (bytes 12-15).
	p := rxDescRing + uintptr(i)*e1000RxDescSize + 12
	*(*uint32)(unsafe.Pointer(p)) = 0
}

//go:nosplit
func txDescSetAddr(i uint32, addr uint64) {
	p := txDescRing + uintptr(i)*e1000TxDescSize
	*(*uint64)(unsafe.Pointer(p)) = addr
}

//go:nosplit
func txDescSetLength(i uint32, length uint16) {
	p := txDescRing + uintptr(i)*e1000TxDescSize + 8
	*(*uint16)(unsafe.Pointer(p)) = length
}

//go:nosplit
func txDescSetCMD(i uint32, cmd uint8) {
	p := txDescRing + uintptr(i)*e1000TxDescSize + 11
	*(*uint8)(unsafe.Pointer(p)) = cmd
}

//go:nosplit
func txDescStatus(i uint32) uint8 {
	p := txDescRing + uintptr(i)*e1000TxDescSize + 12
	return *(*uint8)(unsafe.Pointer(p))
}

//go:nosplit
func txDescClear(i uint32) {
	// Zero bytes 8-15 (length/CSO/CMD/status/CSS/special).
	p := txDescRing + uintptr(i)*e1000TxDescSize + 8
	*(*uint64)(unsafe.Pointer(p)) = 0
}

// ---------------------------------------------------------------------
// MMIO mapping and ring allocation
// ---------------------------------------------------------------------

// e1000MapMMIO identity-maps the BAR0 region with PCD+PWT so CPU reads
// and writes bypass the data cache (required for device registers).
func e1000MapMMIO(bar0 uint32) {
	base := uintptr(bar0)
	flags := uintptr(pagePresent | pageWrite | pagePCD | pagePWT)
	for off := uintptr(0); off < e1000MmioSize; off += pageSize {
		mapPage(base+off, base+off, flags)
	}
	e1000Base = base
}

// e1000AllocRings reserves physically-contiguous pages for the descriptor
// rings and their backing buffer pools. Asserts the resulting physical
// addresses lie inside the boot identity map (< 1 GiB) so we can treat
// the returned uintptr as both physical and virtual.
func e1000AllocRings() {
	rxDescRing = allocPagesContig(1)                              // 64 × 16 = 1024 bytes
	txDescRing = allocPagesContig(1)                              // 32 × 16 = 512 bytes
	rxBufs = allocPagesContig(32)                                 // 64 × 2048 = 128 KiB
	txBufs = allocPagesContig(int(e1000NumTxDesc) * 2048 / pageSize) // 32 × 2048 = 64 KiB

	const identityLimit = uintptr(0x40000000)
	if rxDescRing >= identityLimit || txDescRing >= identityLimit ||
		rxBufs >= identityLimit || txBufs >= identityLimit {
		serialPrintln("e1000: DMA buffer above 1 GiB identity map — unsupported")
		for {
			hlt()
		}
	}
}

// e1000InitRxRing wires each RX descriptor to its backing buffer and
// programs the hardware RX queue registers.
func e1000InitRxRing() {
	for i := uint32(0); i < e1000NumRxDesc; i++ {
		buf := rxBufs + uintptr(i)*e1000BufSize
		rxDescSetAddr(i, uint64(buf))
		rxDescClear(i)
	}

	e1000Write(e1000RDBAL, uint32(rxDescRing))
	e1000Write(e1000RDBAH, uint32(uint64(rxDescRing)>>32))
	e1000Write(e1000RDLEN, e1000NumRxDesc*uint32(e1000RxDescSize))
	e1000Write(e1000RDH, 0)
	e1000Write(e1000RDT, e1000NumRxDesc-1)
	rxTail = e1000NumRxDesc - 1
}

// e1000InitTxRing programs an empty TX queue (head = tail = 0).
func e1000InitTxRing() {
	for i := uint32(0); i < e1000NumTxDesc; i++ {
		txDescSetAddr(i, 0)
		txDescClear(i)
	}

	e1000Write(e1000TDBAL, uint32(txDescRing))
	e1000Write(e1000TDBAH, uint32(uint64(txDescRing)>>32))
	e1000Write(e1000TDLEN, e1000NumTxDesc*uint32(e1000TxDescSize))
	e1000Write(e1000TDH, 0)
	e1000Write(e1000TDT, 0)
	txTail = 0
}

// ---------------------------------------------------------------------
// Init sequence
// ---------------------------------------------------------------------

// e1000ReadMAC pulls the station MAC out of RAL0/RAH0 (QEMU fills
// these from the NIC command-line MAC option; EEPROM read is optional).
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

// e1000WaitLinkUp polls STATUS.LU for up to 5 seconds at 100 Hz PIT.
// Returns whether link came up. In QEMU with `-netdev user` the link
// is up as soon as the NIC exits reset; this is a safety net.
func e1000WaitLinkUp() bool {
	const timeoutTicks = uint64(500)
	start := pitTicks
	for {
		if e1000Read(e1000STATUS)&e1000StatusLU != 0 {
			serialPrintln("e1000: link up")
			return true
		}
		if pitTicks-start > timeoutTicks {
			serialPrintln("e1000: link up timeout (continuing anyway)")
			return false
		}
		hlt()
	}
}

// e1000Init brings the NIC from reset to a configured, link-up state.
// Must be called after pciInit has populated e1000PCI and before any
// IRQ handler runs. Callers must guard with `if e1000Found`.
func e1000Init() {
	// Channel exists before the poll-loop goroutine starts so early
	// frames have somewhere to land.
	rxPacketCh = make(chan []byte, 16)

	e1000MapMMIO(e1000PCI.BAR0)
	e1000AllocRings()

	// 1) Reset the device. RST bit is self-clearing; wait 2 PIT ticks.
	e1000Write(e1000CTRL, e1000Read(e1000CTRL)|e1000CTRLReset)
	resetStart := pitTicks
	for pitTicks-resetStart < 2 {
		hlt()
	}

	// 2) Mask all interrupts while we configure.
	e1000Write(e1000IMC, 0xFFFFFFFF)

	// 3) MAC address.
	e1000ReadMAC()
	serialPrintln("e1000: MAC=" + macToString(e1000MAC))

	// 4) Descriptor rings.
	e1000InitRxRing()
	e1000InitTxRing()

	// 5) Receive control: enable, broadcast accept, strip CRC, 2 KiB buffers.
	e1000Write(e1000RCTL,
		e1000RCTLEN|e1000RCTLBAM|e1000RCTLBSECRC|e1000RCTLBSIZE2048)

	// 6) Transmit control: enable, pad shorts, standard collision tuning.
	e1000Write(e1000TCTL,
		e1000TCTLEN|e1000TCTLPSP|e1000TCTLCT|e1000TCTLCOLD)

	// 7) Request link up.
	e1000Write(e1000CTRL,
		e1000Read(e1000CTRL)|e1000CTRLSLU|e1000CTRLASDE)

	// 8) Zero the Multicast Table Array (128 × 4 bytes).
	for i := uint32(0); i < 128; i++ {
		e1000Write(e1000MTA+i*4, 0)
	}

	// 9) Unmask RX-timer + link-status-change interrupts.
	e1000Write(e1000IMS, e1000ICRRXT0|e1000ICRLSC)

	// 10) Wait for link.
	e1000WaitLinkUp()
}

// ---------------------------------------------------------------------
// Transmit
// ---------------------------------------------------------------------

// e1000Transmit copies a frame into the next TX buffer, arms its
// descriptor, and advances TDT. Returns false if the frame is the wrong
// size (14 ≤ len ≤ 1518) or if the next descriptor is still busy.
//
// Frame length excludes the FCS (CRC-32) — the NIC inserts it when
// TxCmdIFCS is set on the descriptor.
func e1000Transmit(frame []byte) bool {
	n := len(frame)
	if n < 14 || n > 1518 {
		return false
	}
	i := txTail

	// If the descriptor hasn't been acknowledged by hardware yet, drop.
	// (A more sophisticated driver would block; we keep this simple.)
	// Non-zero buffer addr + DD not set means a prior frame is still in
	// flight on this slot. Fresh (addr==0) descriptors are fine.
	prevAddr := *(*uint64)(unsafe.Pointer(txDescRing + uintptr(i)*e1000TxDescSize))
	if prevAddr != 0 && txDescStatus(i)&1 == 0 {
		return false
	}

	// Copy payload into the TX buffer owned by this descriptor.
	dst := txBufs + uintptr(i)*e1000BufSize
	for k := 0; k < n; k++ {
		*(*uint8)(unsafe.Pointer(dst + uintptr(k))) = frame[k]
	}

	txDescClear(i)
	txDescSetAddr(i, uint64(dst))
	txDescSetLength(i, uint16(n))
	txDescSetCMD(i, e1000TxCmdEOP|e1000TxCmdIFCS|e1000TxCmdRS)

	txTail = (txTail + 1) % e1000NumTxDesc
	e1000Write(e1000TDT, txTail)

	statsInc(&netStats.TxPackets)
	statsAdd(&netStats.TxBytes, uint64(n))
	return true
}

// ---------------------------------------------------------------------
// Receive
// ---------------------------------------------------------------------

// e1000TryReceive drains one completed RX descriptor, if any, and
// returns a freshly-allocated slice holding the frame bytes. Returns
// nil when the ring has no DD-marked descriptor to consume.
//
// The allocation is acceptable for Phases 1–3; Phase 4's netbuf pool
// replaces it with a fixed DMA-safe pool.
func e1000TryReceive() []byte {
	// Next descriptor hardware could have written = (rxTail + 1) % N.
	next := (rxTail + 1) % e1000NumRxDesc

	// DD bit in the status byte indicates the NIC has finished writing.
	if rxDescStatus(next)&1 == 0 {
		return nil
	}

	length := rxDescLength(next)
	src := rxBufs + uintptr(next)*e1000BufSize
	out := make([]byte, length)
	for k := uint16(0); k < length; k++ {
		out[k] = *(*uint8)(unsafe.Pointer(src + uintptr(k)))
	}

	// Re-arm the descriptor for future packets.
	rxDescClear(next)
	rxDescSetAddr(next, uint64(src))

	rxTail = next
	e1000Write(e1000RDT, rxTail)
	return out
}
