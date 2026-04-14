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
