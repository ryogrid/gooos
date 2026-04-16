package gooos

import "unsafe"

// POSIX-style standard fd numbers, matching the kernel's
// per-process fd table (see impldoc/shell_io_fd_table.md).
const (
	Stdin  = 0
	Stdout = 1
	Stderr = 2
)

// Open modes for sys_open.
const (
	OpenRead   = 1
	OpenWrite  = 2 // truncate on open
	OpenAppend = 3
)

// Write writes len(buf) bytes to fd. Returns bytes written, or
// a negative errno on failure.
func Write(fd int, buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	p := unsafe.Pointer(&buf[0])
	r := syscall3(sysWrite, uintptr(fd), uintptr(p), uintptr(len(buf)))
	return int(int64(r))
}

// Read reads up to len(buf) bytes from fd into buf. Returns
// bytes read (0 on EOF for file/pipe fds), or a negative errno
// on failure.
func Read(fd int, buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	p := unsafe.Pointer(&buf[0])
	r := syscall3(sysRead, uintptr(fd), uintptr(p), uintptr(len(buf)))
	return int(int64(r))
}

// Open opens a named file in the in-memory FS. Returns fd ≥ 0
// on success, negative errno on failure.
func Open(name string, mode int) int {
	if len(name) == 0 {
		return -1
	}
	p := unsafe.Pointer(unsafe.StringData(name))
	r := syscall3(sysOpen, uintptr(p), uintptr(len(name)), uintptr(mode))
	return int(int64(r))
}

// Close releases an fd. Returns 0 on success, negative errno on
// failure.
func Close(fd int) int {
	r := syscall1(sysClose, uintptr(fd))
	return int(int64(r))
}

// Dup2 duplicates oldfd onto newfd, closing whatever was at
// newfd first. Returns newfd on success, negative errno on
// failure.
func Dup2(oldfd, newfd int) int {
	r := syscall2(sysDup2, uintptr(oldfd), uintptr(newfd))
	return int(int64(r))
}

// Print writes a string to stdout (fd 1).
func Print(s string) {
	if len(s) == 0 {
		return
	}
	p := unsafe.Pointer(unsafe.StringData(s))
	syscall3(sysWrite, uintptr(Stdout), uintptr(p), uintptr(len(s)))
}

// Println writes a string followed by a newline to stdout.
func Println(s string) {
	Print(s)
	Print("\n")
}

// ReadLine reads one line of input from stdin (fd 0). Returns
// the input string without the trailing newline.
func ReadLine() string {
	var buf [128]byte
	n := syscall3(sysRead, uintptr(Stdin), uintptr(unsafe.Pointer(&buf[0])), 128)
	return string(buf[:int(int64(n))])
}

// VgaClear clears the VGA text buffer and resets the cursor.
func VgaClear() {
	syscall0(sysVgaClear)
}

// ReadKey blocks until a single keystroke is available and
// returns the raw key event components:
//   scancode — PS/2 scancode set 1 (make code, 0x80 stripped)
//   ascii    — translated ASCII (0 for non-printable)
//   mods     — bit 0=Shift, bit 1=Ctrl, bit 2=Alt
//   flags    — bit 0=extended key (0xE0 prefix, e.g. arrow keys)
func ReadKey() (scancode, ascii, mods, flags uint8) {
	var buf [8]byte
	syscall1(sysReadKey, uintptr(unsafe.Pointer(&buf[0])))
	return buf[0], buf[1], buf[2], buf[3]
}

// VgaWriteAt writes a single character at (row, col) with the
// given color attribute. attr=0 uses the default (white on black).
func VgaWriteAt(row, col int, ch byte, attr uint16) {
	syscall4(sysVgaWriteAt, uintptr(row), uintptr(col), uintptr(ch), uintptr(attr))
}

// VgaSetCursor moves the hardware blinking cursor to (row, col).
func VgaSetCursor(row, col int) {
	syscall2(sysVgaSetCursor, uintptr(row), uintptr(col))
}

// Pipe returns [readFd, writeFd] on success. On failure both
// values are -1 and the second return is the negative errno.
func Pipe() (int, int, int) {
	var fds [2]uint64
	r := syscall1(sysPipe, uintptr(unsafe.Pointer(&fds[0])))
	if int64(r) < 0 {
		return -1, -1, int(int64(r))
	}
	return int(fds[0]), int(fds[1]), 0
}
