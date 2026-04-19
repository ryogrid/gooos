# 02 — Evidence and Hypotheses

The previous session added instrumentation, captured serial logs
at late timing, and narrowed the search. This file summarises
what is **known**, what has been **ruled out**, and the **leading
hypothesis** with its source evidence.

The authoritative root-cause writeup in the repo is
`pasttodos/TODO_NET3.md` lines 472-574. This file abridges and
orients; consult that block for the original phrasing.

## Known (from captured serial logs during investigation)

The five items below were captured during the investigation using
temporary serial prints that have **since been removed** from HEAD
(see commit `2abec07` body for which prints were stripped). The
evidence is reproducible by re-adding the prints if needed, but
for reading HEAD alone you will see only the residual
instrumentation: `e1000IRQCount`, `rxReadyFlag`, `lastICR`, the
piggybacked netDiag in `netRxLoop`, and the expanded `netDiag`
body (IMS / RCTL / RDBAL / RDBAH / RDLEN / DD[0..7]).

1. **The PIT keeps ticking indefinitely.** A one-line
   `pit alive` diagnostic temporarily inserted into `handleTimer`
   (`src/pit.go`) fired continuously for 30+ seconds post-Ring-3.
   `pitTicks` in the last captured netDiag also confirms monotonic
   advance. The print is gone from HEAD; `pitTicks` in netDiag
   is the remaining signal.

2. **Short-lived goroutines die early.** A 2-second heartbeat
   goroutine using the obvious `for { <-afterTicks(200);
   serialPrintln("heartbeat") }` pattern fired **6 times and
   stopped** — the scheduler stopped dispatching it after
   ~12-14 s. The heartbeat goroutine was removed in `2abec07`
   because it was no longer needed for proving scheduler
   liveness (that question is settled).

3. **Self-rescheduling goroutines also die.** An attempted
   diagnostic that spawned a fresh sibling every tick via
   `go func(){ <-afterTicks(200); netDiag() }()` died after
   **~2 fires**. Spawning more, sooner, exhausted something
   faster. Also removed in `2abec07`.

4. **`netRxLoop` survives longer but not forever.** The only
   long-lived goroutine with no sub-spawns — pure
   `drainRxRing() ; runtime.Gosched()` loop — survives **two**
   piggyback netDiag snapshots (iter=1000, iter=2000 at ~200/s
   ≈ 5 s + 10 s) but stops before iter=3000. The piggyback
   itself **is** still in HEAD (`src/net.go:70-85`, including
   the `netRxDiagPeriodIterations` constant) and is the primary
   observable signal of the bug today.

5. **The e1000 ISR keeps firing post-stall.** `e1000IRQCount`
   continues to advance when the host retransmits a TCP SYN.
   `rxReadyFlag` is set. But `NetRxLoopWakes` (the loop counter
   inside `netRxLoop`) has stopped advancing — nothing drains
   the descriptor ring, so no frame reaches `ethernetDispatch`,
   so the guest emits no reply. All three counters are still
   in HEAD and visible in a `netDiag` snapshot.

## Ruled out (with refutation)

### CR3 / identity-map corruption

Refuted because `netDiag` calls `e1000Read(e1000RDH)` — an MMIO
read against the BAR0 mapping — and the call succeeds, returning
`0` (the same value `e1000Init` left RDH at). That proves the
BAR0 PT chain is still live in the currently-active PML4.

Also `newProcPML4` in `src/proc_pml4.go` copies boot `PDP[3]`
*after* `e1000Init` runs, so every per-process PML4 shares the
e1000 PT chain by entry value. A CR3 swap on Ring-3 entry cannot
un-map BAR0.

See TODO_NET3.md lines 506-512.

### Conservative GC reclaiming DMA pages

Refuted because the RX descriptor ring and RX buffers come from
`allocPagesContig` (`src/vm.go`), which is a bump allocator over
a physical-memory pool disjoint from the Go heap. The GC has no
visibility into those pages. And `rxDescRing` / `rxBufs` are
stored as `uintptr`, so the conservative GC's root scan does not
misidentify them as heap pointers either.

See TODO_NET3.md lines 513-518.

### e1000 IMS / IMC cleared after boot

Refuted:

```
$ grep -n 'e1000Write(e1000IMS' src/
src/e1000.go:...   (one hit, inside e1000Init)
$ grep -n 'e1000Write(e1000IMC' src/
src/e1000.go:...   (one hit, inside e1000Init)
```

Nothing post-boot touches IMS or IMC. Reading IMS from `netDiag`
also returns the configured value. Interrupts remain enabled;
the NIC is not "deaf".

See TODO_NET3.md lines 519-521.

### Channel-based ISR wakeup race

Pre-investigation the ISR used `select { case rxSignalCh <-
struct{}{}: default }` to wake `netRxLoop`. This was rewritten
to a plain `rxReadyFlag uint32` on suspicion of ISR-context
channel sends corrupting the runqueue (see
`current_impl_doc/scheduler.md` for why ISRs cannot touch
runqueue state). The stall persisted after the rewrite, so this
is **not** the root cause, but the rewrite is kept: it's a
correct simplification regardless.

## Leading hypothesis

**TinyGo task-slot or task-stack exhaustion**, driven by the
per-call goroutine spawn pattern in `afterTicks`.

Every call to `afterTicks(d)` spawns a fresh goroutine that
busy-yields until `pitTicks` reaches the deadline:

```go
// src/afterticks.go:24-36
func afterTicks(d uint64) <-chan struct{} {
    ch := make(chan struct{}, 1)
    go func() {
        deadline := pitTicks + d
        for pitTicks < deadline {
            runtime.Gosched()
        }
        ch <- struct{}{}
    }()
    return ch
}
```

A naive `for { <-afterTicks(N) }` loop spawns a new task on every
iteration. The TinyGo `scheduler=tasks` runtime allocates a
fixed-size task structure (stack + state) from a pool. If that
pool has a cap that is not being recycled (e.g. completed tasks
do not return their slots until GC, and the conservative GC is
not reclaiming them because their stacks are still rooted), the
pool fills up and no new goroutine can be scheduled — including
the long-lived `netRxLoop` after enough context switches.

The observed evidence timing (6 heartbeat fires before stall,
2 self-resched fires, netRxLoop survives only ~20 s even with no
sub-spawns) is consistent with a slow-but-monotonic leak.

## Also plausible (not confirmed)

**Ring-3 shell starvation of kernel goroutines.** The stall
timing correlates with shell activity. If the TinyGo scheduler
strongly prefers Ring-3 wrappers over kernel goroutines once
the shell is running, cooperative yields from the shell's syscall
path may be enough to keep kernel goroutines off the runqueue.
Evidence is circumstantial — the bootup `testAfterTicks` marker
("afterTicks: OK") lands AFTER the shell prompt, not before —
but this could also be coincidence.

Either explanation leads to the same place: something about the
post-Ring-3 scheduler state is preventing kernel goroutines from
being dispatched. Pick one hypothesis to test first, but be
prepared for the answer to be the other.

## Candidate fix approaches

Carried over from TODO_NET3.md lines 552-570. This handoff
deliberately does NOT pick one:

- **(a) Reuse the `afterTicks` goroutine.** Replace the
  per-call spawn with a single long-lived timer-wheel goroutine
  that dispatches wake channels when `pitTicks` crosses a
  deadline. Removes the leak source if the leak is in the
  spawn pattern. Moderate plumbing.
- **(b) Bump the task-slot cap** in the patched TinyGo runtime
  (`~/.local/tinygo/src/runtime/`). Cheapest if confirmed cap.
  Delays the symptom rather than fixing the pattern; not a
  stand-alone solution.
- **(c) Rewrite the RX dispatch to not depend on any kernel
  goroutine.** ISR drains the ring inline and hands off to a
  bottom-half driven by the e1000 TX-done IRQ. Invasive; ISR
  must stay fast and allocation-free. Probably overkill.
- **(d) Dedicated non-cooperative thread for `netRxLoop`.**
  Would need TinyGo-runtime work to give one task a separate
  runqueue. Most invasive.

My recommendation (for what it's worth): start with **(a)**.
It's the smallest change that attacks the suspect mechanism
directly, and it also fixes `sys_sleep` and every other
`afterTicks` consumer (which are numerous).

## Confirmation step before committing to a fix

Do step 1 in `04_investigation_next_steps.md` first —
instrument the TinyGo scheduler to expose its task-slot
occupancy over serial. If occupancy plateaus at the pool cap
when the stall begins, that's confirmation. If it doesn't,
pivot to the Ring-3 starvation hypothesis.
