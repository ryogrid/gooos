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

// Syscall numbers. See impldoc/shell_io_fd_table.md §5.1 for
// the canonical table; numbers ≥ 12 land with the shell-IO
// implementation.
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
	sysOpen     = 12
	sysClose    = 13
	sysDup2     = 14
	sysSpawn       = 15
	sysWait        = 16
	sysPipe        = 17
	sysReadKey     = 18
	sysVgaWriteAt  = 19
	sysVgaSetCursor = 20
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
	case sysOpen:
		sysOpenHandler(frame)
	case sysClose:
		sysCloseHandler(frame)
	case sysDup2:
		sysDup2Handler(frame)
	case sysPipe:
		sysPipeHandler(frame)
	case sysSpawn:
		sysSpawnHandler(frame)
	case sysWait:
		sysWaitHandler(frame)
	case sysReadKey:
		sysReadKeyHandler(frame)
	case sysVgaWriteAt:
		sysVgaWriteAtHandler(frame)
	case sysVgaSetCursor:
		sysVgaSetCursorHandler(frame)
	default:
		frame.RAX = 0xFFFFFFFFFFFFFFFF // -1 for invalid syscall
	}
}

// --- Syscall 0: sys_exit ---

func sysExitHandler(frame *SyscallFrame) {
	processExit(frame.RDI)
	// Does not return if parent exists; if no parent, halts in processExit.
}

// sysReadLineBuf is the kernel-side line buffer used by
// readKeyboardLine in src/fd.go (consoleStdin.Read). Lives here
// because the linkage between kernel reader and userspace
// sys_read predates the fd table; kept in this file to minimize
// diff for 1c.
var sysReadLineBuf [128]byte
var sysReadLineLen int

// --- Syscall 1: sys_write ---
// RDI = fd, RSI = buf_ptr, RDX = buf_len
//
// 1c ABI: same wire signature as before, but fd values now
// follow POSIX semantics (0=stdin, 1=stdout, 2=stderr, 3+=open
// fds). Existing user binaries are rebuilt in this commit so
// they pass Stdout (1) instead of the legacy 0.

func sysWriteHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	fd := int(frame.RDI)
	buf := frame.RSI
	length := frame.RDX
	if length > 4096 {
		length = 4096
	}
	desc := procGetFD(proc, fd)
	if desc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}

	// Loop in 256-byte chunks via a kernel scratch buffer so
	// FileDesc impls stay backend-agnostic.
	var scratch [256]byte
	written := uintptr(0)
	for written < length {
		chunk := length - written
		if chunk > 256 {
			chunk = 256
		}
		for i := uintptr(0); i < chunk; i++ {
			scratch[i] = *(*byte)(unsafe.Pointer(buf + written + i))
		}
		n, err := desc.Write(scratch[:chunk])
		if err != fdErrOK {
			if written == 0 {
				frame.RAX = sysFail(err)
				return
			}
			break
		}
		written += uintptr(n)
		if uintptr(n) < chunk {
			break
		}
	}
	frame.RAX = written
}

// --- Syscall 2: sys_read ---
// RDI = fd, RSI = buf_ptr, RDX = buf_max
//
// 1c ABI: BREAKING — adds fd as the first arg. Every shipped
// user binary that calls sys_read is rebuilt in this commit
// (only user/gooos/io.go ReadLine actually called sys_read).

func sysReadHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	fd := int(frame.RDI)
	buf := frame.RSI
	maxLen := frame.RDX
	desc := procGetFD(proc, fd)
	if desc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}

	// Single-shot read into a 256-byte scratch then copy out.
	var scratch [256]byte
	chunk := maxLen
	if chunk > 256 {
		chunk = 256
	}
	n, err := desc.Read(scratch[:chunk])
	if err != fdErrOK && err != fdErrEOF {
		frame.RAX = sysFail(err)
		return
	}
	for i := 0; i < n; i++ {
		*(*byte)(unsafe.Pointer(buf + uintptr(i))) = scratch[i]
	}
	frame.RAX = uintptr(n)
}

// --- Syscall 3: sys_exec ---
// RDI = path_ptr, RSI = path_len, RDX = arg_ptr, R10 = arg_len

func sysExecHandler(frame *SyscallFrame) {
	parent := currentProc()
	if parent == nil {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}

	// Nested exec is now allowed: per-process PML4 (4e) gives
	// each Process its own address space, so child + grandchild
	// don't collide.

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
//
// Uses afterTicks rather than time.Sleep: the kernel's patched
// sleepTicks (runtime_gooos.go) is a busy sti/hlt/cli loop used
// by the scheduler idle path, not a parking primitive — calling
// it from a goroutine body via time.Sleep blocks the CPU without
// yielding to cooperative consumers. Same rationale as in
// src/afterticks.go. A user program sleeping here leaves every
// other kernel goroutine (fsTask, keyboardPump, sibling
// ring3Wrappers) free to run.

func sysSleepHandler(frame *SyscallFrame) {
	ticks := uint64(frame.RDI)
	if ticks > 0 {
		<-afterTicks(ticks)
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

// --- Syscall 18: sys_read_key ---
// RDI = buf_ptr (receives 4 bytes: scancode, ascii, mods, flags)
//
// Blocks until one keyboard event is available, then writes
// the unpacked event to the caller's buffer. No echo — the
// editor manages all screen output. Foreground process only.

func sysReadKeyHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil || proc != foregroundProc {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}
	event := <-keyboardCh
	buf := frame.RDI
	*(*uint8)(unsafe.Pointer(buf + 0)) = uint8(event & 0xFF)         // scancode
	*(*uint8)(unsafe.Pointer(buf + 1)) = uint8((event >> 8) & 0xFF)  // ascii
	*(*uint8)(unsafe.Pointer(buf + 2)) = uint8((event >> 16) & 0xFF) // mods
	*(*uint8)(unsafe.Pointer(buf + 3)) = uint8((event >> 24) & 0xFF) // flags
	frame.RAX = 0
}

// --- Syscall 19: sys_vga_write_at ---
// RDI = row, RSI = col, RDX = char, R10 = attr
//
// Writes a single character with color attribute at (row, col)
// in the VGA text buffer. Does NOT advance the software cursor.
// attr=0 defaults to 0x0F (white on black).

func sysVgaWriteAtHandler(frame *SyscallFrame) {
	row := int(frame.RDI)
	col := int(frame.RSI)
	ch := byte(frame.RDX)
	attr := uint16(frame.R10)
	if row < 0 || row >= vgaHeight || col < 0 || col >= vgaWidth {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}
	if attr == 0 {
		attr = 0x0F // default: white on black
	}
	vga := (*[vgaCells]uint16)(unsafe.Pointer(uintptr(0xB8000)))
	offset := row*vgaWidth + col
	vga[offset] = uint16(ch) | (attr << 8)
	frame.RAX = 0
}

// --- Syscall 20: sys_vga_set_cursor ---
// RDI = row, RSI = col
//
// Moves the hardware blinking cursor by programming the VGA CRT
// controller (ports 0x3D4/0x3D5). Also updates the kernel's
// software cursor position (vgaCursorRow/Col).

func sysVgaSetCursorHandler(frame *SyscallFrame) {
	row := int(frame.RDI)
	col := int(frame.RSI)
	if row < 0 || row >= vgaHeight || col < 0 || col >= vgaWidth {
		frame.RAX = 0xFFFFFFFFFFFFFFFF
		return
	}
	vgaCursorRow = row
	vgaCursorCol = col

	// Enable the hardware cursor with a standard underline shape
	// (scanlines 14-15). Register 0x0A bit 5 = cursor-disable; clear
	// it and set the start scanline. Register 0x0B sets the end
	// scanline. Needed because QEMU may boot with the cursor disabled
	// or with start > end (which also hides it).
	outb(0x3D4, 0x0A)
	outb(0x3D5, 14) // cursor start scanline (bit 5 = 0 enables)
	outb(0x3D4, 0x0B)
	outb(0x3D5, 15) // cursor end scanline

	// Program the cursor position (high byte = 0x0E, low = 0x0F).
	pos := uint16(row*vgaWidth + col)
	outb(0x3D4, 0x0F)
	outb(0x3D5, uint8(pos&0xFF))
	outb(0x3D4, 0x0E)
	outb(0x3D5, uint8((pos>>8)&0xFF))
	frame.RAX = 0
}

// --- Syscall 12: sys_open ---
// RDI = path_ptr, RSI = path_len, RDX = mode (1=read, 2=write, 3=append)

func sysOpenHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	pathLen := frame.RSI
	if pathLen > 256 {
		pathLen = 256
	}
	var pathBuf [256]byte
	for i := uintptr(0); i < pathLen; i++ {
		pathBuf[i] = *(*byte)(unsafe.Pointer(frame.RDI + i))
	}
	name := string(pathBuf[:pathLen])
	mode := fileMode(frame.RDX)
	desc, err := openFileFd(name, mode)
	if err != fdErrOK {
		frame.RAX = sysFail(err)
		return
	}
	fd, allocErr := procAllocFD(proc, desc)
	if allocErr != fdErrOK {
		desc.Close()
		frame.RAX = sysFail(allocErr)
		return
	}
	frame.RAX = uintptr(fd)
}

// --- Syscall 13: sys_close ---
// RDI = fd

func sysCloseHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	err := procClose(proc, int(frame.RDI))
	if err != fdErrOK {
		frame.RAX = sysFail(err)
		return
	}
	frame.RAX = 0
}

// --- Syscall 14: sys_dup2 ---
// RDI = oldfd, RSI = newfd

func sysDup2Handler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	newfd, err := procDup2(proc, int(frame.RDI), int(frame.RSI))
	if err != fdErrOK {
		frame.RAX = sysFail(err)
		return
	}
	frame.RAX = uintptr(newfd)
}

// --- Syscall 17: sys_pipe ---
// RDI = pointer to two consecutive uint64s in user memory
// (kernel writes [readFd, writeFd])

func sysPipeHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	rd, wr := newPipe()
	rdFd, err := procAllocFD(proc, rd)
	if err != fdErrOK {
		frame.RAX = sysFail(err)
		return
	}
	wrFd, err := procAllocFD(proc, wr)
	if err != fdErrOK {
		// Roll back the partial allocation so the caller's
		// fd table doesn't leak the read end.
		procClose(proc, rdFd)
		frame.RAX = sysFail(err)
		return
	}
	out := (*[2]uint64)(unsafe.Pointer(frame.RDI))
	out[0] = uint64(rdFd)
	out[1] = uint64(wrFd)
	frame.RAX = 0
}

// --- Syscall 15: sys_spawn ---
// RDI = path_ptr, RSI = path_len, RDX = arg_ptr, R10 = arg_len
// Non-blocking: returns the child's PID, or -fdErr on failure.

func sysSpawnHandler(frame *SyscallFrame) {
	parent := currentProc()
	if parent == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	pathLen := frame.RSI
	if pathLen > 256 {
		pathLen = 256
	}
	var pathBuf [256]byte
	for i := uintptr(0); i < pathLen; i++ {
		pathBuf[i] = *(*byte)(unsafe.Pointer(frame.RDI + i))
	}
	filename := string(pathBuf[:pathLen])

	argLen := frame.R10
	if argLen > 256 {
		argLen = 256
	}
	var argBuf [256]byte
	for i := uintptr(0); i < argLen; i++ {
		argBuf[i] = *(*byte)(unsafe.Pointer(frame.RDX + i))
	}
	args := string(argBuf[:argLen])

	child, ok := elfSpawn(filename, args, parent)
	if !ok {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	frame.RAX = uintptr(child.pid)
}

// --- Syscall 16: sys_wait ---
// RDI = pid
// Blocks until the named child exits; returns its exit code or
// -fdErr if the pid is not a child of the caller.

func sysWaitHandler(frame *SyscallFrame) {
	parent := currentProc()
	if parent == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	pid := uint32(frame.RDI)
	child := procByPID[pid]
	if child == nil || child.parent != parent {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	frame.RAX = processWait(child)
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
