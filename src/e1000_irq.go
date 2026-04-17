// src/e1000_irq.go -- Interrupt handler for the e1000 NIC.
//
// The handler runs in ISR context (//go:nosplit, no allocation). It
// reads ICR (clear-on-read), signals the RX goroutine via a buffered
// channel, logs link-status-change transitions, and acknowledges the
// interrupt at the PIC (or LAPIC when the IOAPIC is active).
//
// The deferred RX work — draining the descriptor ring and feeding the
// Ethernet dispatcher — runs on the goroutine that waits on
// rxSignalCh. See `netRxLoop` in src/net.go (Phase 2+).

package main

// rxSignalCh is the ISR → RX-goroutine wake-up pipe. Buffered cap=4
// so a burst of IRQs is coalesced rather than lost; the goroutine
// drains the whole descriptor ring on each wake, so at most one
// pending signal is ever useful.
var rxSignalCh = make(chan struct{}, 4)

// handleE1000IRQ is the e1000 ISR. Registered at vector
// `32 + e1000PCI.IRQLine` in main.go after e1000Init.
//
//go:nosplit
func handleE1000IRQ(vector uint64) {
	// ICR is clear-on-read: reading it both tells us what happened
	// and acknowledges the causes so the NIC can re-assert on the
	// next event.
	icr := e1000Read(e1000ICR)

	if icr&e1000ICRRXT0 != 0 {
		// Non-blocking signal — drop the wake-up if the goroutine
		// has not yet consumed the previous one. The channel buffer
		// absorbs small bursts, and a single wake-up causes the
		// goroutine to drain all ready descriptors anyway.
		select {
		case rxSignalCh <- struct{}{}:
		default:
		}
	}

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
