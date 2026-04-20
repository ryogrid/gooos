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
		// TinyGo represents a `func()` value as {context, code}
		// (context first, code pointer at +8). For a package-level
		// function the context is 0, so reading the FIRST word
		// yields zero — which installs a null handler. Read the
		// code pointer at +8 instead.
		p := (*[2]uintptr)(unsafe.Pointer(&handler))
		h = p[1]
		if h == 0 {
			// Safety fallback if this build uses the reversed layout.
			h = p[0]
		}
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
