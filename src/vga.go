// src/vga.go -- VGA text-mode console with cursor and scrolling.
//
// Provides a scrollable console for user output. Characters are written
// at the current cursor position; newlines advance to the next row.
// When the cursor reaches the bottom of the screen, all rows shift up
// and the last row is cleared.

package main

import "unsafe"

// VGA console state.
var (
	vgaCursorRow int // current cursor row (0-24)
	vgaCursorCol int // current cursor column (0-79)
)

// vgaConsolePutChar writes a single character at the cursor position
// and advances the cursor. Handles newline, carriage return, and backspace.
func vgaConsolePutChar(c byte) {
	switch c {
	case '\n':
		vgaCursorCol = 0
		vgaCursorRow++
		if vgaCursorRow >= vgaHeight {
			vgaConsoleScroll()
			vgaCursorRow = vgaHeight - 1
		}
	case '\r':
		vgaCursorCol = 0
	case '\b':
		if vgaCursorCol > 0 {
			vgaCursorCol--
			vga := (*[vgaCells]uint16)(unsafe.Pointer(vgaAddr))
			offset := vgaCursorRow*vgaWidth + vgaCursorCol
			vga[offset] = uint16(' ') | colorAttr
		}
	default:
		if vgaCursorCol >= vgaWidth {
			vgaCursorCol = 0
			vgaCursorRow++
			if vgaCursorRow >= vgaHeight {
				vgaConsoleScroll()
				vgaCursorRow = vgaHeight - 1
			}
		}
		vga := (*[vgaCells]uint16)(unsafe.Pointer(vgaAddr))
		offset := vgaCursorRow*vgaWidth + vgaCursorCol
		vga[offset] = uint16(c) | colorAttr
		vgaCursorCol++
	}
}

// vgaConsoleScroll shifts all VGA rows up by one. Row 0 is lost and
// the last row (24) is cleared to spaces.
func vgaConsoleScroll() {
	vga := (*[vgaCells]uint16)(unsafe.Pointer(vgaAddr))
	// Shift rows 1..24 up to 0..23.
	for row := 0; row < vgaHeight-1; row++ {
		dstOff := row * vgaWidth
		srcOff := (row + 1) * vgaWidth
		for col := 0; col < vgaWidth; col++ {
			vga[dstOff+col] = vga[srcOff+col]
		}
	}
	// Clear the last row.
	lastRow := (vgaHeight - 1) * vgaWidth
	for col := 0; col < vgaWidth; col++ {
		vga[lastRow+col] = uint16(' ') | colorAttr
	}
}

// vgaConsolePrint writes a string to the VGA console at the current
// cursor position.
func vgaConsolePrint(s string) {
	for i := 0; i < len(s); i++ {
		vgaConsolePutChar(s[i])
	}
}

// vgaConsoleClear fills the entire VGA text buffer with spaces and
// resets the cursor to the top-left corner.
func vgaConsoleClear() {
	vgaClear()
	vgaCursorRow = 0
	vgaCursorCol = 0
}
