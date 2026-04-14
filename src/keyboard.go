// src/keyboard.go -- PS/2 keyboard driver (IRQ1, scancode set 1).
//
// The IRQ1 handler reads scancodes from port 0x60, packs them into
// KeyEvent messages, and publishes to keyboardChannel via chanTrySend.
// A dedicated keyboardConsumerTask receives events and handles VGA
// echo and serial logging — no VGA/serial work in interrupt context.

package main

// KeyEvent represents a keyboard event with raw scancode and ASCII translation.
type KeyEvent struct {
	scancode uint8
	ascii    byte
}

// keyboardChannel receives KeyEvent messages from the IRQ1 handler.
// Created by keyboardInit() before the IRQ handler is registered.
var keyboardChannel *Channel

// PS/2 keyboard I/O port.
const kbdDataPort = 0x60

// VGA line used for the keyboard echo buffer.
const kbdVGALine = 12

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

// keyboardInit creates the keyboard event channel. Must be called
// before registering the IRQ1 handler.
func keyboardInit() {
	keyboardChannel = chanCreate(16)
}

// handleKeyboard is the IRQ1 handler (vector 33). Reads the scancode
// from port 0x60, packs a KeyEvent, and publishes via chanTrySend.
// Never blocks — drops events if the channel is full.
func handleKeyboard(vector uint64) {
	scancode := inb(kbdDataPort)
	picSendEOI(1)

	// Ignore key release events (bit 7 set).
	if scancode&0x80 != 0 {
		return
	}

	// Translate scancode to ASCII.
	var ascii byte
	if scancode < 128 {
		ascii = scancodeToASCII[scancode]
	}

	// Pack KeyEvent into uintptr: low byte = scancode, next byte = ascii.
	event := uintptr(scancode) | (uintptr(ascii) << 8)
	chanTrySend(keyboardChannel, event)
}

// keyboardConsumerTaskAddr returns the address of keyboardConsumerTask.
// Implemented in switch.S.
//
//go:linkname keyboardConsumerTaskAddr keyboardConsumerTaskAddr
func keyboardConsumerTaskAddr() uintptr

// keyboardConsumerTask receives KeyEvent messages from keyboardChannel
// and handles VGA echo buffer logic and serial logging.
//
//export keyboardConsumerTask
func keyboardConsumerTask() {
	sti()
	serialPrintln("Keyboard: consumer task started")
	for {
		event := chanRecv(keyboardChannel)
		scancode := uint8(event & 0xFF)
		ascii := byte((event >> 8) & 0xFF)

		// Handle backspace.
		if scancode == scBackspace {
			if kbdBufLen > 0 {
				kbdBufLen--
				kbdRedraw()
				serialPrintln("Key: <backspace>")
			}
			continue
		}

		// Handle enter: log and clear the buffer.
		if scancode == scEnter {
			serialPrint("Key: <enter> line='")
			serialPrint(string(kbdBuf[:kbdBufLen]))
			serialPrintln("'")
			kbdBufLen = 0
			kbdRedraw()
			continue
		}

		// Normal key: echo to VGA and serial.
		if ascii != 0 && kbdBufLen < vgaWidth {
			kbdBuf[kbdBufLen] = ascii
			kbdBufLen++
			kbdRedraw()
			serialPrint("Key: ")
			serialPutChar(ascii)
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
