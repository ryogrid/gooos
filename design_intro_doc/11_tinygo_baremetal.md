# Chapter 11 — TinyGo and Bare-Metal Specifics

## Overview

gooos is written in Go — but it cannot be built with the stock Go compiler. The standard `go` toolchain emits binaries that depend on a host operating system: thread creation, memory mapping, futex syscalls, and a sophisticated multi-million-line runtime that assumes someone else owns the hardware. gooos has none of that. It *is* the operating system.

TinyGo, an alternative Go compiler built on LLVM (Low Level Virtual Machine), is what makes Go viable here. It produces standalone freestanding binaries with a runtime small enough to inspect, replace, and patch. This chapter explains *which* TinyGo features gooos relies on, which it deliberately turns off, and what hand-written patches are layered onto the runtime so that conservative garbage collection, interrupt-aware scheduling, and per-CPU state can coexist with a kernel that has no host underneath it.

## Prerequisites

- Reader has used Go and understands `go func()`, channels, and `select`.
- Reader knows what a Garbage Collector (GC) is and what Stop The World (STW) means.
- Reader has skimmed Chapters 04 (memory management), 05 (KernelThread runtime), and 09 (synchronization) — this chapter explains *why* the design choices in those chapters are forced by the toolchain.
- Reader has not used TinyGo for OS development before; no familiarity with linker scripts or ABI patching is assumed.

## Why TinyGo, Not Stock Go

Stock Go's runtime is intricately tied to a host kernel: `runtime.mallocgc` ultimately calls `mmap`, goroutine scheduling assumes pthreads, signal handling assumes a POSIX signal table, and the GC assumes virtual memory tricks (e.g. madvise) for stack growth. Removing those dependencies is not a small patch — it is an entire new runtime.

TinyGo takes a different tack. It targets LLVM Intermediate Representation, generates standalone object files, links with LLD (LLVM linker), and provides a compact runtime whose moving parts can be swapped per-target. Three TinyGo configuration knobs are load-bearing for gooos:

- `gc=conservative`: a small mark-sweep collector that scans memory ranges treating every aligned word as a *possible* pointer. No write barriers, no precise type metadata required.
- `scheduler=none`: disables TinyGo's built-in goroutine scheduler entirely. The kernel build does not get goroutines at all.
- `scheduler=tasks`: enables TinyGo's cooperative goroutine scheduler. The userspace build uses this so Ring-3 processes can spawn cheap concurrent tasks.
- Custom linker script via `linker=ld.lld`: gooos supplies `src/linker.ld` to control section placement (multiboot header first 8 KiB, page tables outside GC range, etc.).

The two TinyGo target descriptors in the tree are short and worth reading verbatim. Kernel target at `src/target.json:1` and userspace target at `user/target.json:1` differ in three deliberate ways: the kernel adds the `kernelspace` build tag, sets `scheduler=none`, and uses the `unknown-linux-elf` LLVM triple (so freestanding ELFs still produce x86-64 SystemV ABI calls).

| Field | Kernel (`src/target.json`) | User (`user/target.json`) |
|---|---|---|
| `llvm-target` | `x86_64-unknown-linux-elf` | `x86_64-unknown-none-elf` |
| `build-tags` | `gooos`, `baremetal`, `kernelspace` | `gooos`, `baremetal` |
| `gc` | `conservative` | `conservative` |
| `scheduler` | `none` | `tasks` |
| `default-stack-size` | 8192 | 8192 |
| `automatic-stack-size` | true | true |
| `linker` | `ld.lld` | `ld.lld` |

## `scheduler=none` (Kernel) vs `scheduler=tasks` (User)

The single biggest split between the two builds is the scheduler choice. It is not a performance knob; it changes what *language features compile*.

| Aspect | `scheduler=none` (kernel) | `scheduler=tasks` (user) |
|---|---|---|
| Where used | Kernel (`src/target.json:9`) | User ELFs (`user/target.json:9`) |
| Goroutines available | NO | YES (cooperative) |
| What schedules code | gooos's own KernelThread (Ch 05) | TinyGo's task scheduler |
| `go func()` | Compile-time error | Works |
| `chan T` | Absent / unused | Works |
| `select` | Absent / unused | Works |
| Conservative GC | Yes — gooos enumerates roots itself | Yes — TinyGo runtime enumerates tasks |

The consequences ripple through the kernel source tree. There is no `go` statement anywhere under `src/`. There are no `chan T` declarations as cross-thread communication primitives. `select` does not appear. Anything that *would* be a goroutine in stock Go becomes a `KernelThread` (Ch 05); anything that would be a channel becomes a Spinlock + KEvent + bounded queue (Ch 09).

User Ring-3 programs are unaffected. Inside a user process, `go func()` works exactly as Go programmers expect — it spawns a cooperative task whose stack lives in the process's heap, scheduled by TinyGo's task scheduler. Cross-process communication goes through syscalls, not channels.

A practical example of the constraint: `src/scheduler_none_stubs.go:18` provides a halt stub for `tinygo_task_exit`, the symbol the assembly task-switch code references. Under `scheduler=none` the `internal/task` package is not compiled, so the symbol is undefined at link time. The stub satisfies the linker; it is never reached because no goroutine exists to return from.

## Conservative GC Under `scheduler=none`

A conservative GC scans memory ranges and marks any aligned word that *looks like* a heap pointer. Two ranges matter:

1. **Globals**: the `.data` and `.bss` sections holding all package-level variables.
2. **Live stacks**: every executing thread's stack from current SP up to the top of stack.

The linker exports two symbols, `_globals_start` and `_globals_end` (defined in `src/linker.ld:26` and `src/linker.ld:48`), bracketing the global range. The TinyGo runtime — under stock `scheduler=tasks` — would also enumerate every Task it knows about and scan each task's stack.

Under `scheduler=none` there is no Task table. gooos must enumerate live KernelThreads itself. The kthread pool at `src/kthread_pool.go:23` is a `.bss`-resident slab of 32 entries; each slot contains an embedded 16 KiB stack. Because the slab is in `.bss` it falls inside `[_globals_start, _globals_end)` automatically, so the conservative scan picks up every kthread's saved register state and stack as part of the global walk — no separate per-thread enumeration is required.

```mermaid
flowchart LR
    subgraph KernelImage[Kernel ELF Layout]
        A[.text] --> B[.rodata]
        B --> C[".data ← _globals_start"]
        C --> D[.bss with kthreadPool]
        D --> E[".bss end ← _globals_end"]
        E --> F[".heap (scanned via mark)"]
        F --> G[guard 4 KiB]
        G --> H[".pagetables (EXCLUDED)"]
    end
    GC[Conservative GC mark phase] -.-> C
    GC -.-> D
    GC -.-> F
    GC -.x H
```

The `verify-globals.sh` script (`scripts/verify_globals.sh:1`) is a build-time guard that checks the linker actually placed the runtime symbols holding GC roots inside the `[_globals_start, _globals_end)` window. A TinyGo upgrade or an accidental linker-script edit that pushed one of those symbols outside the range would silently make the collector miss live pointers and cause use-after-free. The script reads the kernel ELF with `nm`, locates `_globals_start`/`_globals_end`, and verifies a curated set of symbol names (e.g. `main.kthreadPool`, `main.kschedRunning`) all land inside the window. It exits non-zero with a diagnostic if any symbol falls outside.

## `.pagetables` Exclusion (Revisited from Ch 04)

A page table entry is a 64-bit word whose low bits hold permission flags and whose upper bits hold a physical address. A typical present + writable + huge entry has the value `0x83`. To a conservative GC, this looks like a heap pointer to address 0x83.

If the GC scanned `.pagetables`, it would mark whatever heap object lives at the physical address embedded in each PTE, treating page-table entries as live pointers. The result would be **over-retention** — heap blocks that should be freed remain marked. This is *not* memory unsafety; the GC still won't free in-use memory. It is a memory-pressure issue: the heap fills up faster than it should.

The linker script places `.pagetables` after the heap, separated by a 1-page guard gap, *outside* `[_globals_start, _globals_end)` (`src/linker.ld:73`). The comment block at `src/linker.ld:33-46` explains the trade-off explicitly: putting the page tables in `.bss` would re-introduce the false-positive scan; placing them after `_globals_end` keeps the GC honest while still letting the boot loader load them with the rest of the kernel image.

It is easy to invert this in one's head. The risk is *over-retention, not memory unsafety*. The fix is *exclusion from the scan range, not exclusion from the image*.

## The TinyGo Runtime Patch

Stock TinyGo has no hooks for cross-CPU IPI (Inter-Processor Interrupt) wake-up, ISR (Interrupt Service Routine) depth tracking, conservative-GC under a custom scheduler, or per-CPU runqueues for SMP. gooos adds those hooks via a 1252-line unified diff at `scripts/tinygo_runtime.patch` applied by `scripts/patch_tinygo_runtime.sh:1`.

The patch touches 16 files in the TinyGo source tree under `~/.local/tinygo0.40.1/src/`:

| Patched File | Category | Purpose |
|---|---|---|
| `runtime/runtime_gooos.go` | NEW | Kernel-side runtime bodies (build tag `gooos && baremetal && kernelspace`) — `numCPU`, `atomicsLock`, `futexLock`, `runtime.startupAt`, etc. |
| `runtime/runtime_gooos_user.go` | NEW | Userspace-side runtime bodies (`!kernelspace`) |
| `runtime/runtime_gooos_sched_cores.go` | NEW | Inert under `scheduler=none`; used by `scheduler=cores` builds only |
| `runtime/wait_gooos.go` | NEW | Kernel `waitForEvents` = `sti; hlt; cli` |
| `runtime/wait_gooos_user.go` | NEW | Userspace `waitForEvents` no-op |
| `runtime/wait_other.go` | MOD | Add `&& !gooos` to its build tag so it stops shadowing |
| `runtime/interrupt/interrupt_gooos.go` | NEW | Interrupt-aware versions of `interrupt.In()` etc.; reads `gooos_in_interrupt_depth` |
| `runtime/interrupt/interrupt_gooos_user.go` | NEW | Userspace no-op interrupt shims |
| `runtime/scheduler_cooperative.go` | MOD | Per-CPU runqueues, `stealWork()`, `apScheduler()` (inert under `scheduler=none`) |
| `runtime/scheduler_cores.go` | MOD | Same hunks duplicated for `scheduler=cores` builds |
| `runtime/gc_blocks.go` | MOD | Adds `gcLockWord uint32` — explicit kernel spinlock around `runGC()` |
| `internal/task/task_stack.go` | MOD | Adds `state.stackTop` field |
| `internal/task/task_stack_amd64.go` | MOD | `gooosOnResume` hook + `runtime_systemStackPtr` linkname |
| `internal/task/task_stack_unicore.go` | MOD | Per-CPU `currentTasks[17]`, `gooosStackOverflow` hook |
| `internal/task/task_stack_multicore.go` | MOD | Same set of hunks for multicore variant |
| `internal/task/queue.go` | MOD | Per-Queue spinlock (`gooos_spinlockAcquire`) |

Two important consequences:

- The patch is **mandatory**. Without it, `make` fails at the link step: stock TinyGo references no `gooosWakeupCPU`, no `gooosOnResume`, no `gooos_in_interrupt_depth`. The kernel object provides these; the runtime must be patched to call them.
- The patch is **idempotent**. The shell wrapper detects an already-patched tree by grepping for sentinel strings (`numCPU = 17`, `atomicsLock`, `gcLockWord`) and exits silently. Re-running it on a fresh TinyGo install also works — `--forward` skips already-applied hunks.

`gc_blocks.go`'s `gcLockWord` deserves a callout. Stock TinyGo guards `runGC()` with a `task.PMutex` that becomes a no-op when no scheduler is running (e.g. `tinygo.unicore` builds). The kernel runs `scheduler=none` *but* still has multiple kernel threads on multiple CPUs that can both call into the GC. A no-op mutex would let two CPUs run `runGC()` simultaneously, corrupting mark-bit metadata. Replacing the mutex with a plain `uint32` acquired via `gooos_spinlockAcquire` gives real cross-CPU exclusion without parking — important because the GC mark phase must not block on a scheduler that would itself need allocation.

## Patched Runtime Hooks Visible to gooos

Once the patch is applied, the gooos kernel (`src/`) and userspace runtime (`user/gooos/`) supply implementations for hooks the patched TinyGo runtime calls.

| Hook | Kernel Implementation | Active Under `scheduler=none`? |
|---|---|---|
| `gooosWakeupCPU(cpuID)` | `src/ipi.go:109` | Yes — sends IPI vector 0xFC to wake a halted AP |
| `gooosOnResume()` | `src/goroutine_tss.go:175` | Yes — swaps CR3 (Control Register 3) for Ring-3 host kthreads |
| `gooosNotePush(cpuIdx)` | `src/percpu.go:115` | Diagnostic only (gated on `runSleepAudit`); inert otherwise |
| `gooosNotePop(cpuIdx, ok)` | `src/percpu.go:125` | Diagnostic only |
| `gooosStackOverflow(t)` | `src/panic.go:94` | Yes — prints panic, halts; reachable only from user-side TinyGo task scheduler |
| `gooos_readInterruptDepth` | `src/isr.S` (`gooos_in_interrupt_depth`) | Yes — read by `interrupt_gooos.go` to gate GC actions |
| `gooos_readSyscallDepth` | per-CPU block field | Yes |
| `gooos_spinlockAcquire/Release` | `src/stubs.S` | Yes |

`gooosWakeupCPU` is the most interesting because it bridges the runtime to gooos's IPI machinery. When a kthread becomes runnable on CPU N while N is currently halted in `sti; hlt`, the runtime calls `gooosWakeupCPU(N)`, which translates to `lapicSendIPI(apicID, 0xFC)`. The handler at vector 0xFC is a no-op except that it forces the receiving CPU out of `hlt`; the scheduler then runs and finds the new work. Self-IPI (`cpuIdx == cpuID()`) short-circuits.

`gooosOnResume` is conceptually the inverse of context-switch: every time the kthread/scheduler resumes a Ring-3 host goroutine, this hook fires *before* user code runs. It looks up the per-process PML4 (Page Map Level 4) and writes CR3 so the user goroutine sees its own address space. This must be `//go:nosplit` because the hook runs after the new stack pointer is loaded but before the new goroutine has run any prologue — a stack-grow detour here would be catastrophic.

`gooosNotePush` and `gooosNotePop` were carried over from earlier sleep-audit code. They're guarded by the `runSleepAudit` flag and stay dormant in production builds.

## User-Side Runtime Hooks as No-Ops

Ring-3 user programs link against the same patched TinyGo runtime, so the runtime's references to `gooosOnResume` and `gooosStackOverflow` must resolve. But Ring-3 cannot touch the TSS (Task State Segment) and is already running in the correct address space — there is no CR3 to swap, no kernel state to track. The user-side bodies at `user/gooos/runtime_hooks.go:18` and `user/gooos/runtime_hooks.go:26` are deliberate no-ops:

- `gooosOnResume`: empty body. Comment notes "the CPU is already in Ring 3 and the process's PML4 is current."
- `gooosStackOverflow`: writes a fixed message via `sys_write`, then `sys_exit(1)`.

This is the reason the kernel and user TinyGo builds share the patched runtime files but produce different behavior: the build-tag split (`kernelspace` vs `!kernelspace`) selects the appropriate body, but both bodies satisfy the same set of symbols.

## Stack-Size Considerations

There are *two* notions of "stack" in a gooos build, and they do not interact.

**TinyGo stacks (user only).** Configured by `default-stack-size = 8192` and `automatic-stack-size = true` in `user/target.json:13-14`. TinyGo emits stack-overflow checks at function entry; on overflow it grows the stack by reallocating. This is what makes `go func()` cheap inside a user process — initial stack is small, grows on demand.

**KernelThread stacks (kernel only).** 16 KiB per slot, embedded in the `KernelThread` struct, allocated from `kthreadPool[32]` in `.bss`. Total kernel stack footprint = 32 × 16 KiB = 512 KiB, fixed at link time. The kernel does *not* use TinyGo's growable-stack mechanism — `scheduler=none` strips out the runtime that would handle a stack-grow trap, and in any case the kthread stack lives in `.bss` (statically positioned, not heap-allocated), so growing it would mean memmove-ing every other `.bss` symbol. Instead, kernel code is written carefully to fit within 16 KiB, and `kthread_event.go`-style `//go:nosplit` annotations prevent TinyGo from inserting stack-grow checks where they are inappropriate.

| Stack Kind | Lives in | Size | Grows? | Build Side |
|---|---|---|---|---|
| KernelThread stack | `.bss` (`kthreadPool`) | 16 KiB embedded | No | Kernel |
| Boot stack | `.bss` (`stack_top` symbol) | One page | No | Kernel |
| TinyGo task stack | Process heap (in user) | 8 KiB initial | Yes (TinyGo grows on overflow) | User |
| Ring-3 wrapper stack | KernelThread stack | 16 KiB embedded | No | Kernel hosting Ring-3 |

## ISR Safety

Kernel ISRs run with interrupts disabled and may be reached at arbitrary points in any kthread's execution. Three rules:

1. **Do not allocate.** Allocation invokes `runGC()` if the heap is full; `runGC()` acquires `gcLockWord`; while holding `gcLockWord` the GC mutates metadata; an ISR that interrupted GC and then tried to allocate would deadlock.
2. **Do not call `fmt.Printf`.** `fmt.*` allocates a builder buffer. Use `appendStr` / `appendDec` / `appendHex` (`src/panic.go:22-67`) writing into a fixed `.bss` buffer like `panicHexBuf`, then `serialPrintBytes` directly.
3. **Annotate with `//go:nosplit` where stack growth would be catastrophic.** The pragma tells TinyGo "do not insert a stack-grow check at function entry." Required when the function runs on a stack TinyGo doesn't own (e.g. ISR running on the IST stack), or when growing the stack would re-enter code that's currently held in a non-reentrant state.

Concrete examples in the tree (search results above):

- `src/panic.go` — every helper (`appendStr`, `appendDec`, `appendHex`, `bytesToString`, `gooosStackOverflow`) is `//go:nosplit`.
- `src/keyboard_irq.go:38`, `src/e1000_irq.go:41` — IRQ handlers.
- `src/goroutine_irq.go` — five `//go:nosplit` functions wrapping interrupt-context work.
- `src/kthread_event.go:31, 79, 103, 114` — the KEvent fast paths used by the wait/wake primitive.
- `src/user_signal.go:172, 190, 206, 219, 246, 265` — Ring-3 signal delivery primitives.
- `src/percpu.go:88` — `sleepAuditISRDump` formatting straight from a PIT (Programmable Interval Timer) tick.

The `gooosStackOverflow` body at `src/panic.go:94` is the canonical example. By the time it runs, the goroutine's stack canary has already been overwritten — anything that *re-touches* the stack (a non-nosplit call, a stack-grow check, a complex value-copy parameter) might compound the corruption. The function is `//go:nosplit`, takes only a `uintptr`, formats via `appendStr`/`appendHex` into the `.bss` buffer, prints, and halts.

## `verify-globals.sh` Invariant

The script (`scripts/verify_globals.sh:1`) is run as part of `make` (or a CI check) after the kernel ELF is built. Its job is one assertion: **every runtime symbol whose contents include heap pointers lives inside `[_globals_start, _globals_end)`**.

The mechanism:

1. Read `_globals_start` and `_globals_end` from `nm <kernel>`.
2. List all symbols whose names match the curated regex: under `scheduler=none`, `main.kschedQueues`, `main.kthreadPool`, `main.kschedRunning`, `main.kthreadHostedProc`. (Under the older `scheduler=cores` builds: `runtime.runqueue`, `runtime.runqueues`, `runtime.sleepQueue`, `runtime.timerQueue`.)
3. For each symbol, compute its address; verify `start <= addr < end`.
4. Exit non-zero with a diagnostic if any symbol falls outside.

Why this matters: the conservative GC scans only the global window and the live kthread stacks. If, after a TinyGo upgrade, a runtime patch reshuffle, or an unrelated linker-script edit, one of these queue/pool symbols moved *outside* the window, the GC would silently miss live `*KernelThread` pointers. Use-after-free of kthread state would manifest as random scheduler hangs many test-runs later — extremely hard to debug. The script catches the mistake at build time.

## Rebuilding After a TinyGo Upgrade

The patch as committed applies cleanly to TinyGo 0.40.1 only. The shell wrapper `scripts/patch_tinygo_runtime.sh` checks for `~/.local/tinygo0.40.1/src` (with a deprecation warning if only the legacy 0.33.0 path is present).

Upgrading to a future TinyGo release is not automatic:

1. Install the new TinyGo into a fresh path (e.g. `~/.local/tinygo0.41.0/src`).
2. Apply the existing patch with `--forward`. Hunks that fail need manual rebasing against the new TinyGo source.
3. Adjust line numbers and any surrounding context drift in `scripts/tinygo_runtime.patch`.
4. Run `scripts/patch_tinygo_runtime.sh` to confirm idempotent re-apply.
5. Build the kernel and run `scripts/verify_globals.sh tmp/kernel.bin`.
6. Run the regression test suite (Ch 12).

This is a maintenance cost, not a casual upgrade. The patch's reach (16 files, ~1250 lines) reflects how invasive it is to retrofit cross-CPU and ISR-aware behavior into a runtime that did not anticipate either.

## Summary

- TinyGo replaces stock Go because gooos has no host OS to depend on.
- Kernel uses `scheduler=none` (no goroutines, no channels) → KernelThread + Spinlock + KEvent replace those primitives.
- User uses `scheduler=tasks` (cooperative goroutines work normally inside a process).
- Conservative GC scans `[_globals_start, _globals_end)` plus live kthread stacks; the kthreadPool slab is in `.bss` so the global walk picks up every kthread automatically.
- `.pagetables` is placed *outside* the GC scan range to avoid PTE words being mis-read as heap pointers — over-retention, not memory unsafety.
- A 1252-line patch (`scripts/tinygo_runtime.patch`, 16 files) layers IPI wake-up, ISR-depth tracking, per-CPU spinlocks, and explicit GC locking onto stock TinyGo 0.40.1.
- Hooks: `gooosWakeupCPU`, `gooosOnResume`, `gooosStackOverflow`, `gooos_readInterruptDepth`, `gooos_readSyscallDepth` are active under `scheduler=none`; the per-CPU runqueue scheduler hunks in `scheduler_cooperative.go` are inert.
- `gooosOnResume` exists as a no-op in user space — the CPU is already in Ring 3 and CR3 is correct.
- KernelThread stacks are 16 KiB embedded in `.bss`, fixed; user TinyGo task stacks live in process heap and grow on overflow.
- ISR code is `//go:nosplit`, allocation-free, uses `appendStr`/`appendHex`/`serialPrintBytes` directly; `src/panic.go` is the canonical example.
- `scripts/verify_globals.sh` is a build-time guard ensuring runtime queue/pool symbols stay inside the GC scan window.

## Cross-references

- `./02_build_and_run.md` — when and how `scripts/patch_tinygo_runtime.sh` is invoked during `make`.
- `./04_memory_management.md` — full `.pagetables` placement rationale and heap layout.
- `./05_kernel_thread_runtime.md` — KernelThread, the structure that replaces goroutines under `scheduler=none`.
- `./09_synchronization.md` — Spinlock, KEvent, and bounded-queue primitives that replace channels and `select`.
