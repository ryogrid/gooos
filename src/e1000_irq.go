// src/e1000_irq.go -- Interrupt handler for the e1000 NIC.
//
// The handler runs in ISR context (//go:nosplit, no allocation). It
// reads ICR (clear-on-read), sets a plain memory flag to signal the
// RX goroutine, logs link-status-change transitions, and
// acknowledges the interrupt at the PIC (or LAPIC when the IOAPIC
// is active).
//
// Why a memory flag instead of a channel: waking a parked receiver
// on a Go channel from ISR context is unsafe under gooos's
// cooperative TinyGo scheduler — the wake touches scheduler
// runqueue state that is not ISR-safe. The keyboard driver
// (src/keyboard_irq.go) avoids the same trap with its own ring
// buffer + pump-goroutine pattern. Here we use an even simpler
// flag: the ISR sets it, netRxLoop polls it at its Gosched /
// sti+hlt cadence and drains the RX ring whenever set.

package main

// e1000IRQCount is incremented on every e1000 ISR entry. Plain
// uint64 (no lock) — single writer (the ISR), diagnostic read only.
// Exposed by netDiag to confirm IRQ delivery.
var e1000IRQCount uint64

// rxReadyFlag is a single-bit signal the ISR sets on any RX-causing
// interrupt (e1000ICRRXT0). netRxLoop polls this flag at its idle
// cadence; after draining the RX ring it clears the flag. Plain
// uint32 (no atomics / lock) is safe on BSP-only single-core
// gooos: writer is the ISR, reader is the single netRxLoop
// goroutine, x86-TSO guarantees visibility via plain mov.
var rxReadyFlag uint32

// lastICR captures the most recent ICR value read by the ISR, so
// that non-ISR code (netDiag) can display what interrupt sources
// are actually firing.
var lastICR uint32

// handleE1000IRQ is the e1000 ISR. Registered at vector
// `32 + e1000PCI.IRQLine` in main.go after e1000Init.
//
//go:nosplit
func handleE1000IRQ(vector uint64) {
	e1000IRQCount++

	// DIAG — remove once RX path is stable again.
	if e1000IRQCount <= 20 {
		serialPrintln("e1000 IRQ fired")
	}

	// ICR is clear-on-read: reading it both tells us what happened
	// and acknowledges the causes so the NIC can re-assert on the
	// next event.
	icr := e1000Read(e1000ICR)
	lastICR = icr

	// Always set rxReadyFlag — even if RXT0 bit isn't set, a poll
	// is cheap. Previously we gated on RXT0, but that might miss
	// RX events that use a different cause bit.
	rxReadyFlag = 1
	_ = icr & e1000ICRRXT0 // keep for future per-bit handling

	if icr&e1000ICRLSC != 0 {
		status := e1000Read(e1000STATUS)
		if status&e1000StatusLU != 0 {
			serialPrintln("e1000: link up (IRQ)")
		} else {
			serialPrintln("e1000: link down (IRQ)")
		}
	}

	// Acknowledge at the interrupt controller.
	if ioapicActive {
		lapicSendEOI()
	} else {
		picSendEOI(uint8(vector - 32))
	}
}
