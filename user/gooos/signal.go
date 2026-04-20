// user/gooos/signal.go — user SDK wrappers for feature 2.2 (kernel-
// delivered SIGALRM preemption). Mirrors src/user_signal.go.

package gooos

import "unsafe"

const (
	// SIGALRM is the only signal number supported by the gooos
	// preemption signal mechanism today.
	SIGALRM = 14
)

const (
	sysSigaction = 35
	sysSigreturn = 36
)

// Sigaction installs handler as the SIGALRM handler for the current
// process. Calling with handler == nil does NOT uninstall; install a
// real no-op handler instead. Returns 0 on success, negative errno
// on failure.
//
// The handler MUST terminate with a call to Sigreturn() — it cannot
// return normally, because the kernel pushed a sigFrame onto the
// stack before invoking it and a normal return would land in garbage.
// See impldoc/preempt_user_goroutines.md §5 for the protocol.
func Sigaction(signum uint32, handler func()) int {
	var h uintptr
	if handler != nil {
		// A plain `func()` is a fat pointer: (code_ptr, data_ptr).
		// What we want for the kernel to jump to is the code_ptr.
		// *(*uintptr)(&handler) reads the first word.
		h = *(*uintptr)(unsafe.Pointer(&handler))
	}
	r := syscall3(sysSigaction, uintptr(signum), h, 0)
	return int(int64(r))
}

// Sigreturn is the tail call every SIGALRM handler must issue to
// restore the interrupted context. It does not return.
//
//go:noinline
func Sigreturn() {
	syscall0(sysSigreturn)
	// Unreachable; kernel sys_sigreturn rewrites RIP to the saved
	// interrupt RIP and iretq-s.
	for {
	}
}
