// src/keyboard_irq.go — ISR-safe lock-free ring buffer for keyboard
// scancode events + a pump goroutine that forwards into a native Go
// channel.
//
// Design: single-producer (ISR: handleKeyboard) single-consumer (the
// keyboardPump goroutine) bounded ring, size=64 uint32 slots. x86-TSO
// guarantees all four required orderings via plain mov; no
// atomic.Load/Store needed on BSP-only v1. See
// impldoc/phase_b_keyboard_irq.md §3 for the memory-order proof.
//
// Event encoding (matches the existing handleKeyboard packing):
//   uint32 event = (scancode & 0xFF) | ((ascii & 0xFF) << 8)

package main

import "runtime"

// kbdRingSize is a power of two so `idx & mask` replaces modulo.
// 64 slots is ample: PIT fires at 100 Hz, typical typing ≤10
// keystrokes/sec, pump drains faster than the ring fills.
const kbdRingSize = 64

var (
	gooosKbdRing [kbdRingSize]uint32
	gooosKbdHead uint32 // writer (ISR) — monotonically increments
	gooosKbdTail uint32 // reader (pump) — monotonically increments
)

// keyboardCh delivers scancode+ASCII events to sysReadHandler. Buffer
// of 16 absorbs typing bursts without forcing the pump to park.
var keyboardCh = make(chan uint32, 16)

// keyboardIRQSend is invoked from the ISR (handleKeyboard). It must
// not allocate and must not call any Go-runtime operation that could
// park or take a lock. `//go:nosplit` keeps TinyGo from inserting a
// stack-growth check; we never grow stacks anyway. Drop-on-full
// behavior matches the old chanTrySend semantics.
//
//go:nosplit
func keyboardIRQSend(event uint32) {
	h := gooosKbdHead
	if h-gooosKbdTail >= kbdRingSize {
		return // full, drop
	}
	gooosKbdRing[h&(kbdRingSize-1)] = event
	gooosKbdHead = h + 1
}

// keyboardIRQRecv is called by keyboardPump. Non-blocking; returns
// false when the ring is empty.
//
//go:nosplit
func keyboardIRQRecv() (uint32, bool) {
	t := gooosKbdTail
	if t == gooosKbdHead {
		return 0, false
	}
	event := gooosKbdRing[t&(kbdRingSize-1)]
	gooosKbdTail = t + 1
	return event, true
}

// keyboardPump forwards ring events into keyboardCh. On empty ring
// it yields via Gosched repeatedly until it is running on the BSP,
// and only then parks on sti+hlt. Reason: the keyboard IRQ (IRQ1
// via PIC pass-through) only ever fires on the BSP (LVT0 ExtINT
// unmasked on BSP only; APs have LVT0 masked). If work-stealing
// parks the pump on an AP's hlt it will never wake, stalling the
// whole keyboard → shell path. Staying in a Gosched loop on APs
// lets the scheduler migrate the pump back onto BSP (the BSP is
// always runnable thanks to PIT / LAPIC-timer ticks), where the
// hlt is safely serviced by IRQ1.
func keyboardPump() {
	keyboardPumpHandle = taskCurrent()
	for {
		ev, ok := keyboardIRQRecv()
		if ok {
			keyboardCh <- ev
			continue
		}
		// If we're on an AP, just yield and spin — do NOT park on
		// sti+hlt because IRQ1 won't wake us here.
		if cpuID() != 0 {
			runtime.Gosched()
			continue
		}
		// Empty ring on BSP. Yield so fsTask / shell / ring3Wrapper
		// can run first. If still empty after scheduler round-trip,
		// park on sti+hlt — IRQ1 on BSP wakes us directly.
		runtime.Gosched()
		if _, again := keyboardIRQRecv(); again {
			continue
		}
		if cpuID() != 0 {
			// Migrated off BSP mid-round — loop back to the
			// on-AP path rather than hlt here.
			continue
		}
		sti()
		hlt()
	}
}
