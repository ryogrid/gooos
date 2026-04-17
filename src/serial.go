// src/serial.go -- COM1 serial output for the gooos bare-metal kernel.
//
// Initializes the 16550 UART at COM1 (0x3F8) to 115200 baud, 8N1.
// Provides serialPutChar and serialPrint for kernel logging to the
// host terminal via QEMU's -serial stdio.

package main

// No imports needed now that the task/channel plumbing is gone —
// all serial helpers call stubs.S via package-level //go:linkname
// declarations on their own.

// serialLock protects serial port output for SMP safety.
var serialLock Spinlock

// COM1 port addresses.
const (
	com1Port      = 0x3F8
	com1Data      = com1Port + 0 // Data register (R/W)
	com1IntEn     = com1Port + 1 // Interrupt enable register
	com1FifoCtrl  = com1Port + 2 // FIFO control register
	com1LineCtrl  = com1Port + 3 // Line control register
	com1ModemCtrl = com1Port + 4 // Modem control register
	com1LineStat  = com1Port + 5 // Line status register
)

// outb writes a byte to an x86 I/O port. Implemented in stubs.S.
//
//go:linkname outb outb
func outb(port uint16, val uint8)

// inb reads a byte from an x86 I/O port. Implemented in stubs.S.
//
//go:linkname inb inb
func inb(port uint16) uint8

// serialInit initializes COM1 at 115200 baud, 8N1.
func serialInit() {
	outb(com1IntEn, 0x00)     // Disable all interrupts
	outb(com1LineCtrl, 0x80)  // Enable DLAB (set baud rate divisor)
	outb(com1Data, 0x01)      // Divisor low byte: 1 (115200 baud)
	outb(com1IntEn, 0x00)     // Divisor high byte: 0
	outb(com1LineCtrl, 0x03)  // 8 bits, no parity, one stop bit
	outb(com1FifoCtrl, 0xC7)  // Enable FIFO, clear TX/RX, 14-byte threshold
	outb(com1ModemCtrl, 0x0B) // DTR + RTS + OUT2
}

// serialPutChar sends a single byte to COM1.
// Waits for the transmit holding register to be empty before writing.
func serialPutChar(c byte) {
	for inb(com1LineStat)&0x20 == 0 {
	}
	outb(com1Data, c)
}

// serialPrint sends a string to COM1.
// Protected by serialLock for SMP safety.
func serialPrint(s string) {
	flags := serialLock.Acquire()
	for i := 0; i < len(s); i++ {
		serialPutChar(s[i])
	}
	serialLock.Release(flags)
}

// serialPrintln sends a string followed by a newline to COM1.
// Protected by serialLock for SMP safety.
func serialPrintln(s string) {
	flags := serialLock.Acquire()
	for i := 0; i < len(s); i++ {
		serialPutChar(s[i])
	}
	serialPutChar('\r')
	serialPutChar('\n')
	serialLock.Release(flags)
}

// serialPrintBytes is the allocation-free sibling of serialPrint.
// Used by ISR-context panic helpers that format into a fixed
// scratch buffer (see src/panic.go).
//
//go:nosplit
func serialPrintBytes(b []byte) {
	for i := 0; i < len(b); i++ {
		serialPutChar(b[i])
	}
}

