//go:build gooos && baremetal && kernelspace && scheduler.none

// scheduler_none_stubs.go -- M5.2: undefined-symbol stubs for the
// scheduler=none build.
//
// task_stack_amd64.S references `tinygo_task_exit` (a Go function
// exported by internal/task/task_stack.go). Under scheduler=none
// internal/task isn't compiled, so the symbol is undefined at link.
// Provide a halt stub here — the asm `jmp tinygo_task_exit` is
// only reached when a goroutine returns from its top function, and
// with scheduler=none NO goroutines exist (the `go` keyword is a
// compile error), so the asm path is unreachable and the halt
// content doesn't matter.

package main

//export tinygo_task_exit
func tinygoTaskExitStub() {
	for {
		hlt()
	}
}
