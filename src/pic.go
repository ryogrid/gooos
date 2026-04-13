// src/pic.go -- 8259A PIC (Programmable Interrupt Controller) driver.
//
// Remaps master PIC to vectors 32-39 and slave PIC to vectors 40-47,
// then unmasks all IRQs. Provides picSendEOI for acknowledging interrupts.

package main

// PIC I/O ports.
const (
	pic1Cmd  = 0x20 // Master PIC command port
	pic1Data = 0x21 // Master PIC data port
	pic2Cmd  = 0xA0 // Slave PIC command port
	pic2Data = 0xA1 // Slave PIC data port
)

// ICW (Initialization Command Word) constants.
const (
	icw1Init = 0x11 // ICW1: init + ICW4 needed
	icw4Auto = 0x01 // ICW4: 8086/88 mode (not auto EOI)
	picEOI   = 0x20 // End-of-interrupt command
)

// PIC vector offsets after remap.
const (
	picMasterOffset = 32 // Master PIC: IRQ 0-7 -> vectors 32-39
	picSlaveOffset  = 40 // Slave PIC: IRQ 8-15 -> vectors 40-47
)

// picRemap remaps the 8259A PICs so that IRQ 0-7 map to vectors 32-39
// and IRQ 8-15 map to vectors 40-47. Unmasks all IRQs after remap.
func picRemap() {
	// Save current masks.
	mask1 := inb(pic1Data)
	mask2 := inb(pic2Data)

	// ICW1: start initialization sequence (cascade mode, ICW4 needed).
	outb(pic1Cmd, icw1Init)
	outb(pic2Cmd, icw1Init)

	// ICW2: set vector offsets.
	outb(pic1Data, picMasterOffset)
	outb(pic2Data, picSlaveOffset)

	// ICW3: master has slave on IRQ2 (bit 2), slave cascade identity = 2.
	outb(pic1Data, 0x04)
	outb(pic2Data, 0x02)

	// ICW4: 8086 mode.
	outb(pic1Data, icw4Auto)
	outb(pic2Data, icw4Auto)

	// Restore saved masks.
	outb(pic1Data, mask1)
	outb(pic2Data, mask2)

	// Unmask all IRQs on both PICs (clear all mask bits).
	outb(pic1Data, 0x00)
	outb(pic2Data, 0x00)
}

// picSendEOI sends an End-of-Interrupt signal to the PIC(s).
// For IRQs 8-15 (slave PIC), EOI must be sent to both slave and master.
func picSendEOI(irq uint8) {
	if irq >= 8 {
		outb(pic2Cmd, picEOI)
	}
	outb(pic1Cmd, picEOI)
}
