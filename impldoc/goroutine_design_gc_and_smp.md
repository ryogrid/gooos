# GC Interaction, SMP Scope, Files, Verification, Risks

This document covers how the conservative GC interacts with multiple
goroutine stacks, the SMP story for v1 (and a v2 sketch), the complete
list of files added, modified, or removed, the verification plan, and
the open risks that require a spike before implementation. It completes
the four-document set started by `goroutine_design_overview.md`.

## 1. GC interaction

### 1.1 How TinyGo finds every goroutine's stack

TinyGo's conservative GC
(`/usr/local/lib/tinygo/src/runtime/gc_blocks.go`) runs in two phases:

1. **Mark** (`gc_blocks.go:422-453`):
   - `markStack()` walks the currently active stack (the goroutine that
     called `runtime.GC()` or that triggered the allocation that
     caused a collection).
   - `findGlobals(markRoots)` walks `.data` and `.bss`, treating every
     aligned machine word that falls inside the heap as a potential
     pointer (via `isOnHeap`).
   - If `baremetal && hasScheduler` (true under `scheduler=tasks`),
     the collector additionally scans the runqueue: pops each `Task`,
     marks it as a root, pushes into a temporary queue, then restores
     the queue (`gc_blocks.go:425-450`).

2. **Sweep** (`gc_blocks.go:458-459`): frees every block that was not
   marked.

The runqueue scan is a **dedicated code path** — `gc_blocks.go:425-451`
pops every `Task` off the queue, marks it as a root, then restores the
queue under `interrupt.Disable()`/`Restore`. This is stronger than
"additive cover for a race window": it ensures Task pointers held in
the queue are marked even if the queue's internal representation is
not fully scanned by `markRoots`' generic global walk. The v1 design
relies on this dedicated path rather than assuming `findGlobals`
alone suffices.

### 1.2 Are all goroutine stacks reachable?

Every goroutine is, at any moment, in exactly one of four states:

| State                        | Where the `Task` pointer lives                                   | GC reachability                                          |
|------------------------------|------------------------------------------------------------------|----------------------------------------------------------|
| Currently running            | `task.Current()` (per-CPU), `currentTask` global                 | Scanned via `markStack` and globals                      |
| On the runqueue (ready)      | `runqueue` (global doubly-linked list)                           | Scanned via the runqueue special case                    |
| Sleeping (`time.Sleep`)      | `sleepQueue` / `timerQueue` (globals, `scheduler.go:29-31`)      | Scanned via `findGlobals` — `sleepQueue` is in `.bss`    |
| Parked on a channel          | Channel struct's `blocked` linked list                           | Scanned transitively: channel is heap-reachable from some global or stack, and the `Task` pointer is a field inside |

Therefore every live goroutine's `Task` pointer is reachable from some
GC root, and the `Task` struct contains the goroutine's stack pointer
(`task_stack_amd64.go:12-21` `calleeSavedRegs`), so the stack itself is
reachable as a heap block. **No additional root registration is needed
for v1.**

The one gotcha: because `sleepQueue` and `timerQueue` live in `.bss`, we
must confirm they are included in `findGlobals`. The synthetic
`__ehdr_start` in `src/stubs.S` (documented in the current `README.md`
architecture section) is exactly what TinyGo uses to enumerate globals.
As long as `sleepQueue`/`timerQueue` get laid out in a section
`findGlobals` visits, they are scanned automatically.

**Action for the implementer**: after the first successful `scheduler=tasks`
build, run `objdump -t tmp/kernel.bin | grep -E "sleepQueue|timerQueue|runqueue"`
and verify each symbol falls in `.bss` (or `.data`) and thus within the
range `_globals_start`..`_globals_end`. Document the result.

### 1.3 Write-barrier gap

Go's upstream runtime uses a hybrid write barrier for precise GC.
TinyGo's conservative GC does not — it just rescans memory. Two
consequences:

1. Concurrent mutators during a mark phase are not strictly safe in the
   precise-GC sense. In gooos's single-core v1 this is moot: GC runs
   synchronously on the goroutine that triggered it, and every other
   goroutine is either parked (safe) or running on a different CPU
   (impossible in BSP-only v1).

2. A goroutine suspended mid-store — e.g., the two instructions for
   `slice.data = newSlice.data; slice.len = newSlice.len` interrupted
   between them — leaves the mutator's registers/stack consistent,
   because the stack and registers are saved *before* any reclamation
   happens. Conservative scanning of both the old and new pointer is
   safe (over-approximation).

**Conclusion**: no write-barrier retrofit is needed. Document explicitly
so that a future contributor adding concurrent collection (or SMP v2)
knows this is a deferred item.

### 1.4 GC triggering inside an ISR

The GC is triggered by `runtime_alloc` when a block request cannot be
satisfied. ISRs must never allocate (`goroutine_design_channels_and_isr.md
§3.1`), so a GC cycle cannot be triggered from an ISR. This rules out
the scariest class of bugs (GC mark phase reentrant from an interrupt).

## 2. SMP v1: BSP-only goroutines

### 2.1 Behavior

- Boot sequence is unchanged: `src/smp.go` runs ACPI MADT discovery,
  INIT-SIPI-SIPI, and each AP enters a trampoline.
- **Change**: the AP trampoline's final kernel-side function is
  replaced by a bare `for { sti(); hlt() }` loop. APs never touch the
  goroutine runqueue, the heap (except for their one-time bootstrap
  read), or the page allocator at steady state.
- Ring-3 user processes still run only on the BSP. `elfExec` does not
  migrate.

### 2.2 Why not go SMP in v1

TinyGo's `scheduler=tasks` runqueue is a single global structure with no
locking (`runtime/scheduler.go:29`, `runqueue task.Queue`). Running
concurrent goroutines on multiple CPUs against that runqueue would
corrupt it. Every alternative — a single lock, per-CPU runqueues with
work stealing, lock-free bounded queues — is a significant runtime
rewrite that is out of scope.

The v1 model preserves the current observable behavior (SMP boots to a
working shell; goroutines exist; APs are warm but idle) and leaves the
v2 door open.

## 3. SMP v2 sketch (not specified to implementation depth)

v2 enables true multi-goroutine execution across all cores. High-level
components:

- **Per-CPU `in_interrupt_depth` counter**: the v1 implementation
  uses a single `.bss` u32 (`src/isr.S`) incremented/decremented
  non-atomically. Safe on BSP-only because IDT gates keep IF=0 and
  ISRs do not `sti`. Under v2 each AP's ISR prologue writes the
  counter too; without per-CPU storage or `lock incl`, concurrent
  updates lose increments and `interrupt.In()` returns the wrong
  value. Fix: switch the counter to a per-CPU slot (FSBASE or a
  per-CPU GS segment).


- **Per-CPU runqueue**: replace `runtime.runqueue` with
  `perCPURunqueue[cpuID]`. Upstream TinyGo has no hook for this; it
  requires patching TinyGo's `scheduler.go` locally (or vendoring it).
- **Work stealing**: when a CPU's local runqueue is empty, steal from a
  random peer. This is the same algorithm the Go runtime uses; TinyGo
  does not implement it.
- **APIC timer preemption on APs**: currently only the BSP's PIT fires.
  APs need their own preemption source so their resident goroutines
  yield. Use the LAPIC timer.
- **Memory fences**: the x86_64 memory model is TSO-ish; it gets most
  things right, but explicit `mfence` around runqueue pops/pushes is
  needed when multiple CPUs access the queue.
- **Ring-3 process migration**: `process.go`'s TSS model assumes a
  single CPU. A user process that starts on CPU 0 and gets rescheduled
  onto CPU 2 needs that CPU's TSS.RSP0 set to its kernel stack. This
  requires per-CPU TSS/GDT.

All of the above is future work. Tracking: open an issue titled "SMP v2:
per-CPU runqueues for goroutine scheduling" after v1 lands.

## 4. SMP: does anything break under v1?

No. The `src/smp.go:128` AP stack allocation
(`page := allocPage(); apStacks[i] = page + pageSize`) remains safe —
APs just never run Go code. The idle-loop body in the AP trampoline must
be pure assembly or use only `//go:nosplit` runtime-free helpers
(`sti`, `hlt`) to avoid triggering the scheduler.

## 5. Files added, modified, removed

### 5.1 Modified

| File                       | Change summary                                                                                                |
|----------------------------|---------------------------------------------------------------------------------------------------------------|
| `src/target.json`          | `"scheduler": "none"` → `"scheduler": "tasks"` (line 9); add `"build-tags"` entry per `goroutine_design_scheduler.md §5.1` |
| `src/main.go`              | Replace `createTask` calls (lines 320, 324) with `go` launches; rewrite fatal handlers to use `serialPanicPrint`; force boot-goroutine stack size (see §8 R-main-stack) |
| `src/serial.go`            | Replace `serialChannel` with `serialCh chan string`; add `serialPanicPrint` helper; delete `serialTaskEntryAddr` `//go:linkname` declaration |
| `src/fs.go`                | Replace custom channel types with native `chan *fsRequest` and per-request reply channels; delete `fsTaskEntryAddr` `//go:linkname` declaration |
| `src/keyboard.go`          | Replace `chanTrySend` with IRQ ring-buffer write; add `keyboardPump` goroutine bridge; delete `keyboardConsumerTaskAddr` declaration |
| `src/userspace.go`         | `sysReadHandler` consumes from native `keyboardCh` instead of `chanRecv(userKeyboardChannel)`                  |
| `src/process.go`           | `elfExec` spawns a `go ring3Wrapper(...)` goroutine instead of `createTask`; adds `exitCh` channel             |
| `src/pit.go` / `src/interrupt.go` | `handleTimer` increments `pitTicks` and sets `wantReschedule` flag; no `schedule()` call in ISR context |
| `src/vm.go`                | `handlePageFault` switches to `serialPanicPrint` (no heap allocation on fatal)                                 |
| `src/smp.go`               | AP trampoline final stage becomes bare `sti; hlt` loop                                                        |
| `src/gdt.go`               | `tssSetRSP0` is called from `ring3Wrapper` (per-Ring-3-goroutine) rather than from `schedule()` — verify comments match new flow |
| `src/isr.S`                | Common prologue increments `in_interrupt_depth`; epilogue decrements and checks `wantReschedule` (see `goroutine_design_scheduler.md §5.3`) |
| `src/stubs.S`              | No code changes, but audit `__ehdr_start` / `_globals_start` / `_globals_end` symbols against runtime globals once `scheduler=tasks` is on (see §1.2) |

### 5.2 Removed

| File                       | Reason                                                                                                 |
|----------------------------|--------------------------------------------------------------------------------------------------------|
| `src/scheduler.go`         | Entire hand-written scheduler replaced by TinyGo runtime                                               |
| `src/channel.go`           | Entire hand-written channel API replaced by Go-native `chan`                                           |
| `src/switch.S` — most stubs | Only `elfExecTrampoline` and its address stub survive; every `*TaskAddr` entry point for dead test goroutines goes |

### 5.3 Added

| File                       | Purpose                                                                                                       |
|----------------------------|---------------------------------------------------------------------------------------------------------------|
| `src/goroutine_stubs.go`   | gooos-local bodies for `sleepTicks`, `ticks`, `ticksToNanoseconds`, `nanosecondsToTicks`, `deadlock`, `tinygo_register_fatal_signals`. **Where** these live (runtime package via vendoring vs. external linkname stubs vs. custom-goos target) is spike-determined — see `goroutine_design_scheduler.md §5.1`. |
| `src/goroutine_tss.go`     | `tssSetRSP0ForCurrentG()` used by `ring3Wrapper`                                                              |
| `src/goroutine_irq.go`     | `in_interrupt_depth` counter + `interruptIn` + `interruptDisable`/`Restore` linkname bridges; `wantReschedule` flag        |
| `src/keyboard_irq.go`      | ISR-side lock-free ring buffer for keyboard events + `keyboardIRQSend` / `keyboardIRQRecv` / `keyboardPump`   |

### 5.4 Linker script

`src/linker.ld` does not strictly need changes for v1. Verify after the
build that `sleepQueue`, `timerQueue`, and `runqueue` all land in
`.bss` or `.data` (not `.rodata`, not a section outside
`_globals_start`..`_globals_end`). If any of them land outside, add an
explicit section emit to `linker.ld`.

### 5.5 Assembly survival

`src/switch.S`: shrinks to only the pieces that support ISR entry/exit
and `elfExecTrampoline`. Concretely keep:

- `taskReturnHaltAddr` — still wanted as a safety net for a goroutine
  function that returns unexpectedly.
- `elfExecTrampolineAddr` — referenced by `process.go`.

Everything else (`switchContext`, `demoTaskAAddr`, `serialTaskEntryAddr`,
`fsTaskEntryAddr`, `chanProducer*`, `selectTest*`, `userPrintTaskAddr`,
`keyboardConsumerTaskAddr`) is removed.

`src/isr.S`, `src/stubs.S`, `src/boot.S`, `src/trampoline.S` are
unchanged.

## 6. Verification plan

### 6.1 Regression: existing sendkey harness must pass unchanged

Run `tmp/test_sendkey.sh` for 10 trials of `ls → cat hello.txt → ls → help`.
Requirements (same as the page-reclaim and conservative-GC commits):

- 0 page faults across all trials
- 30 `processExit` log lines (3 commands × 10 trials)
- 10 `Hello from the gooos filesystem` outputs from `cat`
- Shell returns to `$ ` every time

Run the stress test `tmp/stress_test.sh` (5 `ls` + `cat` in one session):
6 exits, 0 PF, 1 cat output.

Run `make run-smp` briefly and confirm `SMP: 4 cores online` + shell
prompt.

### 6.2 New goroutine-specific smoke tests

Add a temporary function `goroutineSmokeTests()` called from `main()`
before the shell is launched. On success it prints a single line
`Goroutine smoke tests: PASS` to the serial; on failure it halts with a
specific message. Tests:

1. **Channel throughput**: spawn `N = 100` goroutines, each sends its
   index on `ch := make(chan int, 100)`. A loop in `main` reads 100
   values and asserts the sum equals `100 * 99 / 2 = 4950`.
2. **Sleep ordering**: two goroutines, one sleeps 10 ms and prints "A",
   the other sleeps 30 ms and prints "B". Verify the serial log shows
   "A" before "B".
3. **Select with timeout**: block on a never-ready channel `select`ed
   with `time.After(20 * time.Millisecond)`; assert the timeout fires.
4. **GC under load**: spawn 10 goroutines each reading from a
   dedicated channel, force `runtime.GC()`, then wake every goroutine
   by sending on its channel and assert every goroutine wakes,
   completes, and terminates — the channel/Task graph must survive
   the collection. Use synchronization on a shared counter
   (atomically incremented by each woken goroutine) to verify
   completion rather than comparing `HeapInuse` deltas (which is
   flaky because the collector's internal bookkeeping size varies).

These tests run once at boot and are disabled (behind a
`const runGoroutineSmoke = false`) before landing to master.

### 6.3 SMP sanity

`make run-smp`, confirm the serial log shows the GC demo output,
`SMP: 4 cores online`, and that the shell reaches `$ `. No change in
user-visible behavior expected.

## 7. Verification gates before requesting manual user review

- `make build` succeeds with no unresolved symbols
  (`nm tmp/kernel.bin | grep " U "` empty)
- 10/10 sendkey trials pass
- Stress test passes
- Goroutine smoke tests pass (during development)
- SMP boot to shell prompt works

Manual user verification is requested only after all four gates are
green. If a gate fails, stop and re-plan; do not patch around it.

## 8. Open risks and required spikes

| Risk                                                                                                    | Mitigation                                                                                                    |
|---------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------|
| **R-runtime-collision** (CRITICAL): `goos=linux` pulls in `runtime_unix.go`, which *defines* `sleepTicks`/`ticks`/`deadlock`/`tinygo_register_fatal_signals`. `//go:linkname` from a kernel file conflicts with those definitions, and `runtime_unix.go` itself calls libc that does not exist in Ring 0. | **Spike required before any work**: decide between (a) custom `goos` string in `target.json`, (b) adding a build tag like `baremetal` plus vendored runtime file replacements, (c) forking TinyGo's runtime. See `goroutine_design_scheduler.md §5.1` for detail. |
| **R-interrupt-in** (CRITICAL): `task.Pause()` panics when `interrupt.In()` returns true, and there is no amd64-baremetal `interrupt.In()` today. | Add per-CPU `in_interrupt_depth` counter in `isr.S` prologue/epilogue and expose via `//go:linkname`. `handleTimer` flags a reschedule request rather than calling `task.Pause` directly. See `goroutine_design_scheduler.md §5.3-§5.4` and `goroutine_design_channels_and_isr.md §3.5`. |
| **R-link-spike** (CRITICAL): `scheduler=tasks` may not link at all on `x86_64-unknown-linux-elf` even after R-runtime-collision is resolved. | **Spike**: minimal `ch := make(chan int); go func(){ ch<-1 }(); <-ch` program built against the modified target. If it produces a valid ELF with no unresolved externals, proceed. Otherwise re-evaluate the entire design. |
| **R-main-stack** (CRITICAL): TinyGo wraps user `main()` in a call from the scheduler loop on goroutine 0. A 1-page default stack will overflow during gooos's long synchronous boot (IDT, SMP, ELF loading, GC demo). | **Spike required** (listed as spike #4 in `goroutine_design_overview.md §7`). Determine the TinyGo 0.33.0 mechanism — build flag, compiler intrinsic, runtime hook — to force a larger initial stack. If no upstream mechanism exists, design a manual early-stack swap in `main`'s first instruction. |
| **R-sleep-granularity**: `sleepTicks` granularity is 10 ms (PIT at 100 Hz). | Document as v1 limitation. Higher resolution requires reprogramming PIT or switching to LAPIC one-shot timer (future work). |
| **R-goroutine-stack-size**: Per-goroutine stack size picked by `getGoroutineStackSize` may underestimate kernel call depth (e.g., deep `serialPrintln` + string concat chains). | Pad frames with large local `var _ [4096]byte` in deep call paths, or use a build-time stack-size flag if available. Verify via smoke tests under `-O0`. |
| **R-isr-safety-enforcement**: ISR-safety rule is only enforced by review. | Lint pattern (future work): grep for `go ` / `make(chan` / `serialPrintln` inside functions registered via `registerHandler`. |
| **R-keyboard-latency**: `keyboardPump` adds ≤10 ms keystroke latency. | Acceptable for v1. If latency-sensitive, tighten with `runtime.Gosched()` in the pump loop, or replace with a direct non-blocking channel send once ISR-safety is proven for that path. |
| **R-fatal-detail-loss**: `handlePageFault` / `handleDivisionError` use fixed-string `serialPanicPrint`, losing CR2/RIP/error-code detail. | Write register values into a pre-allocated static `[64]byte` buffer via a hex-to-ASCII helper that does **not** allocate, then pass the buffer to `serialPanicPrint`. |
| **R-global-layout**: TinyGo runtime globals (`sleepQueue`, `timerQueue`, `runqueue`) must land inside `_globals_start..end` for `findGlobals` to scan them. | After first build with `scheduler=tasks`, run `objdump -t tmp/kernel.bin \| grep -E "sleepQueue\|timerQueue\|runqueue"`. If any symbol lands outside, add an explicit section emit in `src/linker.ld`. |
| **R-runtime-alloc-reentry**: `go func(){}()` calls `runtime_alloc`. If reached during GC mark from a conservative-collector reentrancy window, the collector's metadata is corrupted. | Forbid `go` in any function reachable from an ISR; document in `goroutine_design_channels_and_isr.md §3.1`. Lint check in the same future-work bucket as R-isr-safety-enforcement. |
| **R-task-stack-top-unknown**: `task.state.sp` is the *saved* SP, not the stack top; `canaryPtr` is the bottom. There is no built-in way to get the top of a goroutine's stack for TSS.RSP0. | v1 uses the "local variable address as RSP0" trick in `ring3Wrapper` (see `goroutine_design_scheduler.md §3.3`). If that proves unreliable, fall back to a side table keyed on `*task.Task`. |

## 8a. Review history

The first draft of this four-document set was reviewed by a
`general-purpose` subagent (2026-04-15). Reviewer findings classified
as CRITICAL or MAJOR were addressed in-place; the resulting changes
are visible as §5.1 of `goroutine_design_scheduler.md` (runtime-
collision story), §5.3/§5.4 of the same file (ISR reschedule flag and
`interrupt.In()`), §3.5–§3.6 of `goroutine_design_channels_and_isr.md`
(`interrupt.In()` primitive and keyboard-ring-buffer rationale), the
softened §1.1 of this doc, and the expanded risk table below.

Reviewer items **intentionally deferred** (not a rejection — scoped
out of v1):

- Precise (write-barrier) GC — future work alongside SMP v2.
- Enforcement of the ISR-safety rule via lint/CI — future work.
- Grow-on-demand goroutine stacks — future work; v1 uses fixed sizes.

## 9. Implementation ordering (non-binding recommendation)

A suggested sequence so each step is testable before the next:

1. **Spike**: confirm `scheduler=tasks` links. If it does not, stop and revisit.
2. Add `src/goroutine_stubs.go` with `sleepTicks`, `ticks`, `deadlock`, signal stubs. Build under `scheduler=none` first (stubs are no-ops there) to confirm compilation.
3. Flip `src/target.json` to `scheduler=tasks`. Leave `src/scheduler.go` in place; delete only `src/channel.go`'s contents that conflict. Confirm build.
4. Replace `serialChannel` with `chan string`. Run sendkey regression.
5. Replace `fsRequestChannel` + `replyCh` with native channels. Run sendkey regression.
6. Switch `handleKeyboard` to the IRQ ring buffer + pump. Run sendkey regression.
7. Rewrite fatal handlers. Run sendkey regression (fatals don't fire in happy-path tests, so this is a manual check — trigger a #DE intentionally once).
8. Replace `createTask` in `src/main.go` with `go` launches. Delete `src/scheduler.go`. Run sendkey regression.
9. Convert `elfExec` to `ring3Wrapper` + `exitCh`. Run sendkey regression.
10. Run goroutine smoke tests. Address failures.
11. SMP sanity. Request manual verification.

Each step is small enough to revert if a regression appears. Stop at any step whose regression is hard to diagnose — the blast radius is smaller at step 4 than at step 9.
