# Chapter 09 — Synchronization Primitives

## Overview

Ring-0 gooos cannot rely on Go channels, `sync.Mutex`, or `time.After`. The TinyGo runtime under `scheduler=none` does not provide channel wake-up semantics that survive across CPUs, and goroutine task structs leak indefinitely (no reap path). Chapter 05 already explained why the kernel grew its own kernel-thread runtime ("Route C"). This chapter is the matching synchronization toolkit: an interrupt-safe spinlock keyed by a static rank table, a single-shot edge-triggered event (`KEvent`), bounded MPSC (Multi-Producer Single-Consumer) ring queues, and a single-dispatcher timer wheel that lets a kernel thread "park until tick" without spawning per-call goroutines.

The chapter is laid out so each primitive is grounded in actual source: spinlock acquire/release flow, the 17-rank deadlock-prevention table copied verbatim from `src/spinlock.go`, the `KEvent` flag-and-waiter list, the `fsReqQueue` / `udpDgramQueue` ring layouts with their park-on-empty / wake-on-push paths, and the `afterTicks` timer wheel that backs `kschedTimedPark`. The chapter closes with a "no Go channels" cheat sheet that maps each idiomatic Go construct onto its gooos replacement.

## Prerequisites

- Chapter 05 — Kernel thread runtime (`KernelThread`, `kschedSwitch`, `kschedRunning[cpu]`, ParkLock).
- Chapter 06 — SMP (Symmetric Multi-Processing) bring-up and the wake IPI (Inter-Processor Interrupt) used by `kschedWake` for cross-CPU wake-ups.
- Familiarity with the concepts of spinlocks, condition variables, mutexes, bounded queues. The novelty here is not the concept but the rank-based deadlock prevention, the explicit RFLAGS (Register Flags) save/restore, and the hand-rolled park/wake protocol coordinated by an intrusive `WakeLink` field plus per-thread `ParkLock`.

## Why no Go channels in kernel Ring 0

Channels live inside the TinyGo runtime. Under `scheduler=none` the runtime no longer drives goroutine wake-ups, and the M0..M5 work documented in Chapter 05 traced concrete failures: `<-ch` would block a goroutine that had no scheduler to resume it once a sender on another CPU made the value available. Even before scheduler removal, `time.After` per-call spawns leaked task slots — `tcp_retx` and `netRxLoop` accumulated hundreds of dead tasks within ~15 seconds because there is no goroutine free-list / reap path in the patched TinyGo (`src/afterticks.go:13-34`).

gooos therefore exposes its own primitives. The kernel never calls `make(chan T)` for synchronization; everything coordinates through `Spinlock` + `KEvent` + bounded MPSC queues. The legacy goroutine helpers (`afterTicks`) remain only as a transitional shim that backs both channel-based and event-based callers from the same dispatcher (`src/afterticks.go:75-145`).

## Spinlock

`Spinlock` (`src/spinlock.go:107-109`) is a single `uint32` word, locked via `xchg` (atomic exchange / Exchange instruction). Zero is unlocked; one is held. The `xchg` instruction has an implicit `lock` prefix on x86, providing a full memory barrier (`src/spinlock.go:3-5`).

### Acquire / Release flow

```
Acquire():
    flags := readFlags()        // capture caller's RFLAGS (IF bit, etc.)
    cli()                       // disable interrupts on this CPU
    spinlockAcquire(&s.locked)  // xchg-loop until 0->1 wins
    return flags                // caller stashes for matching Release

Release(flags):
    spinlockRelease(&s.locked)  // store 0 + mfence (memory fence)
    restoreFlags(flags)         // re-enable interrupts iff caller had them on
```

Source: `src/spinlock.go:128-142`. The asm helpers live in `src/stubs.S:450-472`. The acquire path uses TTAS (test-then-test-and-set) with a `pause` hint to avoid bus contention; the release path uses `movl $0` followed by `mfence` so prior critical-section writes drain before the lock is observable as free.

### Why save RFLAGS rather than always `sti`

Naive "always `sti` on release" would re-enable interrupts even when the caller had them disabled — for example, a caller that arrived inside an ISR (Interrupt Service Routine), or a caller already holding a higher-rank lock with interrupts off. Saving and restoring RFLAGS lets `Spinlock` nest correctly: only the outermost `Release` re-enables IF (interrupt flag), inner releases keep interrupts off. This is also what makes the lock interrupt-safe — the same lock can be taken on a kthread path and from an ISR on the same CPU because interrupts are masked across the critical section.

## The lock-rank table

Every spinlock in gooos has a static rank, copied here verbatim from the comment block at the top of `src/spinlock.go:7-37`:

| Rank | Lock                       | File                          | Purpose                                  |
| ---: | :------------------------- | :---------------------------- | :--------------------------------------- |
|    1 | `pageAllocLock`            | `src/vm.go`                   | Page allocator                           |
|    2 | `procLock`                 | `src/process.go`              | `procByTask` / `procByPID`               |
|    3 | `gInfoLock`                | `src/goroutine_tss.go`        | `gInfoBySlot`                            |
|    4 | `vgaLock`                  | `src/vga.go`                  | VGA console output                       |
|    5 | `netBufLock`               | `src/netbuf.go`               | Packet buffer pool bitmap                |
|    6 | `arpLock`                  | `src/arp.go`                  | ARP cache                                |
|    7 | `udpLock`                  | `src/udp.go`                  | UDP bind table                           |
|    8 | `statsLock`                | `src/netstats.go`             | Network statistics counters              |
|    9 | `tcbTableLock`             | `src/tcp.go`                  | TCP TCB table                            |
|   10 | `tcpListenLock`            | `src/tcp.go`                  | TCP listener + accept queue              |
|   11 | `tcpTimerLock`             | `src/tcp_retx.go`             | TCP timer bookkeeping                    |
|   12 | `timerListLock`            | `src/afterticks.go`           | afterTicks timer wheel                   |
|   13 | `fsReqQueue.lock`          | `src/kthread_queue.go`        | fsTask MPSC queue                        |
|   13 | `udpDgramQueue.lock`       | `src/kthread_queue.go`        | UDP MPSC queue                           |
|   14 | `KEvent.lock`              | `src/kthread_event.go`        | Single-shot event                        |
|   15 | `kschedQueues[cpu].lock`   | `src/kthread_sched.go`        | Per-CPU scheduler ready queue            |
|  15a | `kschedQueuesRing3[cpu]`   | `src/kthread_sched.go`        | Per-CPU Ring-3 host ready queue (M7)     |
|   16 | `kthreadPoolLock`          | `src/kthread_pool.go`         | kthread pool slot bitmap                 |
|   17 | `serialLock`               | `src/serial.go`               | COM1 serial output (leaf)                |

### Why ranks prevent deadlocks

The rule is one line: **a function holding lock N must not acquire lock M where M ≤ N** (`src/spinlock.go:39`). This forms a strict partial order on lock acquisition. The classical deadlock pattern requires a cycle (thread A holds X waiting for Y while thread B holds Y waiting for X); with a strict total order, no such cycle can exist, so the deadlock graph has no cycles by construction.

Two ranks (13 and 15a) are listed as siblings: same rank, never nested with each other. The two rank-13 queue locks serve disjoint queues (`fsReqQueue` vs `udpDgramQueue`); the two rank-15 scheduler queues (service tier `kschedQueues` vs Ring-3 tier `kschedQueuesRing3`) are likewise never held simultaneously.

### Single-step nesting at ranks 13–15

Both queue locks (rank 13) and `KEvent.lock` (rank 14) drop their lock before calling `kschedWake`, which acquires rank 15 internally (`src/spinlock.go:23-31`, `src/kthread_event.go:80-95`). That keeps the nesting **single-step** — at any moment a thread holds at most one of {13, 14, 15} — even though all three appear in the same code path. The rank table is the authoritative deadlock-prevention scheme; nothing else in the kernel polices this.

### M6 and M7 addenda

Under `uniprocessorKernel = true` (the M6 mode, see Chapter 06 §M6) ranks 13..16 lose cross-CPU contention because every kthread runs on the BSP (Bootstrap Processor). The ranked table itself is unchanged; the locking discipline is preserved so that an SMP rollback is `git revert` of the M6 commit range (`src/spinlock.go:52-70`).

Under `userspaceSMP = true` (M7) Ring-3 hosts dispatch on APs (Application Processors) via the new `kschedQueuesRing3[cpu]` tier (rank 15a, sibling of rank 15). Cross-CPU contention returns at ranks 2 and 13..16 (`src/spinlock.go:72-102`). The "drop lock before `kschedWake`" rule is what keeps the rank-15 nesting single-step in the cross-CPU AP path.

## KEvent — single-shot edge-triggered

```
                    Signal()
        ┌──── flag=0 ────────► flag=1 ─────┐
        │                                  │
       Reset()                          (idempotent)
```

`KEvent` (`src/kthread_event.go:21-25`) is the gooos replacement for `chan struct{}` used as a completion signal. Layout:

```go
type KEvent struct {
    lock    Spinlock         // rank 14
    flag    uint32           // 0=unsignalled, 1=signalled
    waiters *KernelThread    // intrusive list via WakeLink
}
```

Zero value is unsignalled. `Signal()` flips `flag` to 1 and wakes every parked waiter; calling it again is a no-op. To reuse a `KEvent` you must call `Reset()` while no waiters are parked — gooos labels this "single-shot" precisely because re-arming is the caller's responsibility.

### Wait / Signal sequence

```
Producer (Signal)                 Consumer (Wait)
─────────────────                 ─────────────────
                                   flags := lock.Acquire()
                                   if flag != 0 { lock.Release; return }
                                   me.WakeLink = waiters
                                   waiters = me
                                   me.State = KStateParked
                                   me.ParkLock = &lock
                                   lock.Release(flags)
                                   kschedSwitch(bootstrap, me) ◄── parks here
flags := lock.Acquire()
flag = 1
w := waiters
waiters = nil
lock.Release(flags)
for each w:                  ─────►   resumes (possibly other CPU)
    w.WakeLink = nil                  re-install CR3+TSS
    w.ParkLock = nil                  loop, re-check flag under lock
    kschedWake(w)  (rank 15)          flag != 0 → return
```

Source: `src/kthread_event.go:31-71` (Wait), `:79-96` (Signal). Two subtleties:

1. **Snapshot-then-wake outside the lock.** Signal copies `waiters` into a local, clears the field, releases the rank-14 lock, *then* walks the snapshot calling `kschedWake` (rank 15). This honours single-step nesting (`src/kthread_event.go:86-95`).
2. **Re-check after resume.** Wait loops back and re-acquires the lock to re-check `flag`. A spurious wake (or a Reset between Signal and resume) does not produce a stale return.

### Non-kthread fallback

If `Wait` is called from a context that is not a managed kernel thread (e.g. the TinyGo goroutine context during early boot before `kschedLoop` dispatches the first kthread on this CPU), it cannot park — there is no kthread for `kschedSwitch` to swap. Instead it pumps `kschedLoopOnce()` until the flag flips (`src/kthread_event.go:41-53`). This is the boot-time degraded path; once steady-state kthread dispatch is up, no caller hits it.

## Bounded MPSC queues

`fsReqQueue` (`src/kthread_queue.go:25-35`) and `udpDgramQueue` (`src/kthread_queue.go:169-177`) are bounded ring buffers protected by an internal Spinlock (rank 13) plus an embedded park/wake protocol. The two are nearly identical; they differ only in element type (`*fsRequest` vs `UDPDatagram`) and capacity (8 vs 16).

### Ring layout

```
        +--+--+--+--+--+--+--+--+
ring →  |  |  |  |  |  |  |  |  |     fsReqQueueCap = 8
        +--+--+--+--+--+--+--+--+
         ▲              ▲
       head           tail
       (next pop)     (next push)

  count = (tail - head) mod cap, tracked separately
  Empty: count == 0     Full: count == cap

  producers ─► T1 ─► T2 ─► nil  (intrusive via WakeLink, parked on full)
  consumer  ─► fsTask           (or nil; one slot for the lone consumer)
```

`head` is the next slot to pop; `tail` is the next slot to push; `count` is the number of in-flight entries. Wrap-around uses `% cap`. Memory ordering for ring writes is provided by the spinlock release fence — the producer's store into `ring[tail]` is visible before any other CPU sees `count` increment because both happen inside the same `Acquire` / `Release` pair (`src/kthread_queue.go:41-48`).

### Push / Pop / TryPush / TryPop

Push (`src/kthread_queue.go:38-81`):
1. Take the lock.
2. If `count < cap`: write the slot, advance `tail`, increment `count`, snapshot `consumer`, release lock, then `kschedWake(consumer)` outside the lock.
3. Else (full): link self into `producers` via `WakeLink`, set `State = KStateParked`, `ParkLock = &q.lock`, release lock, `kschedSwitch` to the bootstrap to park. Loop on resume.

Pop (`src/kthread_queue.go:84-139`):
1. Take the lock.
2. If `count > 0`: take `ring[head]`, advance `head`, decrement `count`, pop one parked producer (if any), release lock, then `kschedWake(producer)` outside the lock.
3. Else (empty): self-link as `consumer` (or behind producers if a second consumer somehow appears), park.

`TryPush` (`src/kthread_queue.go:230-249`) and `TryPop` (`src/kthread_queue.go:303-326`) are the non-blocking variants exposed only by `udpDgramQueue`. `TryPush` mirrors the legacy `select { ch <- dg: default: drop }` drop-on-full of `udpHandle`. `TryPop` backs the `sys_recvfrom` timeout-bounded poll and the `socketFd.Close` drain.

### Lock + intrusive list — no node allocation

Notice the queue carries no separate "waiter node" slab: it links parked threads through `KernelThread.WakeLink` (`src/kthread.go:64-65`). `producers` and `consumer` (or `KEvent.waiters`) are roots of the same intrusive list type. The wake path therefore allocates nothing — important because `kschedWake` runs in a context that may be ISR-adjacent, where heap allocation would be unsafe.

## Producer / consumer pattern

```
Ring-3 user                  fsReqQ.Push        fsTask kthread
─────────────                ───────────        ──────────────
syscall(open)
    │
    ▼
sys_open builds *fsRequest
    │
    ▼
fsReqQ.Push(req) ──────► count++; consumer=fsTask
                          ┌─ kschedWake(fsTask)
                          ▼
                                                fsReqQ.Pop() returns req
                                                process: open the file
                                                req.resp = result
                                                req.ev.Signal() ─┐
                                                                 │
parked on req.ev.Wait() ◄────────────────────────────────────────┘
read req.resp; return to user
```

`fsTask` (`src/fs.go:197-204`) is the lone consumer of `fsReqQueue`; every `fs*Send` syscall handler (e.g. `fs.go:226`, `:233`, `:240`, `:247`, `:254`) is a producer. The reply travels through a per-request `KEvent` (`req.ev`), so the per-syscall pattern is:

1. Producer pushes a request, then calls `req.ev.Wait()` to park.
2. Consumer pops the request, processes it, writes the response, and calls `req.ev.Signal()`.
3. Producer resumes, reads the response, returns to userspace.

The same pattern reappears for UDP: `udpDgramQueue` (kept in `socketFd.recvQ`, `src/netsock.go:116`) is consumed by `sys_recvfrom` and `Read` (`src/netsock.go:131`, `:389`); the producer is the kernel RX path that calls `recvQ.TryPush(dg)` from `udpHandle`.

## afterticks timer wheel

The naive replacement for `time.After` would spawn a goroutine per call. That leaked task slots until the cooperative scheduler stalled (`src/afterticks.go:13-34`). gooos replaces it with a single long-lived dispatcher — `timerDispatcher` — that owns the entire deadline list:

```
Caller                timerListLock         timerDispatcher (long-lived kthread)
──────                ─────────────         ─────────────────
KEventAfter(d) or                            for {
afterTicks(d)                                  now := pitTicks
  ▼                                            lock; collect entries
  acquire lock                                 with deadline ≤ now;
  scan timerList[]                             release lock;
  for free slot:                               for each ready: signal/send;
    deadline = now + d                         kschedYield()
    ev or ch = ...                           }
    used = true
  release lock
  return ev (or ch)
                                              ◄── on tick: ev.Signal()
caller blocks on ev.Wait()
                                              ─── waiter wakes on resume
```

Source: `src/afterticks.go:108-153` (dispatcher), `:158-183` (afterTicks), `:194-213` (KEventAfter), `:223-243` (kschedTimedPark).

`kschedTimedPark(d)` parks the calling kernel thread for `d` PIT (Programmable Interval Timer) ticks (10 ms each on the 100 Hz PIT, `src/afterticks.go:9`). It is shorthand for `KEventAfter(d).Wait()` with one optimisation: the `KEvent` lives on the caller's stack (`src/afterticks.go:225`). It backs the `sys_sleep` syscall and any kthread that needs a bounded wait (TCP retransmit scanner, kernel echo idle poll, accept/connect/recv-with-timeout in `netsock`).

Overflow policy: if `timerList[]` (256 slots, `src/afterticks.go:69`) is full, the call returns immediately (channel pre-fired or event pre-signalled). Blocking would deadlock the TCP RTO scanner and similar critical loops; silent dropping would deadlock callers that expect to wake. Immediate return is the only safe failure mode — a caller that asked to sleep N ticks wakes too early but never deadlocks (`src/afterticks.go:36-42`).

`timerListLock` is rank 12; the dispatcher releases it before sending on channels or calling `Signal` (`src/afterticks.go:131-145`), which keeps a rank-13/14 lock acquisition during dispatch single-step.

## WakeLink — intrusive list field

`KernelThread.WakeLink *KernelThread` (`src/kthread.go:64-65`) is the single intrusive-list slot. Invariant: a thread is on at most one wait list at a time — `KEvent.waiters`, `fsReqQueue.producers`, `fsReqQueue.consumer`, `udpDgramQueue.producers`, etc., or the per-CPU `kschedReadyQueue`. Each list reuses the same `WakeLink` field, so the wake path never allocates a new list node.

```
KEvent.waiters ─► T1.WakeLink ─► T2.WakeLink ─► T3.WakeLink ─► nil
                  (parked)        (parked)       (parked)
```

The wake path simply walks the list, clearing each thread's `WakeLink` and `ParkLock` before calling `kschedWake` so that the woken thread sees a clean state on resume (`src/kthread_event.go:89-94`, `src/kthread_queue.go:96-103`).

## ParkLock — closing the lost-wakeup race

The lost-wakeup hazard: thread C (consumer) checks the predicate (e.g. "queue empty"), decides to park, but before C actually transitions to `KStateParked` a producer P pushes an item and calls `kschedWake(C)`. If `kschedWake` runs first and observes `C.State == KStateRunning`, it does nothing — and C then parks forever.

gooos closes this race two ways:

1. **State transition under the predicate's lock.** The consumer holds the queue spinlock (rank 13) while it both rechecks the predicate *and* writes `me.State = KStateParked` and `me.ParkLock = &q.lock` (`src/kthread_queue.go:69-73`). The producer cannot wake C until P also acquires the same lock — and P does, every push (`:41`).
2. **Producer takes the wake list under the same lock.** The producer pops `prod = q.producers` (or `cons = q.consumer`) inside the lock, releases the lock, *then* calls `kschedWake(prod)` (`:101-103`). By the time `kschedWake` runs, the parked thread is guaranteed to have hit `KStateParked` (because that store happened under the lock the producer just held).

`KernelThread.ParkLock *Spinlock` (`src/kthread.go:68-69`) records which lock guarded the parking; it is purely informational at runtime (the asm switch path does not consult it), but it makes the protocol explicit and is cleared on wake (`src/kthread_event.go:92`, `src/kthread_queue.go:51`). A future condvar-style primitive could re-acquire it on resume; today's primitives all re-acquire by hand.

## Memory ordering and `mfence`

The x86 memory model is strong (TSO — Total Store Order); explicit fences are required only for store-load ordering. gooos uses two:

- **`xchg` on Acquire** has an implicit `lock` prefix and is a full barrier — prior loads cannot move past it (`src/stubs.S:451-456`).
- **`mfence` on Release** drains the critical-section stores before the unlock store becomes visible (`src/stubs.S:469-470`). Strictly speaking the unlock store cannot be reordered before earlier stores under TSO, but `mfence` is conservative and free in the lock-release path's overall cost (`impldoc/smp_percpu_and_sync.md:253-257`).

`mfence` does not appear elsewhere in `src/`. The kernel relies on the spinlock-release fence and the `lock`-prefixed `xchg` of `Acquire` to provide all visibility guarantees other code paths need.

## GC interaction (forward to Chapter 11)

The gooos GC (Garbage Collector) is a conservative STW (Stop The World) collector: at GC time it pauses every kthread, scans stacks, and resumes them. Synchronization primitives are therefore GC-safe by construction — the GC pause point is a kthread switch boundary, and parked / running threads are equally suspendable. Chapter 11 covers the STW handshake; the relevant fact here is that holding a `Spinlock` does *not* prevent GC from happening on another CPU (it only prevents preemption of the holder). Critical sections must remain finite so the GC can complete in bounded time.

## "No Go channels" cheat sheet

| Idiomatic Go                       | gooos kernel replacement                              | Source                          |
| :--------------------------------- | :---------------------------------------------------- | :------------------------------ |
| `make(chan struct{})` + `<-ch`     | `KEvent.Wait()` / `KEvent.Signal()`                   | `src/kthread_event.go`          |
| `make(chan T, N)` + `ch <- v`      | bounded MPSC ring queue (`fsReqQueue` / `udpDgramQueue`) | `src/kthread_queue.go`       |
| `select { ch <- v: default: drop }`| `q.TryPush(v)` returning `false` on full              | `src/kthread_queue.go:230-249`  |
| `select { case v=<-ch: case <-tmo}`| `q.TryPop()` polled inside `kschedTimedPark` budget   | `src/kthread_queue.go:303-326`  |
| `time.After(d)`                    | `KEventAfter(d).Wait()` or `kschedTimedPark(d)`       | `src/afterticks.go:194-243`     |
| `sync.Mutex`                       | `Spinlock` with documented rank                       | `src/spinlock.go`               |
| `sync.Cond` style wait/notify      | `KEvent` (single-shot) or queue's built-in park/wake  | `src/kthread_event.go`          |
| `runtime.Gosched()`                | `kschedYield()` (when on a kthread)                   | `src/kthread_lifecycle.go`      |

The mapping is deliberately one-to-one: any Go program that uses channels for the patterns above translates to a gooos kthread that uses these primitives. The translation cost is paying the rank tax — every new spinlock must slot into the rank table at the top of `src/spinlock.go` before it is allowed to participate in any nesting.

## Summary

- `Spinlock` is xchg-based test-and-set with explicit RFLAGS save/restore; that is what makes it interrupt-safe and properly nestable across kthread / ISR contexts.
- The 17-rank table at the top of `src/spinlock.go` is the authoritative deadlock-prevention scheme. A holder of rank N may only acquire ranks > N. Ranks 13..15 use single-step nesting via "drop lock before `kschedWake`".
- `KEvent` is a single-shot edge-triggered event with a Spinlock (rank 14) + flag + intrusive waiter list. `Signal` snapshots and clears the list under the lock, then walks the snapshot calling `kschedWake` outside the lock.
- Bounded MPSC queues (`fsReqQueue`, `udpDgramQueue`) are fixed-size rings + Spinlock + intrusive producer/consumer wait lists. `Push`/`Pop` block; `TryPush`/`TryPop` are the non-blocking variants used by drop-on-full RX paths and timeout polls.
- `WakeLink` is the per-thread intrusive-list field that lets every wait list reuse the same node — no allocation in the wake path. `ParkLock` records the predicate's lock and closes the lost-wakeup race when paired with the "transition state under predicate lock" rule.
- `kschedTimedPark(d)` parks the caller until `pitTicks >= now + d`, backed by a single long-lived `timerDispatcher` kthread that owns all deadlines; this is what replaces `time.After`.

## Cross-references

- `./05_kernel_thread_runtime.md` — `kschedPark` / `kschedWake` / `kschedSwitch` and the `KernelThread` struct that backs all of the wait lists here.
- `./06_smp_and_preemption.md` — wake IPI used by `kschedWake` when a target thread's `OwnerCPU` differs from the caller's.
- `./08_syscalls.md` — blocking syscalls (`sys_open`, `sys_read`, `sys_recvfrom`, `sys_sleep`) that compose `fsReqQueue` / `udpDgramQueue` push + per-request `KEvent.Wait` and `kschedTimedPark`.
- `./10_drivers_filesystem_network.md` — the actual queue users (`fsTask`, `socketFd.recvQ`, `udpHandle`).
- `./11_tinygo_baremetal.md` — STW GC handshake; how synchronization primitives stay GC-safe.
