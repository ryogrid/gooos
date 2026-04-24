// src/elf.go -- ELF64 parser and loader.
//
// Parses ELF64 binaries from byte slices, validating the magic number,
// class (64-bit), machine (x86_64), and type (executable). Returns the
// entry point address and a slice of PT_LOAD program headers for loading.
//
// elfLoad reads an ELF binary from the in-memory filesystem, maps its
// PT_LOAD segments into userspace, allocates a user stack, and jumps
// to Ring 3 at the entry point.
//
// Uses manual little-endian byte reading — no encoding/binary dependency.

package main

import "unsafe"

// ELF identification constants.
const (
	elfMagic0   = 0x7f
	elfMagic1   = 'E'
	elfMagic2   = 'L'
	elfMagic3   = 'F'
	elfClass64  = 2    // 64-bit ELF
	elfData2LSB = 1    // little-endian
	etExec      = 2    // executable file
	emX86_64    = 0x3E // AMD x86-64

	ptLoad = 1 // loadable segment

	elf64HdrSize      = 64 // sizeof(Elf64_Ehdr)
	elf64PhdrSize     = 56 // sizeof(Elf64_Phdr)
	maxPTLoadSegments = 16 // maximum PT_LOAD segments we track
)

// Elf64Ehdr represents the ELF64 file header fields needed for loading.
type Elf64Ehdr struct {
	Ident     [16]byte
	Type      uint16
	Machine   uint16
	Entry     uintptr
	Phoff     uint64
	Phentsize uint16
	Phnum     uint16
}

// Elf64Phdr represents an ELF64 program header entry.
type Elf64Phdr struct {
	Type   uint32
	Flags  uint32
	Offset uint64
	Vaddr  uintptr
	Paddr  uintptr
	Filesz uint64
	Memsz  uint64
	Align  uint64
}

// readU16LE reads a little-endian uint16 from data at the given offset.
func readU16LE(data []byte, off int) uint16 {
	return uint16(data[off]) | uint16(data[off+1])<<8
}

// readU32LE reads a little-endian uint32 from data at the given offset.
func readU32LE(data []byte, off int) uint32 {
	return uint32(data[off]) | uint32(data[off+1])<<8 |
		uint32(data[off+2])<<16 | uint32(data[off+3])<<24
}

// readU64LE reads a little-endian uint64 from data at the given offset.
func readU64LE(data []byte, off int) uint64 {
	return uint64(data[off]) | uint64(data[off+1])<<8 |
		uint64(data[off+2])<<16 | uint64(data[off+3])<<24 |
		uint64(data[off+4])<<32 | uint64(data[off+5])<<40 |
		uint64(data[off+6])<<48 | uint64(data[off+7])<<56
}

// elfPTLoadBuf is a static buffer for PT_LOAD program headers returned by elfParse.
// Avoids heap allocation and the memmove dependency that append would require.
var (
	elfPTLoadBuf   [maxPTLoadSegments]Elf64Phdr
	elfPTLoadCount int
)

// elfParse validates an ELF64 binary and extracts the entry point and
// PT_LOAD program headers. Returns ok=false if any validation fails:
// bad magic, not ELFCLASS64, not little-endian, not ET_EXEC, not EM_X86_64.
//
// The returned slice is backed by a static buffer; callers must consume
// the data before calling elfParse again.
func elfParse(data []byte) (entry uintptr, phdrs []Elf64Phdr, ok bool) {
	elfPTLoadCount = 0

	// Need at least the ELF header.
	if len(data) < elf64HdrSize {
		return 0, nil, false
	}

	// Validate ELF magic: 0x7f 'E' 'L' 'F'.
	if data[0] != elfMagic0 || data[1] != elfMagic1 || data[2] != elfMagic2 || data[3] != elfMagic3 {
		return 0, nil, false
	}

	// Validate ELFCLASS64.
	if data[4] != elfClass64 {
		return 0, nil, false
	}

	// Validate little-endian encoding.
	if data[5] != elfData2LSB {
		return 0, nil, false
	}

	// Validate e_type = ET_EXEC.
	eType := readU16LE(data, 16)
	if eType != etExec {
		return 0, nil, false
	}

	// Validate e_machine = EM_X86_64.
	eMachine := readU16LE(data, 18)
	if eMachine != emX86_64 {
		return 0, nil, false
	}

	// Read entry point (offset 24, 8 bytes).
	entry = uintptr(readU64LE(data, 24))

	// Read program header table location.
	phoff := readU64LE(data, 32)
	phentsize := readU16LE(data, 54)
	phnum := readU16LE(data, 56)

	// Validate program header entry size.
	if phentsize < elf64PhdrSize {
		return 0, nil, false
	}

	// Validate data contains all program headers.
	phEnd := phoff + uint64(phnum)*uint64(phentsize)
	if uint64(len(data)) < phEnd {
		return 0, nil, false
	}

	// Extract PT_LOAD segments into static buffer.
	for i := uint16(0); i < phnum; i++ {
		off := int(phoff) + int(i)*int(phentsize)
		pType := readU32LE(data, off)
		if pType == ptLoad && elfPTLoadCount < maxPTLoadSegments {
			elfPTLoadBuf[elfPTLoadCount] = Elf64Phdr{
				Type:   pType,
				Flags:  readU32LE(data, off+4),
				Offset: readU64LE(data, off+8),
				Vaddr:  uintptr(readU64LE(data, off+16)),
				Paddr:  uintptr(readU64LE(data, off+24)),
				Filesz: readU64LE(data, off+32),
				Memsz:  readU64LE(data, off+40),
				Align:  readU64LE(data, off+48),
			}
			elfPTLoadCount++
		}
	}

	return entry, elfPTLoadBuf[:elfPTLoadCount], true
}

// elfLoad reads an ELF64 binary from the filesystem via the FS task channel,
// validates it, maps PT_LOAD segments into userspace memory, allocates a user
// stack, and jumps to Ring 3 at the entry point. Does not return on success.
// Returns false if the file is not found or the ELF is invalid.
func elfLoad(name string, args string) bool {
	// Read the ELF binary from the filesystem via the FS task.
	data := fsSendRead(name)
	if data == nil {
		serialPrintln("ELF: file not found: " + name)
		return false
	}

	// Parse and validate ELF headers.
	entry, phdrs, ok := elfParse(data)
	if !ok {
		serialPrintln("ELF: invalid ELF: " + name)
		return false
	}

	serialPrintln("ELF: loading " + name + ", entry=0x" + hextoa(uint64(entry)) +
		", " + utoa(uint64(len(phdrs))) + " PT_LOAD segment(s)")

	// Phase B: allocate a fresh Process for the boot shell. No
	// parent — processExit signals ExitEv but the main goroutine
	// is what Waits on it.
	// Route C M4.1: ExitEv (KEvent) replaces exitCh.
	proc := &Process{parent: nil, poolIdx: -1}
	procInitStdio(proc)     // boot shell gets console fds 0,1,2
	setForegroundProc(proc) // boot shell starts as foreground
	proc.ArgLen = len(args)
	if proc.ArgLen > 256 {
		proc.ArgLen = 256
	}
	for i := 0; i < proc.ArgLen; i++ {
		proc.ArgString[i] = args[i]
	}
	userFlags := uintptr(pagePresent | pageWrite | pageUser)

	// Map and load each PT_LOAD segment.
	for i := 0; i < len(phdrs); i++ {
		ph := &phdrs[i]
		startPage := ph.Vaddr &^ (pageSize - 1)
		endAddr := ph.Vaddr + uintptr(ph.Memsz)

		for addr := startPage; addr < endAddr; addr += pageSize {
			// Skip if this page is already mapped (segments may overlap pages).
			if walkAndGetPaddr(addr) != 0 {
				continue
			}
			paddr := allocPage()
			mapPage(addr, paddr, userFlags)
			processRecordPage(proc, addr, paddr)
		}

		// Copy p_filesz bytes from file data to the mapped virtual address.
		for j := uint64(0); j < ph.Filesz; j++ {
			*(*byte)(unsafe.Pointer(ph.Vaddr + uintptr(j))) = data[ph.Offset+j]
		}
	}

	// Allocate user stack: 4 pages (16 KiB) at userStackBase.
	// Keep initial RSP one page below the mapped top so boundary
	// accesses above RSP don't fault at process start.
	for i := uintptr(0); i < 4; i++ {
		paddr := allocPage()
		mapPage(userStackBase+i*pageSize, paddr, userFlags)
		processRecordPage(proc, userStackBase+i*pageSize, paddr)
	}
	stackTop := userStackBase + 3*pageSize - 8

	// Set heap break to end of last PT_LOAD (page-aligned up) and
	// install the per-process heap ceiling. See userHeapLimit in
	// src/process.go.
	if len(phdrs) > 0 {
		lastPh := &phdrs[len(phdrs)-1]
		proc.HeapBreak = (lastPh.Vaddr + uintptr(lastPh.Memsz) + pageSize - 1) &^ (pageSize - 1)
		proc.HeapLimit = proc.HeapBreak + userHeapLimit
	}

	proc.EntryPoint = entry
	proc.StackTop = stackTop

	serialPrintln("ELF: spawning boot shell goroutine at 0x" + hextoa(uint64(entry)))

	// Route C M4.1: host the boot shell on a gooos kernel
	// thread. main() (this caller) then blocks on proc.ExitEv —
	// if the shell ever exits, the kernel halts.
	kschedInit()
	kschedSpawnProc("ring3Wrapper", func() { ring3Wrapper(proc) }, proc)
	proc.ExitEv.Wait()
	serialPrintln("ELF: boot shell exited, halting")
	for {
		hlt()
	}
}
