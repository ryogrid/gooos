# 03 — Sync primitives (channel replacements)

§02 introduced kernel threads. This doc defines the channel-less IPC
primitives that replace `chan` in the Ring 0 kernel. All primitives
build on the existing `Spinlock` at `src/spinlock.go:33` and on the
`kschedPark` / `kschedWake` hooks from §02.

## Inventory of what needs replacing

From the §06 inventory — 8 channel-typed struct fields + 21
`make(chan …)` sites + 12 `<-afterTicks(…)` consumers — the kernel
side uses channels in four distinct patterns. §03 provides one
primitive per pattern.

| Pattern | Current site (examples) | Replacement (this doc) |
|--------|-------------------------|------------------------|
| Request / reply RPC | `fsTask` + `fsReqCh` / per-call `reply chan *fsResponse` (`src/fs.go:190`, `src/fs.go:220`..`src/fs.go:248`) | Bounded MPSC queue `KQueue` + per-caller `KEvent` |
| Packet / datagram handoff | `UDPBinding.Ch` (`src/udp.go:143`), `socketFd.recvCh` (`src/netsock.go:239`), `pipe.ch` (`src/pipe.go:42`) | Bounded SPSC / MPMC queue `KQueue` |
| Timed wait / sleep | `afterTicks(d)` return value (`src/afterticks.go:126`) | `kschedTimedPark(deadline)` + `timerWheelFire` |
| One-shot completion | `Process.exitCh` (`src/process.go:310`), `ring3StackPoolCh` (`src/ring3_pool.go:61`) | `KEvent` (edge-triggered) or slot-pool re-borrow |

The four primitives — `Spinlock` (reused), `KEvent`, `KQueue`,
`kschedTimedPark` — cover 100 % of current channel usage in the
kernel. §06 rewires each site to one of them.

## Primitive 1: `Spinlock` (reused, no change)

Already at `src/spinlock.go:33..68`; xchg-based test-and-set with
interrupt save/restore; ranked lock order documented at
`src/spinlock.go:7..20`. Route C reuses `Spinlock` verbatim. The
only behavioural addition relevant to §03 is:

**INVARIANT L1** — `Spinlock.Acquire` bumps `%gs:48`
(PreemptDisable, `src/percpu.go:47`) and `Spinlock.Release` drops it.
This is how the preempt ISR at `src/goroutine_irq.go:89..112`
already skips preempt when a spinlock is held; Route C preserves the
contract verbatim. See §04.

The existing lock-order table (ranks 1..12) stays. §03 adds two new
ranks below rank 12 (ring for `afterTicks`) to cover the new
primitives:

- **rank 13** (lowest): `kqLock` — per-`KQueue` lock. Never acquired
  while holding any lock of rank ≤ 12, because the push/pop paths
  only wake a kernel thread (`kschedWake`) and never touch tcpx /
  arp / fs / vga / page-alloc state. In other words: rank 13 sits
  *above* rank 12 in the "acquired last" sense; a holder can call
  `afterTicks` safely from inside kqLock only if the kqLock path
  never re-enters kqLock recursively (it doesn't).
- **rank 14**: `keventLock` — per-`KEvent` lock. Same shape: highest
  rank; held briefly around state changes; releases before wake.

If §06 surfaces a case where an existing rank-N lock must wake a
`KQueue` waiter while held, the wake path is **release-then-wake**:
do `lock.Release()` first, then `q.Wake()`. The current
`timerDispatcher` uses exactly this pattern
(`src/afterticks.go:106..115` — lock released at line 106, channel
sends at line 113 happen unlocked), so the pattern is proven.

## Primitive 2: `KEvent`

An edge-triggered single-shot event. One thread waits, one thread
signals, the signal unsticks the waiter. Analogous semantics to a
`chan struct{}` used as a completion signal.

```go
// src/kthread_event.go (PROPOSED)

// KEvent is a single-shot edge-triggered event. Zero value is
// unsignalled; Signal transitions to signalled (idempotent once
// signalled). Waiters parked before Signal wake; waiters that call
// Wait after Signal return immediately.
type KEvent struct {
    lock    Spinlock
    flag    uint32        // 0 = unsignalled, 1 = signalled
    waiters *KernelThread // intrusive WakeLink list
}
```

API:

- `Wait()` — caller blocks until signalled. Park via `kschedPark`
  after re-checking `flag` under `lock`. On wake, return.
- `Signal()` — caller sets `flag = 1`; snapshots waiters list under
  lock; releases lock; `kschedWake` each waiter. Idempotent.
- `Reset()` — caller sets `flag = 0`. Intended only for re-armable
  slots (e.g. the §02 kernel-thread exit handshake). Callers must
  not Reset while anyone is waiting; §06 calls this out per-site.

Waker/waitee memory ordering: the spinlock's Release contains the
x86 store-release needed for TSO; after `kschedWake` the scheduler
push contains its own lock (`kschedQueues[cpu].lock`), so the Release
→ WakeLink → RunQueue path is ordered. Reviewers in §Phase-B gate 3
(STW deadlock-freedom) verify this.

### Usage pattern

Replaces `Process.exitCh` (`src/process.go:310`):

- Before: child `processExit` sends `exitCode` on a `chan uintptr`;
  `processWait` receives.
- After: child stores `ExitCode` into its own `KernelThread`,
  signals a `KEvent` on the child. `processWait` calls `ev.Wait()`,
  then reads the stored code.

See §07 for the full Ring-3 exit rewire.

## Primitive 3: `KQueue[T]`

A bounded producer-consumer queue. The single primitive covers three
cases with different cardinality at the producer / consumer ends:

- **SPSC** — one producer, one consumer (e.g. pipe byte stream:
  `pipe.ch` at `src/pipe.go:42`, buffer 4096).
- **MPSC** — many producers, one consumer (e.g. `fsReqCh`:
  producers are every kernel thread that calls `fsSend*`, consumer
  is `fsTask`).
- **MPMC** — many producers, many consumers (e.g. `udpBindWithChannel`
  datagram handoff at `src/udp.go:143`; multiple kernel threads can
  read the same binding in principle, though in practice usually
  one).

Rather than three different queue types, §03 specifies one queue
with a small behavioural knob and leaves microoptimizations to a
future cycle.

```go
// src/kthread_queue.go (PROPOSED)

// KQueue is a bounded ring buffer. Parametric over the element
// type E (TinyGo supports Go generics as of 0.35; gooos runs 0.40.1
// so this is fine). E is the caller's concrete type; we pass by
// value to keep ISR-unsafe pointers from leaking into the ring.
type KQueue[E any] struct {
    lock     Spinlock
    ring     []E
    head     uint32
    tail     uint32
    cap      uint32
    count    uint32
    // Waiter lists (intrusive via KernelThread.WakeLink).
    // Separate lists for "full-blocked producers" and
    // "empty-blocked consumers" because Go's chan select doesn't
    // apply and the waits are unambiguous.
    producers *KernelThread
    consumers *KernelThread
}
```

API:

- `Init(cap uint32)` — allocate backing slice. Must be called at
  boot-time; callers should treat KQueues as long-lived.
- `Push(v E)` — block if full. Wake one parked consumer if any.
- `TryPush(v E) bool` — never blocks; returns false if full.
- `Pop() E` — block if empty. Wake one parked producer if any.
- `TryPop() (E, bool)` — never blocks; returns (zero, false) if
  empty.
- `Len() uint32` — for diagnostics only; racey by design (called
  outside the lock in dump paths like `netDiag`).

Memory ordering: all mutations happen under `lock`. Waker wakes
*after* `lock.Release`. `kschedPark` re-checks the condition under
`lock` (the caller closure re-reads `count`), so the classic
"missed wakeup" race is handled by re-check-then-park.

### Bounded vs unbounded

All channels in the current kernel are bounded (`cap=1`, `cap=4096`,
`cap=8`, `cap=16`). `KQueue.Init` takes `cap` as an argument; §06
specifies the exact capacity per site (same as the existing
`make(chan T, N)` argument). Unbounded queues are not provided
because no current site needs them.

### SPSC fast path

For pipes (4 KiB buffer, single reader/writer) the lock contention
will dominate if every byte takes `lock.Acquire/Release`. The §06
mapping for `pipe.ch` prescribes batching at the `sys_read`/`sys_write`
boundary so each syscall acquires once and moves up to N bytes,
matching the current Go-chan-based implementation which the runtime
already buffers byte-wise. No new "lock-free SPSC" primitive is
introduced. If profiling in M2 shows pipe throughput regressed, §10
parks an "upgrade pipe to lock-free SPSC" follow-up.

## Primitive 4: Timed wait / timer wheel

The current `afterTicks` (`src/afterticks.go:92`, `src/afterticks.go:126`)
is a single-dispatcher timer wheel: a goroutine `timerDispatcher`
scans a fixed-size deadline list on every `runtime.Gosched()`, and
fires channel sends on matured entries.

Under Route C:

- `timerDispatcher` becomes a kernel thread (`kschedSpawn(
   "timerDispatcher", timerDispatcher)`). §06 M3 migrates it.
- `afterTicks(d) <-chan struct{}` is replaced by
  `kschedTimedPark(d)` and by a new helper `KEventAfter(d uint64)
  *KEvent`. The returned event is single-shot; caller `Wait()` on it.
- Entries in `timerList` (`src/afterticks.go:77`) store a
  `*KEvent` instead of a `chan<- struct{}`. The dispatcher signals
  the event on deadline expiry.
- `timerListLock` stays at rank 12 (`src/spinlock.go:19`).

The replacement API:

```go
// src/afterticks.go (MIGRATED)

// KEventAfter returns a KEvent that signals after `d` PIT ticks.
// Replacement for the channel-returning afterTicks. Call Wait() on
// the returned event; after Wait returns the event is signalled and
// should be discarded (not Reset — it's a one-shot per call).
func KEventAfter(d uint64) *KEvent
```

The overflow semantics (`src/afterticks.go:141..148`, "fire
immediately on full list") are preserved: if `timerList` is full,
`KEventAfter` returns a pre-signalled `KEvent` so the caller's
`Wait()` returns immediately.

**Keep the `afterTicks` shim during migration**: M3 (§09) lands
`KEventAfter` and keeps `afterTicks` as a thin wrapper backed by a
goroutine that waits then fires a channel. This lets §06 migrate
call sites piecemeal. M5 deletes `afterTicks` entirely after the
last caller is moved.

### Timer accuracy

Unchanged from today. 10 ms PIT tick granularity. The tick source
stays `pitTicks` at `src/pit.go:22` (BSP-incremented in
`handleTimer`). Reviewer note: the AP LAPIC-timer tick
(`src/lapic_timer.go:88`) does **not** increment `pitTicks`; only BSP
does. This is correct for the wheel because the wheel runs from a
single thread and reads pitTicks freely.

## Primitive 5: Select-equivalent (bounded poll)

TinyGo's `select` has no direct analogue in Route C. The only
current uses of `select` in the kernel are:

1. `timerDispatcher`'s non-blocking send (`src/afterticks.go:112`).
   Under Route C this becomes `ev.Signal()` which is idempotent;
   no select needed.
2. `afterTicks`'s overflow path (`src/afterticks.go:145`). Same
   fix — `ev.Signal()` idempotent.

Grep `select {` across `src/*.go` shows those two sites only
(verifiable in review). No new "select on N events" primitive is
needed. If §06 surfaces a new site, §10 parks it as a future
follow-up.

## Memory-ordering summary

| Primitive | Wake side | Wait side | Ordering barrier |
|---------|-----------|-----------|------------------|
| `KEvent.Signal` → `Wait` | Store `flag=1`, snapshot waiters under lock, release lock, call `kschedWake` | Lock, read flag, park if 0, re-check on resume | Release-store on lock unlock; scheduler's queue lock re-acquires on push |
| `KQueue.Push` → waiting consumer | Write ring slot, increment `count`, release lock, wake one consumer | Acquire lock, read `count`, park if 0, on resume re-read `count` | x86-TSO gives acquire/release on the spinlock; `kschedPush` enqueues the wakee after its state becomes Runnable |
| `KQueue.Pop` → waiting producer | Read slot, decrement `count`, release lock, wake one producer | Acquire lock, read `count`, park if == cap, on resume re-read | Same |
| `kschedTimedPark` → timer fire | `timerDispatcher` reads deadline, signals event, cycles | Park on the event | Same as `KEvent.Signal` |

None of the above need fences beyond what `Spinlock.Acquire` /
`Spinlock.Release` already carry (xchg + lock prefix on acquire,
locked store on release).

## Ownership of state

- **Lock ownership**: each primitive has one lock; the primitive's
  state is protected by that lock. Crossing primitives (e.g.
  `fsTask` holding `kqLock` while calling `kschedWake`) is
  release-then-wake — release the primitive's lock first, then
  wake. §06 flags any site that must cross.
- **Wait-queue ownership**: the wait list is a singly-linked list
  anchored in the primitive struct. A thread is on at most one
  wait list at a time (K2-like invariant enforced in §02). A thread
  that exits while parked is a bug; §02 kschedExit requires the
  thread to be running or runnable.
- **Stack ownership**: a parked thread's kernel stack is its own,
  allocated from `kthread_pool.go` (§02). Signalling a thread does
  not touch the waiter's stack — only state + WakeLink, which live
  in the `KernelThread` struct in the kernel heap.

## Non-goals

- No priority inheritance. Spinlocks are short-held; the
  pre-existing rank system already prevents priority inversion on
  the current workload. If a future service needs PI, §10 parks it.
- No deadline inheritance. Timed parks do not propagate deadlines
  across wait-relay hops (e.g. if thread A timed-waits for B to
  return an FS result, A times out on its own timer; B keeps
  working). Same as current `<-afterTicks(d)` semantics.
- No cancellation tokens. The current kernel has no
  `context.Context` usage (verified: no `ctx.Done()` grep in
  `src/*.go`). If a future Ring-3 signal (e.g. SIGINT) needs to
  interrupt a kernel wait, §10 parks that.

## Reviewer gates

- Release-then-wake pattern is explicit: **yes** (§ Primitive 1 +
  all primitives' description).
- STW deadlock-freedom: **§05 is load-bearing here**. The rule is:
  under STW, no thread waits on a lock held by a frozen thread. §05
  implements the freeze by making every thread stop at the next
  safe point (not mid-critical-section), which is guaranteed by
  the INVARIANT L1 / K4 pair.
- Every channel shape in §06 maps to one primitive here: **yes**
  (table at top).
