// src/spinlock.go -- SMP spinlock primitive.
//
// xchg-based test-and-set spinlock with interrupt save/restore.
// The xchg instruction has an implicit lock prefix on x86,
// providing a full memory barrier.
//
// Lock ordering (outermost acquired first):
//   1. pageAllocLock     — page allocator (src/vm.go)
//   2. procLock          — procByTask / procByPID (src/process.go)
//   3. gInfoLock         — gInfoBySlot (src/goroutine_tss.go)
//   4. vgaLock           — VGA console output (src/vga.go)
//   5. netBufLock        — packet buffer pool bitmap (src/netbuf.go)
//   6. arpLock           — ARP cache (src/arp.go)
//   7. udpLock           — UDP bind table (src/udp.go)
//   8. statsLock         — network statistics counters (src/netstats.go)
//   9. tcbTableLock      — TCP TCB table (src/tcp.go)
//  10. tcpListenLock     — TCP listener + accept queue (src/tcp.go)
//  11. tcpTimerLock      — TCP timer bookkeeping (src/tcp_retx.go)
//  12. timerListLock     — afterTicks timer wheel (src/afterticks.go)
//   --- Route C primitives (M0..M5) ---
//  13. fsReqQueue.lock   — fsTask MPSC queue (src/kthread_queue.go)
//  13. udpDgramQueue.lock — UDP MPSC queue (src/kthread_queue.go)
//      (13 a/b: same rank, never nested with each other; both
//       drop their lock before kschedWake, so the rank-15 nesting
//       is single-step.)
//  14. KEvent.lock       — single-shot event (src/kthread_event.go)
//      (Signal drops e.lock before kschedWake on each waiter, so
//       the rank-15 nesting is single-step.)
//  15. kschedQueues[cpu].lock — per-CPU scheduler ready queue
//      (src/kthread_sched.go). Acquired briefly inside kschedPush /
//      kschedPop / kschedSteal; never holds another lock.
//  16. kthreadPoolLock   — kthread pool slot bitmap
//      (src/kthread_pool.go). Acquired in alloc/free only; never
//      holds another lock.
//  17. serialLock        — COM1 serial output (src/serial.go).
//      Leaf lock; never holds another. Held across full-line
//      writes to keep cross-CPU output from interleaving.
//
// A function holding lock N must not acquire lock M where M < N.
//
// Rank 12 (afterTicks timer wheel) is the highest pre-Route-C
// rank; ranks 13..17 cover the Route C kthread primitives. The
// scheduler-side locks (15, 16) are acquired briefly inside
// non-blocking primitives and never hold another lock; the
// queue/event locks (13, 14) drop before nesting into rank-15
// via kschedWake.
//
// See impldoc/smp_percpu_and_sync.md §4 for the pre-Route-C
// design and no_goroutine_kernel_design/03_sync_primitives.md
// for the Route C primitives.
//
// §14 (M6) — Uniprocessor kernel addendum:
//   Under uniprocessorKernel = true (preempt_config.go), the
//   gooos kthread scheduler runs on BSP only; APs idle in
//   kernel mode. Ranks 13..16 lose cross-CPU contention:
//     - Rank 13 (fsReqQueue.lock, udpDgramQueue.lock):
//         producers may still be ISRs (cross-context) but
//         never cross-CPU. Same-CPU ISR ↔ kthread is handled
//         by Spinlock.Acquire's cli.
//     - Rank 14 (KEvent.lock): same-CPU only.
//     - Rank 15 (kschedQueues[cpu].lock): only
//         kschedQueues[0] is ever contended; others see no
//         writes (kschedSpawn{,At} clamp targetCPU=0).
//     - Rank 16 (kthreadPoolLock): all alloc/free on BSP.
//   Rank 17 (serialLock): unchanged — kept because ISRs
//   from APs (their own LAPIC timer) still write to COM1.
//   The ranked table itself is unchanged; rollback to SMP
//   kernel is `git revert` of the §14 commit range.
//   See no_goroutine_kernel_design/14_uniprocessor_kernel.md
//   §4 for the rationale.
//
// §15 (M7) — Userspace SMP on APs addendum:
//   Under userspaceSMP = true (preempt_config.go), Ring-3
//   processes dispatch on APs via the new
//   kschedQueuesRing3[cpu] tier. Ranks 2 + 13..16 regain
//   cross-CPU contention for the Ring-3 path:
//     - Rank 2 (procLock): foreground-process transitions
//         (setForegroundProc) and processWait crossings now
//         span BSP↔AP. Spinlock semantics already correct.
//     - Rank 13 (fsReqQueue, udpDgramQueue): producers may
//         be Ring-3 hosts on APs. Same-CPU-only inside
//         Spinlock; safe.
//     - Rank 14 (KEvent.lock): KEvent.Wait/Signal cross BSP
//         (timerDispatcher signal) and AP (parked Ring-3
//         host) regularly. The "drop e.lock before
//         kschedWake" rule still matters for the rank-15
//         nesting that follows.
//     - Rank 15 (kschedQueues[cpu].lock): unchanged —
//         service tier still BSP-only.
//     - Rank 15a NEW (kschedQueuesRing3[cpu].lock): same
//         rank as 15. Per-CPU Ring-3 ready queue. AP↔AP
//         steal: holds 15a once on the source, drops, then
//         15a on self in the dispatch path; never nested
//         simultaneously with kschedQueues[cpu].lock.
//     - Rank 16 (kthreadPoolLock): alloc/free now happens
//         on any CPU (Ring-3 host alloc on BSP via
//         processExec; free on AP via kthreadPoolFree
//         inside kschedLoopRing3Only). Spinlock-safe.
//   Rollback: flip userspaceSMP=false in preempt_config.go,
//   rebuild — APs return to M6 idle. See
//   no_goroutine_kernel_design/15_userspace_smp_on_aps.md
//   §6 for the full rationale.

package main

// Spinlock is an SMP spinlock. Zero value is unlocked.
type Spinlock struct {
	locked uint32
}

// spinlockAcquire spins on xchg until the lock is acquired.
// Implemented in stubs.S.
//
//go:nosplit
//go:linkname spinlockAcquire spinlockAcquire
func spinlockAcquire(lock *uint32)

// spinlockRelease releases the lock with a store + mfence.
// Implemented in stubs.S.
//
//go:nosplit
//go:linkname spinlockRelease spinlockRelease
func spinlockRelease(lock *uint32)

// Acquire disables interrupts and spins until the lock is held.
// Returns the saved RFLAGS for Release.
//
//go:nosplit
func (s *Spinlock) Acquire() uintptr {
	flags := readFlags()
	cli()
	spinlockAcquire(&s.locked)
	return flags
}

// Release releases the lock and restores interrupt state.
//
//go:nosplit
func (s *Spinlock) Release(flags uintptr) {
	spinlockRelease(&s.locked)
	restoreFlags(flags)
}
