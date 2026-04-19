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

- [ ] **M2-1. Per-CPU `readInterruptDepth` + `readSyscallDepth` asm helpers + `syscallDepth` field**
  - `src/stubs.S`: add `readInterruptDepth` (movl `%gs:4`, `%eax`; ret) and `readSyscallDepth` (movl `%gs:12`, `%eax`; ret) leaf functions
  - `src/percpu.go`: add `syscallDepth uint32` field at offset 12 to `PerCPU` struct; add `pcpuOffSyscallDepth = 12` constant; keep 64-byte alignment
  - Verify: `grep -n 'readSyscallDepth' src/stubs.S src/percpu.go` shows the new declarations; `make build` clean; `make run` (single-CPU) still boots to shell
  - Commit: `fix(smp): per-CPU readInterruptDepth + readSyscallDepth helpers`

- [ ] **M2-2. Drop global `gooos_in_interrupt_depth`; syscall-aware ISR prologue/epilogue**
  - `src/isr.S` prologue (~line 110-111): drop `incl gooos_in_interrupt_depth(%rip)`; keep `incl %gs:4`; add `cmpq $0x80, 120(%rsp); jne .Lnosys_enter; incl %gs:12; .Lnosys_enter:`
  - `src/isr.S` epilogue (~line 129-130): mirror decrement + conditional `decl %gs:12`
  - `src/isr.S` (~line 152-168): delete the `.bss` block for `gooos_in_interrupt_depth`
  - `src/goroutine_irq.go`: migrate any reader of the global counter to the per-CPU helper
  - Verify: `grep -n 'gooos_in_interrupt_depth' src/` → 0 matches; `make build` clean; `make run` boots; no `blocked inside interrupt` panics under `scripts/test_sendkey.sh 1`
  - Commit: `fix(smp): drop global gooos_in_interrupt_depth; syscall-aware per-CPU depth`

- [ ] **M2-3. Migrate runtime `interrupt.In()` to per-CPU counters**
  - `~/.local/tinygo0.40.1/src/runtime/interrupt/interrupt_gooos.go`: replace `func In() bool { return false }` with `readInterruptDepth() != 0 && readSyscallDepth() == 0`; add linkname declarations for the two helpers
  - Regenerate `scripts/tinygo_runtime.patch` via `git -C /home/ryo/work/tinygo diff > scripts/tinygo_runtime.patch`
  - Update `scripts/patch_tinygo_runtime.sh` post-conditions: grep for `readInterruptDepth`/`readSyscallDepth`
  - Verify: `bash scripts/patch_tinygo_runtime.sh` prints `already-applied:` on the patched tree; `make build` clean; `bash scripts/test_net.sh` PASS; `bash scripts/test_tcp_phase5.sh` PASS
  - Commit: `fix(smp): migrate runtime interrupt.In() to per-CPU counters`

- [ ] **M2-4. Enable AP LAPIC timer at 100 Hz**
  - `src/main.go`: un-gate `lapicTimerInitAP()` call on each AP
  - Verify: `make run-smp` — serial log shows `LAPIC timer: N ticks/10ms` on BSP calibration; boot reaches shell without hanging at "Scheduler: TinyGo goroutines active"; `scripts/test_net.sh` + `test_tcp_phase5.sh` PASS
  - Commit: `fix(smp): enable AP LAPIC timer at 100 Hz`

- [ ] **M2-5. `scripts/test_smp_m2_timer.sh` + boot-time probe**
  - `src/main.go` (gated `const runM2Probe = true`): spawn N goroutines × 100 × `time.Sleep(10ms)`; emit `m2_probe: PASS count=N` on success
  - `scripts/test_smp_m2_timer.sh`: boot `-smp 4`, grep serial for `m2_probe: PASS`
  - Verify: `bash scripts/test_smp_m2_timer.sh` PASSes
  - Commit: `test(smp): add M2 timer probe harness`

## M4 — AP Ring-3 iretq triple-fault investigation + fix (per `impldoc/smp_m4_ring3_fault.md`)

- [ ] **M4-investigation. QEMU + GDB evidence-capture pass**
  - Temporarily enable `stealWork()` call in `scheduler_cooperative.go:247-254`
  - Rebuild; boot `-smp 4` with `-s -S -d int,cpu_reset,guest_errors -D tmp/m4_qemu.log`
  - GDB session per `smp_m4_ring3_fault.md §3.2`: breakpoints, register dumps
  - Work hypothesis table (a-e) per `§4`; capture evidence per `§4.1` to `tmp/m4_evidence_*.txt`
  - Revert the stealWork repro-enable edit (tree returns to Wave 1 safe state)
  - Identify confirmed hypothesis; surface to user if ambiguous (stop condition per `hoge.md §12`)
  - Verify: evidence file committed; Wave 1 build still clean
  - Commit (evidence): `test(smp): M4 investigation evidence capture`

- [ ] **M4-fix. Apply fix per confirmed hypothesis**
  - Code edit at the fix site named by the confirmed hypothesis row
  - Verify: `make build` clean; **reproducer now passes** — with stealWork enabled, shell boots under `-smp 4` without triple-faulting; `scripts/test_sendkey.sh 1` PASSes under `-smp 4`; mark `impldoc/smp_deferred_and_known_issues.md §2.1` Resolved
  - Commit: `fix(smp): AP Ring-3 iretq <root cause>`

## M3 — scheduler=cores promotion + stealWork wire-up (per `impldoc/smp_m3_cores_promotion.md`)

- [ ] **M3-1. Wave 2 runtime declarations (`numCPU`, `gooosSpinLock`, lock vars, `currentCPU`, `gcPauseCore` stub)**
  - Patched `~/.local/tinygo0.40.1/src/runtime/runtime_gooos.go`: add declarations per `smp_m3_cores_promotion.md §4.1`
  - Regenerate `scripts/tinygo_runtime.patch`
  - Verify: `make build` still clean (declarations unused until M3-4); `grep 'numCPU = 17\|atomicsLock\|futexLock' runtime_gooos.go` all present
  - Commit: `build(toolchain): Wave 2 runtime declarations for scheduler=cores`

- [ ] **M3-2. `task_stack_amd64.go` build-tag widening + `runtime_systemStackPtr` linkname**
  - Widen build tag to `(scheduler.tasks || scheduler.cores) && amd64 && !windows`
  - Retire gooos per-CPU `systemStacks` array; consume upstream `systemStackPtr()` via linkname (mirror `task_stack_tinygoriscv.go:12-13`)
  - Rewrite `resume()` / `pause()` / `SystemStack()` per `§4.2`
  - Regenerate patch
  - Verify: `make build` clean under tasks mode (still); `scripts/test_tcp_phase5.sh` PASS
  - Commit: `build(toolchain): widen task_stack_amd64.go build tag; consume systemStackPtr linkname`

- [ ] **M3-3. `scheduler_cores.go` push-site retargeting + `stealWork` + `apScheduler`**
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

- [ ] **M3-4. Flip `src/target.json` `"tasks"` → `"cores"`**
  - Single-line edit; first commit where cores mode actually activates
  - Verify: `make build` clean under cores mode; `make run-smp` boots to shell (stealWork still dormant per M3-6); atomics smoke OK (boot doesn't hang); `scripts/test_net.sh` + `test_tcp_phase5.sh` PASS
  - Commit: `build(target): flip scheduler to cores`

- [ ] **M3-5. `scripts/patch_tinygo_runtime.sh` Wave 2 post-conditions**
  - Add grep probes: `numCPU = 17`, `atomicsLock`, `futexLock`, `runqueues` in `scheduler_cores.go`, build-tag widening in `task_stack_amd64.go`
  - Verify: re-running the script on already-patched tree prints `already-applied:` with the new checks
  - Commit: `build(toolchain): patch script Wave 2 post-conditions`

- [ ] **M3-6. Wire `stealWork()` into scheduler pop site**
  - In patched `scheduler_cores.go`, replace the "stealWork NOT called" comment block with the active `if t == nil { t = stealWork() }` call (mirrors what Wave 1 commit `d0cba8e` disabled in the cooperative file)
  - Regenerate patch
  - Verify: `make run-smp` shell still reachable (requires M4 resolved); existing Ring-3 harnesses PASS under `-smp 4`
  - Commit: `fix(smp): wire stealWork call into scheduler_cores pop site`

- [ ] **M3-7. `scripts/test_smp_basic.sh` + boot-time probe**
  - `src/main.go` (gated `const runSmpBasicProbe = true`): spawn N kernel goroutines, each prints cpuID; assert ≥2 distinct
  - `scripts/test_smp_basic.sh`: boot `-smp 4`, grep serial for `smp_basic: PASS distinct=N`
  - Verify: `bash scripts/test_smp_basic.sh` PASSes
  - Commit: `test(smp): add M3 basic distribution harness`

## Closing: README + doc updates + reviewer pass + final audit

- [ ] **C-1. Update `impldoc/smp_deferred_and_known_issues.md` §2.1/§2.2/§5**
  - Mark resolved items with "Resolved <date>, commit <hash>" banner per `smp_unblock_readme_update_plan.md §4`
  - Flip §5 "work stealing" row from Dormant to Done
  - Commit: `docs(smp): mark M2/M4 resolved + update work-stealing row`

- [ ] **C-2. Update `current_impl_doc/scheduler.md` SMP-v2 paragraph**
  - Third-pass rewrite per `smp_unblock_readme_update_plan.md §3`
  - Commit: `docs(impl): refresh as-built scheduler.md for cores-mode + live stealWork`

- [ ] **C-3. Update `README.md` progress table + known-limitations**
  - Apply the applicable scheduler-row variant per `smp_unblock_readme_update_plan.md §2.3`
  - Update Scheduler row `§2.2`; remove/rewrite Known-limitations bullet `§2.5`
  - Audit greps per `§2.8`
  - Commit: `docs(README): multi-core SMP scheduling live`

- [ ] **C-4. Update `TODO_SMP3.md` tick M2/M3/M4 + trim Deferred tail**
  - Remove `~~…~~` strike-through; flip `[ ]` → `[x]`; append commit hashes; trim rationale paragraphs
  - Commit: `docs(smp): tick M2/M3/M4 in TODO_SMP3 after unblock landing`

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
