// src/keyboard.go -- PS/2 keyboard driver (IRQ1, scancode set 1).
//
// Reads scancodes from port 0x60, translates to ASCII, and echoes
// typed characters on a dedicated VGA line. Logs keypresses to serial.

package main

// PS/2 keyboard I/O port.
const kbdDataPort = 0x60

// VGA line used for the keyboard echo buffer.
const kbdVGALine = 11

// kbdBuf holds typed characters for VGA echo display.
var kbdBuf [vgaWidth]byte
var kbdBufLen int

// scancodeToASCII maps scancode set 1 make codes to ASCII characters.
// Index = scancode, value = ASCII (0 means unmapped).
var scancodeToASCII = [128]byte{
	// 0x00: no key
	0x02: '1', 0x03: '2', 0x04: '3', 0x05: '4', 0x06: '5',
	0x07: '6', 0x08: '7', 0x09: '8', 0x0A: '9', 0x0B: '0',
	// 0x0E: backspace (handled separately)
	0x10: 'q', 0x11: 'w', 0x12: 'e', 0x13: 'r', 0x14: 't',
	0x15: 'y', 0x16: 'u', 0x17: 'i', 0x18: 'o', 0x19: 'p',
	0x1E: 'a', 0x1F: 's', 0x20: 'd', 0x21: 'f', 0x22: 'g',
	0x23: 'h', 0x24: 'j', 0x25: 'k', 0x26: 'l',
	0x2C: 'z', 0x2D: 'x', 0x2E: 'c', 0x2F: 'v', 0x30: 'b',
	0x31: 'n', 0x32: 'm',
	0x39: ' ', // space
}

const (
	scBackspace = 0x0E
	scEnter     = 0x1C
)

// handleKeyboard is the IRQ1 handler (vector 33). Reads the scancode
// from port 0x60, translates it to ASCII, and echoes to VGA and serial.
func handleKeyboard(vector uint64) {
	scancode := inb(kbdDataPort)
	picSendEOI(1)

	// Ignore key release events (bit 7 set).
	if scancode&0x80 != 0 {
		return
	}

	// Handle backspace.
	if scancode == scBackspace {
		if kbdBufLen > 0 {
			kbdBufLen--
			kbdRedraw()
			serialPrintln("Key: <backspace>")
		}
		return
	}

	// Handle enter: log and clear the buffer.
	if scancode == scEnter {
		serialPrint("Key: <enter> line='")
		serialPrint(string(kbdBuf[:kbdBufLen]))
		serialPrintln("'")
		kbdBufLen = 0
		kbdRedraw()
		return
	}

	// Translate scancode to ASCII.
	if scancode < 128 {
		ch := scancodeToASCII[scancode]
		if ch != 0 && kbdBufLen < vgaWidth {
			kbdBuf[kbdBufLen] = ch
			kbdBufLen++
			kbdRedraw()
			serialPrint("Key: ")
			serialPutChar(ch)
			serialPutChar('\r')
			serialPutChar('\n')
		}
	}
}

// kbdRedraw redraws the keyboard echo line on VGA.
func kbdRedraw() {
	vgaWriteLine(kbdVGALine, string(kbdBuf[:kbdBufLen]))
	// Clear remaining characters on the line.
	vgaClearLine(kbdVGALine, kbdBufLen)
}
