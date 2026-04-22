// src/fd.go — per-process file descriptor table foundation.
//
// FileDesc is the polymorphic backend for fd-aware syscalls
// (sys_read / sys_write / sys_close / sys_dup2 once they are
// rewritten in phase 1c). Process.fds holds up to procMaxFDs
// slots; each slot is either nil or points at a concrete impl.
//
// Phase 1a wires the foundation: interface, error codes, the
// three console-shaped concrete impls (consoleStdin /
// consoleStdout / consoleStdout-stderr), helpers for slot
// allocation / lookup / dup / close, plus Process.fds and the
// processExit walk. Syscall handlers continue to use the
// pre-fd code paths in this commit; they switch over in 1c.
//
// See impldoc/shell_io_fd_table.md.

package main

import "unsafe"

// procMaxFDs caps the per-process descriptor table.
// shell_io_fd_table.md §2 §10: 16 = 3 stdio + 4 redirect
// headroom + ~8 pipe-end headroom.
const procMaxFDs = 16

// fdErr is a small enum carried back to user space as a
// negative return value (sysFail). Keeping it a uint64 instead
// of a Go error lets the syscall path stay allocation-free.
type fdErr uint64

const (
	fdErrOK   fdErr = 0
	fdErrEOF  fdErr = 1
	fdErrPipe fdErr = 2
	fdErrBad  fdErr = 3
)

// FileDesc is the per-fd backend interface. Concrete impls:
// consoleStdin, consoleStdout, fileFd (added in 1b),
// pipe ends (added in phase 3+).
type FileDesc interface {
	Read(buf []byte) (int, fdErr)
	Write(buf []byte) (int, fdErr)
	Close() fdErr
}

// --- consoleStdin ---------------------------------------------------------

// consoleStdin wraps the existing line-buffered keyboard reader
// (sysReadHandler logic) so that fd 0 dispatches through the
// FileDesc interface in phase 1c.
type consoleStdin struct{}

// readKeyboardLine factors the existing sysReadHandler body so
// the impl can be shared by the in-place sys_read (pre-1c) and
// the consoleStdin.Read path (post-1c).
//
// Returns the number of bytes copied into buf.
func readKeyboardLine(buf []byte) int {
	sysReadLineLen = 0
	for {
		event := <-keyboardCh
		scancode := uint8(event & 0xFF)
		ascii := byte((event >> 8) & 0xFF)

		if scancode == scEnter {
			vgaConsolePutChar('\n')
			serialPutChar('\r')
			serialPutChar('\n')
			break
		}
		if scancode == scBackspace {
			if sysReadLineLen > 0 {
				sysReadLineLen--
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
			vgaConsolePutChar(ascii)
			serialPutChar(ascii)
		}
	}
	n := sysReadLineLen
	if n > len(buf) {
		n = len(buf)
	}
	for i := 0; i < n; i++ {
		buf[i] = sysReadLineBuf[i]
	}
	return n
}

func (consoleStdin) Read(buf []byte) (int, fdErr) {
	// Foreground model (4h): only the keyboard owner reads
	// real input; everyone else sees EOF. Keeps two concurrent
	// Ring-3 processes from racing on keyboardCh and lets
	// pipe-stage children (whose stdin is a pipe end, not the
	// console) coexist with the foreground.
	if currentProc() != getForegroundProc() {
		return 0, fdErrEOF
	}
	return readKeyboardLine(buf), fdErrOK
}

func (consoleStdin) Write([]byte) (int, fdErr) { return 0, fdErrBad }
func (consoleStdin) Close() fdErr              { return fdErrOK }

// --- consoleStdout / consoleStderr ---------------------------------------

// consoleStdout writes to serial; toVGA also mirrors to VGA.
// Stdout (fd 1) sets toVGA=true; stderr (fd 2) sets toVGA=false.
type consoleStdout struct{ toVGA bool }

func (c consoleStdout) Read([]byte) (int, fdErr) { return 0, fdErrBad }

func (c consoleStdout) Write(buf []byte) (int, fdErr) {
	for i := 0; i < len(buf); i++ {
		if c.toVGA {
			vgaConsolePutChar(buf[i])
		}
		serialPutChar(buf[i])
	}
	return len(buf), fdErrOK
}

func (consoleStdout) Close() fdErr { return fdErrOK }

// --- fileFd ---------------------------------------------------------------

// fileMode picks the open mode of a fileFd.
type fileMode uint8

const (
	fileModeRead   fileMode = 1
	fileModeWrite  fileMode = 2 // truncate on open; subsequent writes append
	fileModeAppend fileMode = 3 // O_APPEND-style; offset starts at file end
)

// fileFd wraps a name in the in-memory filesystem with a byte
// offset and an open mode. Per impldoc/shell_io_fd_table.md §6,
// offset is shared on fd inheritance (POSIX semantics).
type fileFd struct {
	name   string
	offset int
	mode   fileMode
}

// openFileFd is the constructor used by sysOpenHandler (1c). It
// creates / truncates the underlying file as the mode requires
// and returns a ready-to-use *fileFd.
func openFileFd(name string, mode fileMode) (*fileFd, fdErr) {
	switch mode {
	case fileModeRead:
		if fsSize(name) < 0 {
			return nil, fdErrBad
		}
		return &fileFd{name: name, mode: mode}, fdErrOK
	case fileModeWrite:
		// POSIX O_WRONLY|O_CREAT|O_TRUNC.
		if fsSize(name) < 0 {
			if !fsCreate(name) {
				return nil, fdErrBad
			}
		} else {
			fsTruncate(name)
		}
		return &fileFd{name: name, mode: mode}, fdErrOK
	case fileModeAppend:
		// POSIX O_WRONLY|O_CREAT|O_APPEND.
		if fsSize(name) < 0 {
			if !fsCreate(name) {
				return nil, fdErrBad
			}
		}
		return &fileFd{name: name, mode: mode}, fdErrOK
	default:
		return nil, fdErrBad
	}
}

func (f *fileFd) Read(buf []byte) (int, fdErr) {
	if f.mode != fileModeRead {
		return 0, fdErrBad
	}
	data := fsRead(f.name)
	if data == nil {
		return 0, fdErrBad
	}
	if f.offset >= len(data) {
		return 0, fdErrEOF
	}
	n := copy(buf, data[f.offset:])
	f.offset += n
	return n, fdErrOK
}

func (f *fileFd) Write(buf []byte) (int, fdErr) {
	if f.mode != fileModeWrite && f.mode != fileModeAppend {
		return 0, fdErrBad
	}
	n := fsAppend(f.name, buf)
	f.offset += n
	if n == 0 && len(buf) > 0 {
		// Either file disappeared or fs is full.
		return 0, fdErrBad
	}
	return n, fdErrOK
}

func (f *fileFd) Close() fdErr { return fdErrOK }

// Package-scope singletons. Inherited by fork/exec via shallow
// Process.fds copy.
var (
	stdinFD  FileDesc
	stdoutFD FileDesc
	stderrFD FileDesc
)

func ensureStdioFDs() {
	if stdinFD == nil {
		stdinFD = consoleStdin{}
	}
	if stdoutFD == nil {
		stdoutFD = consoleStdout{toVGA: true}
	}
	if stderrFD == nil {
		stderrFD = consoleStdout{toVGA: false}
	}
}

// --- Process.fds helpers --------------------------------------------------

// procGetFD returns the slot at index fd, or nil for invalid /
// closed slots.
func procGetFD(p *Process, fd int) FileDesc {
	if p == nil || fd < 0 || fd >= procMaxFDs {
		return nil
	}
	return p.fds[fd]
}

// procAllocFD finds the lowest free slot, installs desc, and
// returns the index. Returns (-1, fdErrBad) if the table is
// full. Does NOT bump pipe refcounts — the caller (sysPipe,
// sysOpen) is responsible because newPipe already starts at
// refcount 1 which represents this first Stash.
func procAllocFD(p *Process, desc FileDesc) (int, fdErr) {
	if p == nil {
		return -1, fdErrBad
	}
	for i := 0; i < procMaxFDs; i++ {
		if p.fds[i] == nil {
			p.fds[i] = desc
			return i, fdErrOK
		}
	}
	return -1, fdErrBad
}

// fdAddRef bumps the pipe-end refcount when a *pipeReader or
// *pipeWriter is copied into an additional fd slot (dup2 or
// Process.fds inheritance). No-op for non-pipe FileDescs.
func fdAddRef(desc FileDesc) {
	switch d := desc.(type) {
	case *pipeReader:
		pipeReaderAddRef(d)
	case *pipeWriter:
		pipeWriterAddRef(d)
	}
}

// procClose calls Close on the slot's FileDesc and clears it.
// Idempotent: closing an already-closed slot is fdErrOK.
func procClose(p *Process, fd int) fdErr {
	if p == nil || fd < 0 || fd >= procMaxFDs {
		return fdErrBad
	}
	desc := p.fds[fd]
	if desc == nil {
		return fdErrOK
	}
	p.fds[fd] = nil
	return desc.Close()
}

// procDup2 duplicates oldfd onto newfd. If newfd is in use,
// it is closed first. Returns newfd on success. Bumps pipe
// refcounts so the writer/reader survives until the new slot
// is eventually closed.
func procDup2(p *Process, oldfd, newfd int) (int, fdErr) {
	if p == nil || oldfd < 0 || oldfd >= procMaxFDs ||
		newfd < 0 || newfd >= procMaxFDs {
		return -1, fdErrBad
	}
	desc := p.fds[oldfd]
	if desc == nil {
		return -1, fdErrBad
	}
	if oldfd == newfd {
		return newfd, fdErrOK
	}
	if p.fds[newfd] != nil {
		p.fds[newfd].Close()
	}
	p.fds[newfd] = desc
	fdAddRef(desc)
	return newfd, fdErrOK
}

// procInitStdio installs the three console fds at slots 0, 1,
// 2 of the given Process. Used by elfLoad for the boot shell;
// elfExec / elfSpawn (phase 4) inherit from the parent
// instead.
func procInitStdio(p *Process) {
	ensureStdioFDs()
	p.fds[0] = stdinFD
	p.fds[1] = stdoutFD
	p.fds[2] = stderrFD
}

// procCloseAll walks the table and closes every non-nil slot.
// Called by processExit before pool release.
func procCloseAll(p *Process) {
	if p == nil {
		return
	}
	for i := 0; i < procMaxFDs; i++ {
		if p.fds[i] != nil {
			p.fds[i].Close()
			p.fds[i] = nil
		}
	}
}

// sysFail packs a fdErr into a uintptr as -err so userspace
// sees a negative return value. fdErrOK packs to 0; the syscall
// handler should not call sysFail on success.
func sysFail(e fdErr) uintptr { return ^uintptr(uint64(e)) + 1 }

// sysReadFromUser / sysWriteToUser read/write a contiguous
// span of user memory through a kernel pointer. Currently the
// kernel uses a single PML4, so the user pointer is directly
// dereferenceable. Phase 4 (per-process PML4) keeps this true
// because gooosOnResume swaps CR3 to the calling proc's PML4
// before the syscall handler runs (the int 0x80 ISR runs on
// the calling proc's PML4, with the kernel-half identity map
// shared via PDP[0]).
//
// Kept here as wrappers so a future precise-GC or copy-to-user
// retrofit can rewrite them without touching every handler.

func sysWriteUserByte(addr uintptr, b byte) {
	*(*byte)(unsafe.Pointer(addr)) = b
}

func sysReadUserByte(addr uintptr) byte {
	return *(*byte)(unsafe.Pointer(addr))
}
