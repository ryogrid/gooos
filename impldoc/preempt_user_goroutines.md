# User-space Goroutine Preemption (feature 2.2)

## Scope

Make user-level goroutines preemptive within a single Ring-3 process so that a user `for {}` compute loop does not starve sibling goroutines hosted by the same `ring3Wrapper` kernel goroutine. Implementation: **kernel-delivered SIGALRM-style signal** (mechanism B from the overview's Design Decisions table) that redirects user RIP to a TinyGo-runtime-registered handler performing `runtime.Gosched()`.

Out of scope: cross-process user preemption (already covered by kernel preemption, feature 2.1 — when the hosting ring3Wrapper is preempted, the whole process yields its core); changing `user/target.json` from `scheduler=tasks` (mechanism A, rejected — see §5).

Mechanism B is the load-bearing design decision for this doc. Approved by user in plan-mode on 2026-04-20.

## Cross-links

- `preempt_shell_overview.md` — Design Decisions table names B as primary, A rejected, C in §Rejected alternatives below.
- `preempt_kernel_goroutines.md` — feature 2.1. Composes cleanly: 2.1 preempts the hosting ring3Wrapper at the kernel level; 2.2 preempts user goroutines inside one ring3Wrapper while it's running.
- `shell_multicore_preempt.md` — feature 2.3 probes intra-process fairness as a separate sub-gate.
- `impldoc/runtime_patches.md` — TinyGo patch surface conventions. 2.2 extends that doc rather than duplicating.
- `impldoc/smp_m4_ring3_fault.md` — the Ring-3 `iretq` fault fix (commit `5aea173`) shares the hardware mechanism 2.2 exercises. §7 Risks covers the interaction.

## 1. Current State

- User processes target `scheduler=tasks` (`user/target.json` line 9). Single cooperative runqueue per user runtime instance; goroutines yield only at `runtime.Gosched()`, channel ops, or blocking syscalls.
- Each user process is hosted by one `ring3Wrapper` kernel goroutine (`src/process.go:342 go ring3Wrapper(child)`, spawned per process in `elfSpawn`). One ring3Wrapper runs on one kernel CPU at a time. A user process cannot span multiple kernel CPUs at Ring-3 granularity; this is the architectural reason mechanism A is rejected (see §5).
- Existing user syscalls (see `src/userspace.go:47-85` table): 0..33 allocated. 34 is the first free slot; 2.4 claims it for `sys_waitpid`, and 2.2 claims **35 for `sys_sigaction`**.
- `sys_yield` (#7, `user/gooos/proc.go:49`) calls `runtime.Gosched()` from user space today — this is the existing cooperative yield.
- No kernel signal-delivery path exists. No way for the kernel to force user-RIP redirection. This doc designs that path from scratch.
- TinyGo user runtime: `~/.local/tinygo0.40.1/src/runtime/runtime_gooos_user.go` (referenced by `user/Makefile:12 TINYGOROOT`). Patched via `scripts/tinygo_runtime.patch`; post-conditions in `scripts/patch_tinygo_runtime.sh`.
- The Ring-3 trap-return mechanism is the same one exercised by M4's `iretq` fix (`smp_m4_ring3_fault.md`, landed at `5aea173`). The kernel pushes CS/SS/RSP/RFLAGS/RIP on the kernel stack and `iretq`-es to Ring-3. Mechanism B reuses this without new GDT/IDT work.

## 2. Design Overview

Three participants:

1. **Kernel tick path**. On every 100 Hz BSP LAPIC timer (`src/lapic_timer.go:76 handleLAPICTimer`), after setting `WantReschedule`, examine the current process (`currentProc()` in `src/userspace.go`). If the process has registered a SIGALRM-style handler AND has accumulated `userQuantumTicks` (e.g. 10, = 100 ms) since last delivery, set its per-process `UserPreemptPending = 1` and record that the next Ring-3 return must detour through the handler.
2. **Ring-3 return path**. Whenever the kernel would `iretq` back to user space (`src/userspace.go` syscall-return path, `src/stubs.S jumpToRing3`), check `UserPreemptPending`. If set and the user has a handler registered, rewrite the CS/SS/RSP/RFLAGS/RIP slots on the kernel stack so that `iretq` lands at the handler rather than the original user RIP. Push the original RIP/RSP onto the user stack in a fixed layout the handler knows how to read. Clear `UserPreemptPending`.
3. **User runtime handler**. The TinyGo user-runtime patch installs a handler on startup via `sys_sigaction(SIGALRM, handler, 0)`. The handler calls `runtime.Gosched()`, then executes a trampoline that reads the saved RIP/RSP off the top of its stack and `sys_sigreturn`s — a new syscall #36 that restores the saved context and returns to the original user code.

Every user goroutine inside a process therefore preempts at ~100 ms granularity regardless of whether it ever makes a syscall, at a cost of two extra kernel trips per quantum (sigaction on startup, sigreturn on resume).

## 3. ABIs

### 3.1 sys_sigaction (syscall #35)

Signature: `sys_sigaction(signum uint32, handler uintptr, flags uint32) → errno int32`.

- `signum` — only `SIGALRM = 14` supported in this batch. Other values return `fdErrBad`.
- `handler` — user-space function pointer. `0` to uninstall.
- `flags` — reserved; must be 0.
- errno — `0` on success, negative on failure. `fdErrBad` for unsupported signum or no-current-process.

Register ABI (matches existing syscall pattern at `src/userspace.go:697-729`):
- `RAX = 35` (syscall #)
- `RDI = signum`
- `RSI = handler`
- `RDX = flags`
- Return in `RAX`.

SDK wrapper in `user/gooos/signal.go` (new file):

```go
const SIGALRM = 14

// Sigaction installs handler for signum. Returns 0 on success,
// negative errno on failure. handler == nil uninstalls.
func Sigaction(signum uint32, handler func()) int {
    var h uintptr
    if handler != nil {
        h = **(**uintptr)(unsafe.Pointer(&handler)) // fat func ptr
    }
    r := syscall3(sysSigaction, uintptr(signum), h, 0)
    return int(int64(r))
}
```

### 3.2 sys_sigreturn (syscall #36)

Signature: `sys_sigreturn() → [does not return]`.

- Kernel pops the saved RIP/RSP/RFLAGS from a known offset on the user stack, rewrites the kernel-stack iretq frame, `iretq`s.
- No arguments, no return value (the syscall never returns to the calling user code; it resumes the interrupted code).
- errno path: if no saved context present (misuse), the process is SIGKILL-equivalent: call `processExit(-1)`.

Register ABI: `RAX = 36`, all other regs are ignored. Kernel handler reads saved context from `user_rsp + sigreturn_offset`.

### 3.3 Per-process signal state (PCB extension)

Extend `Process` struct (`src/process.go:32`) with a new signal block at the tail:

```go
// Signal-delivery state (feature 2.2). Zero = no handler
// installed; preemption IPI is a no-op for this process.
SigAlrmHandler     uintptr // user-space fn pointer; 0 = unregistered
UserPreemptPending uint32  // set by kernel tick; cleared on delivery
UserQuantumTicks   uint32  // default 10 (100 ms @ 100 Hz)
UserQuantumCounter uint32  // incremented per BSP tick while this proc is the active ring3Wrapper
```

All four fields are protected by `procLock` for writes from the tick path; reads from the Ring-3 return path are the *same CPU* that runs the ring3Wrapper, so no lock needed on the read side.

### 3.4 Signal-delivery frame layout (on user stack)

When the kernel decides to deliver SIGALRM, it rewrites the iretq frame to point at `SigAlrmHandler` AND pushes a `sigFrame` onto the user stack (growing down from the interrupted RSP):

```
+--------------------+  user_rsp (after push)
| saved RIP          |  original interrupted RIP
| saved RSP          |  original interrupted RSP (before push)
| saved RFLAGS       |  original RFLAGS
| saved RAX..R11     |  caller-saved GPRs (9 words)
| magic = 0xDEADBEEF |  integrity check for sys_sigreturn
+--------------------+  (higher addresses)
```

`sys_sigreturn` reads from `rsp + 0..sigFrameSize`, verifies the magic, and restores.

Size: 13 * 8 = 104 bytes. Must fit in the 2-page user stack (`src/process.go:326-330` allocates 2 pages = 8 KiB); worst-case nested handler invocations (per quantum = 10 per second) × 104 bytes = 1040 bytes/s, bounded by the fact that `sys_sigreturn` pops before the next signal can land.

## 4. Kernel-side Implementation

### 4.1 Tick path (`src/lapic_timer.go:76-80`)

```go
//go:nosplit
func handleLAPICTimer(vector uint64) {
    idx := cpuID()
    perCPUBlocks[idx].WantReschedule = 1

    // 2.2: tick-driven user-space preempt accounting.
    if preemptEnabled {
        // see 2.1 for preempt IPI broadcast (omitted here).
        maybeSignalUserPreempt(idx)
    }
    lapicSendEOI()
}
```

`maybeSignalUserPreempt(cpuIdx)`: consult `perCPUBlocks[cpuIdx].CurrentPoolIdx` (ring3 pool slot of the currently-running process, if any), resolve to `*Process`, bump `UserQuantumCounter`, if ≥ `UserQuantumTicks` and `SigAlrmHandler != 0` set `UserPreemptPending = 1` and reset the counter.

### 4.2 Ring-3 return path

Two sites return to Ring-3 today: syscall-return (at the end of `syscallDispatch` via `isr_common` epilogue in `src/isr.S:144-149`) and initial user entry via `jumpToRing3` in `src/stubs.S`.

Add a new kernel-side function `maybeDeliverSignal(frame *SyscallFrame)` called at both sites just before `iretq`. Pseudocode:

```go
//go:nosplit
func maybeDeliverSignal(frame *SyscallFrame) {
    p := currentProc()
    if p == nil || p.UserPreemptPending == 0 || p.SigAlrmHandler == 0 {
        return
    }
    // Build sigFrame on user stack (growing down).
    userRSP := frame.RSP
    pushU64(&userRSP, 0xDEADBEEF)        // magic
    pushU64(&userRSP, uint64(frame.R11)) // caller-saved GPRs
    // ... rax..r11 ...
    pushU64(&userRSP, uint64(frame.RFLAGS))
    pushU64(&userRSP, uint64(frame.RSP))
    pushU64(&userRSP, uint64(frame.RIP))
    // Rewrite iretq frame to jump to handler.
    frame.RIP = p.SigAlrmHandler
    frame.RSP = userRSP
    // RFLAGS: disable interrupts inside handler? No — handler is just
    // user code, interrupts remain on.
    p.UserPreemptPending = 0
}
```

`pushU64` writes through the same paddr mechanism as `elfSpawn` uses for argument-page population (`src/process.go:317-323`): walk the user's PML4 to find the paddr for `userRSP-8`, write through the identity-mapped kernel half. No new VM plumbing required.

### 4.3 sys_sigaction handler (`src/userspace.go`, new)

```go
// --- Syscall 35: sys_sigaction ---
// RDI = signum, RSI = handler, RDX = flags.
func sysSigactionHandler(frame *SyscallFrame) {
    p := currentProc()
    if p == nil {
        frame.RAX = sysFail(fdErrBad)
        return
    }
    if frame.RDI != SIGALRM || frame.RDX != 0 {
        frame.RAX = sysFail(fdErrBad)
        return
    }
    fl := procLock.Acquire()
    p.SigAlrmHandler = frame.RSI
    p.UserQuantumCounter = 0
    if p.UserQuantumTicks == 0 {
        p.UserQuantumTicks = 10
    }
    procLock.Release(fl)
    frame.RAX = 0
}
```

Add dispatch case in `syscallDispatch` (`src/userspace.go:95`).

### 4.4 sys_sigreturn handler (`src/userspace.go`, new)

```go
// --- Syscall 36: sys_sigreturn ---
// No args; restores RIP/RSP/RFLAGS/RAX..R11 from sigFrame at user stack top.
func sysSigreturnHandler(frame *SyscallFrame) {
    p := currentProc()
    if p == nil {
        processExit(-1)
        return
    }
    magic := readU64(frame.RSP)
    if magic != 0xDEADBEEF {
        processExit(-1) // trashed sigFrame
        return
    }
    // Pop fields in reverse of push order.
    popU64(&frame.RSP) // magic (consumed)
    frame.R11 = popU64(&frame.RSP)
    // ... restore rax..r10 ...
    frame.RFLAGS = popU64(&frame.RSP)
    savedRSP := popU64(&frame.RSP)
    savedRIP := popU64(&frame.RSP)
    frame.RIP = savedRIP
    frame.RSP = savedRSP
}
```

## 5. User-runtime side (patched TinyGo)

### 5.1 Handler registration (called once from `runtime_gooos_user.go init`)

```go
// gooosSigAlrmHandler is called by the kernel via iretq redirection
// when the user process's quantum expires. The kernel has already
// saved the interrupted context onto the user stack above this
// frame; we just yield and then sigreturn.
//
//go:nosplit
func gooosSigAlrmHandler() {
    Gosched()
    gooosSigreturn() // sysSigreturn; does not return
}

//go:linkname gooosSigreturn gooosSigreturn
func gooosSigreturn()

// Registered in init at process start.
func init() {
    gooosSigaction(SIGALRM, unsafe.Pointer(&gooosSigAlrmHandler))
}
```

- The handler is `//go:nosplit` to guarantee no stack growth (the kernel already pushed 104 bytes; a split would ask the user heap for more stack mid-delivery).
- `Gosched()` returns after other runnable user goroutines have had a turn.
- `gooosSigreturn()` is the raw `int 0x80` with RAX=36; never returns.

### 5.2 Trampoline considerations

The 104-byte sigFrame grows the user stack by slightly more than one guard page's worth after 70 quanta (~7 s) of un-returned handler invocations. This cannot happen under normal control flow because `sys_sigreturn` pops before the next tick can deliver. But if user code jumps out of the handler without calling `sys_sigreturn` (e.g. `longjmp`), stack leaks. **Out of scope**: documented as a safety invariant. User code must return normally from the handler.

## 6. GDT / IDT / TSS audit

- **GDT** (`src/gdt.go`): unchanged. Existing `userCodeSelector` and `userDataSelector` are valid for the handler CS/SS.
- **IDT** (`src/idt.go`): unchanged for 2.2. The sigaction/sigreturn syscalls use the existing int 0x80 gate (DPL=3, per M2-2 landing).
- **TSS** (`src/goroutine_tss.go`): unchanged. `TSS.RSP0` is already the per-process kernel stack via ring3_pool; the signal-delivery path reuses it without changes.
- **Interaction with M4 fix (`5aea173`)**: the fix at M4 ensured the AP Ring-3 `iretq` path sets up a valid trap frame. Mechanism B rewrites that trap frame in-place; it does not alter the AP bring-up or TSS-install sequence. A reviewer should specifically verify that our `frame.RIP = p.SigAlrmHandler` overwrite at §4.2 lands on a kernel stack that was constructed under M4's invariants (i.e. at an entry where `frame` is valid). The safe sites are: end of `syscallDispatch`, end of `ring3Wrapper` setup just before `jumpToRing3`. Do NOT call `maybeDeliverSignal` from a path that could run before the initial `jumpToRing3` (no Ring-3 context yet).

## 7. Commit-per-edit Plan

1. `feat(proc): PCB signal fields (SigAlrmHandler, UserPreemptPending, quantum)` — struct extension in `src/process.go:32`. Build-only.
2. `feat(syscall): sys_sigaction #35 handler + dispatch` — `src/userspace.go` number const, handler, dispatch case; `user/gooos/syscall.go` number const; `user/gooos/signal.go` (new) SDK wrapper.
3. `feat(syscall): sys_sigreturn #36 handler + dispatch` — mirror commit 2 for the return path.
4. `feat(smp): maybeSignalUserPreempt tick accounting` — `src/lapic_timer.go` + a new `src/user_signal.go` housing `maybeSignalUserPreempt` and `maybeDeliverSignal`. Gated by the existing `preemptEnabled` flag from 2.1.
5. `feat(smp): iretq frame rewrite in Ring-3 return path` — call `maybeDeliverSignal(frame)` at the end of `syscallDispatch` and from `jumpToRing3` setup. Push `sigFrame` onto user stack.
6. `feat(runtime): SIGALRM handler + gooosSigreturn in user runtime` — `~/.local/tinygo0.40.1/src/runtime/runtime_gooos_user.go`. Regen `scripts/tinygo_runtime.patch`. Add post-condition greps in `scripts/patch_tinygo_runtime.sh`.
7. `test(user): preempt harness` — new `user/cmd/userpreempt/main.go` (or reuse `gothreadprobe/main.go` already on disk in `smp-take4` state) that spawns two user goroutines where one runs a tight loop and the other prints markers. `scripts/test_preempt_user.sh` greps for ≥ 5 marker lines within 5 s under `-smp 1`.
8. Enable: confirm `preemptEnabled = true` from 2.1 flows through. If this batch ships 2.2 without 2.1, add a separate `userPreemptEnabled` gate that defaults to true.

## 8. Per-File Edits

Kernel (`/home/ryo/work/gooos/src/`):
- `process.go:32-64` — append 4 signal fields.
- `userspace.go:47-85` — add `sysSigaction = 35`, `sysSigreturn = 36`, `SIGALRM = 14`.
- `userspace.go:95 syscallDispatch` — add `case sysSigaction: sysSigactionHandler(frame)` and `case sysSigreturn: sysSigreturnHandler(frame)`.
- `userspace.go` tail — new `sysSigactionHandler`, `sysSigreturnHandler` functions.
- `user_signal.go` (NEW) — `maybeSignalUserPreempt`, `maybeDeliverSignal`, helpers `pushU64Through`, `readU64Through` that walk user PML4.
- `lapic_timer.go:76` — insert `maybeSignalUserPreempt(idx)` call inside `if preemptEnabled`.
- `stubs.S` — no change; existing `jumpToRing3` entry is upstream of signal delivery.

User SDK (`/home/ryo/work/gooos/user/gooos/`):
- `syscall.go:47-54` — add `sysSigaction = 35`, `sysSigreturn = 36`.
- `signal.go` (NEW) — `const SIGALRM = 14`, `Sigaction(signum, handler) int`, `Sigreturn()` (opaque asm stub calling `syscall0(sysSigreturn)` with no return).

Patched TinyGo (`~/.local/tinygo0.40.1/src/runtime/`):
- `runtime_gooos_user.go` — `gooosSigAlrmHandler` (nosplit), `gooosSigreturn` linkname, `init` registering the handler. Imports kernel-side syscall stubs already present in the file.

Patch surface:
- `scripts/tinygo_runtime.patch` regen.
- `scripts/patch_tinygo_runtime.sh` — add greps: `grep -q 'gooosSigAlrmHandler' runtime_gooos_user.go`, `grep -q 'gooosSigreturn' runtime_gooos_user.go`.

Referenced but unmodified: `scripts/embed_elfs.sh` (if a new `userpreempt.elf` is added under `user/cmd/`, `user/Makefile:21 CMDS` picks it up automatically on next rebuild).

## 9. Entry Criteria

- 2.1 has landed OR `userPreemptEnabled = true` gate is added independently (see §7 commit 8 note).
- `make build && make lint && make verify-globals` clean.
- M4's Ring-3 fault fix (`5aea173`) is in the history (sanity-check via `git log --oneline | grep 5aea173`).
- Full regression matrix green under `-smp 1` and `-smp 4`.

## 10. Exit Criteria

- `scripts/test_preempt_user.sh` PASS under `-smp 1` (≥ 5 marker observations within 5 s from the "starved" goroutine).
- `scripts/test_net.sh`, `scripts/test_tcp_phase{1..5}.sh`, `scripts/test_gochan.sh`, `scripts/test_pipe_matrix.sh`, `scripts/test_smp_basic.sh` remain PASS under `-smp 1` and `-smp 4`.
- No `blocked inside interrupt` panic in any run (reviewer check (e) from `preempt_shell_overview.md §Reviewer brief`).
- No Ring-3 triple-fault in any run.

## 11. Rollback

- Primary: uninstall SIGALRM handler from all running processes by writing 0 to `SigAlrmHandler`; new processes start with the field already 0. Disable tick-side signal accounting by gate-flipping `preemptEnabled` (2.1's flag) or adding a dedicated `userPreemptEnabled = false` in `src/preempt_config.go`.
- Secondary: `git revert` commits 7 → 5 → 4 in reverse. Commits 1-3 + 6 are behavior-neutral (syscalls exist but never invoked; PCB fields exist but unused).

## 12. Risks

- **iretq-frame-rewrite race**. The kernel rewrites `frame.RIP` and `frame.RSP` at the end of `syscallDispatch`. If another kernel goroutine on the same CPU observes the half-rewritten frame (impossible — it's a stack-local struct), or if the timer fires between `frame.RIP =` and `iretq`, the partially-rewritten frame could cause a triple-fault. Mitigation: write RFLAGS/RSP/RIP under `interrupt.Disable`. **Reviewer bullet (c)** of the brief should specifically verify this atomic invariant.
- **Nested signal delivery**. If the handler itself runs long enough to see another quantum tick, the kernel would try to push a second sigFrame on top of the handler's stack. Mitigation: set a PCB flag `SigInProgress = 1` on delivery, clear on `sys_sigreturn`; `maybeDeliverSignal` early-returns when `SigInProgress == 1`.
- **GC alloc inside handler**. TinyGo's `Gosched()` does not allocate, but if the user runtime inserts any check that *could* allocate (e.g. goroutine-state transition records), a mid-handler GC cycle could call `runtime.alloc` under an unexpected stack. Mitigation: handler is `//go:nosplit` which bans allocation.
- **Interaction with `blocked inside interrupt` regression class (`smp_deferred_and_known_issues.md §2.2`)**. `interrupt.In()` is driven by kernel-side `InterruptDepth`/`SyscallDepth` and is not affected by Ring-3 signal delivery. However, `maybeDeliverSignal` is called *inside* `syscallDispatch`, which runs at `SyscallDepth > 0`. Any `task.Pause` inside the handler path is still safe (same invariant M2-2 established). Reviewer bullet (e) covers this.
- **Handler address corruption**. User process could write garbage to `SigAlrmHandler` (any vaddr, even zero page). Kernel never dereferences the pointer from kernel space — it's written into `frame.RIP` and dereferenced by `iretq`. Bad value = Ring-3 fault, which is the user process's problem. Mitigation: none needed; behaves like SIGSEGV semantics.

## 13. Deliverables

- 8 commits per §7.
- New files: `src/user_signal.go`, `user/gooos/signal.go`, optionally `user/cmd/userpreempt/main.go`.
- Modified TinyGo runtime: `runtime_gooos_user.go`.
- Harness: `scripts/test_preempt_user.sh`.
- Patch artifacts: updated `scripts/tinygo_runtime.patch`, extended post-conditions in `scripts/patch_tinygo_runtime.sh`.

## 14. Rejected Alternatives

### 14.1 Mechanism A: user target `scheduler=cores`

Rejected. One user process = one `ring3Wrapper` kernel goroutine = runs on one CPU at a time. A user runtime with `scheduler=cores` expects N independent execution contexts with per-core runqueues, per-core systemStacks, and cross-core wakeup IPIs. A single ring3Wrapper cannot provide any of those: there is one kernel-side execution context per user process, regardless of how many kernel CPUs exist. Giving a user process multiple ring3Wrappers would mean either (i) multiple kernel goroutines sharing one user PML4 — introduces TLB-shootdown, fd-table concurrency, and user-stack aliasing problems the kernel is nowhere near ready to handle (see `impldoc/smp_kernel_data_audit.md`) — or (ii) separate address spaces per wrapper, i.e. separate processes, which defeats the point.

Mechanism A is closed until multi-threaded user processes ship. That is a strictly larger project than 2.2.

### 14.2 Mechanism C: check-pending-flag at syscall return

Rejected as primary; mentioned here for completeness.

Sketch: kernel sets `UserPreemptPending` on the quantum tick; user runtime checks the flag on every syscall return and calls `Gosched()` when set.

Pros: cheapest possible implementation (~10 LoC kernel + ~5 LoC user runtime), no new syscalls, no iretq rewrite, no handler registration.

Rejected because: a pure compute loop with no syscalls never observes the flag. A user `for {}` continues to starve siblings forever. This defeats the point of feature 2.2 — the entire reason we are not already satisfied with the existing `sys_yield`-based cooperative model is that it doesn't cover hostile or accidental pure-compute code. Mechanism C solves nothing that the current design already solves.

Kept documented so a future simplification (if TinyGo gains a compiler-inserted periodic Gosched-check under `scheduler=tasks`) can revisit.

## Reviewer MINOR notes

*Populated during the mandatory reviewer pass.*
