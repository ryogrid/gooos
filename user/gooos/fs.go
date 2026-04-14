package gooos

import "unsafe"

// ReadFile reads the full contents of a named file.
// Returns nil if the file does not exist.
// The buffer is heap-allocated (not stack) because the user stack is only 8 KiB.
func ReadFile(name string) []byte {
	nameBytes := []byte(name)
	buf := make([]byte, 65536) // max file size
	n := syscall4(sysFsRead,
		uintptr(unsafe.Pointer(&nameBytes[0])),
		uintptr(len(name)),
		uintptr(unsafe.Pointer(&buf[0])),
		65536,
	)
	if n == 0xFFFFFFFFFFFFFFFF {
		return nil
	}
	return buf[:n]
}

// ListDir returns all filenames in the filesystem.
func ListDir() []string {
	var buf [4096]byte
	n := syscall2(sysFsList,
		uintptr(unsafe.Pointer(&buf[0])),
		4096,
	)
	if n == 0 {
		return nil
	}
	// Parse NUL-separated names.
	var names []string
	start := 0
	for i := 0; i < int(n); i++ {
		if buf[i] == 0 {
			if i > start {
				names = append(names, string(buf[start:i]))
			}
			start = i + 1
		}
	}
	return names
}
