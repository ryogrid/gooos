// src/userspace.go -- Ring 3 user mode support and syscall handling.
//
// Allocates user-accessible code and stack pages, writes a minimal user
// program, registers the int 0x80 syscall handler, and provides the
// jumpToRing3 entry point for transitioning to user mode via iretq.

package main

import "unsafe"

// Virtual addresses for user code and stack (outside the boot 1 GiB identity map).
const (
	userCodeVaddr  = uintptr(0x40010000)
	userStackVaddr = uintptr(0x40020000)
)

// User program machine code (x86_64, 6 bytes):
//
//	31 C0      xor eax, eax    ; syscall 0 = print
//	CD 80      int $0x80       ; invoke syscall
//	EB FE      jmp $           ; infinite loop
var userProgram = [6]byte{0x31, 0xC0, 0xCD, 0x80, 0xEB, 0xFE}

// jumpToRing3 transitions the CPU to Ring 3 user mode via iretq.
// Builds an iretq stack frame (SS, RSP, RFLAGS, CS, RIP) and executes
// iretq to switch to the user code at userRIP with stack at userRSP.
// Does not return.
// Implemented in stubs.S.
//
//go:linkname jumpToRing3 jumpToRing3
func jumpToRing3(userRIP uintptr, userRSP uintptr)

// setupUserspace prepares user-mode pages, registers the syscall handler,
// sets the IDT entry for int 0x80 to DPL=3, and jumps to Ring 3.
// Does not return.
func setupUserspace() {
	// Allocate physical pages for user code and stack.
	codePaddr := allocPage()
	stackPaddr := allocPage()

	// Map with user-accessible permissions (Present | Writable | User).
	userFlags := uintptr(pagePresent | pageWrite | pageUser)
	mapPage(userCodeVaddr, codePaddr, userFlags)
	mapPage(userStackVaddr, stackPaddr, userFlags)

	// Copy user program into the code page (via identity-mapped physical address).
	for i := 0; i < len(userProgram); i++ {
		*(*byte)(unsafe.Pointer(codePaddr + uintptr(i))) = userProgram[i]
	}

	// Register int 0x80 as the syscall vector and allow Ring 3 access.
	registerHandler(0x80, syscallHandler)
	setGateDPL3(0x80)

	serialPrintln("Userspace: jumping to Ring 3")

	// Jump to user mode — does not return.
	userStackTop := userStackVaddr + pageSize
	jumpToRing3(userCodeVaddr, userStackTop)
}

// syscallHandler handles int 0x80 from user mode.
// Syscall 0 prints a greeting to VGA and serial, demonstrating a
// successful Ring 3 -> Ring 0 transition via the interrupt mechanism.
func syscallHandler(vector uint64) {
	vgaWriteLine(18, "User: Hello from Ring 3!")
	serialPrintln("User: Hello from Ring 3!")
}
