// Package gooos provides the userspace API for the gooos operating system.
// Syscall wrappers call assembly stubs in rt0.S which issue int $0x80.

package gooos

// Raw syscall functions — implemented in assembly (rt0.S).
// The Go calling convention (SysV ABI) passes arguments in
// RDI, RSI, RDX, RCX, R8, R9. The assembly shuffles these
// to the kernel ABI: RAX=nr, RDI=a1, RSI=a2, RDX=a3, R10=a4.

//go:linkname syscall0 syscall0
func syscall0(nr uintptr) uintptr

//go:linkname syscall1 syscall1
func syscall1(nr, a1 uintptr) uintptr

//go:linkname syscall2 syscall2
func syscall2(nr, a1, a2 uintptr) uintptr

//go:linkname syscall3 syscall3
func syscall3(nr, a1, a2, a3 uintptr) uintptr

//go:linkname syscall4 syscall4
func syscall4(nr, a1, a2, a3, a4 uintptr) uintptr

// Syscall numbers matching the kernel dispatch table (src/userspace.go).
// See impldoc/shell_io_fd_table.md §5.1 for the canonical table.
const (
	sysExit     = 0
	sysWrite    = 1 // (fd, buf, len) since 1c — sys_write fd repurposed POSIX
	sysRead     = 2 // (fd, buf, max) since 1c — added fd first arg
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
	sysSpawn    = 15
	sysWait     = 16
	sysPipe     = 17
)
