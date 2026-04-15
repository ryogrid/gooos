# Deferred — GC and goroutine stacks (items 6, 7, 13)

Covers inventory items 6, 7, and 13 from `deferred_overview.md §1`:

- §2: item 6 — precise (write-barrier) GC gap analysis
- §3: item 7 — growable per-goroutine stacks
- §4: item 13 — `R-goroutine-stack-size` audit + bounds

These three items cluster because they share the same
substrate: how TinyGo's `scheduler=tasks` allocates and manages
goroutine stacks, and how the conservative collector interacts
with them.

## 1. Shared context

Current state (Phase A/B):

- `src/target.json:10-11` enables `"automatic-stack-size": true`
  with `"default-stack-size": 8192`. TinyGo's compiler estimates
  each goroutine's stack need via static call-graph analysis and
  sizes the allocation accordingly.
- Stacks are allocated via `runtime_alloc` from the 4 MiB kernel
  heap (`src/linker.ld:.heap`). No stack growth at runtime.
- GC is conservative (`src/target.json:8`: `"gc": "conservative"`)
  — the collector treats any aligned machine word that points
  inside the heap as a live pointer. No write barriers.
- TinyGo Task struct stores `canaryPtr` (bottom) and, after Phase
  B's patch, `stackTop` (top). Stack overflow is detected on the
  next `task.Pause` via canary check and hard-panics via
  `runtimePanic("goroutine stack overflow")`.

## 2. Item 6 — Precise (write-barrier) GC

### 2.1 Why deferred

`goroutine_design_gc_and_smp.md §1.3` acknowledges that
TinyGo's conservative collector is good enough for gooos:

- No write-barrier overhead on every pointer store.
- No cooperation with concurrent mutators.
- Works on any platform TinyGo compiles to.

The cost is precision: conservative scanning can pin blocks
that "look like" pointers. In practice the false-positive rate
is low enough that the Phase B stress test sees bounded heap
growth.

Item 6 is deferred because full precise GC requires either (a)
switching TinyGo to its precise variant and accepting the
write-barrier overhead on every store, (b) upstreaming a
write-barrier implementation into TinyGo's conservative
collector, or (c) implementing precise GC in a gooos fork.

### 2.2 Gap analysis

**What TinyGo currently provides (conservative):**

- `runtime/gc_blocks.go`: mark / sweep.
- `runtime/gc_stack_*`: scan the currently-running goroutine's
  stack for pointers.
- `gc_blocks.go:425-454`: special-case scan of `runqueue` to
  catch Task pointers.
- Sleep / timer / channel-blocked tasks are reachable via
  their containing structures (globals or heap objects that
  hold them).

**What upstream Go's precise GC needs, summarized:**

- **Stack maps** for every function frame, emitted by the
  compiler. Lists which slots on the stack hold pointers.
  TinyGo does not emit these today under `scheduler=tasks`.
- **Write barriers** on every pointer store. `go:linkname`'d
  `gcWriteBarrier` is invoked to record the old value before
  overwriting. Upstream Go generates these via the compiler;
  TinyGo's conservative GC does not.
- **Stop-the-world coordination** for mark-phase safe points.
  Precise GC assumes a mechanism to pause every goroutine at
  a safe point (where stack maps are valid).

**Kernel invariants that would change:**

- ISR handlers would need to pause / re-enter at well-defined
  safe points so stack maps could be trusted. This is
  incompatible with gooos's "never yield inside ISR" rule
  (`goroutine_design_channels_and_isr.md §3.1`).
- `runtime_alloc` call sites inside the kernel would take the
  write-barrier hit; tight loops (`serialPrintln` concat,
  channel ops) would slow down measurably.
- The existing `checkTaskOffset` hack
  (`src/goroutine_tss.go`) hard-codes a layout offset; precise
  GC could reshape the Task struct.

### 2.3 Decision: document the gap, do not implement

Full precise GC is out of scope for gooos for the foreseeable
future. This doc serves as the reference for a future
contributor who revisits the question. The realistic paths are:

1. **Wait for TinyGo upstream** to add write barriers. Track
   `tinygo-org/tinygo` issues.
2. **Run a precise-GC prototype on a branch** for benchmarking;
   don't merge unless throughput gains justify the invariant
   churn.
3. **Accept conservative GC indefinitely**. Measured impact is
   low because:
   - Heap size is 4 MiB, small enough that false-positive pins
     are bounded.
   - The kernel allocates far less than the heap limit per
     sendkey run.
   - Long-lived objects (channels, tasks) are intentionally
     few and small.

No file-level changes are proposed. Update `TODO.md`
"Deferred" to reference this doc as the explanation.

## 3. Item 7 — Growable per-goroutine stacks

### 3.1 Why deferred

TinyGo's `scheduler=tasks` allocates a fixed-size stack at
goroutine creation and does not grow it. Overflow corrupts the
following heap object. The compiler inserts per-function
prologue checks (`__stack_chk_fail`-style) that panic on
overflow, but "panic" in our case means halt — the program
cannot recover.

Upstream Go grows goroutine stacks by **copying**: when the
prologue check detects impending overflow, the runtime
allocates a larger stack, copies the old frames, and updates
pointers. This requires precise GC (stack maps) to know which
slots hold pointers.

### 3.2 Minimum viable mitigation (ship first)

Precise GC is out of scope (§2.3). The minimum mitigation that
ships without stack maps:

- **Better overflow diagnosis.** The current `runtimePanic
  "goroutine stack overflow"` is a trap that halts with no
  context. Replace with a message that includes:
  - The offending goroutine's start function (`task.Task`
    struct has it).
  - The stack base + size.
  - The last N words of the stack (read via `canaryPtr`).

  Implementation: when `task.Pause()` detects canary mismatch,
  call a new gooos helper `serialPanicStackOverflow(t)` that
  formats into `panicHexBuf` (see `deferred_fatal_handlers.md
  §2`) and writes via `serialPrintBytes`. Both helpers are added
  by item 8; this item depends on item 8 landing first.

- **Boot-time stack sizing audit.** See item 13 (§4). If the
  audit shows a goroutine comes within 25% of its stack limit,
  bump `default-stack-size` in `target.json` (and have
  automatic-stack-size estimate with more padding).

### 3.3 Full growable stacks (future work)

Real growable stacks require:

1. **Stack maps emitted per function** — a compiler-side
   change to TinyGo. Out of scope.
2. **Stack-growth trampoline** — an assembly stub invoked
   from the prologue check that allocates a new stack,
   copies frames, fixes up `state.sp` / `canaryPtr` /
   `stackTop` / TSS.RSP0 for Ring-3 goroutines, and
   resumes.
3. **Pinned-object awareness** — some Ring-0 code takes the
   address of locals and passes them across goroutine
   boundaries (e.g., the `SyscallFrame` in `src/isr.S`
   points into the current goroutine's ISR stack). Growable
   stacks must either forbid this pattern or patch it.

This block is deferred jointly with item 6 because it depends
on the same stack-map machinery.

### 3.4 Files to modify (mitigation only)

| File | Change |
|---|---|
| `src/panic.go` | **extends item 8's file** — add `serialPanicStackOverflow(t *task.Task)` that formats into `panicHexBuf` + emits via `serialPrintBytes` (no allocation) |
| `~/.local/tinygo/src/internal/task/task_stack.go` | patched to call the gooos helper from `Pause()` on canary mismatch (piggybacks on the existing check) |
| `scripts/tinygo_runtime.patch` | add the one-line change so reapply remains automatic |

No file changes for full growable stacks — they stay deferred.

## 4. Item 13 — `R-goroutine-stack-size` audit + bounds

### 4.1 Problem

Phase A risked that TinyGo's `getGoroutineStackSize` estimator
underestimates actual stack depth for deep call chains
(`serialPrintln` + string concat cascade through the GC
allocator, for example). Today the estimator is trusted
blindly; any overflow is detected only at the next canary
check, which may be many function calls late.

### 4.2 Boot-time sweep

**Scope (v1)**: only the goroutines the kernel itself spawns and
holds references to, plus the currently-running Ring-3 wrapper.
This is implementable today without touching TinyGo internals.
Full enumeration (walking `runqueue` / `sleepQueue` / channel
blocker lists) requires gooos-side iterators that don't yet
exist; defer to a follow-up once SMP v2's runtime fork
(`deferred_smp_v2.md §2.1`) makes those structures accessible.

The known-task set the kernel can hand to `stackSizeAudit()`
without runtime cooperation:

- `fsTaskHandle` — the filesystem service goroutine
  (`src/main.go` spawns it; capture the `*task.Task` returned
  by `taskCurrent()` inside the goroutine's body and store in a
  package-scope variable).
- `keyboardPumpHandle` — same pattern for the keyboard pump.
- `taskCurrent()` — the audit caller itself (typically `main`).
- `ring3WrapperHandle` — captured inside `ring3Wrapper` once a
  Ring-3 process is live.

For each task, compute:

- `stackSize = stackTop - canaryPtr` (both on the patched Task
  struct).
- `stackUsed = stackTop - state.sp` (valid only for parked tasks;
  the running task's `state.sp` is stale until next yield).

Log each one to serial:

```
stack-audit: fn=main.fsTask               size=8192  used=256   (3%)
stack-audit: fn=main.keyboardPump         size=8192  used=128   (1%)
stack-audit: fn=main.ring3Wrapper(sh.elf) size=8192  used=4096  (50%)
```

Threshold: if any goroutine's `used / size > 0.75`, flag a
warning and recommend bumping `default-stack-size` (or, if
`automatic-stack-size` is on, padding the per-function estimate;
see §7 Q3).

### 4.3 Integration

Call `stackSizeAudit()` once after boot's `checkTaskOffset()`
and again after the first successful `exec` (so the Ring-3
wrapper's stack use is measurable). Output only on serial;
VGA is not needed for this.

The audit runs once and exits — it is not a permanent
goroutine; call sites are a one-shot boot diagnostic. In a
release build, compile-time flag `const runStackAudit = false`
disables it (use `//go:build audit` or similar).

### 4.4 Files to modify

| File | Change |
|---|---|
| `src/stack_audit.go` | **new** — iterator + logger |
| `src/main.go` | add calls (guarded by `runStackAudit` constant) |
| `impldoc/deferred_gc_and_stacks.md` | this file; updated with measurement results after first run |

## 5. Dependencies

- Item 7's mitigation (§3.2) depends on item 8's
  `panic.go` helpers (shared `serialPanicPrint` +
  `appendHex` / `appendStr`).
- Item 13 should run before item 7's full design is
  finalized: real measurements inform the threshold.
- Item 6 gates true growable stacks; ship the mitigation
  (§3.2) independently.

## 6. Verification

**Item 6:** Documentation-only. No verification step.

**Item 7 (mitigation):**

1. `make build` clean.
2. Deliberately overflow a kernel goroutine — e.g., a
   recursive function spawned as `go recursive(0)` that
   consumes ~12 KiB. Confirm the new
   `serialPanicStackOverflow` message appears on serial.
   Remove the trigger.

**Item 13:**

1. Enable `runStackAudit = true`.
2. Boot, run `ls; cat hello.txt`, observe the audit output.
3. Pass criterion: no goroutine exceeds 75% of its stack.
4. If any do: bump `default-stack-size` and re-run.
5. Disable `runStackAudit` before landing.

Overall SE regression: 10/10 sendkey trials green with and
without instrumentation.

## 7. Open questions

1. **Which goroutines to audit?** The minimum set (service
   tasks + current Ring-3 wrapper) is cheap; enumerating
   every parked goroutine requires walking channel blocker
   lists, which touches internal TinyGo data structures.
   Start minimal.
2. **What about the main goroutine's stack?** The TinyGo
   scheduler wraps `main()` in its own goroutine; the audit
   should include it. Verify `task.Current()` returns a
   non-nil Task for the main thread at audit time.
3. **Is bumping `default-stack-size` enough, or does
   `automatic-stack-size` override it per-goroutine?** The
   auto sizing is based on per-function call-graph analysis;
   bumping the default affects only goroutines whose estimate
   fails. Understand which lever actually applies before
   turning it.

## 8. Risk register delta

- **Retires** (item 7 mitigation + item 13):
  `R-goroutine-stack-size`, partial retirement.
- **Retires** (item 6): none — the risk stays, just
  documented as "not implemented by choice".
- **Adds**:
  - `R-precise-gc-scope` — the cost of precise GC is
    documented as "too large to schedule"; revisit when
    TinyGo upstream moves.
  - `R-audit-enumeration-completeness` — the stack audit
    iterates only a known subset of goroutines; enumerating
    all is brittle and deferred until SMP v2 forces it.
