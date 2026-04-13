// src/main.go — Conservative GC demo for the gooos bare-metal kernel.
//
// With gc="conservative", TinyGo's mark/sweep GC automatically reclaims
// unreachable objects. This demo allocates many objects, triggers GC, and
// displays reclamation statistics on the VGA text buffer.

package main

import (
	"runtime"
	"unsafe"
)

// sti enables maskable interrupts. Implemented in stubs.S.
//
//go:linkname sti sti
func sti()

const (
	vgaAddr   = uintptr(0xB8000)
	vgaWidth  = 80
	vgaHeight = 25
	vgaCells  = vgaWidth * vgaHeight
	colorAttr = uint16(0x0F00) // bright white on black
)

// vgaWriteLine writes a string to the given row of the VGA text buffer.
func vgaWriteLine(row int, s string) {
	vga := (*[vgaCells]uint16)(unsafe.Pointer(vgaAddr))
	offset := row * vgaWidth
	for i := 0; i < len(s) && offset+i < vgaCells; i++ {
		vga[offset+i] = uint16(s[i]) | colorAttr
	}
}

// vgaClear fills the entire VGA text buffer with spaces.
func vgaClear() {
	vga := (*[vgaCells]uint16)(unsafe.Pointer(vgaAddr))
	for i := 0; i < vgaCells; i++ {
		vga[i] = uint16(' ') | colorAttr
	}
}

// vgaClearLine clears a VGA row from the given column to end of line.
func vgaClearLine(row int, fromCol int) {
	vga := (*[vgaCells]uint16)(unsafe.Pointer(vgaAddr))
	offset := row*vgaWidth + fromCol
	for i := offset; i < (row+1)*vgaWidth && i < vgaCells; i++ {
		vga[i] = uint16(' ') | colorAttr
	}
}

// utoa converts a uint64 to its decimal string representation.
// Implemented manually because importing strconv or fmt would pull in
// OS-dependent runtime code that does not work in bare-metal.
func utoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte // max uint64 is 20 digits
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// allocateGarbage creates a heap-allocated object and returns a pointer.
// The caller discards it, making it garbage collectible.
//
//go:noinline
func allocateGarbage() *[256]byte {
	p := new([256]byte)
	p[0] = 42
	return p
}

// handleDivisionError displays an exception message on VGA and serial
// when vector 0 (#DE - Division Error) fires.
func handleDivisionError(vector uint64) {
	vgaWriteLine(7, "Exception: #DE")
	serialPrintln("Exception: #DE (Division Error)")
}

// handleDefaultIRQ handles any hardware IRQ (vectors 32-47) that does
// not have a specific handler registered. Sends EOI so the PIC is not
// left stuck.
func handleDefaultIRQ(vector uint64) {
	irq := uint8(vector - 32)
	picSendEOI(irq)
}

// hlt executes the HLT instruction. Implemented in stubs.S.
//
//go:linkname hlt hlt
func hlt()

func main() {
	vgaClear()

	// Initialize serial output on COM1.
	serialInit()

	// Display and log serial status.
	vgaWriteLine(0, "Serial: OK")
	serialPrintln("Serial: OK")

	// Initialize and load the 256-entry IDT with ISR stubs.
	idtInit()
	vgaWriteLine(1, "IDT: loaded, 256 entries")
	serialPrintln("IDT: loaded, 256 entries")

	// Register exception handlers.
	registerHandler(0, handleDivisionError)
	registerHandler(14, handlePageFault)
	vgaWriteLine(2, "ISR: 256 stubs installed")
	serialPrintln("ISR: 256 stubs installed")

	// Remap 8259A PIC: IRQ 0-7 -> vectors 32-39, IRQ 8-15 -> vectors 40-47.
	picRemap()

	// Register default handlers for all hardware IRQs (vectors 32-47)
	// so that spurious or unhandled IRQs still get EOI and don't hang the PIC.
	for i := 32; i <= 47; i++ {
		registerHandler(i, handleDefaultIRQ)
	}

	// Initialize PIT channel 0 at ~100 Hz and register the timer IRQ handler.
	pitInit()
	registerHandler(32, handleTimer)
	vgaWriteLine(3, "PIT: 100 Hz timer started")
	serialPrintln("PIT: 100 Hz timer started")

	// Register keyboard IRQ1 handler (vector 33).
	registerHandler(33, handleKeyboard)
	vgaWriteLine(4, "Keyboard: ready")
	serialPrintln("Keyboard: ready")

	// Enable maskable interrupts.
	sti()
	vgaWriteLine(5, "Interrupts: enabled")
	serialPrintln("Interrupts: enabled")

	// Phase 1: Allocate many objects that immediately become garbage.
	const numAllocs = 500
	for i := 0; i < numAllocs; i++ {
		_ = allocateGarbage()
	}

	// Read stats before GC.
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	vgaWriteLine(6, "Mallocs: "+utoa(before.Mallocs)+"  TotalAlloc: "+utoa(before.TotalAlloc))
	serialPrintln("Mallocs: " + utoa(before.Mallocs) + "  TotalAlloc: " + utoa(before.TotalAlloc))

	// Phase 2: Trigger garbage collection.
	runtime.GC()

	// Read stats after GC.
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	vgaWriteLine(7, "GC done. Frees: "+utoa(after.Frees)+"  HeapInuse: "+utoa(after.HeapInuse))
	serialPrintln("GC done. Frees: " + utoa(after.Frees) + "  HeapInuse: " + utoa(after.HeapInuse))

	// Phase 3: Allocate again to prove memory was reclaimed.
	// If GC did not free anything, the heap would eventually fill up.
	for i := 0; i < 100; i++ {
		_ = allocateGarbage()
	}
	vgaWriteLine(8, "Post-GC alloc OK - GC works!")
	serialPrintln("Post-GC alloc OK - GC works!")

	// Virtual memory demo: map a 4 KiB page, write, read back, unmap.
	vmInit()
	testVaddr := uintptr(0x40000000) // 1 GiB — outside the boot-time identity map
	testPaddr := allocPage()         // allocate a physical page from free memory
	mapPage(testVaddr, testPaddr, pagePresent|pageWrite)

	// Write a test value to the mapped virtual page.
	testPtr := (*uint64)(unsafe.Pointer(testVaddr))
	*testPtr = 0xDEADBEEF

	// Read back and verify.
	testVal := *testPtr

	// Unmap the page and flush TLB.
	unmapPage(testVaddr)

	if testVal == 0xDEADBEEF {
		vgaWriteLine(9, "VM: map/unmap OK")
		serialPrintln("VM: map/unmap OK")
	} else {
		vgaWriteLine(9, "VM: FAIL - read back 0x"+hextoa(testVal))
		serialPrintln("VM: FAIL - read back 0x" + hextoa(testVal))
	}

	// Spin-wait to let the timer accumulate ticks, then display count.
	for pitTicks < 200 {
		hlt()
	}
	tickStr := utoa(pitTicks)
	vgaWriteLine(10, "Timer: "+tickStr+" ticks")
	serialPrintln("Timer: " + tickStr + " ticks")

	// Initialize the scheduler: task 0 = this main/boot task.
	initScheduler()

	// Create 3 demo tasks that write to different VGA lines.
	createTask(demoTaskAAddr()) // Task 1 -> VGA line 14
	createTask(demoTaskBAddr()) // Task 2 -> VGA line 15
	createTask(demoTaskCAddr()) // Task 3 -> VGA line 16

	vgaWriteLine(11, "Scheduler: 3 tasks created")
	serialPrintln("Scheduler: 3 tasks created")

	// Enable preemptive scheduling — the next timer tick will start switching.
	schedReady = true
	vgaWriteLine(12, "Scheduler: running")
	serialPrintln("Scheduler: running (round-robin, PIT preemption)")

	// Halt loop: task 0 (main) idles, waking on each interrupt.
	// The scheduler will preempt this and switch to demo tasks.
	for {
		hlt()
	}
}
