// src/userspace.go -- Ring 3 user mode support and syscall handling.
//
// Provides the 12-syscall ABI for the BusyBox shell. Syscalls are
// dispatched from go_interrupt_handler (interrupt.go) via the saved
// register frame for vector 0x80.
//
// Syscall ABI: RAX=number, RDI/RSI/RDX/R10/R8/R9=args, return in RAX.

package main

import (
	"runtime"
	"time"
	"unsafe"
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
	sysExit     = 0
	sysWrite    = 1
	sysRead     = 2
	sysExec     = 3
	sysFsRead   = 4
	sysFsWrite  = 5
	sysFsList   = 6
	sysYield    = 7
	sysSleep    = 8
	sysGetargs  = 9
	sysSbrk     = 10
	sysVgaClear = 11
)

// jumpToRing3 transitions the CPU to Ring 3 user mode via iretq.
// Implemented in stubs.S.
//
//go:linkname jumpToRing3 jumpToRing3
func jumpToRing3(userRIP uintptr, userRSP uintptr)

// syscallDispatch reads the syscall number from frame.RAX and dispatches
// to the appropriate handler function.
func syscallDispatch(frame *SyscallFrame) {
	switch frame.RAX {
	case sysExit:
		sysExitHandler(frame)
	case sysWrite:
		sysWriteHandler(frame)
	case sysRead:
		sysReadHandler(frame)
	case sysExec:
		sysExecHandler(frame)
	case sysFsRead:
		sysFsReadHandler(frame)
	case sysFsWrite:
		sysFsWriteHandler(frame)
	case sysFsList:
		sysFsListHandler(frame)
	case sysYield:
		sysYieldHandler(frame)
	case sysSleep:
		sysSleepHandler(frame)
	case sysGetargs:
		sysGetargsHandler(frame)
	case sysSbrk:
		sysSbrkHandler(frame)
	case sysVgaClear:
		sysVgaClearHandler(frame)
	default:
		frame.RAX = 0xFFFFFFFFFFFFFFFF // -1 for invalid syscall
	}
}

// --- Syscall 0: sys_exit ---

func sysExitHandler(frame *SyscallFrame) {
	processExit(frame.RDI)
	// Does not return if parent exists; if no parent, halts in processExit.
}

// --- Syscall 1: sys_write ---
// RDI = buf_ptr, RSI = buf_len, RDX = fd (0=VGA+serial, 1=serial only)

func sysWriteHandler(frame *SyscallFrame) {
	buf := frame.RDI
	length := frame.RSI
	fd := frame.RDX

	if length > 4096 {
		length = 4096
	}

	for i := uintptr(0); i < length; i++ {
		ch := *(*byte)(unsafe.Pointer(buf + i))
		if fd == 0 {
			vgaConsolePutChar(ch)
		}
		serialPutChar(ch)
	}
	frame.RAX = length
}

// --- Syscall 2: sys_read ---
// RDI = buf_ptr, RSI = buf_max
// Blocking line-buffered keyboard input with kernel-side echo.

// sysReadLineBuf is a kernel-side line buffer for sys_read.
var sysReadLineBuf [128]byte
var sysReadLineLen int

func sysReadHandler(frame *SyscallFrame) {
	buf := frame.RDI
	maxLen := frame.RSI

	sysReadLineLen = 0

	for {
		// Block waiting for a keyboard event.
		event := <-keyboardCh
		scancode := uint8(event & 0xFF)
		ascii := byte((event >> 8) & 0xFF)

		if scancode == scEnter {
			// Echo newline.
			vgaConsolePutChar('\n')
			serialPutChar('\r')
			serialPutChar('\n')
			break
		}

		if scancode == scBackspace {
			if sysReadLineLen > 0 {
				sysReadLineLen--
				// Echo backspace: move cursor back, overwrite with space, move back again.
				vgaConsolePutChar('\b')
				serialPutChar('\b')
				serialPutChar(' ')
				serialPutChar('\b')
			}
			continue
		}

		if ascii != 0 && sysReadLineLen < 128 {
			sysReadLineBuf[sysReadLineLen] = ascii
			sysReadLineLen++
			// Echo character.
			vgaConsolePutChar(ascii)
			serialPutChar(ascii)
		}
	}

	// Copy to user buffer.
	n := uintptr(sysReadLineLen)
	if n > maxLen {
		n = maxLen
	}
	for i := uintptr(0); i < n; i++ {
		*(*byte)(unsafe.Pointer(buf + i)) = sysReadLineBuf[i]
	}
	frame.RAX = n
}

// --- Syscall 3: sys_exec ---
// RDI = path_ptr, RSI = path_len, RDX = arg_ptr, R10 = arg_len

func sysExecHandler(frame *SyscallFrame) {
	parent := currentProc()
	if parent == nil {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}

	// Reject nested exec: only one level of exec nesting is supported
	// because savedParent is a single global.
	if parent.parent != nil {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}

	// Copy filename from user memory.
	pathLen := frame.RSI
	if pathLen > 256 {
		pathLen = 256
	}
	var pathBuf [256]byte
	for i := uintptr(0); i < pathLen; i++ {
		pathBuf[i] = *(*byte)(unsafe.Pointer(frame.RDI + i))
	}
	filename := string(pathBuf[:pathLen])

	// Copy arguments from user memory.
	argLen := frame.R10
	if argLen > 256 {
		argLen = 256
	}
	var argBuf [256]byte
	for i := uintptr(0); i < argLen; i++ {
		argBuf[i] = *(*byte)(unsafe.Pointer(frame.RDX + i))
	}
	args := string(argBuf[:argLen])

	exitCode, ok := elfExec(filename, args, parent)
	if !ok {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}

	// elfExec blocked until the child exited; the parent's pages are
	// already restored by processExit.
	frame.RAX = exitCode
}

// --- Syscall 4: sys_fs_read ---
// RDI = path_ptr, RSI = path_len, RDX = out_buf, R10 = out_max

func sysFsReadHandler(frame *SyscallFrame) {
	pathLen := frame.RSI
	if pathLen > 256 {
		pathLen = 256
	}
	var pathBuf [256]byte
	for i := uintptr(0); i < pathLen; i++ {
		pathBuf[i] = *(*byte)(unsafe.Pointer(frame.RDI + i))
	}
	filename := string(pathBuf[:pathLen])

	data := fsSendRead(filename)
	if data == nil {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}

	outBuf := frame.RDX
	outMax := frame.R10
	n := uintptr(len(data))
	if n > outMax {
		n = outMax
	}
	for i := uintptr(0); i < n; i++ {
		*(*byte)(unsafe.Pointer(outBuf + i)) = data[i]
	}
	frame.RAX = n
}

// --- Syscall 5: sys_fs_write ---
// RDI = path_ptr, RSI = path_len, RDX = data_ptr, R10 = data_len

func sysFsWriteHandler(frame *SyscallFrame) {
	pathLen := frame.RSI
	if pathLen > 256 {
		pathLen = 256
	}
	var pathBuf [256]byte
	for i := uintptr(0); i < pathLen; i++ {
		pathBuf[i] = *(*byte)(unsafe.Pointer(frame.RDI + i))
	}
	filename := string(pathBuf[:pathLen])

	dataLen := frame.R10
	if dataLen > maxFileData {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}
	dataBuf := make([]byte, dataLen)
	for i := uintptr(0); i < dataLen; i++ {
		dataBuf[i] = *(*byte)(unsafe.Pointer(frame.RDX + i))
	}

	// Create file if it doesn't exist, then write.
	fsSendCreate(filename) // ignore error if already exists
	if fsSendWrite(filename, dataBuf) {
		frame.RAX = 0
	} else {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
	}
}

// --- Syscall 6: sys_fs_list ---
// RDI = buf_ptr, RSI = buf_max

func sysFsListHandler(frame *SyscallFrame) {
	names := fsSendList()
	buf := frame.RDI
	maxLen := frame.RSI
	var written uintptr

	for _, name := range names {
		needed := uintptr(len(name)) + 1 // name + NUL
		if written+needed > maxLen {
			break
		}
		for j := 0; j < len(name); j++ {
			*(*byte)(unsafe.Pointer(buf + written)) = name[j]
			written++
		}
		*(*byte)(unsafe.Pointer(buf + written)) = 0 // NUL separator
		written++
	}
	frame.RAX = written
}

// --- Syscall 7: sys_yield ---

func sysYieldHandler(frame *SyscallFrame) {
	runtime.Gosched()
	frame.RAX = 0
}

// --- Syscall 8: sys_sleep ---
// RDI = ticks (10 ms each at 100 Hz PIT)

func sysSleepHandler(frame *SyscallFrame) {
	ticks := uint64(frame.RDI)
	if ticks > 0 {
		time.Sleep(time.Duration(ticks) * 10 * time.Millisecond)
	}
	frame.RAX = 0
}

// --- Syscall 9: sys_getargs ---
// RDI = buf_ptr, RSI = buf_max

func sysGetargsHandler(frame *SyscallFrame) {
	proc := currentProc()
	buf := frame.RDI
	maxLen := frame.RSI

	n := uintptr(proc.ArgLen)
	if n > maxLen {
		n = maxLen
	}
	for i := uintptr(0); i < n; i++ {
		*(*byte)(unsafe.Pointer(buf + i)) = proc.ArgString[i]
	}
	frame.RAX = n
}

// --- Syscall 10: sys_sbrk ---
// RDI = increment

func sysSbrkHandler(frame *SyscallFrame) {
	proc := currentProc()
	increment := frame.RDI

	if increment == 0 {
		frame.RAX = proc.HeapBreak
		return
	}

	oldBreak := proc.HeapBreak
	newBreak := oldBreak + increment

	// Allocate and map pages for the new region.
	userFlags := uintptr(pagePresent | pageWrite | pageUser)
	pageStart := oldBreak &^ (pageSize - 1)
	if oldBreak&(pageSize-1) != 0 {
		pageStart += pageSize
	}

	for addr := pageStart; addr < newBreak; addr += pageSize {
		paddr := allocPage()
		mapPage(addr, paddr, userFlags)
		processRecordPage(proc, addr, paddr)
	}

	proc.HeapBreak = newBreak
	frame.RAX = oldBreak
}

// --- Syscall 11: sys_vga_clear ---

func sysVgaClearHandler(frame *SyscallFrame) {
	vgaConsoleClear()
	frame.RAX = 0
}

// --- Shell bootstrap ---

// setupUserspace loads the shell ELF and enters Ring 3 via a
// ring3Wrapper goroutine. Blocks main's goroutine forever because
// TinyGo's scheduler stops if main returns (`schedulerDone = true`).
func setupUserspace() {
	if !elfLoad("sh.elf") {
		serialPrintln("Userspace: shell ELF load failed, halting")
		for {
			hlt()
		}
	}
	// Unreachable: elfLoad blocks on the shell's exitCh.
	for {
		hlt()
	}
}
