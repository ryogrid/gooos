# TODO — Preempt + Shell Enhancement Batch (features 2.1 … 2.5)

Design sources: `impldoc/preempt_shell_overview.md` + sibling docs
(`preempt_kernel_goroutines.md`, `preempt_user_goroutines.md`,
`shell_multicore_preempt.md`, `shell_background_jobs.md`,
`shell_ps_command.md`, `preempt_shell_milestones_and_verification.md`,
`preempt_shell_readme_update_plan.md`).

One git commit per top-level item. Mark `- [x]` only when the commit
lands AND the listed verification passes.

**Feature order** (per `preempt_shell_overview.md §3` — lowest-risk first):
2.5 → 2.4 → 2.3 (sub-gate a) → 2.1 → 2.2 → 2.3 (sub-gates b, c) → Closing.

## Environment verified (baseline)

- [x] `smp-take4` at commit `ea45da2` (reviewer fold complete); impldoc design set fully committed in commits `6292f1f…ea45da2`
- [x] TODO baseline captured at `tmp/todo_baseline.txt` (125 markers) per `hoge.md §10 step 3`

## Feature 2.5 — ps command + sys_listprocs (per `impldoc/shell_ps_command.md`)

- [ ] **2.5-1. `Process.LastCpuID` field + `gooosOnResume` hook**
  - `src/process.go`: append `LastCpuID uint32` to `Process` struct (tail position so existing offsets unchanged)
  - `src/goroutine_tss.go`: update `gooosOnResume` body to write `gi.proc.LastCpuID = cpuID()` using existing cached `gi.proc` pointer (nosplit-safe)
  - Verify: `make build` clean; boot to shell under `-smp 1` and `-smp 4`
  - Commit: `feat(proc): track Process.LastCpuID in gooosOnResume for ps (2.5-1)`

- [ ] **2.5-2. `sys_listprocs` #37 handler + dispatch**
  - `src/userspace.go:47-85`: add `sysListprocs = 37`
  - `src/userspace.go:95 syscallDispatch`: add `case sysListprocs: sysListprocsHandler(frame)`
  - `src/ps.go` (NEW): `ProcInfo` struct (64 bytes with `_pad1[3]`), `fillProcInfo`, `sysListprocsHandler`, `writeStructThrough` helper
  - `user/gooos/syscall.go:47-54`: add `sysListprocs = 37`
  - `user/gooos/ps.go` (NEW): `ProcInfo` mirror, `Listprocs` wrapper, `StateString` helper
  - Verify: `make build` clean; `unsafe.Sizeof(ProcInfo{}) == 64` asserted at compile time (build-time const guard); shell boots
  - Commit: `feat(syscall): sys_listprocs #37 handler + ProcInfo ABI (2.5-2)`

- [ ] **2.5-3. `ps` user ELF**
  - `user/cmd/ps/main.go` (NEW): 6-column tabular output (PID, PPID, STATE, CPU, TICKS, NAME)
  - `user/Makefile:21 CMDS`: append `ps`
  - Verify: `make user` builds `user/build/ps.elf`; `make iso` embeds; interactive `ps` at shell prints header + ≥ 1 row
  - Commit: `feat(user): ps command frontend (2.5-3)`

- [ ] **2.5-4. `test_ps.sh` harness**
  - `scripts/test_ps.sh` (NEW): boots shell via QEMU monitor sendkey, runs `ps`, greps for header + shell row
  - Verify: `bash scripts/test_ps.sh` PASS under `-smp 1` and `-smp 4`
  - Commit: `test(user): test_ps.sh harness (2.5-4)`

## Feature 2.4 — shell & + sys_waitpid (per `impldoc/shell_background_jobs.md`)

- [ ] **2.4-1. `sys_waitpid` #34 handler + dispatch (WNOHANG-only)**
  - `src/userspace.go:47-85`: add `sysWaitpid = 34`, `const WNOHANG = 1`
  - `src/userspace.go:95 syscallDispatch`: add `case sysWaitpid: sysWaitpidHandler(frame)`
  - `src/userspace.go` tail: `sysWaitpidHandler` per `shell_background_jobs.md §3.3` (WNOHANG-only; `procByPID[child.pid] == child` reap-race guard; does NOT call `setForegroundProc`)
  - `user/gooos/syscall.go:47-54`: add `sysWaitpid = 34`
  - `user/gooos/proc.go`: append `const WNOHANG = 1` + `Waitpid(pid, options) (int, bool)` wrapper
  - Verify: `make build` clean; `Waitpid(unknown_pid, WNOHANG)` returns negative errno; `Waitpid(live_pid, WNOHANG)` returns `(0, false)`
  - Commit: `feat(syscall): sys_waitpid #34 with WNOHANG (2.4-1)`

- [ ] **2.4-2. Shell parser recognises `&` token + `pipeline.background`**
  - `user/cmd/sh/parse.go:14-16`: add `background bool` to `pipeline` struct
  - `user/cmd/sh/parse.go:90-125 tokenize`: insert `case '&':` with `&&`-lookahead (parseStage rejects `&&`)
  - `user/cmd/sh/parse.go:20-49 parsePipeline`: after final flush, if last token is `&`, drop it and set `p.background = true`
  - Verify: `parsePipeline("hello &")` returns `p.background == true`; `parsePipeline("hello && ls")` returns `(_, false)` (syntax error); existing pipes/redirection still parse correctly
  - Commit: `feat(sh): parser recognises & token and pipeline.background (2.4-2)`

- [ ] **2.4-3. Shell jobs table + reap poll**
  - `user/cmd/sh/jobs.go` (NEW): `jobEntry` struct, `jobs [16]jobEntry` global, `nextJobID`, `reapBackgroundJobs()`, `registerJob(pid, cmd) int` (returns job id, or -1 if table full)
  - `user/cmd/sh/main.go:13-32 main()` REPL: call `reapBackgroundJobs()` before each `gooos.Print("$ ")`
  - Verify: `make user` clean; shell still boots; jobs table always empty (no `&` integration yet)
  - Commit: `feat(sh): jobs table + reap poll (2.4-3)`

- [ ] **2.4-4. Executor honors `pipeline.background`**
  - `user/cmd/sh/main.go:30,39,44,47,57`: thread `background bool` through `executePipeline` → `executeCmdLine` / `executeConcurrentPipe`
  - When background: spawn via `gooos.Spawn` (NOT `Exec`), skip the Wait loop at `:134-138`, register in jobs table, print `[id] pid cmd` immediately
  - Single-stage external command path refactored to use `Spawn`+no-Wait in background case; foreground path unchanged
  - Pipeline + `&`: whole pipeline backgrounded (POSIX); one completion-line per stage
  - Verify: interactive `hello &` returns prompt immediately + completion line within 2 s; `hello` (no `&`) still blocks; `ls | wc &` backgrounds whole pipeline
  - Commit: `feat(sh): executor honors pipeline.background (2.4-4)`

- [ ] **2.4-5. `test_shell_background.sh` harness**
  - `scripts/test_shell_background.sh` (NEW): boots shell, issues `hello &` via QEMU monitor sendkey, verifies completion-line appears within 3 s
  - Verify: PASS under `-smp 1` and `-smp 4`; no regression in `test_pipe_matrix.sh`
  - Commit: `test(sh): test_shell_background.sh harness (2.4-5)`

## Feature 2.3 — shell multicore scheduling verification (per `impldoc/shell_multicore_preempt.md`)

- [ ] **2.3-1. `cpuhog` user ELF**
  - `user/cmd/cpuhog/main.go` (NEW): `func main() { for {} }`
  - `user/Makefile:21 CMDS`: append `cpuhog`
  - Verify: `make user` builds `user/build/cpuhog.elf`
  - Commit: `feat(user): cpuhog user ELF (2.3-1)`

- [ ] **2.3-2. `markerprint` user ELF**
  - `user/cmd/markerprint/main.go` (NEW): 20 iterations `println("marker N"); gooos.Sleep(100)`
  - `user/Makefile:21 CMDS`: append `markerprint`
  - Verify: `make user` builds `user/build/markerprint.elf`
  - Commit: `feat(user): markerprint user ELF (2.3-2)`

- [ ] **2.3-3. `test_smp_shell_distribution.sh` harness (sub-gate a)**
  - `scripts/test_smp_shell_distribution.sh` (NEW): boots `-smp 4`, runs `smpprobe` from shell, greps for ≥ 2 distinct cpuIDs
  - Verify: PASS under `-smp 4`
  - Commit: `test(smp): test_smp_shell_distribution.sh — sub-gate a (2.3-3)`

- [ ] **2.3-4. `test_smp_shell_preempt.sh` harness (sub-gate b)**
  - Depends on 2.1 landed. If 2.1 deferred, skip this item and record in `## Deferred further`.
  - `scripts/test_smp_shell_preempt.sh` (NEW): boots `-smp 4`, issues `cpuhog &` + `markerprint`, verifies ≥ 5 markers within 3 s
  - Verify: PASS under `-smp 1` and `-smp 4` (only if 2.1 landed)
  - Commit: `test(smp): test_smp_shell_preempt.sh — sub-gate b (2.3-4)`

## Feature 2.1 — kernel goroutine preemption (per `impldoc/preempt_kernel_goroutines.md`)

- [ ] **2.1-1. `PreemptDisable` per-CPU field at gs:48**
  - `src/percpu.go:22-33`: append `PreemptDisable uint32` at offset 48 (reusing first 4 bytes of existing `_pad[16]`); trim pad to `_pad [12]byte`; add `pcpuOffPreemptDisable = 48` const
  - Verify: `unsafe.Sizeof(PerCPU{}) == 64` asserted; `make build` clean; `make verify-globals` clean
  - Commit: `feat(smp): PreemptDisable per-CPU field at gs:48 (2.1-1)`

- [ ] **2.1-2. Wire `PreemptDisable` into spinlock asm (Option A)**
  - `src/stubs.S:437-459`: `spinlockAcquire` — insert `incl %gs:48` before return; `spinlockRelease` — insert `decl %gs:48` before return. Covers BOTH kernel-Go and TinyGo-runtime callers transparently.
  - Verify: `make build` clean; `scripts/test_net.sh`, `scripts/test_tcp_phase{1..5}.sh`, `scripts/test_smp_basic.sh` PASS (no functional change yet; just counter mutation)
  - Commit: `feat(smp): wire PreemptDisable into spinlock asm primitives (2.1-2)`

- [ ] **2.1-3. `preemptedFrame` + `savedContext` discriminator in TinyGo runtime**
  - `~/.local/tinygo0.40.1/src/internal/task/task_stack_amd64.go:21-30`: introduce `savedContext` wrapping existing `calleeSavedRegs` + new `preemptedFrame` (15 GPRs + RIP/CS/RFLAGS/RSP/SS)
  - `~/.local/tinygo0.40.1/src/internal/task/task_stack_amd64.S`: existing `swapTask` unchanged; add `resumePreempted` helper in a new `task_stack_preempt_amd64.S` sibling file
  - Update `state.resume()` to branch on `kind` discriminator: if `kind == 1`, call `resumePreempted` (no return); else existing `swapTask` cooperative path
  - Regenerate `scripts/tinygo_runtime.patch`
  - Extend `scripts/patch_tinygo_runtime.sh` post-conditions: `grep -q 'kind uint8' task_stack_amd64.go`, `grep -q 'resumePreempted' task_stack_preempt_amd64.S`
  - Verify: `bash scripts/patch_tinygo_runtime.sh` prints `already-applied:` on patched tree; `make build` clean; cooperative path still works (shell boots)
  - Commit: `build(toolchain): preemptedFrame + resumePreempted in task runtime (2.1-3)`

- [ ] **2.1-4. Preempt ISR vector 0xFB + handler skeleton**
  - `src/idt.go`: add IDT gate for vector 0xFB → `isr_preempt` stub
  - `src/isr.S`: new `isr_preempt` entry; pushes 15 GPRs like `isr_common`; calls `handlePreemptIPI(frame *preemptedFrame)` in Go
  - `src/goroutine_irq.go`: `handlePreemptIPI` — checks 4 safe-point conditions (InterruptDepth>1, PreemptDisable>0, SyscallDepth>0, currently in nosplit via approximation), early-return if any true; else populates `preemptedFrame`, sets `kind=1`, calls `runtime.gooosPreempt`
  - `src/preempt_config.go` (NEW): `const preemptEnabled = false` (gate defaults off)
  - Verify: `make build` clean; vector 0xFB handler fires harmlessly when manually invoked via `lapicSendIPI` test (or skip and verify at 2.1-5); existing regression matrix green
  - Commit: `feat(smp): preempt ISR vector 0xFB handler skeleton (2.1-4)`

- [ ] **2.1-5. BSP timer broadcasts preempt IPI (gated)**
  - `src/lapic_timer.go:76-80`: append `if preemptEnabled { broadcastPreemptIPI() }` after `WantReschedule = 1`
  - `src/ipi.go`: add `broadcastPreemptIPI()` using new `ipiPreemptVector = 0xFB`, modeled on `schedulerWake`
  - Still gated off by `preemptEnabled = false`; runtime state unchanged
  - Verify: `make build` clean; boot to shell under `-smp 4`; regression matrix green
  - Commit: `feat(smp): BSP timer broadcasts preempt IPI (gated) (2.1-5)`

- [ ] **2.1-6. Runtime `gooosPreempt` entry + scheduler integration**
  - `~/.local/tinygo0.40.1/src/runtime/runtime_gooos.go`: add `gooosPreempt(frame *preemptedFrame)` linkname entry that enqueues the preempted task to its CPU's local runqueue (tail) and jumps to `scheduler()` without returning
  - Ensure ISR stack reclaim is explicit (switch to system stack before entering scheduler)
  - Regenerate patch
  - Verify: `make build` clean; regression matrix green with `preemptEnabled = false`
  - Commit: `build(toolchain): runtime gooosPreempt entry + scheduler integration (2.1-6)`

- [ ] **2.1-7. Enable kernel preemption (flip `preemptEnabled = true`)**
  - `src/preempt_config.go`: `const preemptEnabled = true`
  - Verify: `make build` clean; `scripts/test_preempt_kernel.sh` PASS under `-smp 1` AND `-smp 4`; full regression matrix green; no `blocked inside interrupt` panic; no triple-fault
  - Commit: `feat(smp): enable kernel preemption (2.1-7)`

- [ ] **2.1-8. `test_preempt_kernel.sh` harness**
  - `scripts/test_preempt_kernel.sh` (NEW): spawns two BSP-scheduled kernel goroutines (A tight `for {}`, B periodic marker); PASS = ≥ 5 markers in 5 s
  - Verify: PASS under `-smp 1` and `-smp 4`
  - Commit: `test(smp): test_preempt_kernel.sh harness (2.1-8)`

## Feature 2.2 — user goroutine preemption (per `impldoc/preempt_user_goroutines.md`)

- [ ] **2.2-1. PCB signal fields**
  - `src/process.go:32 Process`: append `SigAlrmHandler uintptr`, `UserPreemptPending uint32`, `UserQuantumTicks uint32`, `UserQuantumCounter uint32`, `SigInProgress uint32` (5 fields)
  - Verify: `make build` clean; `unsafe.Sizeof(Process{})` unchanged in any assembly reference
  - Commit: `feat(proc): PCB signal fields (SigAlrm + Quantum + SigInProgress) (2.2-1)`

- [ ] **2.2-2. `sys_sigaction` #35 handler + dispatch**
  - `src/userspace.go:47-85`: add `sysSigaction = 35`, `const SIGALRM = 14`
  - `src/userspace.go:95 syscallDispatch`: add `case sysSigaction: sysSigactionHandler(frame)`
  - `src/userspace.go` tail: `sysSigactionHandler` per `preempt_user_goroutines.md §4.3`
  - `user/gooos/syscall.go:47-54`: add `sysSigaction = 35`
  - `user/gooos/signal.go` (NEW): `const SIGALRM = 14`, `Sigaction(signum, handler) int`
  - Verify: `make build` clean; `Sigaction(SIGALRM, nil)` succeeds; `Sigaction(SIGILL, nil)` returns errno
  - Commit: `feat(syscall): sys_sigaction #35 (2.2-2)`

- [ ] **2.2-3. `sys_sigreturn` #36 handler + dispatch**
  - `src/userspace.go:47-85`: add `sysSigreturn = 36`
  - `src/userspace.go:95 syscallDispatch`: add `case sysSigreturn: sysSigreturnHandler(frame)`
  - `src/userspace.go` tail: `sysSigreturnHandler` per `preempt_user_goroutines.md §4.4`: reads magic + saved context from user stack top, restores `frame.RIP/RSP/RFLAGS/RAX..R11`, clears `SigInProgress`
  - `user/gooos/syscall.go:47-54`: add `sysSigreturn = 36`
  - Verify: `make build` clean; `sysSigreturnHandler` rejects bad-magic frames via `processExit(-1)`
  - Commit: `feat(syscall): sys_sigreturn #36 (2.2-3)`

- [ ] **2.2-4. Kernel tick accounting: `maybeSignalUserPreempt`**
  - `src/user_signal.go` (NEW): `maybeSignalUserPreempt(cpuIdx)` walks `perCPUBlocks[cpuIdx].CurrentPoolIdx` → `*Process`, bumps `UserQuantumCounter`, if ≥ `UserQuantumTicks` and handler registered set `UserPreemptPending = 1` and reset counter
  - `src/lapic_timer.go:76`: insert `if preemptEnabled { maybeSignalUserPreempt(idx) }` after `WantReschedule` set
  - Verify: `make build` clean; counter increments visible via test probe; no Ring-3 yet
  - Commit: `feat(smp): tick-driven user preempt accounting (2.2-4)`

- [ ] **2.2-5. iretq-frame rewrite at syscall-return**
  - `src/user_signal.go`: `maybeDeliverSignal(frame *SyscallFrame)` per `preempt_user_goroutines.md §4.2`: early-return if `UserPreemptPending == 0 || SigAlrmHandler == 0 || SigInProgress == 1`; otherwise push 13-word `sigFrame` onto user stack via `pushU64Through`, rewrite `frame.RIP = SigAlrmHandler` and `frame.RSP` to new user RSP, set `SigInProgress = 1`, clear `UserPreemptPending`
  - Call `maybeDeliverSignal(frame)` at the tail of `syscallDispatch` in `src/userspace.go:95`, under `interrupt.Disable`
  - **Only** from syscall-return; NOT from `jumpToRing3` (per reviewer CRITICAL #4)
  - Add `pushU64Through`, `readU64Through`, `writeU32Through` helpers per `preempt_user_goroutines.md §4.2`
  - Verify: `make build` clean; with `userPreemptEnabled = false` (see 2.2-7), behavior unchanged
  - Commit: `feat(smp): iretq-frame rewrite at syscall-return for SIGALRM delivery (2.2-5)`

- [ ] **2.2-6. TinyGo user-runtime: SIGALRM handler + gooosSigreturn**
  - `~/.local/tinygo0.40.1/src/runtime/runtime_gooos_user.go`: add `gooosSigAlrmHandler` (`//go:nosplit`; calls `Gosched` then `gooosSigreturn`); `gooosSigreturn` linkname to `int 0x80` with RAX=36; register handler in `init`
  - Regenerate `scripts/tinygo_runtime.patch`; extend `scripts/patch_tinygo_runtime.sh` post-conditions: `grep -q 'gooosSigAlrmHandler' runtime_gooos_user.go`
  - Verify: `bash scripts/patch_tinygo_runtime.sh` idempotent; `make user` clean; existing user ELFs still run
  - Commit: `build(toolchain): SIGALRM handler + gooosSigreturn in user runtime (2.2-6)`

- [ ] **2.2-7. Enable user preemption**
  - If 2.1 landed: preempt flows through existing `preemptEnabled`. Skip this step (already enabled).
  - If 2.1 NOT landed: add `src/preempt_config.go` `const userPreemptEnabled = true` gate; have `maybeSignalUserPreempt` and `maybeDeliverSignal` gate on it independently
  - Verify: `scripts/test_preempt_user.sh` PASS under `-smp 1`; full regression matrix green; no Ring-3 triple-fault
  - Commit: `feat(smp): enable user-space SIGALRM preemption (2.2-7)`

- [ ] **2.2-8. `test_preempt_user.sh` harness**
  - `scripts/test_preempt_user.sh` (NEW): spawns user ELF that creates two user goroutines (A tight `for {}`, B periodic marker); PASS = ≥ 5 markers in 5 s
  - Candidate test binary: adapt `user/cmd/gothreadprobe/main.go` which is already in the tree from the prior session
  - Verify: PASS under `-smp 1` and `-smp 4`
  - Commit: `test(smp): test_preempt_user.sh harness (2.2-8)`

## Closing — README + docs (per `impldoc/preempt_shell_readme_update_plan.md`)

- [ ] **C-1. Remove pre-existing stale README bullet + known_issues row (unconditional)**
  - `README.md`: delete the "SMP user-mode Ring-3 disabled" bullet (Known limitations, ~L416-419). M4 fix at `5aea173` made this stale.
  - `current_impl_doc/known_issues.md`: delete the `| SMP v1: APs halt after boot |` row from the Kernel Active-Limitations table. M3 unblock made this stale.
  - `current_impl_doc/known_issues.md`: remove the `no preemption` node from the mindmap if 2.1 landed.
  - Verify: `grep -n 'SMP user-mode Ring-3 disabled' README.md` returns 0; `grep -n 'SMP v1: APs halt' current_impl_doc/known_issues.md` returns 0.
  - Commit: `docs(readme): remove stale SMP Ring-3 and SMP-v1 notes (C-1)`

- [ ] **C-2. Progress-table row variants per landed features**
  - `README.md`: per `preempt_shell_readme_update_plan.md §2.2`-`§2.6`, apply variant A/B/C as appropriate for Scheduler, SMP, Shell, Syscall ABI, Userspace rows based on which of 2.1-2.5 landed.
  - Verify: `grep -cE '^\| [A-Z]' README.md` row count UNCHANGED; content drift matches landed feature set.
  - Commit: `docs(readme): progress-table row updates for preempt+shell features (C-2)`

- [ ] **C-3. `current_impl_doc/scheduler.md` + `known_issues.md` updates**
  - `current_impl_doc/scheduler.md`: inline-extend SMP v2 subsections with preemption details (if 2.1 landed); optionally rewrite standalone `## Preemption (or lack thereof)` section to `## Preemption` (if 2.1 landed).
  - `current_impl_doc/known_issues.md`: remove "No preemption" Kernel-table row (if 2.1 landed); rewrite "No `&` / `fg` / `bg`" Shell-table row (if 2.4 landed).
  - Verify: `grep -n 'Preemption (or lack thereof)' current_impl_doc/scheduler.md` returns 0 if 2.1 landed; landed features reflected.
  - Commit: `docs(impl): as-built update for preempt+shell features (C-3)`

- [ ] **C-4. `impldoc/smp_deferred_and_known_issues.md` §2.2 update (if 2.1 landed)**
  - Add §2.2 update paragraph noting 2.1 chose BSP+IPI path; AP LAPIC timer remains deferred as a `## Future` item.
  - Verify: `grep -n 'preempt_kernel_goroutines.md' impldoc/smp_deferred_and_known_issues.md` shows the cross-link.
  - Commit: `docs(smp): mark SMP deferred §2.2 updated for preempt batch (C-4)`

## Deferred further

*(populated at end of session with items encountered and intentionally not landed)*

## Reviewer findings

*(populated during the mandatory reviewer pass)*
