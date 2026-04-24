# DEFERRED 1 — Phase 4.4 kernel-thread context switch + long-lived service migration (C1 + C3)

## Scope & goal

**Scope (verbatim from `FINAL_REPORT.md §Deferred` item 1)**:
*Phase 4.4 kernel-thread context switch + migrate long-lived
kernel services (`timerDispatcher`, `netRxLoop`,
`tcpRTOScannerLoop`, `fsTask`).*

**Goal**: give the gooos kernel a per-CPU cooperative kernel-thread
runtime that owns scheduling decisions for long-lived services,
independent of TinyGo's task scheduler. A migrated service that
parks (channel recv, `afterTicks`, `kernelYield`) must not hijack
its host TinyGo goroutine; the kernel-thread scheduler must swap
to the next ready thread on the same CPU. After this lands,
`kernelThreadSpawn(cpu, fn)` becomes a real primitive instead of
the one-shot direct-invocation trap that caused F1.

## Root-cause analysis (why Phase 4.3 is not enough)

Today's `src/kernel_thread.go` (post-C2 landing in commit `e346305`)
provides:

- `ktPool[ktPoolSize]` static allocation
  (`src/kernel_thread.go:70`, `:72`) and `kernelThreadSpawn`
  (`src/kernel_thread.go:119–147`) that pushes a `KernelThread`
  onto `kernelReadyQueues[cpu]` (`src/kernel_thread.go:79`).
- `kernelYield()` (`src/kernel_thread.go:201–217`) that pops the
  local CPU's head and calls `kernelThreadSwitch`.
- `kernelThreadSwitch(next)` (`src/kernel_thread.go:183–192`)
  that **direct-invokes `next.entryFn()`** and returns when
  that function returns.

The failure mode landed in commit `6a45e74` (F1 partial):
`kernelThreadSpawn(0, netRxLoop)` put an infinite-loop function
onto BSP's ready queue, so when `timerDispatcher`'s per-iteration
`kernelYield()` popped it, `netRxLoop` ran forever on
`timerDispatcher`'s stack and every `afterTicks` deadline stopped
firing. F1 removed the spawn call; the ready queue is empty today
and `kernelYield()` is a no-op everywhere.

Phase 4.4 makes `kernelThreadSwitch` **preserve the caller's
continuation**:

- On first entry into a kernel thread, allocate/attach a per-CPU
  stack and jump to `entryFn` on that stack.
- When `entryFn` calls `kernelYield()`, save the thread's
  callee-saved context onto its stack and swap back to the
  caller's context (the thread that invoked `kernelThreadSwitch`).
- When the caller loops around and calls `kernelYield()` again,
  either the same thread resumes (from its saved context) or a
  different ready thread is swapped in.

This is classic coroutine-style yielding with a dedicated per-CPU
kernel-thread scheduler.

## Design approach

### Two separate execution contexts, one CPU

A migrated service still runs from inside some TinyGo goroutine's
stack — the thing that called `kernelYield()`. Call this the
**host** goroutine. We introduce a second stack, the
**kernel-thread stack**, owned by `KernelThread.stackBase..stackTop`.

The swap primitive — new file `src/kernel_thread_swap.S`, modelled
on `tinygo_swapTask` (`src/task_stack_amd64.S:31–61`) — moves
callee-saved registers + `%rsp` between the host's saved context
and the kernel-thread's saved context.

```asm
// kernel_thread_swap.S (new)
// void kernelThreadSwap(SavedContext *newCtx /* %rdi */,
//                       SavedContext *oldCtx /* %rsi */);
//
// SavedContext field layout (matches src/kernel_thread.go:32–39):
//   offset  0  rbx
//   offset  8  rbp
//   offset 16  r12
//   offset 24  r13
//   offset 32  r14
//   offset 40  r15
//   offset 48  rsp
//
// Save current callee-saved regs into *oldCtx, then load *newCtx
// and ret. The return address is implicit at (%rsp) of the new
// context; primeKernelThreadStack writes the trampoline addr
// there.

.global kernelThreadSwap
kernelThreadSwap:
    movq %rbx,  0(%rsi)
    movq %rbp,  8(%rsi)
    movq %r12, 16(%rsi)
    movq %r13, 24(%rsi)
    movq %r14, 32(%rsi)
    movq %r15, 40(%rsi)
    movq %rsp, 48(%rsi)

    movq  0(%rdi), %rbx
    movq  8(%rdi), %rbp
    movq 16(%rdi), %r12
    movq 24(%rdi), %r13
    movq 32(%rdi), %r14
    movq 40(%rdi), %r15
    movq 48(%rdi), %rsp
    ret
```

On the first entry for a KernelThread, `oldCtx` is the host's
slot and `newCtx.rsp` is pre-primed to a trampoline that calls
`entryFn` and — on return — flips the thread to `ThreadTerminated`
and swaps back to the host. A correctly pre-primed `newCtx` is
one where `*(uintptr*)(newCtx.rsp) == trampoline_addr` so the
first `ret` lands in the trampoline.

### Data model (extends the C2 pool)

`KernelThread` (currently `src/kernel_thread.go:43–52`) adds:

| Field | Type | Purpose |
|---|---|---|
| `hostCtx` | `SavedContext` | Host's callee-saved state, filled on swap-to-thread. |
| `started` | `uint32` | 0 until the first `kernelThreadSwap` has run for this slot. |
| `returnAddr` | `uintptr` | Immutable trampoline entry; written into stack when slot is allocated. |

`SavedContext` already has the layout expected by the swap
primitive (`src/kernel_thread.go:32–39`):
`rbx, rbp, r12, r13, r14, r15, rsp` — seven 64-bit fields, no
`rip`. The comment in that file states *"rip is implicit via the
return address on the saved stack"*. Phase 4.4 inherits this
invariant: `primeKernelThreadStack` writes the trampoline's
address as the first quadword at `ctx.rsp`, so the swap stub's
final `ret` lands at the trampoline. Any future reorder of
`SavedContext` must preserve field order — flag with a comment
above the struct that the order is asm-load-bearing.

Each thread's stack is lazy-allocated on first schedule via
`allocPagesContig(kernelStackSize / pageSize)` (`src/vm.go`).
`kernelStackSize` is already defined as `16 * pageSize`
(`src/kernel_thread.go:84`).

### Scheduler loop

`kernelYield()` semantics change:

1. If `currentKernelThread[cpu]` is set, we're running a kernel
   thread — save its context back into the thread's `context` and
   swap to `hostCtx`. Clear `currentKernelThread[cpu]`.
2. Else we're running the host — pop the next ready thread; if
   none, return immediately. Install it as
   `currentKernelThread[cpu]` and swap to its context.

Pseudocode:

```go
//go:nosplit
func kernelYield() {
    cpu := cpuID()
    cur := currentKernelThread[cpu]
    if cur != nil {
        // Thread calling kernelYield: swap back to host.
        kernelThreadSwap(&cur.hostCtx, &cur.context)
        return
    }
    // Host calling kernelYield: try to pick up a ready thread.
    next := kernelThreadPopReady()
    if next == nil {
        return
    }
    if next.started == 0 {
        // Prime a fresh stack with the trampoline return address.
        primeKernelThreadStack(next)
        next.started = 1
    }
    currentKernelThread[cpu] = next
    kernelThreadSwap(&next.context, &next.hostCtx)
    // Returning here = the thread yielded or terminated.
    currentKernelThread[cpu] = nil
    if next.state == ThreadTerminated {
        ktPoolFree(next)
    }
}
```

`primeKernelThreadStack` writes the trampoline's PC onto
`next.stackTop - 8` and stores that address as the initial `rsp`
in `next.context`.

### Trampoline

```go
//go:nosplit
func kernelThreadTrampoline() {
    cpu := cpuID()
    kt := currentKernelThread[cpu]
    if kt == nil || kt.entryFn == nil {
        goto exit
    }
    kt.entryFn()      // returns only if fn returns (rare for services)
exit:
    kt.state = ThreadTerminated
    kernelYield()     // swap back to host; never returns in practice
    for { hlt() }     // safety halt
}
```

**Trampoline address capture**: gooos cannot use `reflect` —
`src/afterticks.go:4` records that `reflect.Value.Complex`
requires SSE registers disabled in the kernel target
(`src/target.json` features `-mmx,-sse,-sse2`). The
implementation therefore writes the trampoline as an assembly
stub in `src/kernel_thread_swap.S` itself, and the Go side
declares it via `//go:linkname` to the asm symbol:

```go
// In src/kernel_thread.go:

//go:linkname kernelThreadTrampoline kernelThreadTrampoline
func kernelThreadTrampoline()

var kernelThreadTrampolinePC = **(*uintptr)(unsafe.Pointer(&kernelThreadTrampoline))
```

The asm-side `kernelThreadTrampoline` is a one-instruction stub
that jumps to a Go-side `kernelThreadTrampolineBody`:

```asm
.global kernelThreadTrampoline
kernelThreadTrampoline:
    jmp kernelThreadTrampolineBody
```

`kernelThreadTrampolineBody` is the plain Go function shown
above (entryFn invocation + state cleanup + `kernelYield`). The
PC capture pattern (`**(*uintptr)(unsafe.Pointer(&fn))`) reads
the func-value's first word — TinyGo's func-value layout stores
the entry PC there. This is the same pattern used elsewhere in
gooos for assembly-bridged functions (e.g.
`src/stubs.go:jumpToRing3` — verify the exact pattern at
implementation time).

### ISR safety and lock-rank placement

- `kernelYield` stays `//go:nosplit`. The swap primitive
  `kernelThreadSwap` is leaf assembly, trivially nosplit.
- `kernelThreadSpawn` (post-C2) is allocation-free and nosplit;
  unchanged.
- Stack allocation (`allocPagesContig`) cannot be called from
  ISR context — it acquires `pageAllocLock` (rank 1). Phase 4.4
  therefore lazy-allocates the stack at the *first* non-ISR
  `kernelYield()` that pops the thread. Since all known callers
  (`timerDispatcher`, `netRxLoop`, etc.) enter via a regular
  goroutine body, this is safe. Add a `//go:nosplit`-adjacent
  invariant comment in `kernel_thread.go`: *"Stacks are
  allocated on the first yield that selects the thread; that
  yield must not be called from ISR context."*
- No new lock acquired across swap. `currentKernelThread[cpu]`
  is per-CPU and only touched by the CPU itself.

### Interaction with existing gooos hooks

- `gooosOnResume` (`src/goroutine_tss.go:195`) fires on TinyGo
  task resumption. Kernel threads never call `task.Resume`; the
  host's resume already fired before we swap in. TSS.RSP0 /
  CR3 remain pointed at the host goroutine's stack — **correct**,
  because no Ring-3 code can run inside a kernel thread (they
  are Ring-0 services) and no ISR on this CPU will transition
  to Ring 3 while we are executing the thread's body (Ring-3
  wrappers are a different set of goroutines).
- `gooosWakeupCPU` / preempt-phase gate: unaffected. The kernel
  thread runs on top of whichever CPU the host was last
  scheduled on; work-stealing on the host moves it, not the
  kernel thread directly. Cross-CPU migration of kernel threads
  is **out of scope** for Phase 4.4 — threads are pinned to
  their `cpuID` field at spawn.

### Service migration (C3) after context switch lands

The six kernel services that are candidates (per the
service-survey for this plan):

| Service | File | Group | Migration risk |
|---|---|---|---|
| `timerDispatcher` | `src/afterticks.go:92` | A (independent) | Low |
| `fsTask` | `src/fs.go` | A (independent) | Low |
| `tcpRTOScannerLoop` | `src/tcp_retx.go:138` | B (timer-dep) | Low |
| `tcpEchoServer` | `src/tcp.go:1351` | B (timer-dep) | Low |
| `udpEchoServer` | `src/udp.go:313` | B (NIC-dep) | Low |
| `netRxLoop` | `src/net.go:74` | C (hot-path) | Medium |

Migration pattern (applied after Phase 4.4 context switch lands
and is verified):

1. Replace `go <service>()` at the spawn site with
   `kernelThreadSpawn(<target cpu>, <service>)`.
2. Keep the existing `go <service>()` AS WELL for one commit so a
   rollback only needs to remove the `kernelThreadSpawn` call.
3. Once the first full soak passes, delete the goroutine spawn.

Target CPU selection:

- Group A and B: `cpuID = 0` (BSP) initially. Moving them to
  dedicated APs is a later optimisation.
- Group C (`netRxLoop`): stays BSP-pinned. e1000 MMIO is CPU-
  independent but we want RX polling to share BSP with PIT /
  LAPIC ISR delivery.

Every migrated service body must:

- Replace `runtime.Gosched()` with `kernelYield()` at the loop's
  yield point. (Both remain semantically "yield the current
  execution"; `kernelYield()` yields to another kernel thread on
  the same CPU, while `runtime.Gosched()` yields to another
  TinyGo goroutine — still a valid way to cooperate with the
  host.) In practice migrated services keep a
  `runtime.Gosched(); kernelYield()` pair so both substrates
  can progress.

The F1 fix in commit `6a45e74` (currently at
`src/net.go:49–59`) can now be un-done: a second
`kernelThreadSpawn(0, netRxLoop)` call is added alongside the
existing `go netRxLoop()` at migration time — and after the soak
verifies no hang, the `go` spawn is removed.

## File / symbol touch-points

| File | Status | Purpose |
|---|---|---|
| `src/kernel_thread.go` | Modify | Add `hostCtx`, `started`, `returnAddr` fields to `KernelThread`; rewrite `kernelYield`; add `primeKernelThreadStack`; add `ktPoolFree` helper; expose `kernelThreadTrampoline`. |
| `src/kernel_thread_swap.S` | **New** | `kernelThreadSwap(newCtx, oldCtx *SavedContext)` asm stub (see `src/task_stack_amd64.S:31–61` for structure). |
| `src/afterticks.go` | Modify | Replace `go timerDispatcher()` with `kernelThreadSpawn(0, timerDispatcher)` after Phase 4.4 core lands. (Optional second-pass change.) |
| `src/net.go` | Modify | Re-add `kernelThreadSpawn(0, netRxLoop)` alongside the existing `go netRxLoop()` (undoes the F1 workaround once Phase 4.4 makes it safe). |
| `src/fs.go` | Modify | Same pattern for `fsTask`. |
| `src/tcp_retx.go` | Modify | Same pattern for `tcpRTOScannerLoop`. |
| `src/tcp.go` | Modify | Same pattern for `tcpEchoServer`. |
| `src/udp.go` | Modify | Same pattern for `udpEchoServer`. |
| `src/main.go` | Modify | No functional change; add `serialPrintln("kernel threads: Phase 4.4 ready")` after `kernelThreadInit()` for visibility. |
| `Makefile` | Modify | Add `tmp/kernel_thread_swap.o` to the kernel assembly list (mirrors `tmp/task_stack_amd64.o`). |
| `scripts/verify_globals.sh` | Check only | No change expected; the new asm file has no globals. |
| `current_impl_2026_04_24/04_scheduler_and_kernel_thread.md` | Doc update | Mark C1/C3 closed; update §Current Implementation Details to reflect real context switch; remove the "kernelThreadSpawn only safe for short-lived functions" caveat. |
| `current_impl_2026_04_24/FINAL_REPORT.md` | Doc update | Remove DEFERRED item 1 or mark it closed. |

## TinyGo runtime patch changes

**None.** Phase 4.4 is fully realised inside `src/`. It does not
need to extend `scripts/tinygo_runtime.patch`:

- The swap primitive lives in a new `.S` file outside
  `$TINYGOROOT`.
- `KernelThread` is a gooos-owned type; no TinyGo runtime awareness
  required.
- Cross-CPU wake of kernel threads is not needed in Phase 4.4 —
  threads are pinned and the host goroutine's migration already
  handles cross-CPU workload placement.

(If a future extension decides to let kernel threads migrate
between CPUs, a new linkname `runqueuePushTo` would be needed —
see `02_ring3wrapper_round_robin_distribution.md`.)

## Acceptance criteria

1. `make build` + `make lint` + `make verify-globals` pass with
   the new `.S` file in place.
2. `kernelThreadSpawn(0, netRxLoop)` re-enabled without
   reproducing F1 (i.e. `timerDispatcher` continues firing
   `afterTicks` deadlines): 20 consecutive boots of
   `scripts/test_net.sh` under `-smp 4` complete their netDiag
   auto-dump within 10 s of "UDP echo: listening".
3. `scripts/test_sleeptest_shell.sh` baseline pass rate is not
   worse than the current ~50% (separate follow-up in DEFERRED 3
   closes the residual).
4. `scripts/test_smp_basic.sh` continues to PASS.
5. A migrated kernel thread's body yielding via `kernelYield`
   returns control to the host goroutine's
   `runtime.Gosched()`-site; no infinite direct-invoke (the F1
   class of bug cannot recur).

## Verification plan

All commands run from `/home/ryo/work/gooos/`, each as a separate
Bash invocation (per `CLAUDE.md §Shell`).

```
make build
make lint
make verify-globals
make iso
```

Harness sweep:

```
bash scripts/test_smp_basic.sh           # PASS required
bash scripts/test_net.sh                  # PASS required (netDiag reached)
bash scripts/test_preempt_kernel.sh      # PASS required
bash scripts/test_preempt_user.sh        # PASS required
bash scripts/test_smp_shell_distribution.sh   # PASS required
bash scripts/test_sleeptest_shell.sh     # expect unchanged ~50 %
```

Targeted ≥95 % sampler (new or existing `test_smp_stability_sample.sh`):

```
bash scripts/test_smp_stability_sample.sh   # ≥95 % PASS across 20 runs
```

For each migrated service, add temporary boot-time tracing
(`serialPrintln("kt-service started: <name>")` inside the
trampoline) and confirm all six targets print on boot.

## Risk & rollback

| Risk | Impact | Mitigation |
|---|---|---|
| Swap-asm register layout drifts from `SavedContext` Go struct | Crash on first swap | Keep both definitions in one file header as a comment table; add a boot-time offset check similar to `checkTaskOffset` (`src/goroutine_tss.go:86`). |
| Stack under-sized for a specific service | Panic at `gooosStackOverflow` | `kernelStackSize = 16 * pageSize` already matches Ring-3 pool size; canary added via `primeKernelThreadStack`. |
| Host goroutine migrates while thread is mid-yield | Benign (thread's state is on its own stack, not host's) | Invariant documented; swap saves/restores only the 6 callee-saved regs + `%rsp`. |
| ISR fires while the CPU is running a kernel-thread body | ISR runs on the kernel-thread's stack (matches host expectation via TSS.RSP0 for Ring-3 frames, kernel-stack for Ring-0) | Kernel threads are Ring-0 only; ISR uses current `%rsp` for Ring-0 preemption frames. |
| Context-switch asm breaks preempt IPI timing | Latent SMP flake | Swap is O(8 qwords); negligible. |

**Rollback**: revert the commit that re-enabled
`kernelThreadSpawn(0, netRxLoop)`. The Phase 4.4 scaffolding
(new asm file, modified `kernelYield`) can remain in-tree as a
no-op since the ready queue would stay empty.

## Dependencies

- **None on other DEFERRED items.** This is the foundation; items
  04 and 05 depend on this landing.

## Estimated effort

**Large.** ~300–500 LOC of new code (mostly Go + ~40 lines of
asm), plus the migration of six services (mechanical). Expected
1–2 focused sessions to land the core swap + tests; a third for
service migration + soak.
