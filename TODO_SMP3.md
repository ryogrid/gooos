# TODO — SMP Migration: TinyGo 0.33.0 → 0.40.1

Design sources: `impldoc/smp_migration_overview.md` and sibling `impldoc/smp_*`/`impldoc/tinygo_0_40_1_assessment.md`/`impldoc/toolchain_switch_plan.md`/`impldoc/runtime_patches.md`/`impldoc/readme_update_plan.md`/`impldoc/rollback_plan.md`/`impldoc/smp_milestones_and_verification.md`.

One git commit per top-level item. Mark `- [x]` only when the commit lands AND the listed verification passes.

## Environment verified

- [x] TinyGo 0.40.1 installed at `~/.local/tinygo0.40.1/` (LLVM 20.1.1, go1.22.2)
- [x] TinyGo 0.33.0 fallback still at `~/.local/tinygo/` (LLVM 18.1.2) — baseline `make build` green

## M0 — Wave 1: Toolchain switch + patch rebase (tasks mode)

- [ ] **W1-1. Makefile: point `TINYGOROOT` at `~/.local/tinygo0.40.1`**
  - `Makefile:13` — `TINYGOROOT ?= $(HOME)/.local/tinygo0.40.1`
  - `Makefile:8-12` — update the leading comment block to mention 0.40.1
  - Verify: `grep -n 'tinygo0.40.1' Makefile` returns the two lines; `make build` still uses the patched tree
  - Commit: `build(toolchain): point TINYGOROOT at ~/.local/tinygo0.40.1`

- [ ] **W1-2. `scripts/patch_tinygo_runtime.sh` targets 0.40.1 tree (+ dual-version fallback)**
  - Line 31 default: `TINYGO_SRC="${TINYGO_SRC:-$HOME/.local/tinygo0.40.1/src}"`
  - Add the dual-version detection block from `impldoc/toolchain_switch_plan.md §2.2`
  - Verify: `bash scripts/patch_tinygo_runtime.sh` with no args on an unpatched `~/.local/tinygo0.40.1/src/` prints the patch-install message or, if still unpatched, proceeds to apply
  - Commit: `build(toolchain): patch script targets 0.40.1 tree with dual-version fallback`

- [ ] **W1-3. Regenerate `scripts/tinygo_runtime.patch` for 0.40.1 tasks mode**
  - Apply existing 0.33.0 patch to a fresh `~/.local/tinygo0.40.1/src/`; resolve rejections manually per `impldoc/runtime_patches.md §3`
  - Key relocations: `scheduler.go` hunks may need to split between `scheduler.go` / `scheduler_cooperative.go` / `scheduler_tasks.go`
  - Regenerate patch via `git diff` inside the 0.40.1 source tree
  - Verify: `patch --dry-run -p1 -d ~/.local/tinygo0.40.1 < scripts/tinygo_runtime.patch` reports clean apply; second apply prints `already-applied:`
  - Commit: `build(toolchain): regenerate tinygo_runtime.patch for 0.40.1 (tasks mode)`

- [ ] **W1-4. Update `scripts/patch_tinygo_runtime.sh` idempotency post-conditions for 0.40.1**
  - Lines 57–69: change `SCHED=$TINYGO_SRC/runtime/scheduler.go` or split across cooperative/tasks files per actual patch targets
  - Lines 96–143: refresh file-list comments for 0.40.1 paths
  - Lines 148–176: refresh trailing heredoc
  - Verify: re-running `bash scripts/patch_tinygo_runtime.sh` on an already-patched 0.40.1 tree prints `already-applied:`
  - Commit: `build(toolchain): patch script post-conditions for 0.40.1`

- [ ] **W1-5. README Wave 1 edits (toolchain setup section)**
  - Per `impldoc/readme_update_plan.md §Wave 1`: update TinyGo version line (0.33.0 → 0.40.1, LLVM 18.1.2 → LLVM 20.1.1), toolchain path (`~/.local/tinygo/` → `~/.local/tinygo0.40.1/`), "pristine TinyGo 0.33.0 tree" phrase, patched-files bullet list (scheduler.go → scheduler_cooperative.go), Reverting bash block, sleepTicks docs reference
  - Grep-replace rules, not absolute line numbers (file shifts after each edit)
  - Verify: `grep -n '0\.33\.0' README.md` returns 0 hits (outside historical notes); `grep -n '\.local/tinygo/' README.md` returns 0 hits (only `/tinygo0.40.1/` remains)
  - Commit: `docs(README): TinyGo 0.40.1 toolchain setup`

## M0 — Exit gate: single-core parity

- [ ] **M0-EXIT. `make build` + lint + verify-globals + regression harnesses green on 0.40.1**
  - `make clean && make build` — clean
  - `make lint` — clean
  - `make verify-globals` — clean
  - `bash scripts/test_net.sh` — PASS
  - `bash scripts/test_tcp_phase1.sh` — PASS
  - `bash scripts/test_tcp_phase2.sh` — PASS
  - `bash scripts/test_tcp_phase3.sh` — PASS
  - `bash scripts/test_tcp_phase4.sh` — PASS
  - `bash scripts/test_tcp_phase5.sh` — PASS
  - Commit (if any follow-up fixes): `test(smp): M0 exit — single-core parity on 0.40.1`, else tick without commit when no fix needed

## M1 — Exit gate: APs boot and idle on 0.40.1

- [ ] **M1-EXIT. `-smp 4` boots to shell under `make run-smp`**
  - `make run-smp` (bounded run — boot-capture via `tmp/smp_boot_log.txt` with a timeout)
  - Serial log: `"BSP cpuID=0"`, `"AP 0 cpuID=1"`, `"AP 1 cpuID=2"`, `"AP 2 cpuID=3"` all present
  - Shell prompt `$ ` reached
  - No kernel triple-fault / panic in log
  - Commit: `test(smp): M1 exit — -smp 4 boot verified on 0.40.1`

## M2 — AP LAPIC timer race fix (attempted if time permits; deferrable)

- [ ] **M2-1. Migrate `interrupt.In()` to read per-CPU `%gs:4` counter only**
  - Remove dual-counter logic in `src/isr.S` prologue/epilogue
  - Update patched `runtime_gooos.go` `interruptIn()` body (or its linkname target) to read per-CPU counter only
  - Verify: `make build` clean; no "blocked inside interrupt" panics under `make run-smp`
  - Commit: `fix(smp): migrate interrupt.In() to per-CPU counter only`

- [ ] **M2-2. Enable AP LAPIC timer init**
  - Un-gate `lapicTimerInitAP()` in `src/main.go`
  - Verify: serial shows `"LAPIC timer: N ticks/10ms"` and per-AP tick markers under `-smp 4`
  - Existing regression harnesses still PASS under `-smp 4`
  - Commit: `fix(smp): enable AP LAPIC timer at 100Hz`

## M3 — Wave 2: scheduler=cores promotion

- [ ] **W2-1. Flip `src/target.json` `"scheduler": "tasks"` → `"cores"`**
  - `src/target.json:9`
  - Verify: `make build` attempts to link against cores-mode runtime; expected failure until W2-2 variables land
  - Commit: `build(target): flip scheduler to cores (blocks until W2-2 lands)`

- [ ] **W2-2. Add Wave 2 patch hunks (numCPU, spinlock variables, gcPauseCore, currentCPU)**
  - In patched `runtime_gooos.go`:
    - `const numCPU = 17`
    - `type gooosSpinLock struct { locked uint32 }`, methods `Lock()` / `Unlock()`
    - `var atomicsLock, schedulerLock, futexLock, printLock gooosSpinLock`
    - `currentCPU()` linkname body → `cpuID()`
    - `gcPauseCore(cpu uint32)` stub body (returns immediately for M3; real impl at M5)
  - In patched `task_stack_amd64.go`:
    - Widen build tag to `(scheduler.tasks || scheduler.cores) && amd64 && !windows`
    - Import `runtime_systemStackPtr` via linkname mirroring `task_stack_tinygoriscv.go:12-13`
    - Rewrite `resume()` / `pause()` / `SystemStack()` to consume `runtime_systemStackPtr()`
  - In patched `scheduler_cores.go`:
    - Add `var runqueues [numCPU]task.Queue`
    - Add `func stealWork(self uint32) *task.Task` (round-robin peer PopTail)
    - Add `func apScheduler()` entry point
    - Retarget `scheduleTask` (line 43) and `Gosched` (line 89) push sites to `runqueues[gooosCpuID()].Push(...)`
    - Adjust `scheduler()` pop site to drain `runqueues[gooosCpuID()]` + call `stealWork` on empty
  - Regenerate patch file via `git diff`
  - Verify: `make build` clean under `scheduler=cores`; smoke: boot under `-smp 4` does not hang before BSP shell
  - Commit: `build(toolchain): Wave 2 patch additions (numCPU, spinlocks, per-CPU runqueues, gcPauseCore stub)`

- [ ] **W2-3. Add `scripts/patch_tinygo_runtime.sh` Wave 2 post-conditions**
  - Grep probes: `numCPU = 17`, `atomicsLock`, `futexLock`, `gcPauseCore`, `runqueues` in `scheduler_cores.go`, build-tag widening in `task_stack_amd64.go`
  - Verify: re-applying patch to already-patched 0.40.1 prints `already-applied:`
  - Commit: `build(toolchain): patch script Wave 2 post-conditions`

## M3 — Exit gate: kernel goroutines on multiple CPUs

- [ ] **M3-EXIT-1. New harness `scripts/test_smp_basic.sh` — kernel goroutine distribution**
  - Boot-time probe in `src/main.go` (gated by `const runSmpProbe = true`): spawns N goroutines that each print their cpuID + increment a shared counter; expects ≥2 distinct cpuIDs
  - Harness greps serial log for `smp_probe: PASS` + counts distinct `cpuID=N` markers
  - Verify: `bash scripts/test_smp_basic.sh` exits 0
  - Commit: `test(smp): add test_smp_basic.sh — kernel goroutine distribution`

- [ ] **M3-EXIT-2. Regression matrix green under `-smp 4`**
  - Wrapper `scripts/test_smp_matrix.sh` (or inline `SMP=4` env var) reruns existing harnesses under `-smp 4`
  - `test_net.sh`, `test_tcp_phase{1..5}.sh`, `test_gochan.sh`, `test_goprobe.sh` all PASS
  - Commit: `test(smp): M3 regression matrix green under -smp 4`

## M4 — Ring-3 on APs (may defer if QEMU+GDB session cannot localise)

- [ ] **M4. Debug AP `iretq` triple-fault (per `impldoc/smp_deferred_and_known_issues.md §2.1`)**
  - One diagnostic pass with QEMU + GDB; if root cause not localised in-session, record findings and defer
  - If resolved: harness `scripts/test_smp_ring3.sh` (wraps `smpprobe.elf` under `-smp 4`) reports Ring-3 workers on ≥2 distinct cpuIDs
  - Commit (if resolved): `fix(smp): AP Ring-3 iretq <root cause>`

## M5 — SMP-safe GC (depends on M3; may defer)

- [ ] **M5. Real `gcPauseCore(cpu)` body + IPI handler**
  - Patched `runtime_gooos.go`: replace stub with IPI-send + per-CPU ack-flag spin
  - New `src/gc_ipi.go` (or extension to `src/smp.go`): `vectorGCPause = 0xFB`, handler sets `perCPUBlocks[cpu].gcPaused = 1` and spins until released
  - Extend `PerCPU` struct (`src/percpu.go`) with `gcPaused uint32` field
  - Harness `scripts/test_smp_gc_stress.sh`: allocation stress under `-smp 4`; verify no heap corruption after 30s
  - Commit: `fix(smp): gcPauseCore IPI for stop-the-world GC under -smp N`

## Closing: README Wave 2 + doc updates + reviewer pass + final audit

- [ ] **C-1. README Wave 2 edits (scheduler + SMP progress rows + known-limitations)**
  - Per `impldoc/readme_update_plan.md §Wave 2`: project tagline scheduler mention, Scheduler row, SMP progress row (reflect milestones actually landed), Known limitations updates, SMP verification section
  - Commit: `docs(README): SMP multi-core scheduling status`

- [ ] **C-2. `current_impl_doc/` updates**
  - `current_impl_doc/scheduler.md` — document cores-mode scheduler, per-CPU runqueues, work stealing
  - `current_impl_doc/known_issues.md` — remove resolved items; update status of deferred
  - Commit: `docs(impl): update as-built docs for SMP migration`

- [ ] **C-3. `impldoc/smp_deferred_and_known_issues.md` update**
  - Mark resolved items (per milestones landed); retain deferred items with current status
  - Commit: `docs(smp): update deferred/known issues post-migration`

- [ ] **C-4. Reviewer pass + CRITICAL/MAJOR fix-in**
  - Spawn `general-purpose` subagent with the review brief from `hoge.md §8`
  - Fix CRITICAL + MAJOR inline; record MINOR in `TODO_SMP3.md` Reviewer findings tail
  - Commit(s): per finding, `docs(review): incorporate SMP migration reviewer <finding>`

- [ ] **C-5. Final completeness audit**
  - `grep -rnE 'TODO|FIXME|XXX' src/ user/ scripts/ impldoc/` — diff vs. pre-session baseline; no new markers
  - Patch re-apply idempotency: fresh 0.40.1 tree → apply → apply (expect `already-applied:`)
  - `git status --porcelain` clean
  - Every checked TODO_SMP3.md item has exactly one landing commit in `git log --oneline`
  - No commit needed — gate only

## Deferred further

(Filled mid-task as deferrals arise.)

## Reviewer findings

(Filled after the reviewer pass.)
