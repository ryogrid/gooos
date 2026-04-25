// src/keyboard_irq.go — ISR-safe lock-free ring buffer for keyboard
// scancode events plus a blocking reader helper for stdin syscalls.
//
// Design: single-producer (ISR: handleKeyboard) single-consumer
// bounded ring, size=64 uint32 slots. x86-TSO guarantees all four
// required orderings via plain mov; no atomic.Load/Store needed on
// BSP-only v1. See
// impldoc/phase_b_keyboard_irq.md §3 for the memory-order proof.
//
// Event encoding (matches the existing handleKeyboard packing):
//   uint32 event = (scancode & 0xFF) | ((ascii & 0xFF) << 8)

package main

// kbdRingSize is a power of two so `idx & mask` replaces modulo.
// 64 slots is ample: PIT fires at 100 Hz, typical typing ≤10
// keystrokes/sec, pump drains faster than the ring fills.
const kbdRingSize = 64

var (
	gooosKbdRing [kbdRingSize]uint32
	gooosKbdHead uint32 // writer (ISR) — monotonically increments
	gooosKbdTail uint32 // reader (pump) — monotonically increments
)

// kbdRingDrops counts events dropped because the ring was full at
// ISR time. Normally zero. Exposed by netDiag so a bursty-paste or
// lost-consumer bug becomes visible without an ISR serial print.
// E1 per TODO_FIX.md.
var kbdRingDrops uint32

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
		kbdRingDrops++ // full, drop (diagnostic, racey increment OK)
		return
	}
	gooosKbdRing[h&(kbdRingSize-1)] = event
	gooosKbdHead = h + 1
}

// keyboardIRQRecv is called by blocking keyboard readers.
// Non-blocking; returns false when the ring is empty.
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

// kbdPumpCpuSeen[i] is set to 1 the FIRST time a blocking keyboard
// reader drains an event while running on CPU i (M9). Flag array, not
// a counter. netDiag reports it as "pump:NNNN" for continuity with the
// existing diagnostics even though the dedicated pump goroutine is gone.
var kbdPumpCpuSeen [maxCPUs]uint32

func markKeyboardDrainCPU() {
	c := cpuID()
	if c >= maxCPUs || kbdPumpCpuSeen[c] != 0 {
		return
	}
	kbdPumpCpuSeen[c] = 1
	switch c {
	case 0:
		serialPrintln("MARKER: M9 pump:drained-on-cpu0")
	case 1:
		serialPrintln("MARKER: M9 pump:drained-on-cpu1")
	case 2:
		serialPrintln("MARKER: M9 pump:drained-on-cpu2")
	case 3:
		serialPrintln("MARKER: M9 pump:drained-on-cpu3")
	}
}

// keyboardReadEventBlocking waits until one keyboard event is available.
// The wait path is intentionally channel-free: stdin syscalls poll the
// shared IRQ ring directly, yielding on APs and only hlt-parking on BSP
// where legacy IRQ1 can actually wake the CPU.
func keyboardReadEventBlocking() uint32 {
	for {
		if ev, ok := keyboardIRQRecv(); ok {
			markKeyboardDrainCPU()
			return ev
		}
		if cpuID() == 0 {
			if pollKeyboardFallback() {
				continue
			}
			if ev, ok := keyboardIRQRecv(); ok {
				markKeyboardDrainCPU()
				return ev
			}
			// M4.2.b/e: when the caller is a kthread (post-M4.1
			// shell-on-kthread), park on a 1-tick timer event
			// instead of hlt'ing the CPU. hlt monopolizes BSP
			// and starves netRxLoop / udpEchoServer kthreads
			// also queued on CPU 0 (no APs on -smp 1). The
			// parked kthread wakes on timer (10 ms) and re-checks
			// the keyboard ring; intervening kschedLoopOnce
			// iterations from elf.go's pump dispatch the other
			// CPU-0 kthreads.
			if kschedRunning[0] != nil {
				kschedTimedPark(1)
				continue
			}
			sti()
			// After sti(), check once more before hlt().
			// On x86, load from gooosKbdHead will be ordered after sti()
			// by x86-TSO, so if an IRQ1 wrote to the ring, we'll see it.
			// This prevents the case where IRQ1 arrives between the check
			// above and hlt() below.
			if gooosKbdTail != gooosKbdHead {
				continue
			}
			hlt()
			continue
		}
		// AP path: bounded sleep. P1-fix: when caller is a kthread
		// (post-M4.1 ring3-on-kthread), use kschedTimedPark to
		// avoid the H-01 chan-recv hazard from bare afterTicks
		// under scheduler=none.
		if kschedRunning[cpuID()] != nil {
			kschedTimedPark(1)
			continue
		}
		<-afterTicks(1)
	}
}
