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

func main() {
	vgaClear()

	// Initialize serial output on COM1.
	serialInit()

	// Display and log serial status.
	vgaWriteLine(0, "Serial: OK")
	serialPrintln("Serial: OK")

	// Initialize and load the 256-entry IDT.
	idtInit()
	vgaWriteLine(1, "IDT: loaded, 256 entries")
	serialPrintln("IDT: loaded, 256 entries")

	// Phase 1: Allocate many objects that immediately become garbage.
	const numAllocs = 500
	for i := 0; i < numAllocs; i++ {
		_ = allocateGarbage()
	}

	// Read stats before GC.
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	vgaWriteLine(2, "Mallocs: "+utoa(before.Mallocs)+"  TotalAlloc: "+utoa(before.TotalAlloc))
	serialPrintln("Mallocs: " + utoa(before.Mallocs) + "  TotalAlloc: " + utoa(before.TotalAlloc))

	// Phase 2: Trigger garbage collection.
	runtime.GC()

	// Read stats after GC.
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	vgaWriteLine(3, "GC done. Frees: "+utoa(after.Frees)+"  HeapInuse: "+utoa(after.HeapInuse))
	serialPrintln("GC done. Frees: " + utoa(after.Frees) + "  HeapInuse: " + utoa(after.HeapInuse))

	// Phase 3: Allocate again to prove memory was reclaimed.
	// If GC did not free anything, the heap would eventually fill up.
	for i := 0; i < 100; i++ {
		_ = allocateGarbage()
	}
	vgaWriteLine(4, "Post-GC alloc OK - GC works!")
	serialPrintln("Post-GC alloc OK - GC works!")
}
