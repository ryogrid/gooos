// userpreempt — feature 2.2 user-goroutine preemption harness.
//
// Registers a SIGALRM handler, spawns two user goroutines:
//
//   - hog: tight `for { x++ }` loop with NO cooperative yield.
//   - marker: prints `userpreempt_marker=N` 20 times with 100ms sleeps.
//
// Without preemption, hog starves marker (both share the single user
// scheduler=tasks runqueue in one ring3Wrapper). With preemption,
// the kernel rewrites the user RIP at SIGALRM delivery so hog's
// tight loop is interrupted; the SIGALRM handler calls Yield() to
// let marker run, then Sigreturn() to restore hog's context.
//
// Run from the shell: `$ userpreempt`.
// Exit: after 20 markers, call Exit(0).

package main

import (
	"runtime"
	"strconv"

	"github.com/ryogrid/gooos/user/gooos"
)

const (
	numMarkers = 20
)

// done is signaled by marker when it finishes. main waits on it then
// exits. A channel keeps the process alive while hog is running.
var done = make(chan int, 1)

// sigalrmHandler is installed via gooos.Sigaction(SIGALRM, ...).
// Called when the kernel decides to preempt this process's quantum.
// Must not return normally — kernel pushed a 104-byte sigFrame onto
// the user stack before invoking it; a normal `ret` would land in
// garbage.
//
// The handler calls Yield() to give the user scheduler a chance to
// pick a different user goroutine (e.g. marker), then tail-calls
// Sigreturn() to restore the pre-signal context.
//
//go:noinline
func sigalrmHandler() {
	// User-level yield: passes control to another user goroutine in
	// this process (e.g. marker) via the TinyGo task scheduler. A
	// kernel-level sys_yield (gooos.Yield) would just swap between
	// ring3Wrapper kernel goroutines — marker wouldn't get a turn.
	runtime.Gosched()
	gooos.Sigreturn()
}

func main() {
	gooos.Println("userpreempt: install SIGALRM handler")
	if err := gooos.Sigaction(gooos.SIGALRM, sigalrmHandler); err != 0 {
		gooos.Println("userpreempt: Sigaction failed errno=" + strconv.Itoa(err))
		gooos.Exit(1)
		return
	}
	gooos.Println("userpreempt: spawn hog + marker")

	go hog()
	go marker()

	<-done
	gooos.Println("userpreempt: done")
}

func hog() {
	var x uint64
	for {
		x++
		if x == 0 {
			// Unreachable under reasonable runtime; exists so the
			// compiler doesn't DCE the loop body.
			gooos.Println("userpreempt: hog wrapped")
		}
	}
}

func marker() {
	// No Sleep between prints — Sleep would park the host
	// ring3Wrapper kernel goroutine, and if the SIGALRM handler on
	// hog's goroutine is mid-Gosched when marker sleeps, hog and
	// marker both stall. Yield via runtime.Gosched instead: both
	// user goroutines stay runnable, so the kernel-side preempt can
	// fire again and drive another marker line.
	for i := 0; i < numMarkers; i++ {
		gooos.Println("userpreempt_marker=" + strconv.Itoa(i))
		runtime.Gosched()
	}
	done <- 1
}
