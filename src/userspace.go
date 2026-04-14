// src/userspace.go -- Ring 3 user mode support and syscall handling.
//
// Allocates user-accessible code and stack pages, writes a minimal user
// program, sets the int 0x80 IDT gate to DPL=3, and provides the
// jumpToRing3 entry point for transitioning to user mode via iretq.
//
// Syscall dispatch: go_interrupt_handler (interrupt.go) passes a pointer
// to the saved register frame for vector 0x80. syscallDispatch reads
// RAX as the syscall number and dispatches to the appropriate handler.
// Arguments in RDI, RSI, RDX, R10, R8, R9; return value written to RAX.

package main

import "unsafe"

// Virtual addresses for user code and stack (outside the boot 1 GiB identity map).
const (
	userCodeVaddr  = uintptr(0x40010000)
	userStackVaddr = uintptr(0x40020000)
)

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

// User program machine code (x86_64, 42 bytes):
//
//	mov eax, 5                      ; syscall 5 = sys_print
//	movabs rdi, 0x40010018          ; buffer address (string at code + 24)
//	mov esi, 18                     ; buffer length
//	int 0x80                        ; invoke syscall
//	jmp $                           ; infinite loop
//	db "Hello from Ring 3!"         ; string data
var userProgram = [42]byte{
	0xB8, 0x05, 0x00, 0x00, 0x00, // mov eax, 5
	0x48, 0xBF, 0x18, 0x00, 0x01, 0x40, 0x00, 0x00, 0x00, 0x00, // movabs rdi, 0x40010018
	0xBE, 0x12, 0x00, 0x00, 0x00, // mov esi, 18
	0xCD, 0x80, // int 0x80
	0xEB, 0xFE, // jmp $
	'H', 'e', 'l', 'l', 'o', ' ', 'f', 'r', 'o', 'm', ' ', 'R', 'i', 'n', 'g', ' ', '3', '!',
}

// jumpToRing3 transitions the CPU to Ring 3 user mode via iretq.
// Builds an iretq stack frame (SS, RSP, RFLAGS, CS, RIP) and executes
// iretq to switch to the user code at userRIP with stack at userRSP.
// Does not return.
// Implemented in stubs.S.
//
//go:linkname jumpToRing3 jumpToRing3
func jumpToRing3(userRIP uintptr, userRSP uintptr)

// setupUserspace prepares user-mode pages, sets the IDT entry for int 0x80
// to DPL=3, and jumps to Ring 3. Does not return.
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

	// Allow Ring 3 to trigger int 0x80 (DPL=3 in IDT gate).
	setGateDPL3(0x80)

	serialPrintln("Userspace: jumping to Ring 3")

	// Jump to user mode — does not return.
	userStackTop := userStackVaddr + pageSize
	jumpToRing3(userCodeVaddr, userStackTop)
}
