// src/pci.go -- Minimal PCI bus scan, limited to locating one e1000 NIC.
//
// Uses legacy I/O-port configuration space (x86 mechanism #1): write a
// 32-bit address word to port 0xCF8, then read/write the 32-bit data
// word at 0xCFC. Enumerates all 256 buses × 32 devices × 8 functions
// until it finds Intel vendor 0x8086 device 0x100E (e1000, 82540EM).
//
// For the first such device, we:
//   1. Record bus/device/function, VendorID/DeviceID, BAR0, IRQ Line.
//   2. Enable Memory Space + Bus Master in the Command register.
//   3. Assert BAR0 is MMIO (bit 0 = 0), not I/O space.
//
// No attempt is made to handle IDE, multi-function enumeration quirks,
// or capability-list traversal. See impldoc/net_pci_e1000_driver.md §2.

package main

// outl writes a 32-bit value to an I/O port. Implemented in stubs.S.
//
//go:linkname outl outl
func outl(port uint16, val uint32)

// inl reads a 32-bit value from an I/O port. Implemented in stubs.S.
//
//go:linkname inl inl
func inl(port uint16) uint32

// PCI configuration mechanism #1 ports.
const (
	pciConfigAddr = uint16(0xCF8)
	pciConfigData = uint16(0xCFC)
)

// PCI config space offsets (all dword-aligned).
const (
	pciOffVendorID = uint8(0x00) // low 16 bits
	pciOffDeviceID = uint8(0x00) // high 16 bits of the same dword
	pciOffCommand  = uint8(0x04) // low 16 bits
	pciOffStatus   = uint8(0x04) // high 16 bits
	pciOffHdrType  = uint8(0x0C) // byte at +2
	pciOffBAR0     = uint8(0x10)
	pciOffIntLine  = uint8(0x3C) // low 8 bits
)

// PCI Command register bits.
const (
	pciCmdIOSpace   = uint16(1 << 0)
	pciCmdMemSpace  = uint16(1 << 1)
	pciCmdBusMaster = uint16(1 << 2)
)

// Intel e1000 (82540EM) identifiers.
const (
	e1000VendorID = uint16(0x8086)
	e1000DeviceID = uint16(0x100E)
)

// PCIDevice describes a located PCI device of interest.
type PCIDevice struct {
	Bus, Device, Function uint8
	VendorID, DeviceID    uint16
	BAR0                  uint32
	IRQLine               uint8
}

var (
	e1000PCI   PCIDevice
	e1000Found bool
)

// pciConfigAddress composes a mechanism-#1 address word.
//
//go:nosplit
func pciConfigAddress(bus, device, function, offset uint8) uint32 {
	// Register number must be dword-aligned.
	return uint32(1)<<31 |
		uint32(bus)<<16 |
		uint32(device&0x1F)<<11 |
		uint32(function&0x07)<<8 |
		uint32(offset&0xFC)
}

// pciConfigRead32 reads a 32-bit dword from PCI config space. `offset`
// should be 4-byte aligned; the lower 2 bits are masked off.
func pciConfigRead32(bus, device, function, offset uint8) uint32 {
	outl(pciConfigAddr, pciConfigAddress(bus, device, function, offset))
	return inl(pciConfigData)
}

// pciConfigWrite32 writes a 32-bit dword to PCI config space.
func pciConfigWrite32(bus, device, function, offset uint8, val uint32) {
	outl(pciConfigAddr, pciConfigAddress(bus, device, function, offset))
	outl(pciConfigData, val)
}

// pciInit scans all PCI buses for an Intel e1000 NIC. On first match,
// records its location and enables Memory Space + Bus Master in the
// Command register. Logs the result to serial.
//
// Safe to call before SMP is active. No locking needed (single caller).
func pciInit() {
	for bus := 0; bus < 256; bus++ {
		for dev := 0; dev < 32; dev++ {
			for fn := 0; fn < 8; fn++ {
				idDword := pciConfigRead32(uint8(bus), uint8(dev), uint8(fn), pciOffVendorID)
				vendor := uint16(idDword & 0xFFFF)
				if vendor == 0xFFFF {
					// No device at this function.
					if fn == 0 {
						break
					}
					continue
				}
				device := uint16(idDword >> 16)

				if vendor == e1000VendorID && device == e1000DeviceID && !e1000Found {
					pciRecordE1000(uint8(bus), uint8(dev), uint8(fn), vendor, device)
					// Keep scanning is pointless for our current needs;
					// return to avoid touching other devices.
					return
				}

				// If this is a single-function device (bit 7 of header
				// type clear), skip the remaining functions.
				if fn == 0 {
					hdrDword := pciConfigRead32(uint8(bus), uint8(dev), uint8(fn), pciOffHdrType)
					hdrType := uint8((hdrDword >> 16) & 0xFF)
					if hdrType&0x80 == 0 {
						break
					}
				}
			}
		}
	}

	if !e1000Found {
		serialPrintln("PCI: no e1000 NIC found")
	}
}

// pciRecordE1000 captures the device coordinates, enables Memory Space
// + Bus Master, and verifies BAR0 is MMIO.
func pciRecordE1000(bus, dev, fn uint8, vendor, device uint16) {
	bar0 := pciConfigRead32(bus, dev, fn, pciOffBAR0)
	intDword := pciConfigRead32(bus, dev, fn, pciOffIntLine)
	irqLine := uint8(intDword & 0xFF)

	// BAR0 bit 0 = 0 for memory-mapped, 1 for I/O space.
	if bar0&0x1 != 0 {
		serialPrintln("PCI: e1000 BAR0 is I/O space — unsupported")
		return
	}
	// Mask off the low 4 memory-BAR info bits to get the base.
	bar0Base := bar0 &^ uint32(0xF)

	// Enable Memory Space (bit 1) + Bus Master (bit 2) in Command.
	cmdDword := pciConfigRead32(bus, dev, fn, pciOffCommand)
	cmd := uint16(cmdDword & 0xFFFF)
	cmd |= pciCmdMemSpace | pciCmdBusMaster
	cmdDword = (cmdDword &^ 0xFFFF) | uint32(cmd)
	pciConfigWrite32(bus, dev, fn, pciOffCommand, cmdDword)

	e1000PCI = PCIDevice{
		Bus: bus, Device: dev, Function: fn,
		VendorID: vendor, DeviceID: device,
		BAR0:    bar0Base,
		IRQLine: irqLine,
	}
	e1000Found = true

	serialPrintln("PCI: found e1000 at " + utoa(uint64(bus)) + ":" +
		utoa(uint64(dev)) + "." + utoa(uint64(fn)) +
		" BAR0=0x" + hextoa(uint64(bar0Base)) +
		" IRQ=" + utoa(uint64(irqLine)))
}
