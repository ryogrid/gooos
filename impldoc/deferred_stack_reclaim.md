# Deferred — Ring-3 goroutine stack reclamation (item 9)

Covers inventory item 9 from `deferred_overview.md §1`. Eliminates
the ~8 KiB goroutine-stack leak that every `exec` causes today,
unblocking long-running shell sessions.

## 1. Problem statement

Current behavior (`src/process.go:processExit`,
`phase_b_ring3_and_exec.md §11`):

- Each `sys_exec` spawns `go ring3Wrapper(child)` — a new
  TinyGo goroutine with its own ~8 KiB heap-allocated stack.
- When the user program calls `sys_exit`, `processExit`:
  1. frees the child's user pages,
  2. restores the parent's mappings,
  3. sends the exit code on `proc.exitCh`,
  4. `taskPause()` — parks the child goroutine permanently.
- The TinyGo runtime has no "this goroutine is done, reap it"
  primitive in the `scheduler=tasks` variant. The parked `Task`
  remains reachable via the scheduler's internal structures, so
  the conservative GC never frees the stack allocation.

Measured impact:

- Each `exec` leaks ~8 KiB (stackTop − canaryPtr inside the
  TinyGo-allocated goroutine stack).
- Heap is 4 MiB (`src/linker.ld:.heap`).
- Worst-case budget: `(4 MiB - fixed-overhead) / 8 KiB ≈ 500`
  execs before the allocator fails. Sendkey harness fits
  comfortably; interactive sessions do not.

This is `partial` state: the leak is documented (`TODO_B.md`,
`phase_b_ring3_and_exec.md §11`), accepted for v1, but no
mitigation lands until this design is executed.

## 2. Design options considered

Three candidate mechanisms. Ranked recommendation follows in §3.

### 2a. TinyGo runtime patch: `task.Reap`

Add a gooos-local runtime primitive `task.Reap(t *Task)` that:

1. Detaches `t` from every runtime list the scheduler walks
   (runqueue is the likeliest; sleepQueue and any channel
   `blocked` list may also hold `t` in edge cases).
2. Clears `t.state.canaryPtr` so subsequent accidents do not
   alias a freed stack.
3. Frees the stack allocation via `runtime_free` (a new
   runtime export that wraps `runtime.free` on the conservative
   collector).

Call site: a **reaper goroutine** (long-lived, one per kernel)
that receives dying tasks on a `chan *task.Task` and reaps them.
`processExit` sends `task.Current()` on the reaper's channel,
then `taskPause()`. The reaper wakes, reaps the child, returns
to `range reaperCh`.

Patch surface (new edits to
`~/.local/tinygo/src/internal/task/task_stack.go`):

- New `//go:linkname`-exported `tinygo_task_reap(t *Task)` that
  does the stack free.
- Invariant: `task.Reap` may only run from another goroutine
  (never on `t` itself).

**Pros**: clean. Full goroutine lifecycle.

**Cons**: grows `scripts/tinygo_runtime.patch` by ~40 lines.
Another TinyGo surface the kernel owns; upgrades require
re-verifying the patch. Needs the same runtime-fork discipline
SMP v2 will need anyway.

### 2b. Kernel-side Ring-3 stack pool

Pre-allocate a fixed pool of kernel-owned 8 KiB stacks
(`[maxProcs][2]page` via `allocPagesContig(2)` at boot). Each
`ring3Wrapper` gets a pool slot instead of TinyGo's heap-
allocated goroutine stack. On `processExit`, the slot returns
to the pool; the goroutine itself still parks forever (stack
leak of the runtime-allocated TinyGo shell stack), but the
per-exec bulk (8 KiB) returns.

Concretely: `ring3Wrapper` becomes thin — it does just enough
to record the goroutine and jump to Ring 3. The real "stack" it
uses is TSS-pointed. Its TinyGo-allocated stack can be tiny
(2 KiB).

**Pros**: no TinyGo patch. Bounded memory by construction.

**Cons**: `maxProcs` cap is surfacing. Processes beyond the
cap have to wait for a free slot. Small TinyGo-stack still
leaks but much less (2 KiB vs 8 KiB × no-reap). Doesn't
generalize to other orphaned goroutines.

### 2c. Accept the leak + oversize the heap

Grow `_heap_end` — `_heap_start` to, say, 32 MiB; hope the user
never exceeds 4000 execs between reboots.

**Pros**: zero code.

**Cons**: not a real fix. Masks the problem. Rejected.

## 3. Recommended: 2b (kernel-side pool) for v1, 2a as follow-up

Rationale:

- 2b ships in one commit without a TinyGo runtime change.
  Phase B already established that the TinyGo patch surface
  should stay minimal (four files, via
  `scripts/tinygo_runtime.patch`).
- Post-2b, per-exec heap consumption drops from ~8 KiB to
  ~2 KiB. The 4 MiB heap now supports ~2000 execs — a 4×
  improvement, enough for any realistic shell session.
- When SMP v2 (`deferred_smp_v2.md`) forces a TinyGo fork, 2a
  becomes cheap to adopt on top of that fork and can then
  deliver a true zero-leak solution.

2b is therefore the v1 mitigation; 2a is the v2 fix.

## 4. Implementation sketch (option 2b)

### 4.1 Pool

New file `src/ring3_pool.go`:

```go
// ring3StackPool holds pre-allocated 8 KiB kernel stacks for Ring 3
// goroutines. ring3Wrapper gets a slot at spawn and releases it on
// processExit; this way the per-exec stack bulk is bounded even
// though the owning goroutine parks forever.

package main

import "unsafe"

const maxRing3Procs = 32

type ring3StackSlot struct {
    base  uintptr // top-of-page address of the 2-page allocation
    inUse bool
}

var ring3StackPool [maxRing3Procs]ring3StackSlot
var ring3StackPoolCh = make(chan int, maxRing3Procs) // slot indices available

func ring3StackPoolInit() {
    for i := range ring3StackPool {
        ring3StackPool[i].base = allocPagesContig(2)
        ring3StackPool[i].inUse = false
        ring3StackPoolCh <- i
    }
}

// ring3StackAcquire blocks until a slot is available; returns the
// slot index and the kernel stack top.
func ring3StackAcquire() (int, uintptr) {
    idx := <-ring3StackPoolCh
    ring3StackPool[idx].inUse = true
    return idx, ring3StackPool[idx].base + 2*pageSize
}

func ring3StackRelease(idx int) {
    ring3StackPool[idx].inUse = false
    ring3StackPoolCh <- idx
}
```

Called once at boot from `main()` after `vmInit()`.

### 4.2 `ring3Wrapper` changes

Today `src/process.go:119` calls `registerRing3G()` (defined at
`src/goroutine_tss.go:93`). The helper internally calls
`taskStackTop(t)` to record the goroutine's own stack top. Under
the pool design, the kernel stack is the pool slot — not the
goroutine's own stack — so we need to record a caller-supplied
top.

**Step 1**: add a sibling helper to `src/goroutine_tss.go`:

```go
// registerRing3GWithStack is like registerRing3G but uses a
// caller-supplied kernel stack top. Used by ring3Wrapper when
// the kernel stack comes from ring3StackPool, not from the
// hosting goroutine's own stack.
func registerRing3GWithStack(stackTop uintptr) {
    t := taskCurrent()
    gInfoByTask[t] = &gInfo{stackTop: stackTop}
}
```

**Step 2**: rewrite `ring3Wrapper` (`src/process.go:104+`):

```go
func ring3Wrapper(proc *Process) {
    idx, stackTop := ring3StackAcquire()
    proc.poolIdx = idx
    proc.kernelStackTop = stackTop

    setCurrentProc(proc)
    registerRing3GWithStack(stackTop) // pool stack, not goroutine stack
    tssSetRSP0(stackTop)
    setGateDPL3(0x80)

    jumpToRing3(proc.EntryPoint, proc.StackTop)
}
```

`processExit` (`src/process.go:266`) adds
`ring3StackRelease(proc.poolIdx)` between `unregisterRing3G()`
and `taskPause()`.

The TinyGo goroutine's own stack (the one `runtime_alloc`
returned at `go` time) is still "leaked" but shrinks dramatically
because the goroutine's live call depth never exceeds a few
frames (only `ring3Wrapper` + `jumpToRing3` before Ring-3
starts).

### 4.3 TinyGo stack size reduction

With 2b in place, the goroutine's own stack is barely used —
the heavy lifting runs on the pool stack (via TSS.RSP0). Pair
with item 13's audit (`deferred_gc_and_stacks.md §4`) before
turning any knobs.

`src/target.json:10-11` currently sets both
`"automatic-stack-size": true` and `"default-stack-size": 8192`.
Under `automatic-stack-size`, TinyGo estimates each goroutine's
need from its static call graph; `default-stack-size` is the
fallback when the estimator cannot decide (e.g., goroutines that
call through interfaces or function values). Lowering the
default has no effect on goroutines whose estimate succeeds. So
the lever order is:

1. Confirm via item 13 audit that `ring3Wrapper`'s estimate
   already comes out small (likely yes — its call graph is
   short and static).
2. If yes, no `target.json` change is needed; the leak is
   already bounded by the estimator.
3. If the estimator falls back to the default for any goroutine
   in the audit, *then* lower `default-stack-size`.

Expected outcome with the pool in place: ~1 KiB per
`ring3Wrapper` goroutine, improving total leak to roughly:
`1 KiB × exec_count`.

### 4.4 Boot-time capacity surfacing

`maxRing3Procs = 32` caps the number of **concurrently
live** Ring-3 processes, not total execs. Per
`phase_b_ring3_and_exec.md §6`, only one exec is in flight at a
time in v1 (nested exec is rejected). So the pool is never more
than 1 slot deep at once; 32 is generous.

The "cap surfacing" concern from §2b is therefore moot in v1.
Document as "increase maxRing3Procs if the process model ever
allows concurrency".

## 5. Files to modify

| File | Change |
|---|---|
| `src/ring3_pool.go` | **new** — pool + init + acquire/release |
| `src/main.go` | call `ring3StackPoolInit()` after `vmInit()` |
| `src/process.go` | `ring3Wrapper` uses pool slot; `processExit` releases; `Process` gains `poolIdx int` field |
| `src/target.json` | consider lowering `default-stack-size` once item 13's audit confirms the pool path uses minimal goroutine stack |

No TinyGo runtime patches for option 2b.

## 6. Dependencies

- Item 13 (stack-size audit) should run first to confirm
  post-pool goroutine stacks fit comfortably below 2 KiB. If
  not, stay with `default-stack-size: 8192`.
- `allocPagesContig` already exists (`src/vm.go:115`); no
  changes to the VM layer.

## 7. Verification

1. `make build` clean.
2. 10/10 sendkey trials pass (regression check).
3. **Long-run stress**: extend `tmp/stress_test.sh` to run
   `ls; ls; ls; ...` 500 times in one session. Sample
   `runtime.MemStats.HeapInuse` via a kernel-side print every
   50 iterations. Pass criterion: `HeapInuse` plateaus (with
   2b, growth rate ~= 1 KiB/exec × 500 = 500 KiB total; under
   the 4 MiB budget).
4. **Pool-exhaustion test** (manual): set `maxRing3Procs = 2`
   temporarily, confirm the third concurrent exec blocks on
   `<-ring3StackPoolCh` instead of panicking. (This is
   theoretical in v1; nested-exec is rejected by
   `sysExecHandler`.)

## 8. Open questions

1. **Is 32 pool slots enough for eventual nested-exec support?**
   Nested exec is rejected today; if a future milestone allows
   it, `maxRing3Procs` must be sized by the deepest legitimate
   exec chain. Likely small (≤8) in practice.
2. **Should the pool pre-zero each slot on release?** A
   freshly-returned slot's last known content is the exited
   process's kernel stack frames. If the next `exec`'s
   `ring3Wrapper` reads uninitialized local variables before
   writing them, stale data could leak. TinyGo's compiler
   zeroes locals, but ISR entry does not. Pre-zero on release
   is cheap (8 KiB memset) and defensive.
3. **Does the pool interact with the orphan-goroutine-reaper
   in option 2a?** If 2a lands later, each `exec` then both
   releases the pool slot *and* reaps the TinyGo goroutine's
   own stack. Full lifecycle cleanup. No conflict.

## 9. Risk register delta

- **Retires**: `R-orphan-goroutine-stack` (the leak is bounded
  to ~1 KiB × exec_count; well within 4 MiB heap for realistic
  sessions).
- **Adds**: `R-ring3-pool-cap` — the new cap is 32 live
  processes; irrelevant until nested exec is allowed.
