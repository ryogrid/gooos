// src/spinlock.go -- SMP spinlock primitive.
//
// xchg-based test-and-set spinlock with interrupt save/restore.
// The xchg instruction has an implicit lock prefix on x86,
// providing a full memory barrier.
//
// Lock ordering (outermost acquired first):
//   1. pageAllocLock  — page allocator (src/vm.go)
//   2. procLock       — procByTask / procByPID (src/process.go)
//   3. gInfoLock      — gInfoBySlot (src/goroutine_tss.go)
//   4. vgaLock        — VGA console output (src/vga.go)
//   5. netBufLock     — packet buffer pool bitmap (src/netbuf.go)
//   6. arpLock        — ARP cache (src/arp.go)
//   7. udpLock        — UDP bind table (src/udp.go)
//   8. statsLock      — network statistics counters (src/netstats.go)
//   9. tcbTableLock   — TCP TCB table (src/tcp.go)
//  10. tcpListenLock  — TCP listener + accept queue (src/tcp.go)
//  11. tcpTimerLock   — TCP timer bookkeeping (src/tcp_retx.go)
//
// A function holding lock N must not acquire lock M where M < N.
//
// See impldoc/smp_percpu_and_sync.md §4 for the full design.

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
