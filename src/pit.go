// src/pit.go -- PIT (Programmable Interval Timer) driver.
//
// Programs PIT channel 0 to fire IRQ0 at ~100 Hz (10 ms interval).
// Provides a global tick counter incremented by the IRQ0 handler.

package main

// PIT I/O ports.
const (
	pitCh0Data = 0x40 // Channel 0 data port
	pitCmd     = 0x43 // Mode/Command register
)

// PIT constants.
const (
	pitFreq    = 1193182 // PIT oscillator frequency in Hz
	pitTargetHz = 100    // Desired interrupt frequency
	pitDivisor = pitFreq / pitTargetHz // ~11932 (0x2E9C)
)

// pitTicks is the global tick counter, incremented by the IRQ0 handler.
var pitTicks uint64

// pitInit programs PIT channel 0 in rate generator mode (mode 2)
// with a divisor that produces ~100 Hz interrupts.
func pitInit() {
	// Command byte: channel 0, lobyte/hibyte access, rate generator (mode 2), binary
	outb(pitCmd, 0x34)

	// Send divisor as low byte then high byte.
	outb(pitCh0Data, uint8(pitDivisor&0xFF))
	outb(pitCh0Data, uint8((pitDivisor>>8)&0xFF))
}

// handleTimer is the IRQ0 handler (vector 32). Under Phase B the
// timer no longer drives the hand-written scheduler; kernel
// goroutines yield cooperatively via Gosched / channel ops, and
// Ring-3 preemption happens naturally through iretq return paths.
//
// Under -smp > 1 it additionally broadcasts a wakeup IPI to every
// online AP. Reason: PIC-pass-through routes external IRQs (incl.
// IRQ1 keyboard) to the BSP only, and APs have LVT0 masked. Without
// an explicit wakeup signal, a kernel goroutine like keyboardPump
// parked on sti+hlt on an AP waits for the next preempt-IPI
// broadcast from handleLAPICTimer (~10 ms) which is sufficient in
// theory but empirically too unreliable for interactive typing.
// Broadcasting from this handler — every PIT tick, 100 Hz — gives
// APs a guaranteed 10 ms wake cadence and restores -smp 1 parity
// for keyboard latency. schedulerWake is a no-op in -smp 1 since it
// self-skips; cost is one LAPIC ICR write per AP per tick.
//
//go:nosplit
func handleTimer(vector uint64) {
	pitTicks++
	if ioapicActive {
		lapicSendEOI()
	} else {
		picSendEOI(0)
	}
	if numCoresOnline > 1 {
		pitWakeAPs()
	}
}

// pitWakeAPs broadcasts a wakeup IPI (vector 0xFC) to every online
// AP. Called from handleTimer 100 times per second. Split from the
// hot ISR path so the nosplit body stays minimal.
//
//go:nosplit
func pitWakeAPs() {
	n := numCoresOnline
	me := cpuID()
	meAPIC := perCPUBlocks[me].APICID
	for i := uint32(0); i < n; i++ {
		if i == me {
			continue
		}
		apicID := perCPUBlocks[i].APICID
		if apicID == meAPIC {
			continue
		}
		lapicSendIPI(uint8(apicID), ipiWakeupVector)
	}
}
