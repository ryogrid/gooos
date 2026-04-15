// src/keyboard.go -- PS/2 keyboard driver (IRQ1, scancode set 1).
//
// The IRQ1 handler reads scancodes from port 0x60, packs them, and
// publishes to the .bss ring buffer in src/keyboard_irq.go via
// keyboardIRQSend. keyboardPump (a goroutine) drains the ring and
// forwards into the native channel keyboardCh, consumed by
// sysReadHandler in src/userspace.go.

package main

// PS/2 keyboard I/O port.
const kbdDataPort = 0x60

// scancodeToASCII maps scancode set 1 make codes to ASCII characters.
// Index = scancode, value = ASCII (0 means unmapped).
var scancodeToASCII = [128]byte{
	// 0x00: no key
	0x02: '1', 0x03: '2', 0x04: '3', 0x05: '4', 0x06: '5',
	0x07: '6', 0x08: '7', 0x09: '8', 0x0A: '9', 0x0B: '0',
	0x0C: '-', 0x0D: '=',
	// 0x0E: backspace (handled separately)
	0x10: 'q', 0x11: 'w', 0x12: 'e', 0x13: 'r', 0x14: 't',
	0x15: 'y', 0x16: 'u', 0x17: 'i', 0x18: 'o', 0x19: 'p',
	0x1A: '[', 0x1B: ']',
	// 0x1C: enter (handled separately)
	0x1E: 'a', 0x1F: 's', 0x20: 'd', 0x21: 'f', 0x22: 'g',
	0x23: 'h', 0x24: 'j', 0x25: 'k', 0x26: 'l',
	0x27: ';', 0x28: '\'', 0x29: '`',
	0x2B: '\\',
	0x2C: 'z', 0x2D: 'x', 0x2E: 'c', 0x2F: 'v', 0x30: 'b',
	0x31: 'n', 0x32: 'm',
	0x33: ',', 0x34: '.', 0x35: '/',
	0x39: ' ', // space
}

const (
	scBackspace = 0x0E
	scEnter     = 0x1C
)

// keyboardInit is a no-op under Phase B — the ring buffer lives in
// .bss and is zero-initialized; keyboardCh is constructed at
// var-init time. Retained for symmetry with the existing init
// call site in main.go, so the call remains valid. The function can
// be deleted along with the corresponding call once we are sure no
// other code references it.
func keyboardInit() {}

// handleKeyboard is the IRQ1 handler (vector 33). Reads the scancode
// from port 0x60, packs event bytes, and publishes into the
// gooosKbdRing via keyboardIRQSend. Never blocks, never allocates.
//
//go:nosplit
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

	// event = (scancode & 0xFF) | ((ascii & 0xFF) << 8)
	event := uint32(scancode) | (uint32(ascii) << 8)
	keyboardIRQSend(event)
}
