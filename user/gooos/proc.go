package gooos

import "unsafe"

// Exit terminates the current process with the given exit code.
func Exit(code int) {
	syscall1(sysExit, uintptr(code))
}

// Exec runs a named program and blocks until it completes.
// Returns the child's exit code, or -1 if the file is not found.
func Exec(path string, args string) int {
	pathBytes := []byte(path)
	argBytes := []byte(args)

	var argPtr uintptr
	var argLen uintptr
	if len(argBytes) > 0 {
		argPtr = uintptr(unsafe.Pointer(&argBytes[0]))
		argLen = uintptr(len(argBytes))
	}

	result := syscall4(sysExec,
		uintptr(unsafe.Pointer(&pathBytes[0])),
		uintptr(len(pathBytes)),
		argPtr,
		argLen,
	)
	if result == 0xFFFFFFFFFFFFFFFF {
		return -1
	}
	return int(result)
}

// Args returns the argument string passed to this process.
func Args() string {
	var buf [256]byte
	n := syscall2(sysGetargs,
		uintptr(unsafe.Pointer(&buf[0])),
		256,
	)
	if n == 0 {
		return ""
	}
	return string(buf[:n])
}

// Yield voluntarily yields the CPU to the next ready task.
func Yield() {
	syscall0(sysYield)
}

// Sleep sleeps for approximately ms milliseconds.
func Sleep(ms int) {
	ticks := ms / 10 // 100 Hz PIT → 10 ms per tick
	if ticks < 1 {
		ticks = 1
	}
	syscall1(sysSleep, uintptr(ticks))
}

// GetCpuID returns the CPU index this process is currently
// running on (0 = BSP, 1+ = APs). The value may change between
// calls if the scheduler migrates the process to a different core.
func GetCpuID() int {
	return int(syscall0(sysGetcpuid))
}

// Spawn starts a child process running the named program and
// returns its PID immediately. The caller invokes Wait(pid)
// later to retrieve the exit code. Returns (-1, -errno) on
// failure.
func Spawn(path string, args string) (int, int) {
	pathBytes := []byte(path)
	argBytes := []byte(args)
	var argPtr uintptr
	var argLen uintptr
	if len(argBytes) > 0 {
		argPtr = uintptr(unsafe.Pointer(&argBytes[0]))
		argLen = uintptr(len(argBytes))
	}
	r := syscall4(sysSpawn,
		uintptr(unsafe.Pointer(&pathBytes[0])),
		uintptr(len(pathBytes)),
		argPtr,
		argLen,
	)
	if int64(r) < 0 {
		return -1, int(int64(r))
	}
	return int(r), 0
}

// Wait blocks until the named child exits and returns its exit
// code. The PID becomes invalid after Wait returns.
func Wait(pid int) int {
	r := syscall1(sysWait, uintptr(pid))
	return int(int64(r))
}

// WNOHANG makes Waitpid return immediately when the child is still
// running rather than block (matches POSIX waitpid semantics).
const WNOHANG = 1

// sysWaitpid is syscall 34. Mirrors src/userspace.go:sysWaitpid.
const sysWaitpid = 34
const sysShellReady = 38

// Waitpid is the non-blocking sibling of Wait. If WNOHANG is set in
// options and the child is still running, returns (0, false). On reap,
// returns (exitcode, true). On error (bad pid, bad options, etc.),
// returns (negative errno, false).
//
// Blocking waits are not supported through this wrapper — use Wait
// (#16) instead. WNOHANG is currently the only supported options bit.
//
// See impldoc/shell_background_jobs.md §3.4.
func Waitpid(pid int, options uint32) (int, bool) {
	var status int32
	r := syscall3(sysWaitpid, uintptr(pid), uintptr(options),
		uintptr(unsafe.Pointer(&status)))
	rs := int64(r)
	if rs < 0 {
		return int(rs), false
	}
	if rs == 0 {
		return 0, false
	}
	return int(status), true
}

// ShellReady tells the kernel that the shell reached interactive-ready
// state and preempt fanout may now transition to operational.
func ShellReady() {
	syscall0(sysShellReady)
}
