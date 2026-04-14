// src/serial.go -- COM1 serial output for the gooos bare-metal kernel.
//
// Initializes the 16550 UART at COM1 (0x3F8) to 115200 baud, 8N1.
// Provides serialPutChar and serialPrint for kernel logging to the
// host terminal via QEMU's -serial stdio.

package main

import "unsafe" // required for go:linkname and unsafe.Pointer

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
func serialPrint(s string) {
	for i := 0; i < len(s); i++ {
		serialPutChar(s[i])
	}
}

// serialPrintln sends a string followed by a newline to COM1.
func serialPrintln(s string) {
	serialPrint(s)
	serialPutChar('\r')
	serialPutChar('\n')
}

// ---------- Serial output task (microkernel service) ----------

// Pool size for serialSend messages, matches channel capacity.
const serialMsgPoolSize = 16

var (
	serialChannel *Channel                  // channel for task-context serial output
	serialMsgs    [serialMsgPoolSize]string  // static pool of message slots
	serialMsgNext int                        // next pool slot index (ring)
)

// serialTaskEntryAddr returns the address of serialTaskEntry. Implemented in switch.S.
//
//go:linkname serialTaskEntryAddr serialTaskEntryAddr
func serialTaskEntryAddr() uintptr

// serialTaskEntry loops receiving string pointers from serialChannel and
// writing each string's bytes to COM1. Runs as a dedicated kernel task.
//
//export serialTaskEntry
func serialTaskEntry() {
	sti()
	serialPrintln("Serial task: started")
	for {
		val := chanRecv(serialChannel)
		sp := (*string)(unsafe.Pointer(val))
		s := *sp
		for i := 0; i < len(s); i++ {
			serialPutChar(s[i])
		}
	}
}

// serialSend sends a string message to the serial output task via channel.
// Blocking call — only use from task context (not from interrupts or early boot).
// The string is stored in a static pool slot so the pointer remains valid
// until the consumer task processes it.
func serialSend(msg string) {
	idx := serialMsgNext
	serialMsgNext = (serialMsgNext + 1) % serialMsgPoolSize
	serialMsgs[idx] = msg
	chanSend(serialChannel, uintptr(unsafe.Pointer(&serialMsgs[idx])))
}
