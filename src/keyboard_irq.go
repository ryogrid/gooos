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

// keyboardPump forwards ring events into keyboardCh. It yields via
// runtime.Gosched on empty; no need for sti+hlt parking in v1.
func keyboardPump() {
	for {
		ev, ok := keyboardIRQRecv()
		if ok {
			keyboardCh <- ev
			runtime.Gosched() // let other goroutines run
			continue
		}
		// Empty ring. Yield first; if nothing else is runnable, the
		// scheduler will come back and we'll sti+hlt to save CPU
		// until the next IRQ.
		runtime.Gosched()
		if _, again := keyboardIRQRecv(); again {
			// new events arrived while we yielded
			continue
		}
		sti()
		hlt()
		cli()
	}
}
