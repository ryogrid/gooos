// src/idt.go -- 256-entry Interrupt Descriptor Table for x86_64 long mode.
//
// Defines the IDT entry structure (16 bytes per gate descriptor), a
// 256-entry table, and initialization logic that populates every entry
// with a default handler and loads the table via lidt.
//
// Interrupts are NOT enabled here (no sti). ISR stubs and PIC remap
// come in subsequent milestones.

package main

import (
	"unsafe"
)

// IDTEntry is a 64-bit mode gate descriptor (16 bytes).
//
// Layout (per Intel SDM Vol. 3A, Section 6.14.1):
//   Bytes 0-1:   Offset [15:0]
//   Bytes 2-3:   Segment selector
//   Byte  4:     IST index (bits 0-2), reserved (bits 3-7)
//   Byte  5:     Type (bits 0-3), zero (bit 4), DPL (bits 5-6), Present (bit 7)
//   Bytes 6-7:   Offset [31:16]
//   Bytes 8-11:  Offset [63:32]
//   Bytes 12-15: Reserved (must be zero)
type IDTEntry struct {
	OffsetLow  uint16
	Selector   uint16
	IST        uint8
	TypeAttr   uint8
	OffsetMid  uint16
	OffsetHigh uint32
	Reserved   uint32
}

const (
	idtEntries    = 256
	kernelCS      = 0x08 // GDT64_CODE selector (second GDT entry)
	gateInterrupt = 0x8E // Present=1 | DPL=0 | Type=0xE (64-bit interrupt gate)
)

var (
	idtTable [idtEntries]IDTEntry
	idtDesc  [10]byte // packed descriptor for lidt: 2-byte limit + 8-byte base
)

// setGate configures an IDT entry as an interrupt gate pointing to handler.
func setGate(vector int, handler uintptr) {
	idtTable[vector].OffsetLow = uint16(handler)
	idtTable[vector].Selector = kernelCS
	idtTable[vector].IST = 0
	idtTable[vector].TypeAttr = gateInterrupt
	idtTable[vector].OffsetMid = uint16(handler >> 16)
	idtTable[vector].OffsetHigh = uint32(handler >> 32)
	idtTable[vector].Reserved = 0
}

// idtInit populates all 256 IDT entries with a default handler and loads
// the IDT via the lidt instruction.
func idtInit() {
	handler := defaultHandlerAddr()

	for i := 0; i < idtEntries; i++ {
		setGate(i, handler)
	}

	// Pack the IDT descriptor (limit + base) into a 10-byte array.
	// lidt expects: uint16 limit at offset 0, uint64 base at offset 2.
	limit := uint16(unsafe.Sizeof(idtTable) - 1)
	base := uint64(uintptr(unsafe.Pointer(&idtTable[0])))

	idtDesc[0] = byte(limit)
	idtDesc[1] = byte(limit >> 8)
	idtDesc[2] = byte(base)
	idtDesc[3] = byte(base >> 8)
	idtDesc[4] = byte(base >> 16)
	idtDesc[5] = byte(base >> 24)
	idtDesc[6] = byte(base >> 32)
	idtDesc[7] = byte(base >> 40)
	idtDesc[8] = byte(base >> 48)
	idtDesc[9] = byte(base >> 56)

	lidt(uintptr(unsafe.Pointer(&idtDesc[0])))
}

// defaultHandlerAddr returns the address of the default interrupt handler
// stub in assembly. Implemented in stubs.S.
//
//go:linkname defaultHandlerAddr defaultHandlerAddr
func defaultHandlerAddr() uintptr

// lidt loads the IDT register from the descriptor at the given address.
// Implemented in stubs.S.
//
//go:linkname lidt lidt
func lidt(desc uintptr)
