package gooos

import "unsafe"

// Print writes a string to VGA + serial (fd=0).
func Print(s string) {
	if len(s) == 0 {
		return
	}
	p := unsafe.Pointer(unsafe.StringData(s))
	syscall3(sysWrite, uintptr(p), uintptr(len(s)), 0)
}

// Println writes a string followed by a newline.
func Println(s string) {
	Print(s)
	Print("\n")
}

// ReadLine reads one line of input from the keyboard (blocking).
// Returns the input string without the trailing newline.
func ReadLine() string {
	var buf [128]byte
	n := syscall2(sysRead, uintptr(unsafe.Pointer(&buf[0])), 128)
	return string(buf[:n])
}

// VgaClear clears the VGA text buffer and resets the cursor.
func VgaClear() {
	syscall0(sysVgaClear)
}
