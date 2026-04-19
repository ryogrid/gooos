// src/afterticks.go — gooos-local replacement for time.After.
//
// The TinyGo `time` package does not link in the gooos kernel
// build because reflect.Value.Complex requires SSE registers we
// keep disabled. afterTicks gives us the same "channel that
// becomes readable after N PIT ticks" primitive without the
// time-package dependency.
//
// Each tick is 10 ms (PIT runs at 100 Hz; see src/pit.go).
//
// Design — single-dispatcher timer wheel:
//
// The naive implementation spawns a fresh goroutine per
// afterTicks call. Under gooos's patched TinyGo `scheduler=tasks`
// runtime there is no task free-list / reap path, so a Task
// struct allocated for each spawn leaks until process exit
// (~/.local/tinygo/src/internal/task/task_stack.go:132).
// Repeated hot-loop callers (tcp_retx scanner, kernel echo idle
// poll, netsock accept/connect/recv waits — all at 50ms cadence)
// accumulated hundreds of dead tasks within ~15 s, at which
// point cooperative scheduling stopped progressing and every
// kernel service goroutine including netRxLoop stalled. See
// tcp_problem_review2/ for the full trail.
//
// The fix: a single long-lived `timerDispatcher` goroutine owns
// all deadline tracking. Every afterTicks(d) call inserts
// {deadline, ch} into a shared fixed-size list under
// timerListLock (lock-order rank 12) and returns the caller's
// channel. The dispatcher walks the list on every Gosched
// cycle, collects entries whose deadline has passed, and fires
// their channels. This mirrors netRxLoop's survival pattern —
// one long-lived goroutine with no sub-spawns and no parking —
// which the prior investigation proved is the only goroutine
// shape that survives post-Ring-3 indefinitely.
//
// On overflow (>maxPendingTimers live waiters), afterTicks fires
// the caller's channel immediately rather than silently dropping
// the wait — a caller that expected to sleep N ticks will wake
// immediately but correctly; the alternative (block or drop)
// would deadlock the TCP RTO scanner or similar critical loop.
// In practice gooos has <50 live waiters at any moment, so
// maxPendingTimers=256 is generous.
//
// Lock-order discipline: timerListLock never acquires another
// lock while held. The channel send (`ch <- struct{}{}`) in the
// dispatcher happens OUTSIDE the lock, so a waiter that holds
// any lower-ranked lock can safely call afterTicks.
//
// See impldoc/deferred_hygiene.md §5 for the prior hygiene
// rationale that carries forward.

package main

import "runtime"

// afterTicksCalls counts every invocation of afterTicks.
// Plain uint64 — multi-writer racey increment, acceptable for a
// diagnostic counter where the order-of-magnitude signal matters
// more than exactness. netDiag prints it to confirm the timer
// wheel stays stable (growth rate must match the hot-loop
// cadence but the stall must not reappear).
var afterTicksCalls uint64

// maxPendingTimers caps the number of in-flight afterTicks
// waiters. In practice gooos has on the order of tens; 256
// is far above steady state even with multiple concurrent TCP
// connections. Overflow path fires immediately — see package
// comment.
const maxPendingTimers = 256

type timerEntry struct {
	deadline uint64
	ch       chan<- struct{}
	used     bool
}

var (
	timerList     [maxPendingTimers]timerEntry
	timerListLock Spinlock
)

// afterTicksInit spawns the timer-wheel dispatcher. Must be
// called once at boot, before any goroutine that might call
// afterTicks.
func afterTicksInit() {
	go timerDispatcher()
}

// timerDispatcher is the single long-lived goroutine that owns
// all deadline tracking. Runs forever; never parks, never calls
// afterTicks itself (it reads pitTicks directly).
func timerDispatcher() {
	var ready [maxPendingTimers]chan<- struct{}
	for {
		now := pitTicks
		flags := timerListLock.Acquire()
		n := 0
		for i := 0; i < maxPendingTimers; i++ {
			if timerList[i].used && timerList[i].deadline <= now {
				ready[n] = timerList[i].ch
				n++
				timerList[i].used = false
				timerList[i].ch = nil
			}
		}
		timerListLock.Release(flags)
		for j := 0; j < n; j++ {
			// Non-blocking send — the channel is buffered cap=1
			// and owned by the caller; the dispatcher is the
			// only sender. If somehow already full, drop the
			// redundant notification rather than block.
			select {
			case ready[j] <- struct{}{}:
			default:
			}
		}
		runtime.Gosched()
	}
}

// afterTicks returns a channel that becomes readable after `d`
// PIT ticks (10 ms each). Replacement for time.After. Signature
// and semantics match the legacy per-call-spawn version.
func afterTicks(d uint64) <-chan struct{} {
	afterTicksCalls++
	ch := make(chan struct{}, 1)
	deadline := pitTicks + d
	flags := timerListLock.Acquire()
	for i := 0; i < maxPendingTimers; i++ {
		if !timerList[i].used {
			timerList[i].deadline = deadline
			timerList[i].ch = ch
			timerList[i].used = true
			timerListLock.Release(flags)
			return ch
		}
	}
	timerListLock.Release(flags)
	// Overflow: fire immediately so the caller doesn't deadlock.
	// Non-blocking send — symmetric with the dispatcher's send
	// so the channel semantics stay consistent if someone later
	// shrinks the buffer to 0.
	select {
	case ch <- struct{}{}:
	default:
	}
	return ch
}
