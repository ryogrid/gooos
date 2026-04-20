// user/gooos/ps.go — SDK wrapper for sys_listprocs (feature 2.5).
//
// ProcInfo layout mirrors src/ps.go byte-for-byte (64 bytes, cache-
// line aligned, explicit _pad1). See impldoc/shell_ps_command.md §2.1
// for the canonical ABI.

package gooos

import "unsafe"

const sysListprocs = 37 // matches src/userspace.go sysListprocs

// Process-state enum values. Matches src/ps.go.
const (
	StateRunning  uint8 = 0
	StateSleeping uint8 = 1
	StateExited   uint8 = 2
	StateUnknown  uint8 = 3
)

// ProcInfo mirrors the kernel-side layout. MUST stay byte-identical
// to src/ps.go ProcInfo — both sides pin this at impldoc/shell_ps_command.md §2.1.
type ProcInfo struct {
	PID       uint32
	PPID      uint32
	State     uint8
	_pad1     [3]byte
	LastCpuID uint32
	Ticks     uint64
	StartTick uint64
	Name      [32]byte
}

// Build-time assertion: ProcInfo must be exactly 64 bytes.
var _ [1]byte = [unsafe.Sizeof(ProcInfo{}) - 63]byte{}

// Listprocs fills buf with live-process entries. Returns (n, 0) on
// success (n entries written), or (-1, errno) on error. `buf` must
// be non-empty.
func Listprocs(buf []ProcInfo) (int, int) {
	if len(buf) == 0 {
		return -1, -1
	}
	r := syscall2(sysListprocs,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)))
	if int64(r) < 0 {
		return -1, int(int64(r))
	}
	return int(r), 0
}

// StateString returns a 1-char abbreviation for a ProcInfo.State value,
// compatible with `ps` output conventions.
func (p *ProcInfo) StateString() string {
	switch p.State {
	case StateRunning:
		return "R"
	case StateSleeping:
		return "S"
	case StateExited:
		return "Z"
	default:
		return "?"
	}
}

// NameString returns the NUL-terminated ELF name as a Go string.
func (p *ProcInfo) NameString() string {
	n := 0
	for n < len(p.Name) && p.Name[n] != 0 {
		n++
	}
	return string(p.Name[:n])
}
