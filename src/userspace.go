// src/userspace.go -- Ring 3 user mode support and syscall handling.
//
// setupUserspace loads a user ELF binary from the in-memory filesystem
// via elfLoad, which maps PT_LOAD segments, allocates a user stack, and
// jumps to Ring 3. See elf.go for the loader implementation.
//
// Syscall dispatch: go_interrupt_handler (interrupt.go) passes a pointer
// to the saved register frame for vector 0x80. syscallDispatch reads
// RAX as the syscall number and dispatches to the appropriate handler.
// Arguments in RDI, RSI, RDX, R10, R8, R9; return value written to RAX.

package main

import "unsafe"

// SyscallFrame matches the register layout pushed by isr_common in isr.S.
// Fields are ordered from lowest stack address (R15, pushed last) to
// highest (SS, pushed by CPU on privilege change).
type SyscallFrame struct {
	R15       uintptr
	R14       uintptr
	R13       uintptr
	R12       uintptr
	R11       uintptr
	R10       uintptr
	R9        uintptr
	R8        uintptr
	RBP       uintptr
	RDI       uintptr
	RSI       uintptr
	RDX       uintptr
	RCX       uintptr
	RBX       uintptr
	RAX       uintptr
	Vector    uintptr
	ErrorCode uintptr
	RIP       uintptr
	CS        uintptr
	RFLAGS    uintptr
	RSP       uintptr
	SS        uintptr
}

// Syscall numbers.
const (
	sysYield = 0
	sysExit  = 1
	sysPrint = 5
)

// syscallDispatch reads the syscall number from frame.RAX and dispatches
// to the appropriate handler function.
func syscallDispatch(frame *SyscallFrame) {
	switch frame.RAX {
	case sysYield:
		sysYieldHandler(frame)
	case sysExit:
		sysExitHandler(frame)
	case sysPrint:
		sysPrintHandler(frame)
	default:
		frame.RAX = 0xFFFFFFFFFFFFFFFF // -1 for invalid syscall
	}
}

// sysYieldHandler implements sys_yield (0): voluntarily relinquish the CPU.
func sysYieldHandler(frame *SyscallFrame) {
	yield()
	frame.RAX = 0
}

// sysExitHandler implements sys_exit (1): terminate the current task.
// Sets task state to taskExited so it is never rescheduled.
func sysExitHandler(frame *SyscallFrame) {
	tasks[currentTask].State = taskExited
	schedule()
	// Does not return if another ready task exists.
}

// sysPrintHandler implements sys_print (5): write a user buffer to serial.
// RDI = buffer virtual address, RSI = buffer length.
func sysPrintHandler(frame *SyscallFrame) {
	buf := frame.RDI
	length := frame.RSI
	if length > 4096 {
		length = 4096
	}
	for i := uintptr(0); i < length; i++ {
		ch := *(*byte)(unsafe.Pointer(buf + i))
		serialPutChar(ch)
	}
	serialPutChar('\n')
	vgaWriteLine(18, "User: Hello from Ring 3!")
	frame.RAX = 0
}

// jumpToRing3 transitions the CPU to Ring 3 user mode via iretq.
// Builds an iretq stack frame (SS, RSP, RFLAGS, CS, RIP) and executes
// iretq to switch to the user code at userRIP with stack at userRSP.
// Does not return.
// Implemented in stubs.S.
//
//go:linkname jumpToRing3 jumpToRing3
func jumpToRing3(userRIP uintptr, userRSP uintptr)

// userElfSize is the size of the hand-crafted user ELF64 binary.
const userElfSize = 162

// userElfBinary holds a minimal ELF64 executable that calls sys_print
// via int 0x80 to output "Hello from Ring 3!" on serial.
// Populated by buildUserElf() before being stored in the filesystem.
var userElfBinary [userElfSize]byte

// buildUserElf constructs the user ELF binary in userElfBinary.
// Must be called before storing the binary in the filesystem.
//
// Binary layout (162 bytes):
//   [0..63]   ELF64 header — entry=0x40010078, phoff=64, phnum=1
//   [64..119] Program header — PT_LOAD, vaddr=0x40010000, filesz=162, memsz=4096
//   [120..161] User code + "Hello from Ring 3!" string data
func buildUserElf() {
	// -- ELF64 header (64 bytes) --
	userElfBinary[0] = 0x7f
	userElfBinary[1] = 'E'
	userElfBinary[2] = 'L'
	userElfBinary[3] = 'F'
	userElfBinary[4] = 2 // ELFCLASS64
	userElfBinary[5] = 1 // ELFDATA2LSB
	userElfBinary[6] = 1 // EV_CURRENT
	// e_type = ET_EXEC (2) at offset 16
	userElfBinary[16] = 2
	// e_machine = EM_X86_64 (0x3E) at offset 18
	userElfBinary[18] = 0x3E
	// e_version = 1 at offset 20
	userElfBinary[20] = 1
	// e_entry = 0x40010078 at offset 24 (code at file offset 120 + vaddr 0x40010000)
	userElfBinary[24] = 0x78
	userElfBinary[26] = 0x01
	userElfBinary[27] = 0x40
	// e_phoff = 64 at offset 32
	userElfBinary[32] = 64
	// e_ehsize = 64 at offset 52
	userElfBinary[52] = 64
	// e_phentsize = 56 at offset 54
	userElfBinary[54] = 56
	// e_phnum = 1 at offset 56
	userElfBinary[56] = 1

	// -- Program header (56 bytes at offset 64) --
	// p_type = PT_LOAD (1)
	userElfBinary[64] = 1
	// p_flags = PF_R|PF_X (5)
	userElfBinary[68] = 5
	// p_vaddr = 0x40010000 at offset 80 (8 bytes LE)
	userElfBinary[82] = 0x01
	userElfBinary[83] = 0x40
	// p_paddr = 0x40010000 at offset 88 (8 bytes LE)
	userElfBinary[90] = 0x01
	userElfBinary[91] = 0x40
	// p_filesz = 162 (0xA2) at offset 96
	userElfBinary[96] = 0xA2
	// p_memsz = 4096 (0x1000) at offset 104
	userElfBinary[105] = 0x10
	// p_align = 4096 (0x1000) at offset 112
	userElfBinary[113] = 0x10

	// -- User code at offset 120 (mapped at vaddr 0x40010078) --
	//   mov eax, 5              ; sys_print
	//   movabs rdi, 0x40010090  ; string address (code + 24)
	//   mov esi, 18             ; string length
	//   int 0x80                ; invoke syscall
	//   jmp $                   ; infinite loop
	//   "Hello from Ring 3!"    ; string data
	userElfBinary[120] = 0xB8 // mov eax, imm32
	userElfBinary[121] = 0x05
	userElfBinary[125] = 0x48 // movabs rdi, imm64
	userElfBinary[126] = 0xBF
	userElfBinary[127] = 0x90 // 0x40010090 LE
	userElfBinary[129] = 0x01
	userElfBinary[130] = 0x40
	userElfBinary[135] = 0xBE // mov esi, imm32
	userElfBinary[136] = 0x12
	userElfBinary[140] = 0xCD // int 0x80
	userElfBinary[141] = 0x80
	userElfBinary[142] = 0xEB // jmp $ (short jump -2)
	userElfBinary[143] = 0xFE
	// "Hello from Ring 3!" at offset 144
	msg := "Hello from Ring 3!"
	for i := 0; i < len(msg); i++ {
		userElfBinary[144+i] = msg[i]
	}
}

// setupUserspace loads the user ELF binary from the filesystem and
// transitions to Ring 3. Does not return on success.
func setupUserspace() {
	if !elfLoad("user.elf") {
		serialPrintln("Userspace: ELF load failed, halting")
		for {
			hlt()
		}
	}
}
