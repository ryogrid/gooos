# TODO — SMP Unblock Implementation (M2 / M3 / M4)

Design sources: `impldoc/smp_unblock_overview.md` and sibling docs
(`smp_m2_ap_lapic_timer.md`, `smp_m3_cores_promotion.md`,
`smp_m4_ring3_fault.md`, `smp_unblock_milestones_and_verification.md`,
`smp_unblock_readme_update_plan.md`).

One git commit per top-level item. Mark `- [x]` only when the commit
lands AND the listed verification passes.

**Milestone order:** M2 ‖ M4 (independent) → M3 (depends on M4) →
Closing (README + docs).

## Environment verified (baseline)

- [x] TinyGo 0.40.1 at `~/.local/tinygo0.40.1/` (Wave 1 patched); baseline `make build` / `make lint` / `make verify-globals` green at `smp-take3` tip `93868c4`
- [x] `smp_unblock_*` + `smp_m{2,3,4}_*.md` design set committed in `93868c4`

## M2 — AP LAPIC timer race fix (per `impldoc/smp_m2_ap_lapic_timer.md`)

- [x] **M2-1. Per-CPU `readInterruptDepth` + `readSyscallDepth` asm helpers + `syscallDepth` field**
  - `src/stubs.S`: add `readInterruptDepth` (movl `%gs:4`, `%eax`; ret) and `readSyscallDepth` (movl `%gs:12`, `%eax`; ret) leaf functions
  - `src/percpu.go`: add `syscallDepth uint32` field at offset 12 to `PerCPU` struct; add `pcpuOffSyscallDepth = 12` constant; keep 64-byte alignment
  - Verify: `grep -n 'readSyscallDepth' src/stubs.S src/percpu.go` shows the new declarations; `make build` clean; `make run` (single-CPU) still boots to shell
  - Commit: `fix(smp): per-CPU readInterruptDepth + readSyscallDepth helpers`

- [x] **M2-2. Drop global `gooos_in_interrupt_depth`; syscall-aware ISR prologue/epilogue**
  - `src/isr.S` prologue (~line 110-111): drop `incl gooos_in_interrupt_depth(%rip)`; keep `incl %gs:4`; add `cmpq $0x80, 120(%rsp); jne .Lnosys_enter; incl %gs:12; .Lnosys_enter:`
  - `src/isr.S` epilogue (~line 129-130): mirror decrement + conditional `decl %gs:12`
  - `src/isr.S` (~line 152-168): delete the `.bss` block for `gooos_in_interrupt_depth`
  - `src/goroutine_irq.go`: migrate any reader of the global counter to the per-CPU helper
  - Verify: `grep -n 'gooos_in_interrupt_depth' src/` → 0 matches; `make build` clean; `make run` boots; no `blocked inside interrupt` panics under `scripts/test_sendkey.sh 1`
  - Commit: `fix(smp): drop global gooos_in_interrupt_depth; syscall-aware per-CPU depth`

- [x] **M2-3. Migrate runtime `interrupt.In()` to per-CPU counters**
  - `~/.local/tinygo0.40.1/src/runtime/interrupt/interrupt_gooos.go`: replace `func In() bool { return false }` with `readInterruptDepth() != 0 && readSyscallDepth() == 0`; add linkname declarations for the two helpers
  - Regenerate `scripts/tinygo_runtime.patch` via `git -C /home/ryo/work/tinygo diff > scripts/tinygo_runtime.patch`
  - Update `scripts/patch_tinygo_runtime.sh` post-conditions: grep for `readInterruptDepth`/`readSyscallDepth`
  - Verify: `bash scripts/patch_tinygo_runtime.sh` prints `already-applied:` on the patched tree; `make build` clean; `bash scripts/test_net.sh` PASS; `bash scripts/test_tcp_phase5.sh` PASS
  - Commit: `fix(smp): migrate runtime interrupt.In() to per-CPU counters`

- [ ] ~~**M2-4. Enable AP LAPIC timer at 100 Hz**~~ **(deferred — second-order hang, see Deferred further)**
  - Attempted: un-gated `lapicTimerInit()` on APs in `src/smp.go`. Boot under `-smp 4` still hangs after "Scheduler: TinyGo goroutines active"; APs never print "AP N online" and BSP's `setupUserspace` never emits "ELF: spawning boot shell". Retired the global counter in M2-2 was necessary but not sufficient.
  - Reverted in commit `dfad7a6 fix(smp): re-disable AP LAPIC timer pending M2-4 follow-up`.

- [ ] ~~**M2-5. `scripts/test_smp_m2_timer.sh` + boot-time probe**~~ **(transitively deferred — depends on M2-4)**

## M4 — AP Ring-3 iretq triple-fault investigation + fix (per `impldoc/smp_m4_ring3_fault.md`)

- [x] **M4-investigation. QEMU + GDB evidence-capture pass** (done via `-d int,cpu_reset,guest_errors` alone; evidence in `tmp/m4_qemu.log`)
  - Temporarily enable `stealWork()` call in `scheduler_cooperative.go:247-254`
  - Rebuild; boot `-smp 4` with `-s -S -d int,cpu_reset,guest_errors -D tmp/m4_qemu.log`
  - GDB session per `smp_m4_ring3_fault.md §3.2`: breakpoints, register dumps
  - Work hypothesis table (a-e) per `§4`; capture evidence per `§4.1` to `tmp/m4_evidence_*.txt`
  - Revert the stealWork repro-enable edit (tree returns to Wave 1 safe state)
  - Identify confirmed hypothesis; surface to user if ambiguous (stop condition per `hoge.md §12`)
  - Verify: evidence file committed; Wave 1 build still clean
  - Commit (evidence): `test(smp): M4 investigation evidence capture`

- [x] **M4-fix. Apply fix per confirmed hypothesis** (IDT not loaded on APs — 2-line fix in idt.go + smp.go)
  - Code edit at the fix site named by the confirmed hypothesis row
  - Verify: `make build` clean; **reproducer now passes** — with stealWork enabled, shell boots under `-smp 4` without triple-faulting; `scripts/test_sendkey.sh 1` PASSes under `-smp 4`; mark `impldoc/smp_deferred_and_known_issues.md §2.1` Resolved
  - Commit: `fix(smp): AP Ring-3 iretq <root cause>`

## M3 — scheduler=cores promotion + stealWork wire-up (per `impldoc/smp_m3_cores_promotion.md`)

- [x] **M3-1. Wave 2 runtime declarations (`numCPU`, `gooosSpinLock`, lock vars, `currentCPU`, `gcPauseCore` stub)** (commit `5fd015f`)

- [x] **M3-2. `task_stack_amd64.go` build-tag widening + `runtime_systemStackPtr` linkname**
  - Widen build tag to `(scheduler.tasks || scheduler.cores) && amd64 && !windows`
  - Retire gooos per-CPU `systemStacks` array; consume upstream `systemStackPtr()` via linkname (mirror `task_stack_tinygoriscv.go:12-13`)
  - Rewrite `resume()` / `pause()` / `SystemStack()` per `§4.2`
  - Regenerate patch
  - Verify: `make build` clean under tasks mode (still); `scripts/test_tcp_phase5.sh` PASS
  - Commit: `build(toolchain): widen task_stack_amd64.go build tag; consume systemStackPtr linkname`

- [x] **M3-3. `scheduler_cores.go` push-site retargeting + `stealWork` + `apScheduler`**
  - Patched `scheduler_cores.go`:
    - Add `var runqueues [numCPU]task.Queue` alongside upstream `runqueue`
    - Retarget `scheduleTask` push at line 37 to `runqueues[gooosCpuID()].Push(t)`
    - Retarget `Gosched` push at line 87 to same per-CPU
    - Add `stealWork()` peer-scan function
    - Add `apScheduler()` exported entry for AP bring-up
    - Update main `scheduler()` pop path to drain per-CPU queue (do NOT wire stealWork call yet — that's M3-6)
  - Regenerate patch
  - Verify: `make build` clean; still in tasks mode so cores file not yet active
  - Commit: `build(toolchain): scheduler_cores.go per-CPU runqueues + stealWork + apScheduler`

- [x] **M3-4. Flip `src/target.json` `"tasks"` → `"cores"`**
  - Single-line edit at `src/target.json:9`; first commit where cores mode actually activates.
  - Also folded in: `//go:noescape` annotations on `gooos_spinlockAcquire`/`gooos_spinlockRelease` in both `internal/task/queue.go` and `runtime/runtime_gooos.go`. Required because without them, escape analysis inside `Queue.Push` conservatively marks `&q.lock` (and therefore `q` itself) as escaping, which caused `var markedTaskQueue task.Queue` inside upstream `gc_blocks.go runGC` to be heap-allocated via `runtime.alloc()`. `alloc()` takes `gcLock`, already held by the enclosing `GC()`, so the mutex re-entered and `Pause()`d the goroutine — the CPU ended up idling in the scheduler hlt-loop at RIP=0x100155 (ret-after-hlt, confirmed via `-d int,cpu_reset,guest_errors`). Under `-smp 1` the symptom was a GC that printed `G1G2R1R2` then hung.
  - Verify: `make build` clean under cores mode; `-smp 1` and `-smp 4` both boot to shell; `scripts/test_net.sh` + `test_tcp_phase{1..5}.sh` all PASS.
  - Commit: `build(target): flip scheduler to cores`

- [x] **M3-5. `scripts/patch_tinygo_runtime.sh` Wave 2 post-conditions**
  - Added idempotency + post-condition grep probes for: `numCPU = 17`, `atomicsLock`, `futexLock` in `runtime_gooos.go`; `runqueues`/`stealWork`/`apScheduler` in `scheduler_cores.go`; `runtime_systemStackPtr` (replaces the retired `systemStacks`) in `task_stack_amd64.go`; `//go:noescape` in `internal/task/queue.go`. Also widened the `.rej` cleanup list to cover `scheduler_cores.go` and `task_stack_multicore.go`.
  - Verify: `bash scripts/patch_tinygo_runtime.sh` on the already-patched tree prints `already-applied:` and exits 0.
  - Commit: `build(toolchain): patch script Wave 2 post-conditions`

- [x] **M3-6. Wire `stealWork()` into scheduler pop site**
  - Replaced the "stealWork NOT called" comment block in `scheduler_cores.go:scheduler()` with an active `runnable = stealWork()` fallback after the local pop returns nil.
  - Also wired cross-CPU wake via IPI: the former no-op `schedulerWake()` stub in `runtime_gooos.go` now broadcasts an IPI to every online AP via `gooosWakeupCPU(i)`, scoped by `numCoresOnline` (new `main.numCoresOnline` variable, initialized in `smp.go`). Without this the APs would have halted in `schedulerUnlockAndWait` and never picked up stolen work under `-smp 4` (M2-4 AP LAPIC timer is still deferred, so they have no independent wake source).
  - Verify: `-smp 4` shell boots; `ring3Wrapper: cpuID=1` / `cpuID=3` observed (shell goroutine stolen by AP). `test_net.sh` + `test_tcp_phase5.sh` both PASS.
  - Commit: `fix(smp): wire stealWork call + IPI-broadcast schedulerWake`

- [x] **M3-7. `scripts/test_smp_basic.sh` + boot-time probe**
  - `src/main.go` spawns `smpBasicProbe()` as a kernel goroutine that yields every tick and prints `smp_basic_cpu=N`. Under `-smp >= 2` with stealWork live, an AP eventually pops the goroutine off BSP's runqueue and `N != 0` is emitted.
  - Initial try used 16 synthetic workers with a map-based `distinct >= 2` check; the workers finished too fast on BSP (the single scheduler lock serializes tightly under QEMU) for APs to race them. The yielding-single-goroutine approach produces the same PASS signal (kernel goroutine ran on AP) in a more robust timing regime. `ring3Wrapper: cpuID=1/3` (shell goroutine migrated to AP) is also checked as a complementary signal.
  - `scripts/test_smp_basic.sh`: boots `-smp 4`, greps for `smp_basic_cpu=[1-9]` OR `ring3Wrapper: cpuID=[1-9]`; exits 0 if either fires.
  - Verify: `bash scripts/test_smp_basic.sh` → `result: PASS` (observed `kernel_on_ap=1`).
  - Commit: `test(smp): add M3 basic distribution harness`

## Closing: README + doc updates + reviewer pass + final audit

- [x] **C-1..C-4. Docs refresh** (single bundled commit, see closing commit):
  - `impldoc/smp_deferred_and_known_issues.md` — §2.1 marked RESOLVED; §2.2 status updated to PARTIAL; §4 work-stealing row marked Done; §6 priority order re-ordered.
  - `current_impl_doc/scheduler.md` — opening paragraph updated to `scheduler=cores`; SMP v2 paragraph rewritten to document per-CPU runqueues, live `stealWork`, IPI-broadcast `schedulerWake`, GC escape-analysis fix, and the remaining AP-LAPIC-timer deferral.
  - `current_impl_doc/known_issues.md` — afterTicks paragraph updated to reflect both scheduler modes.
  - `README.md` — tagline, Scheduler row, SMP row all rewritten for multi-core work-stealing as the live state.
  - `TODO_SMP3.md` — M2 marked PARTIAL; M3 marked LANDED with per-item commit tags; M4 marked LANDED; "Deferred further" trimmed.

- [ ] **C-5. Reviewer pass + CRITICAL/MAJOR fix-in**
  - `general-purpose` subagent with the brief from `hoge.md §8`
  - Fix CRITICAL + MAJOR inline; record MINOR in Reviewer findings tail of this file
  - Commit(s): per finding, `docs(review): …` or `fix(smp): …`

- [ ] **C-6. Final completeness audit**
  - `grep -rnE 'TODO|FIXME|XXX' src/ user/ scripts/ impldoc/` — no new markers vs. pre-session baseline (commit `93868c4`)
  - Patch re-apply idempotent; `git status --porcelain` clean except `hoge.md`
  - Every checked TODO_SMP4.md item has exactly one landing commit
  - No commit needed — gate only

## Deferred further

(Filled mid-task as deferrals arise.)

## Reviewer findings

(Filled after the reviewer pass.)
