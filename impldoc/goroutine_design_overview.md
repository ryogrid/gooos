# Ring-0 Goroutine & Channel Support — Design Overview

## 1. Motivation

gooos currently runs with `scheduler=none` in `src/target.json:9` and implements a
hand-written preemptive round-robin scheduler in `src/scheduler.go`, a bespoke
channel API in `src/channel.go`, and a manual WaitQueue primitive. New kernel
code therefore cannot use `go func()`, built-in `chan`, or `select` — every
concurrency primitive has to be expressed through `createTask`, `chanSend`,
`chanRecv`, `selectWait`. This design specifies the work required to replace
that hand-rolled infrastructure with native Go goroutines and channels while
keeping every existing feature (preemptive Ring-3 userland, PIT preemption,
conservative GC, SMP boot) working.

Previous design docs deliberately chose the hand-written path:
`tasks/prd-goroutine-microkernel.md` lists **"No TinyGo goroutine runtime"** as
an explicit non-goal. This document reverses that decision now that:

- Conservative GC is stable (`src/target.json:8` = `conservative`, commit
  `09c55c0`).
- The page allocator recycles memory safely via the LIFO free stack and
  `allocPagesContig` (commit `1cfa257`).
- `impldoc/busybox_*.md` milestones (shell, 12-syscall ABI, ELF loader) are all
  landed and green under sendkey testing.

## 2. Scope

### 2.1 In scope

- Replacing `src/scheduler.go` with TinyGo's `scheduler=tasks` runtime.
- Replacing `src/channel.go` with native `chan` / `select`.
- Providing the kernel-side runtime glue TinyGo needs (`sleepTicks`, extra GC
  roots, TSS.RSP0 update hook) in new Go files.
- Migrating `serialTask` (`src/serial.go:76`) and `fsTask` (`src/fs.go:149`) to
  goroutines.
- Keeping the existing Ring-3 user process model (TSS, `iretq`, 12-syscall
  ABI) working unchanged — a user process is modeled as a goroutine that
  `iretq`s into Ring 3.
- Single-core (BSP-only) scheduling for v1.

### 2.2 Non-goals

- Changing the Ring-3 user ABI, userland SDK (`user/`), or any ELF user
  program.
- Growable per-goroutine stacks. TinyGo `scheduler=tasks` uses fixed-size
  stacks and we follow that.
- Preemptive *goroutine* scheduling within Ring 0. The timer IRQ remains the
  preemption point for Ring-3 user tasks; kernel-side goroutines cooperate
  (standard Go model).
- Per-CPU runqueues or work stealing. v1 runs goroutines on the BSP only; APs
  idle. An SMP v2 path is sketched but not specified to implementation depth.
- Precise (write-barrier) GC. Conservative scanning only.

## 3. Chosen approach: TinyGo `scheduler=tasks`

TinyGo's `scheduler=tasks` mode provides stackful goroutines with an x86_64
context-switch assembly stub and full channel/`select` runtime. Switching
`src/target.json:9` from `scheduler=none` → `scheduler=tasks` turns on the
entire runtime at once; the work is connecting gooos's existing hardware
glue (PIT, TSS, GC) to the hooks TinyGo's runtime expects.

Key TinyGo files the design relies on (installed at
`/usr/local/lib/tinygo/src/` on the dev box):

| Path (under `/usr/local/lib/tinygo/src/`)                | Role                                        |
|----------------------------------------------------------|---------------------------------------------|
| `internal/task/task_stack_amd64.S`                       | `tinygo_startTask`, `tinygo_swapTask` asm   |
| `internal/task/task_stack_amd64.go`                      | `calleeSavedRegs` layout, `archInit/resume/pause` |
| `internal/task/task_stack.go:73-92`                      | Per-goroutine stack alloc via `runtime_alloc` |
| `runtime/scheduler.go:160-239`                           | Main scheduler loop, calls `sleepTicks` when idle |
| `runtime/scheduler_any.go:8-17`                          | `//go:linkname sleep time.Sleep` hook       |
| `runtime/chan.go:452-518`                                | `chanSend`/`chanRecv` park via `task.Pause` |
| `runtime/gc_blocks.go:425-454`                           | Runqueue-scanning under `baremetal && hasScheduler` |

### 3.1 Rejected alternatives

- **`scheduler=asyncify`**: WebAssembly-only. The stackful-to-stackless
  transform is implemented in `internal/task/task_asyncify_wasm.S` with no
  x86_64 counterpart — it simply will not link.
- **Keep `scheduler=none` + custom `go`-like macro/rewriter**: channels
  immediately deadlock (`/usr/local/lib/tinygo/src/runtime/chan.go:465,500`
  call `deadlock()` under `scheduler=none`), `select` is unusable, and we
  would still need to invent a parking mechanism ourselves — the same work
  as replacing the scheduler outright, with none of the runtime's test
  coverage.

## 4. High-level architecture

```
+------------------------------------------------------------------+
|                       gooos kernel (Ring 0)                       |
|                                                                    |
|   +-------------------+       +----------------------+             |
|   | TinyGo runtime    |       | gooos hardware glue  |             |
|   |  scheduler.go     |<----->| PIT, TSS, APIC, GC   |             |
|   |  chan.go          |       | page tables, IDT     |             |
|   |  gc_blocks.go     |       |                      |             |
|   +---------+---------+       +---------+------------+             |
|             |                           |                          |
|             |      kernel goroutines    |                          |
|             v                           v                          |
|   +-------------------+       +----------------------+             |
|   | go serialTask()   |       | handleTimer (ISR)    |             |
|   | go fsTask()       |       | handleKeyboard (ISR) |             |
|   | go shellWatchdog()|       | handlePageFault (ISR)|             |
|   +---------+---------+       +----------------------+             |
|             |                                                      |
|             | sys_exec spawns a Ring-3 goroutine                   |
|             v                                                      |
|   +-------------------+                                            |
|   | go ring3Wrapper() |---- iretq ----> user ELF (Ring 3)          |
|   | TSS.RSP0 per-g    |<--- int 0x80 -- syscall dispatcher         |
|   +-------------------+                                            |
+------------------------------------------------------------------+
```

TinyGo's runtime takes ownership of the runqueue, sleep queue, and context
switch. gooos's hardware-facing code (timer, keyboard, TSS, page fault
handler) reduces to thin ISRs that drive runtime hooks.

## 5. v1 SMP scope

**BSP-only goroutines.** The boot path in `src/smp.go` continues to
INIT-SIPI-SIPI APs, but APs immediately enter a `sti; hlt` idle loop instead
of a runqueue. Ring-3 processes also run only on the BSP. This matches the
current behavior (`src/scheduler.go:169-171` guards with `taskCount <= 1`
even under SMP) and avoids the hardest problems of multi-goroutine
scheduling (per-CPU runqueues, work stealing, memory fences) until v2.

v2 sketch (detailed in `goroutine_design_gc_and_smp.md §5`): per-CPU
runqueues, APIC-timer preemption on each AP, fences around shared heap
metadata, migration of Ring-3 processes across cores. v2 is not specified to
implementation depth in this design set.

## 6. Document index

This overview is the entry point. The detailed specifications live in three
sibling documents:

1. **`goroutine_design_scheduler.md`** — scheduler coexistence, stack model,
   preemption, required runtime stubs, TSS.RSP0 hook, Ring-3 process model.
2. **`goroutine_design_channels_and_isr.md`** — native channel replacement,
   userspace channel syscall bridge, ISR-context safety rules and catalog.
3. **`goroutine_design_gc_and_smp.md`** — conservative GC root registration,
   write-barrier gap, SMP v1 behavior and v2 sketch, files-to-touch catalog,
   verification plan, open risks.

### 6.1 Coverage map

Each numbered item below is the prompt checklist from the task brief; the
right column names the section that answers it.

| # | Topic                               | Home section                                            |
|---|-------------------------------------|---------------------------------------------------------|
| 1 | Scheduler strategy                  | This doc §3, §3.1                                       |
| 2 | Coexistence with existing scheduler | `goroutine_design_scheduler.md` §2                      |
| 3 | Stack model                         | `goroutine_design_scheduler.md` §3                      |
| 4 | Preemption                          | `goroutine_design_scheduler.md` §4                      |
| 5 | Interrupt context safety            | `goroutine_design_channels_and_isr.md` §3, §3.5 and `goroutine_design_scheduler.md` §5.3–§5.4 |
| 6 | Channels                            | `goroutine_design_channels_and_isr.md` §1, §2           |
| 7 | GC interaction                      | `goroutine_design_gc_and_smp.md` §1, §2                 |
| 8 | SMP scope for v1                    | This doc §5 and `goroutine_design_gc_and_smp.md` §4, §5 |
| 9 | Files added / modified              | `goroutine_design_gc_and_smp.md` §6                     |
| 10| Verification plan                   | `goroutine_design_gc_and_smp.md` §7                     |
| 11| Open risks and unknowns             | `goroutine_design_gc_and_smp.md` §8                     |

## 7. Prerequisite: three spikes before implementation starts

Before any implementation work, four spikes must succeed. They are
listed as CRITICAL risks in `goroutine_design_gc_and_smp.md §8` and
referenced from `goroutine_design_scheduler.md §5.1–§5.4`.

1. **Runtime-collision spike (R-runtime-collision)**: The current
   `target.json` sets `goos=linux`, which makes TinyGo compile
   `runtime_unix.go` — that file *defines* (not just declares)
   `sleepTicks`, `ticks`, `ticksToNanoseconds`, `nanosecondsToTicks`,
   `deadlock`, and `tinygo_register_fatal_signals`, each with a body
   that calls libc. A plain `//go:linkname` override from a gooos file
   conflicts. Determine whether (a) a custom `goos` string, (b) a
   `baremetal` build tag plus vendored runtime, or (c) a TinyGo fork
   is the lightest path to gooos-local bodies.
2. **Link spike (R-link-spike)**: after the runtime-collision fix,
   build a trivial `ch := make(chan int); go func(){ ch<-1 }(); <-ch`
   and confirm it produces a valid ELF with no unresolved externals.
3. **`interrupt.In` spike (R-interrupt-in)**: `task.Pause()` calls
   `interrupt.In()` and panics if it returns true; no amd64-baremetal
   provider exists upstream. Implement the `in_interrupt_depth`
   counter in `src/isr.S` and verify `interrupt.In()` reflects ISR
   context correctly before any goroutine code runs.
4. **Boot-goroutine stack spike (R-main-stack)**: TinyGo wraps user
   `main()` in a goroutine spawned by the scheduler loop. A 1-page
   stack will overflow during gooos's long synchronous boot.
   Determine the mechanism TinyGo 0.33.0 offers to force a larger
   initial stack (build flag, compiler intrinsic, or custom wrapper
   in the runtime); if none exists, the design must add a manual
   stack swap very early in `main`.

If any spike fails, the design must be re-evaluated rather than
patched. The next session should read this overview first, then
`goroutine_design_scheduler.md §5` for the detailed remediation
options.
