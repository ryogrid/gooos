// src/main.go — TinyGo kernel entry point with heap-allocation demo.
//
// With gc="leaking" in target.json, TinyGo's runtime initializes the heap
// via preinit() -> mmap -> initHeap() before calling this main(). Dynamic
// allocation (make, append, new, string +) now works.

package main

import "unsafe"

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

// makeGreeting builds a greeting string via heap-allocating concatenation.
// The //go:noinline directive prevents LLVM from optimizing the heap
// allocations into stack allocations via escape analysis.
//
//go:noinline
func makeGreeting() string {
	a := "Hello"
	b := ", "
	c := "Heap"
	d := "!"
	return a + b + c + d
}

// makeMessage builds a message via make() + append(), proving that slice
// growth (which heap-allocates a new backing array) works. The initial
// capacity is intentionally small (2) so that the first append of "Heap"
// (4 bytes) exceeds it and forces a heap-allocated grow.
//
//go:noinline
func makeMessage() string {
	buf := make([]byte, 0, 2)
	buf = append(buf, "Heap"...)
	buf = append(buf, ' ')
	buf = append(buf, "works!"...)
	return string(buf)
}

// allocateUint64 uses new() to heap-allocate a uint64 and returns the
// pointer. Returning the pointer forces it to escape, ensuring the
// compiler cannot stack-allocate it.
//
//go:noinline
func allocateUint64() *uint64 {
	p := new(uint64)
	*p = 42
	return p
}

func main() {
	vgaClear()

	// Line 0: string concatenation (heap-allocated intermediate strings)
	greeting := makeGreeting()
	vgaWriteLine(0, greeting)

	// Line 1: make() + append() (heap-allocated slice backing array)
	msg := makeMessage()
	vgaWriteLine(1, msg)

	// Line 2: new() (heap-allocated pointer — returned to force escape)
	p := allocateUint64()
	if *p == 42 {
		vgaWriteLine(2, "new(uint64) = 42")
	}
}
