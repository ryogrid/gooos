// src/main.go — TinyGo kernel_main that writes to the VGA text buffer.
//
// Compiled by TinyGo against src/target.json into a relocatable object
// file. The //export directive makes kernel_main appear to the linker as
// a C-ABI symbol that boot.S can reach via `call kernel_main`.

package main

import "unsafe"

// colorAttr is the VGA character attribute byte in the high byte of each
// 16-bit cell. 0x0F = bright white foreground on black background.
const colorAttr uint16 = 0x0F00

const (
	vgaWidth  = 80
	vgaHeight = 25
	vgaCells  = vgaWidth * vgaHeight
)

//export kernel_main
func kernel_main() {
	// Treat the VGA text buffer as a fixed-size array of 16-bit cells.
	vga := (*[vgaCells]uint16)(unsafe.Pointer(uintptr(0xB8000)))

	// Clear the screen (space characters with our attribute).
	for i := 0; i < vgaCells; i++ {
		vga[i] = uint16(' ') | colorAttr
	}

	// Write the greeting at the top-left corner.
	msg := "Hello, World!"
	for i := 0; i < len(msg); i++ {
		vga[i] = uint16(msg[i]) | colorAttr
	}
}

// TinyGo requires a package-level main() to exist, but in bare-metal mode
// with scheduler="none" and gc="none" it is never called.
func main() {}
