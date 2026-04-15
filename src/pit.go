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
//go:nosplit
func handleTimer(vector uint64) {
	pitTicks++
	picSendEOI(0)
}
