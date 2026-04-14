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
	sysSend  = 2
	sysRecv  = 3
	sysSpawn = 4
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
	case sysSend:
		sysSendHandler(frame)
	case sysRecv:
		sysRecvHandler(frame)
	case sysSpawn:
		sysSpawnHandler(frame)
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

// sysSendHandler implements sys_send (2): send a value to a channel.
// RDI = channel ID, RSI = user buffer address, RDX = length (max 8).
// Packs up to 8 bytes from the user buffer into a uintptr and sends via chanSend.
// Sets RAX = 0 on success, -1 on invalid channel.
func sysSendHandler(frame *SyscallFrame) {
	chID := uint64(frame.RDI)
	ch := chanLookup(chID)
	if ch == nil {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}
	ptr := frame.RSI
	length := frame.RDX
	if length > 8 {
		length = 8
	}
	var val uintptr
	for i := uintptr(0); i < length; i++ {
		b := *(*byte)(unsafe.Pointer(ptr + i))
		val |= uintptr(b) << (i * 8)
	}
	chanSend(ch, val)
	frame.RAX = 0
}

// sysRecvHandler implements sys_recv (3): receive a value from a channel.
// RDI = channel ID, RSI = user buffer address, RDX = max length (max 8).
// Calls chanRecv, unpacks the uintptr into the user buffer.
// Sets RAX = bytes written on success, -1 on invalid channel.
func sysRecvHandler(frame *SyscallFrame) {
	chID := uint64(frame.RDI)
	ch := chanLookup(chID)
	if ch == nil {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}
	val := chanRecv(ch)
	ptr := frame.RSI
	maxLen := frame.RDX
	if maxLen > 8 {
		maxLen = 8
	}
	for i := uintptr(0); i < maxLen; i++ {
		*(*byte)(unsafe.Pointer(ptr + i)) = byte(val >> (i * 8))
	}
	frame.RAX = maxLen
}

// sysSpawnHandler implements sys_spawn (4): create a new kernel task.
// RDI = entry address. Sets RAX = task ID on success, -1 if task table full.
func sysSpawnHandler(frame *SyscallFrame) {
	if taskCount >= maxTasks {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}
	tid := createTask(frame.RDI)
	frame.RAX = uintptr(tid)
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
const userElfSize = 277

// userElfBinary holds a minimal ELF64 executable that tests channel syscalls:
// sys_print, sys_recv from keyboard, sys_send to print channel, and a
// confirmation sys_print. Populated by buildUserElf() before filesystem storage.
var userElfBinary [userElfSize]byte

// buildUserElf constructs the user ELF binary in userElfBinary.
// Must be called before storing the binary in the filesystem.
//
// Binary layout (277 bytes):
//   [0..63]    ELF64 header — entry=0x40010078, phoff=64, phnum=1
//   [64..119]  Program header — PT_LOAD, vaddr=0x40010000, filesz=277, memsz=4096
//   [120..219] User code (100 bytes):
//              - sys_print("Waiting for key...")
//              - sys_recv(ch=0, &recv_buf, 8)   — keyboard channel
//              - sys_send(ch=1, &recv_buf, 8)   — print channel
//              - sys_print("Syscall: channel round-trip OK!")
//              - jmp $ (halt)
//   [220..227] recv_buf (8 bytes, zeroed)
//   [228..245] "Waiting for key..." (18 bytes)       vaddr 0x400100E4
//   [246..276] "Syscall: channel round-trip OK!" (31 bytes) vaddr 0x400100F6
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
	// p_flags = PF_R|PF_W|PF_X (7)
	userElfBinary[68] = 7
	// p_vaddr = 0x40010000 at offset 80 (8 bytes LE)
	userElfBinary[82] = 0x01
	userElfBinary[83] = 0x40
	// p_paddr = 0x40010000 at offset 88 (8 bytes LE)
	userElfBinary[90] = 0x01
	userElfBinary[91] = 0x40
	// p_filesz = 277 (0x0115) at offset 96
	userElfBinary[96] = 0x15
	userElfBinary[97] = 0x01
	// p_memsz = 4096 (0x1000) at offset 104
	userElfBinary[105] = 0x10
	// p_align = 4096 (0x1000) at offset 112
	userElfBinary[113] = 0x10

	// -- Block 1 (offset 120): sys_print("Waiting for key...") --
	// mov eax, 5 (sys_print)
	userElfBinary[120] = 0xB8
	userElfBinary[121] = 0x05
	// movabs rdi, 0x400100E4 (prompt string address)
	userElfBinary[125] = 0x48
	userElfBinary[126] = 0xBF
	userElfBinary[127] = 0xE4
	userElfBinary[129] = 0x01
	userElfBinary[130] = 0x40
	// mov esi, 18
	userElfBinary[135] = 0xBE
	userElfBinary[136] = 0x12
	// int 0x80
	userElfBinary[140] = 0xCD
	userElfBinary[141] = 0x80

	// -- Block 2 (offset 142): sys_recv(0, &recv_buf, 8) --
	// mov eax, 3 (sys_recv)
	userElfBinary[142] = 0xB8
	userElfBinary[143] = 0x03
	// mov edi, 0 (keyboard channel ID)
	userElfBinary[147] = 0xBF
	// movabs rsi, 0x400100DC (recv_buf address)
	userElfBinary[152] = 0x48
	userElfBinary[153] = 0xBE
	userElfBinary[154] = 0xDC
	userElfBinary[156] = 0x01
	userElfBinary[157] = 0x40
	// mov edx, 8
	userElfBinary[162] = 0xBA
	userElfBinary[163] = 0x08
	// int 0x80
	userElfBinary[167] = 0xCD
	userElfBinary[168] = 0x80

	// -- Block 3 (offset 169): sys_send(1, &recv_buf, 8) --
	// mov eax, 2 (sys_send)
	userElfBinary[169] = 0xB8
	userElfBinary[170] = 0x02
	// mov edi, 1 (print channel ID)
	userElfBinary[174] = 0xBF
	userElfBinary[175] = 0x01
	// movabs rsi, 0x400100DC (recv_buf address)
	userElfBinary[179] = 0x48
	userElfBinary[180] = 0xBE
	userElfBinary[181] = 0xDC
	userElfBinary[183] = 0x01
	userElfBinary[184] = 0x40
	// mov edx, 8
	userElfBinary[189] = 0xBA
	userElfBinary[190] = 0x08
	// int 0x80
	userElfBinary[194] = 0xCD
	userElfBinary[195] = 0x80

	// -- Block 4 (offset 196): sys_print("Syscall: channel round-trip OK!") --
	// mov eax, 5 (sys_print)
	userElfBinary[196] = 0xB8
	userElfBinary[197] = 0x05
	// movabs rdi, 0x400100F6 (ok_msg string address)
	userElfBinary[201] = 0x48
	userElfBinary[202] = 0xBF
	userElfBinary[203] = 0xF6
	userElfBinary[205] = 0x01
	userElfBinary[206] = 0x40
	// mov esi, 31
	userElfBinary[211] = 0xBE
	userElfBinary[212] = 0x1F
	// int 0x80
	userElfBinary[216] = 0xCD
	userElfBinary[217] = 0x80

	// -- jmp $ (offset 218) --
	userElfBinary[218] = 0xEB
	userElfBinary[219] = 0xFE

	// -- Data section --
	// recv_buf: 8 zero bytes at offset 220 (vaddr 0x400100DC)
	// (already zeroed by Go initialization)

	// "Waiting for key..." at offset 228 (vaddr 0x400100E4)
	prompt := "Waiting for key..."
	for i := 0; i < len(prompt); i++ {
		userElfBinary[228+i] = prompt[i]
	}

	// "Syscall: channel round-trip OK!" at offset 246 (vaddr 0x400100F6)
	okMsg := "Syscall: channel round-trip OK!"
	for i := 0; i < len(okMsg); i++ {
		userElfBinary[246+i] = okMsg[i]
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
