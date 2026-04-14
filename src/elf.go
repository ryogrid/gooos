// src/elf.go -- ELF64 parser: read and validate ELF file headers.
//
// Parses ELF64 binaries from byte slices, validating the magic number,
// class (64-bit), machine (x86_64), and type (executable). Returns the
// entry point address and a slice of PT_LOAD program headers for loading.
//
// Uses manual little-endian byte reading — no encoding/binary dependency.

package main

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
