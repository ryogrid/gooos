# TODO — Preemption Batch (deferred 2.1 / 2.2 / 2.3-b / C-4)

Design sources: `impldoc/preempt_kernel_goroutines.md`,
`impldoc/preempt_user_goroutines.md`, `impldoc/shell_multicore_preempt.md`,
`impldoc/preempt_shell_readme_update_plan.md`.

One git commit per top-level item. Mark `- [x]` only when the commit
lands AND the listed verification passes.

**Feature order**: 2.1 → 2.2 → 2.3-b → Closing (C-4 + scheduler.md + README + known_issues).

## Environment verified (baseline)

- [x] `smp-take4` at commit `671b9d2` (reviewer fold complete from prior session); features 2.3-a, 2.4, 2.5 already shipped
- [x] TODO baseline captured at `tmp/todo_baseline.txt` (125 markers)
- [x] Pristine TinyGo at `/home/ryo/work/tinygo` on tag `v0.40.1` confirmed for patch regen

## Feature 2.1 — Kernel goroutine preemption (per `impldoc/preempt_kernel_goroutines.md`)

- [ ] **2.1-1. `PreemptDisable` per-CPU field at gs:48**
  - `src/percpu.go:22-33`: append `PreemptDisable uint32` reusing first 4 bytes of existing `_pad[16]byte`; trim pad to `[12]byte`
  - `src/percpu.go:36-46`: add `pcpuOffPreemptDisable = 48` constant
  - Verify: `make build` clean; `make verify-globals` clean; boot to shell under `-smp 1`
  - Commit: `feat(smp): PreemptDisable per-CPU field at gs:48 (2.1-1)`

- [ ] **2.1-2. Wire `PreemptDisable` into spinlock asm (Option A)**
  - `src/stubs.S:437-459`: insert `incl %gs:48` at start of `spinlockAcquire`; `decl %gs:48` at start of `spinlockRelease`
  - Add `readPreemptDisable` asm helper mirroring `readInterruptDepth`/`readSyscallDepth`
  - Verify: `make build` clean; regression matrix green (`test_net.sh`, `test_smp_basic.sh`, `test_ps.sh`, `test_shell_background.sh`)
  - Commit: `feat(smp): wire PreemptDisable into spinlock asm primitives (2.1-2)`

- [ ] **2.1-3. `preemptedFrame` + `savedContext` discriminator in TinyGo runtime**
  - Patch `~/.local/tinygo0.40.1/src/internal/task/task_stack_amd64.go`: introduce `savedContext` with `kind uint8` + union (existing `calleeSavedRegs` kind=0; new `preemptedFrame` kind=1 — 15 GPRs + RIP/CS/RFLAGS/RSP/SS)
  - Add `resumePreempted` asm in new `~/.local/tinygo0.40.1/src/internal/task/task_stack_preempt_amd64.S` — pops 15 GPRs then `iretq`
  - Extend `state.resume()` to branch on `kind`: existing path for kind=0; call `resumePreempted` (no return) for kind=1
  - Regen `scripts/tinygo_runtime.patch`; extend `scripts/patch_tinygo_runtime.sh` post-conditions: grep for `kind uint8`, `resumePreempted`
  - Verify: `bash scripts/patch_tinygo_runtime.sh` prints `already-applied:` on patched tree; `make build` clean; cooperative path unchanged (shell boots)
  - Commit: `build(toolchain): preemptedFrame + resumePreempted in task runtime (2.1-3)`

- [ ] **2.1-4. Preempt ISR vector 0xFB + handler skeleton**
  - `src/isr.S`: new `isr_preempt` entry modeled on `isr_common` (15-GPR push, `incl %gs:4`, call Go handler, epilogue)
  - `src/goroutine_irq.go` (or new file): `handlePreemptIPI(vector, errorCode, framePtr)` — 4 safe-point early-returns (`InterruptDepth > 1`, `PreemptDisable > 0`, `SyscallDepth > 0`, active syscall nosplit-RIP approximation); dispatch to runtime `gooosPreempt` entry
  - `src/main.go`: register handler for vector 0xFB via `handlers[]` table at `interrupt.go:16`
  - `src/preempt_config.go` (NEW): `const preemptEnabled = false`
  - Verify: `make build` clean; vector 0xFB is not yet triggered; regression matrix green
  - Commit: `feat(smp): preempt ISR vector 0xFB handler skeleton (2.1-4)`

- [ ] **2.1-5. BSP timer broadcasts preempt IPI (gated)**
  - `src/lapic_timer.go:76-80`: append `if preemptEnabled { broadcastPreemptIPI() }` after `WantReschedule = 1`
  - `src/ipi.go`: add `const ipiPreemptVector = 0xFB`; add `broadcastPreemptIPI()` modeled on `schedulerWake`
  - Still gated off (`preemptEnabled = false`)
  - Verify: `make build` clean; regression matrix green
  - Commit: `feat(smp): BSP timer broadcasts preempt IPI (gated) (2.1-5)`

- [ ] **2.1-6. Runtime `gooosPreempt` entry + scheduler integration**
  - Patch `~/.local/tinygo0.40.1/src/runtime/runtime_gooos.go`: add `gooosPreempt` linkname entry that enqueues preempted task to local runqueue tail and jumps to `scheduler()` without returning
  - Regen `scripts/tinygo_runtime.patch`; extend post-condition greps
  - Verify: `bash scripts/patch_tinygo_runtime.sh` idempotent; `make build` clean; regression matrix green (with `preemptEnabled = false`)
  - Commit: `build(toolchain): runtime gooosPreempt entry + scheduler integration (2.1-6)`

- [ ] **2.1-7. Enable kernel preemption (flip `preemptEnabled = true`)** — **RISK COMMIT**
  - `src/preempt_config.go`: `const preemptEnabled = true`
  - Verify: `make build` clean; `scripts/test_preempt_kernel.sh` (added in 2.1-8) PASS under `-smp 1` AND `-smp 4`; full regression matrix green; no `blocked inside interrupt` panic; no triple-fault
  - On triple-fault: capture QEMU `-d int,cpu_reset,guest_errors -D tmp/qemu_trace.log`; one diagnostic pass; if unresolvable, surface per §Stop Conditions
  - Commit: `feat(smp): enable kernel preemption (2.1-7)`

- [ ] **2.1-8. `test_preempt_kernel.sh` harness**
  - `scripts/test_preempt_kernel.sh` (NEW): boot kernel with built-in test hook — spawn one tight `for{}` kernel goroutine + one periodic-marker sibling; PASS = ≥ 5 markers within 5s
  - Integrate with kernel via a new bootable probe (similar to existing `smpBasicProbe`) gated by `const runPreemptProbe`
  - Verify: PASS under `-smp 1` and `-smp 4`
  - Commit: `test(smp): test_preempt_kernel.sh harness (2.1-8)`

## Feature 2.2 — User goroutine preemption (per `impldoc/preempt_user_goroutines.md`)

- [ ] **2.2-1. PCB signal fields**
  - `src/process.go:32-69`: append 5 fields after `LastCpuID`: `SigAlrmHandler uintptr`, `UserPreemptPending uint32`, `UserQuantumTicks uint32`, `UserQuantumCounter uint32`, `SigInProgress uint32`
  - Verify: `make build` clean; unused fields compile cleanly
  - Commit: `feat(proc): PCB signal fields (SigAlrm + Quantum + SigInProgress) (2.2-1)`

- [ ] **2.2-2. `sys_sigaction` #35 handler + dispatch**
  - `src/userspace.go`: add dispatch case; append `sysSigactionHandler` at file tail (reads signum/handler/flags from frame; validates `signum == SIGALRM == 14`; stores handler in PCB under `procLock`)
  - `user/gooos/syscall.go`: add `sysSigaction = 35`
  - `user/gooos/signal.go` (NEW): `const SIGALRM = 14`, `Sigaction(signum uint32, handler func()) int`
  - Verify: `make build` clean; `make user` clean
  - Commit: `feat(syscall): sys_sigaction #35 (2.2-2)`

- [ ] **2.2-3. `sys_sigreturn` #36 handler + dispatch**
  - `src/userspace.go`: add dispatch case; append `sysSigreturnHandler` (reads 104-byte sigFrame with magic `0xDEADBEEF` from user stack top via new `readU64Through` helper; restores RIP/RSP/RFLAGS/RAX..R11; clears `SigInProgress`; `processExit(-1)` on bad magic)
  - Add `readU64Through(pml4, vaddr) uint64` helper (mirrors `writeU32Through`)
  - `user/gooos/syscall.go`: add `sysSigreturn = 36`
  - Verify: `make build` clean
  - Commit: `feat(syscall): sys_sigreturn #36 (2.2-3)`

- [ ] **2.2-4. Kernel tick accounting `maybeSignalUserPreempt`**
  - `src/user_signal.go` (NEW): `maybeSignalUserPreempt(cpuIdx uint32)` — consult `perCPUBlocks[cpuIdx].CurrentPoolIdx` → `*Process`; bump `UserQuantumCounter`; if ≥ `UserQuantumTicks` (default 10) and handler set, set `UserPreemptPending = 1` and reset counter
  - `src/lapic_timer.go:76-80`: insert call inside existing `if preemptEnabled` guard
  - Verify: `make build` clean; counter increments observable via serial trace (temporary)
  - Commit: `feat(smp): tick-driven user preempt accounting (2.2-4)`

- [ ] **2.2-5. iretq-frame rewrite at syscall-return**
  - `src/user_signal.go`: `maybeDeliverSignal(frame *SyscallFrame)` — early-return unless `UserPreemptPending == 1 && SigAlrmHandler != 0 && SigInProgress == 0`; push 13-word sigFrame (magic, R11..RAX, RFLAGS, RSP, RIP) onto user stack via new `pushU64Through` helper (generalization of `writeU32Through`); rewrite `frame.RIP = SigAlrmHandler`, `frame.RSP = new_user_rsp`; set `SigInProgress = 1`, clear `UserPreemptPending`
  - Add `pushU64Through(pml4 uintptr, userRSP *uintptr, val uint64)` helper
  - Call `maybeDeliverSignal(frame)` at tail of `syscallDispatch` in `src/userspace.go` under `interrupt.Disable`. **ONLY syscall-return, NEVER `jumpToRing3`** (reviewer CRITICAL #4)
  - Verify: `make build` clean; with no user handler registered, behavior unchanged
  - Commit: `feat(smp): iretq-frame rewrite at syscall-return for SIGALRM delivery (2.2-5)`

- [ ] **2.2-6. TinyGo user-runtime SIGALRM handler + gooosSigreturn**
  - Patch `~/.local/tinygo0.40.1/src/runtime/runtime_gooos_user.go`: add `//go:nosplit func gooosSigAlrmHandler()` — calls `Gosched()`, then `gooosSigreturn()` (does not return); `gooosSigreturn` is a linkname to asm stub in `user/rt0.S` that issues `int $0x80` with RAX=36
  - `user/rt0.S`: add `gooosSigreturn` stub
  - Add `init()` block in `runtime_gooos_user.go` that calls `gooosSigaction(SIGALRM, &gooosSigAlrmHandler)` on user-ELF startup
  - Regen `scripts/tinygo_runtime.patch`; extend post-condition greps: `grep -q 'gooosSigAlrmHandler' runtime_gooos_user.go`
  - Verify: `bash scripts/patch_tinygo_runtime.sh` idempotent; `make user` clean; existing user ELFs still boot via shell
  - Commit: `build(toolchain): SIGALRM handler + gooosSigreturn in user runtime (2.2-6)`

- [ ] **2.2-7. Enable user preemption (auto via 2.1 `preemptEnabled`)**
  - No config change needed (2.1 already flipped `preemptEnabled = true`)
  - Verify: `scripts/test_preempt_user.sh` (added in 2.2-8) PASS under `-smp 1`; regression matrix green; no Ring-3 triple-fault
  - Commit: `feat(smp): enable user-space SIGALRM preemption (2.2-7)` — records the landing via a sentinel comment or minor hookup, if needed
  - (If 2.1 didn't land its enable: add separate `userPreemptEnabled` gate)

- [ ] **2.2-8. `test_preempt_user.sh` harness**
  - `scripts/test_preempt_user.sh` (NEW): boots a user ELF with two user goroutines (A tight `for{}`, B periodic marker); PASS = ≥ 5 markers within 5s
  - Repurpose `user/cmd/gothreadprobe/main.go` (already on disk, untracked)
  - Commit: `test(smp): test_preempt_user.sh harness (2.2-8)`

## Feature 2.3 sub-gate (b) — anti-starvation harness

- [ ] **2.3-b. `test_smp_shell_preempt.sh` harness**
  - `scripts/test_smp_shell_preempt.sh` (NEW): boots `-smp 4`, spawns `cpuhog &` + `markerprint` via HMP sendkey (cpuhog.elf + markerprint.elf already committed in prior session), verifies ≥ 5 marker lines within 3s while cpuhog runs
  - Verify: PASS under `-smp 4` (depends on 2.1 landed)
  - Commit: `test(smp): test_smp_shell_preempt.sh — sub-gate b (2.3-b)`

## Closing — C-4 + docs (per `impldoc/preempt_shell_readme_update_plan.md`)

- [ ] **C-4. `impldoc/smp_deferred_and_known_issues.md` §2.2 update**
  - Add `## Update (2026-04-20)` paragraph under §2.2 noting 2.1 chose BSP+IPI broadcast (NOT AP LAPIC timer); AP LAPIC timer remains deferred as a `## Future` item; cross-link `impldoc/preempt_kernel_goroutines.md §Future: per-CPU AP timer`
  - Verify: `grep -n 'preempt_kernel_goroutines.md' impldoc/smp_deferred_and_known_issues.md` shows cross-link
  - Commit: `docs(smp): mark SMP deferred §2.2 updated for preempt batch (C-4)`

- [ ] **Closing-1. `current_impl_doc/scheduler.md` refresh**
  - Rewrite `## Preemption (or lack thereof)` section to `## Preemption` describing BSP-timer + IPI mechanism, safe-points, `PreemptDisable`
  - Extend SMP v2 subsection with preempt details
  - Add new `## Ring-3 signal delivery` subsection for 2.2
  - Commit: `docs(impl): as-built update — preemption live (Closing-1)`

- [ ] **Closing-2. `README.md` Progress-table + `known_issues.md` cleanup**
  - README **Scheduler** row: description → "preemptive via BSP-timer + IPI-broadcast"
  - README **SMP** row: replace "cooperative yield or channel op" language with "APs preempted via BSP-timer-driven IPI broadcast (vector 0xFB)"
  - README **Syscall ABI** row: 36 → 38 (adds #35 sigaction + #36 sigreturn)
  - README **Userspace** row: mention user-goroutine preemption via SIGALRM
  - `current_impl_doc/known_issues.md`: delete "No preemption" Kernel-table row
  - Verify: `grep -n 'cooperative yield or channel op' README.md` → 0; `grep -n 'No preemption' current_impl_doc/known_issues.md` → 0
  - Commit: `docs(readme): progress-table + known-issues refresh for preemption (Closing-2)`

## Deferred further

*(populated at end of session with items encountered and intentionally not landed)*

## Reviewer findings

*(populated during the mandatory reviewer pass)*
