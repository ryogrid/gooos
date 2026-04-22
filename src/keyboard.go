// src/keyboard.go -- PS/2 keyboard driver (IRQ1, scancode set 1).
//
// The IRQ1 handler reads scancodes from port 0x60, packs them, and
// publishes to the .bss ring buffer in src/keyboard_irq.go via
// keyboardIRQSend. keyboardPump (a goroutine) drains the ring and
// forwards into the native channel keyboardCh, consumed by
// sysReadHandler in src/userspace.go.
//
// Event encoding (32-bit):
//   bits  0– 7: scancode (make code, 0x80 stripped)
//   bits  8–15: ASCII    (0 for non-printable / special keys)
//   bits 16–18: modifiers (bit 0=Shift, bit 1=Ctrl, bit 2=Alt)
//   bit     24: extended-key flag (0xE0-prefixed scancode)

package main

// PS/2 keyboard I/O port.
const kbdDataPort = 0x60

// scancodeToASCII maps scancode set 1 make codes to ASCII characters.
// Index = scancode, value = ASCII (0 means unmapped).
var scancodeToASCII [128]byte

// scancodeToASCIIShifted is the shift-held variant of the table
// above. Required so the shell can read `<`, `>`, `|`, `_`
// (and uppercase letters) for redirection / pipes / arguments.
var scancodeToASCIIShifted [128]byte

const (
	scBackspace = 0x0E
	scEnter     = 0x1C
	scLShift    = 0x2A
	scRShift    = 0x36
	scLCtrl     = 0x1D
	scLAlt      = 0x38
)

// Modifier state — tracked via make/break events from the IRQ.
// Single-CPU v1, no race.
var shiftHeld uint8
var ctrlHeld uint8
var altHeld uint8

// extendedPrefix is set when a 0xE0 byte arrives; the NEXT
// scancode is an extended key (arrow, Home, End, Delete, etc.).
var extendedPrefix bool

// keyboardInit is retained so the boot path can construct keyboardCh
// lazily instead of at package-init time.
// call site in main.go, so the call remains valid. The function can
// be deleted along with the corresponding call once we are sure no
// other code references it.
func keyboardInit() {
	if keyboardCh == nil {
		keyboardCh = make(chan uint32, 16)
	}
	if scancodeToASCII[0x02] == 0 {
		scancodeToASCII = [128]byte{
			0x02: '1', 0x03: '2', 0x04: '3', 0x05: '4', 0x06: '5',
			0x07: '6', 0x08: '7', 0x09: '8', 0x0A: '9', 0x0B: '0',
			0x0C: '-', 0x0D: '=',
			0x10: 'q', 0x11: 'w', 0x12: 'e', 0x13: 'r', 0x14: 't',
			0x15: 'y', 0x16: 'u', 0x17: 'i', 0x18: 'o', 0x19: 'p',
			0x1A: '[', 0x1B: ']',
			0x1E: 'a', 0x1F: 's', 0x20: 'd', 0x21: 'f', 0x22: 'g',
			0x23: 'h', 0x24: 'j', 0x25: 'k', 0x26: 'l',
			0x27: ';', 0x28: '\'', 0x29: '`',
			0x2B: '\\',
			0x2C: 'z', 0x2D: 'x', 0x2E: 'c', 0x2F: 'v', 0x30: 'b',
			0x31: 'n', 0x32: 'm',
			0x33: ',', 0x34: '.', 0x35: '/',
			0x39: ' ',
		}
		scancodeToASCIIShifted = [128]byte{
			0x02: '!', 0x03: '@', 0x04: '#', 0x05: '$', 0x06: '%',
			0x07: '^', 0x08: '&', 0x09: '*', 0x0A: '(', 0x0B: ')',
			0x0C: '_', 0x0D: '+',
			0x10: 'Q', 0x11: 'W', 0x12: 'E', 0x13: 'R', 0x14: 'T',
			0x15: 'Y', 0x16: 'U', 0x17: 'I', 0x18: 'O', 0x19: 'P',
			0x1A: '{', 0x1B: '}',
			0x1E: 'A', 0x1F: 'S', 0x20: 'D', 0x21: 'F', 0x22: 'G',
			0x23: 'H', 0x24: 'J', 0x25: 'K', 0x26: 'L',
			0x27: ':', 0x28: '"', 0x29: '~',
			0x2B: '|',
			0x2C: 'Z', 0x2D: 'X', 0x2E: 'C', 0x2F: 'V', 0x30: 'B',
			0x31: 'N', 0x32: 'M',
			0x33: '<', 0x34: '>', 0x35: '?',
			0x39: ' ',
		}
	}
}

// handleKeyboard is the IRQ1 handler (vector 33). Reads the scancode
// from port 0x60, packs event bytes, and publishes into the
// gooosKbdRing via keyboardIRQSend. Never blocks, never allocates.
//
// kbdIRQSeen is flipped to 1 on the FIRST IRQ1 entry (M8). Flag,
// not counter, so the 082051f u64-increment hang can't recur. The
// netDiag dump prints it alongside wake:NNNN so the user can see
// whether the keyboard IRQ is reaching the kernel at all after $
// reappears.
var kbdIRQSeen uint32

//go:nosplit
func handleKeyboard(vector uint64) {
	scancode := inb(kbdDataPort)
	if kbdIRQSeen == 0 {
		kbdIRQSeen = 1
		serialPrintln("MARKER: M8 handleKeyboard first entry")
	}
	if ioapicActive {
		lapicSendEOI()
	} else {
		picSendEOI(1)
	}

	// Extended key prefix (0xE0): consume and set flag for the
	// next scancode. Arrow keys, Home, End, Delete, right-Ctrl
	// and right-Alt all send 0xE0 before the actual scancode.
	if scancode == 0xE0 {
		extendedPrefix = true
		return
	}

	// Track modifier state on make + break.
	switch scancode & 0x7F { // strip break bit for matching
	case scLShift, scRShift:
		if scancode&0x80 == 0 {
			shiftHeld++
		} else if shiftHeld > 0 {
			shiftHeld--
		}
		return
	case scLCtrl:
		if scancode&0x80 == 0 {
			ctrlHeld++
		} else if ctrlHeld > 0 {
			ctrlHeld--
		}
		return
	case scLAlt:
		if scancode&0x80 == 0 {
			altHeld++
		} else if altHeld > 0 {
			altHeld--
		}
		return
	}

	// Ignore other key release events (bit 7 set).
	if scancode&0x80 != 0 {
		extendedPrefix = false
		return
	}

	// Translate scancode to ASCII (shifted variant if shift held).
	var ascii byte
	if scancode < 128 {
		if shiftHeld > 0 {
			ascii = scancodeToASCIIShifted[scancode]
		} else {
			ascii = scancodeToASCII[scancode]
		}
	}

	// Ctrl + letter → control character (0x01–0x1A).
	if ctrlHeld > 0 && ascii >= 'a' && ascii <= 'z' {
		ascii = ascii - 'a' + 1
	}

	// Build modifier + extended-key flags.
	mods := uint8(0)
	if shiftHeld > 0 {
		mods |= 1
	}
	if ctrlHeld > 0 {
		mods |= 2
	}
	if altHeld > 0 {
		mods |= 4
	}
	flags := uint8(0)
	if extendedPrefix {
		flags |= 1
		extendedPrefix = false
	}

	// Pack: scancode[0:7] | ascii[8:15] | mods[16:23] | flags[24:31]
	event := uint32(scancode) | uint32(ascii)<<8 | uint32(mods)<<16 | uint32(flags)<<24
	keyboardIRQSend(event)
}
