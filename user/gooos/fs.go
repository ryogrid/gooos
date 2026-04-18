package gooos

import "unsafe"

// ReadFile reads the full contents of a named file.
// Returns nil if the file does not exist.
// The buffer is heap-allocated (not stack) because the user stack is only 8 KiB.
func ReadFile(name string) []byte {
	nameBytes := []byte(name)
	buf := make([]byte, 131072) // match maxFileData (128 KiB)
	n := syscall4(sysFsRead,
		uintptr(unsafe.Pointer(&nameBytes[0])),
		uintptr(len(name)),
		uintptr(unsafe.Pointer(&buf[0])),
		131072,
	)
	if n == 0xFFFFFFFFFFFFFFFF {
		return nil
	}
	return buf[:n]
}

// WriteFile writes `data` into the in-memory FS under `name`. The
// underlying sys_fs_write creates the file when it is missing and
// truncates it when it exists. Returns true on success.
//
// Used by the DHCP client to persist /network.conf after a
// successful DORA exchange; any userspace program that wants to
// emit a small config file can reuse it.
func WriteFile(name string, data []byte) bool {
	if len(name) == 0 {
		return false
	}
	nameBytes := []byte(name)
	var dataPtr uintptr
	if len(data) > 0 {
		dataPtr = uintptr(unsafe.Pointer(&data[0]))
	}
	r := syscall4(sysFsWrite,
		uintptr(unsafe.Pointer(&nameBytes[0])),
		uintptr(len(nameBytes)),
		dataPtr,
		uintptr(len(data)),
	)
	return r != 0xFFFFFFFFFFFFFFFF
}

// ListDir returns all filenames in the filesystem.
func ListDir() []string {
	buf := make([]byte, 4096)
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
