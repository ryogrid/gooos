// src/gdt.go -- GDT with Ring 3 segments and TSS for userspace support.
//
// Replaces the boot-time GDT (from boot.S) with a new GDT that includes
// Ring 3 code and data segments (DPL=3) and a Task State Segment (TSS)
// for automatic kernel stack switching on privilege transitions.

package main

import "unsafe"

// GDT entry indices.
const (
	gdtNull       = 0 // 0x00: required null descriptor
	gdtKernelCode = 1 // 0x08: Ring 0 64-bit code segment
	gdtKernelData = 2 // 0x10: Ring 0 data segment
	gdtUserCode   = 3 // 0x18: Ring 3 64-bit code segment
	gdtUserData   = 4 // 0x20: Ring 3 data segment
	gdtTSSLow     = 5 // 0x28: TSS descriptor low 8 bytes
	gdtTSSHigh    = 6 // 0x30: TSS descriptor high 8 bytes (base[63:32])
	gdtEntries    = 7
)

// Segment selectors (GDT index * 8, with RPL bits for user segments).
const (
	selectorKernelCS uint16 = 0x08
	selectorKernelDS uint16 = 0x10
	selectorUserCS   uint16 = 0x1B // 0x18 | RPL=3
	selectorUserDS   uint16 = 0x23 // 0x20 | RPL=3
	selectorTSS      uint16 = 0x28
)

// TSS size in bytes (x86_64 long mode Task State Segment).
const tssSize = 104

var (
	gdtTable [gdtEntries]uint64
	tss      [tssSize]byte
	gdtPtr   [10]byte // packed descriptor for lgdt: 2-byte limit + 8-byte base

	// Per-CPU GDT and TSS arrays for SMP v2. Each CPU loads its
	// own GDT (with its own TSS descriptor) via lgdt + ltr.
	perCPUGDT    [maxCPUs][gdtEntries]uint64
	perCPUTSS    [maxCPUs][tssSize]byte
	perCPUGDTPtr [maxCPUs][10]byte
)

// lgdtReload loads a new GDT and reloads all segment registers.
// Implemented in stubs.S.
//
//go:linkname lgdtReload lgdtReload
func lgdtReload(desc uintptr)

// ltr loads the Task Register with the given selector.
// Implemented in stubs.S.
//
//go:linkname ltr ltr
func ltr(selector uint16)

// gdtInit builds the GDT with Ring 0 and Ring 3 segments plus a TSS,
// loads the new GDT via lgdt, reloads segment registers, and loads
// the Task Register.
func gdtInit() {
	// Entry 0: null descriptor (required).
	gdtTable[gdtNull] = 0

	// Entry 1 (0x08): kernel code — same bits as boot.S GDT64_CODE.
	// Bits: 43=Executable, 44=S(code/data), 47=Present, 53=Long mode.
	gdtTable[gdtKernelCode] = (1 << 43) | (1 << 44) | (1 << 47) | (1 << 53)

	// Entry 2 (0x10): kernel data — same bits as boot.S GDT64_DATA.
	// Bits: 41=Writable, 44=S(code/data), 47=Present.
	gdtTable[gdtKernelData] = (1 << 41) | (1 << 44) | (1 << 47)

	// Entry 3 (0x18): user code — kernel code + DPL=3.
	// Bits: 43=Exec, 44=S, 45-46=DPL(3), 47=Present, 53=Long.
	gdtTable[gdtUserCode] = (1 << 43) | (1 << 44) | (1 << 45) | (1 << 46) | (1 << 47) | (1 << 53)

	// Entry 4 (0x20): user data — kernel data + DPL=3.
	// Bits: 41=Writable, 44=S, 45-46=DPL(3), 47=Present.
	gdtTable[gdtUserData] = (1 << 41) | (1 << 44) | (1 << 45) | (1 << 46) | (1 << 47)

	// Zero the TSS, then fill in IOPB offset.
	// RSP0 is set per-task by tssSetRSP0() during context switches.
	for i := 0; i < tssSize; i++ {
		tss[i] = 0
	}

	// IOPB offset (offset 102): set to tssSize (no I/O permission bitmap).
	*(*uint16)(unsafe.Pointer(&tss[102])) = tssSize

	// Build the 16-byte TSS descriptor (occupies 2 consecutive GDT entries).
	tssBase := uint64(uintptr(unsafe.Pointer(&tss[0])))
	tssLimit := uint64(tssSize - 1)

	// Low 8 bytes: limit[15:0], base[23:0], type+flags, limit[19:16], base[31:24].
	var low uint64
	low |= tssLimit & 0xFFFF                  // Limit[15:0]
	low |= ((tssBase & 0xFFFF) << 16)         // Base[15:0]
	low |= ((tssBase >> 16) & 0xFF) << 32     // Base[23:16]
	low |= uint64(0x89) << 40                 // P=1, DPL=0, Type=0x9 (64-bit TSS, available)
	low |= ((tssLimit >> 16) & 0xF) << 48     // Limit[19:16]
	low |= ((tssBase >> 24) & 0xFF) << 56     // Base[31:24]
	gdtTable[gdtTSSLow] = low

	// High 8 bytes: base[63:32] + reserved zero.
	gdtTable[gdtTSSHigh] = tssBase >> 32

	// Pack GDT pointer: 2-byte limit + 8-byte base address.
	limit := uint16(unsafe.Sizeof(gdtTable) - 1)
	base := uint64(uintptr(unsafe.Pointer(&gdtTable[0])))
	gdtPtr[0] = byte(limit)
	gdtPtr[1] = byte(limit >> 8)
	gdtPtr[2] = byte(base)
	gdtPtr[3] = byte(base >> 8)
	gdtPtr[4] = byte(base >> 16)
	gdtPtr[5] = byte(base >> 24)
	gdtPtr[6] = byte(base >> 32)
	gdtPtr[7] = byte(base >> 40)
	gdtPtr[8] = byte(base >> 48)
	gdtPtr[9] = byte(base >> 56)

	// Load the new GDT and reload all segment registers.
	lgdtReload(uintptr(unsafe.Pointer(&gdtPtr[0])))

	// Load the Task State Segment into the Task Register.
	ltr(selectorTSS)

	// Build the BSP's per-CPU GDT + TSS (SMP v2). This copies
	// the canonical gdtTable entries into perCPUGDT[0] and
	// installs a per-CPU TSS descriptor. From this point on,
	// tssSetRSP0 writes perCPUTSS[0] (via cpuID()).
	gdtInitPerCPU(0)
}

// tssSetRSP0 updates the current CPU's TSS RSP0 field (offset 4) to
// point to the given kernel stack top. Called during context switches
// so each task has its own kernel stack for Ring 3 → Ring 0
// transitions.
//
//go:nosplit
func tssSetRSP0(rsp0 uintptr) {
	idx := cpuID()
	*(*uint64)(unsafe.Pointer(&perCPUTSS[idx][4])) = uint64(rsp0)
}

// gdtInitPerCPU builds a per-CPU GDT with the same segment layout
// as the BSP's boot GDT, but with a TSS descriptor pointing at the
// per-CPU TSS. Loads the new GDT via lgdt and the TSS via ltr.
//
// cpuIdx: 0 for BSP, apIndex+1 for APs.
func gdtInitPerCPU(cpuIdx int) {
	// Copy segment descriptors from the BSP's canonical GDT.
	// Entries 0-4 are identical for all CPUs.
	perCPUGDT[cpuIdx][gdtNull] = gdtTable[gdtNull]
	perCPUGDT[cpuIdx][gdtKernelCode] = gdtTable[gdtKernelCode]
	perCPUGDT[cpuIdx][gdtKernelData] = gdtTable[gdtKernelData]
	perCPUGDT[cpuIdx][gdtUserCode] = gdtTable[gdtUserCode]
	perCPUGDT[cpuIdx][gdtUserData] = gdtTable[gdtUserData]

	// Zero this CPU's TSS, set IOPB offset.
	for i := 0; i < tssSize; i++ {
		perCPUTSS[cpuIdx][i] = 0
	}
	*(*uint16)(unsafe.Pointer(&perCPUTSS[cpuIdx][102])) = tssSize

	// Build TSS descriptor pointing at this CPU's TSS.
	tssBase := uint64(uintptr(unsafe.Pointer(&perCPUTSS[cpuIdx][0])))
	tssLimit := uint64(tssSize - 1)

	var low uint64
	low |= tssLimit & 0xFFFF
	low |= ((tssBase & 0xFFFF) << 16)
	low |= ((tssBase >> 16) & 0xFF) << 32
	low |= uint64(0x89) << 40
	low |= ((tssLimit >> 16) & 0xF) << 48
	low |= ((tssBase >> 24) & 0xFF) << 56
	perCPUGDT[cpuIdx][gdtTSSLow] = low
	perCPUGDT[cpuIdx][gdtTSSHigh] = tssBase >> 32

	// Pack GDT pointer.
	limit := uint16(unsafe.Sizeof(perCPUGDT[cpuIdx]) - 1)
	base := uint64(uintptr(unsafe.Pointer(&perCPUGDT[cpuIdx][0])))
	perCPUGDTPtr[cpuIdx][0] = byte(limit)
	perCPUGDTPtr[cpuIdx][1] = byte(limit >> 8)
	perCPUGDTPtr[cpuIdx][2] = byte(base)
	perCPUGDTPtr[cpuIdx][3] = byte(base >> 8)
	perCPUGDTPtr[cpuIdx][4] = byte(base >> 16)
	perCPUGDTPtr[cpuIdx][5] = byte(base >> 24)
	perCPUGDTPtr[cpuIdx][6] = byte(base >> 32)
	perCPUGDTPtr[cpuIdx][7] = byte(base >> 40)
	perCPUGDTPtr[cpuIdx][8] = byte(base >> 48)
	perCPUGDTPtr[cpuIdx][9] = byte(base >> 56)

	// Load the per-CPU GDT and reload segment registers.
	// NOTE: lgdtReload reloads GS to the flat data selector (0x10),
	// which clears the GS base. Re-set it immediately after.
	lgdtReload(uintptr(unsafe.Pointer(&perCPUGDTPtr[cpuIdx][0])))
	wrmsr(ia32GSBASE, uint64(uintptr(unsafe.Pointer(&perCPUBlocks[cpuIdx]))))

	// Load the per-CPU TSS.
	ltr(selectorTSS)
}
