# TODO — SMP Migration: TinyGo 0.33.0 → 0.40.1

Design sources: `impldoc/smp_migration_overview.md` and sibling `impldoc/smp_*`/`impldoc/tinygo_0_40_1_assessment.md`/`impldoc/toolchain_switch_plan.md`/`impldoc/runtime_patches.md`/`impldoc/readme_update_plan.md`/`impldoc/rollback_plan.md`/`impldoc/smp_milestones_and_verification.md`.

One git commit per top-level item. Mark `- [x]` only when the commit lands AND the listed verification passes.

## Environment verified

- [x] TinyGo 0.40.1 installed at `~/.local/tinygo0.40.1/` (LLVM 20.1.1, go1.22.2)
- [x] TinyGo 0.33.0 fallback still at `~/.local/tinygo/` (LLVM 18.1.2) — baseline `make build` green

## M0 — Wave 1: Toolchain switch + patch rebase (tasks mode)

- [x] **W1-1. Makefile: point `TINYGOROOT` at `~/.local/tinygo0.40.1`**
  - `Makefile:13` — `TINYGOROOT ?= $(HOME)/.local/tinygo0.40.1`
  - `Makefile:8-12` — update the leading comment block to mention 0.40.1
  - Verify: `grep -n 'tinygo0.40.1' Makefile` returns the two lines; `make build` still uses the patched tree
  - Commit: `build(toolchain): point TINYGOROOT at ~/.local/tinygo0.40.1`

- [x] **W1-2. `scripts/patch_tinygo_runtime.sh` targets 0.40.1 tree (+ dual-version fallback)**
  - Line 31 default: `TINYGO_SRC="${TINYGO_SRC:-$HOME/.local/tinygo0.40.1/src}"`
  - Add the dual-version detection block from `impldoc/toolchain_switch_plan.md §2.2`
  - Verify: `bash scripts/patch_tinygo_runtime.sh` with no args on an unpatched `~/.local/tinygo0.40.1/src/` prints the patch-install message or, if still unpatched, proceeds to apply
  - Commit: `build(toolchain): patch script targets 0.40.1 tree with dual-version fallback`

- [x] **W1-3. Regenerate `scripts/tinygo_runtime.patch` for 0.40.1 tasks mode**
  - Apply existing 0.33.0 patch to a fresh `~/.local/tinygo0.40.1/src/`; resolve rejections manually per `impldoc/runtime_patches.md §3`
  - Key relocations: `scheduler.go` hunks may need to split between `scheduler.go` / `scheduler_cooperative.go` / `scheduler_tasks.go`
  - Regenerate patch via `git diff` inside the 0.40.1 source tree
  - Verify: `patch --dry-run -p1 -d ~/.local/tinygo0.40.1 < scripts/tinygo_runtime.patch` reports clean apply; second apply prints `already-applied:`
  - Commit: `build(toolchain): regenerate tinygo_runtime.patch for 0.40.1 (tasks mode)`

- [x] **W1-4. Update `scripts/patch_tinygo_runtime.sh` idempotency post-conditions for 0.40.1**
  - Lines 57–69: change `SCHED=$TINYGO_SRC/runtime/scheduler.go` or split across cooperative/tasks files per actual patch targets
  - Lines 96–143: refresh file-list comments for 0.40.1 paths
  - Lines 148–176: refresh trailing heredoc
  - Verify: re-running `bash scripts/patch_tinygo_runtime.sh` on an already-patched 0.40.1 tree prints `already-applied:`
  - Commit: `build(toolchain): patch script post-conditions for 0.40.1`

- [x] **W1-5. README Wave 1 edits (toolchain setup section)**
  - Per `impldoc/readme_update_plan.md §Wave 1`: update TinyGo version line (0.33.0 → 0.40.1, LLVM 18.1.2 → LLVM 20.1.1), toolchain path (`~/.local/tinygo/` → `~/.local/tinygo0.40.1/`), "pristine TinyGo 0.33.0 tree" phrase, patched-files bullet list (scheduler.go → scheduler_cooperative.go), Reverting bash block, sleepTicks docs reference
  - Grep-replace rules, not absolute line numbers (file shifts after each edit)
  - Verify: `grep -n '0\.33\.0' README.md` returns 0 hits (outside historical notes); `grep -n '\.local/tinygo/' README.md` returns 0 hits (only `/tinygo0.40.1/` remains)
  - Commit: `docs(README): TinyGo 0.40.1 toolchain setup`

## M0 — Exit gate: single-core parity

- [x] **M0-EXIT. `make build` + lint + verify-globals + regression harnesses green on 0.40.1**
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

- [x] **M1-EXIT. `-smp 4` boots to shell under `make run-smp`**
  - `make run-smp` (bounded run — boot-capture via `tmp/smp_boot_log.txt` with a timeout)
  - Serial log: `"BSP cpuID=0"`, `"AP 0 cpuID=1"`, `"AP 1 cpuID=2"`, `"AP 2 cpuID=3"` all present
  - Shell prompt `$ ` reached
  - No kernel triple-fault / panic in log
  - Commit: `test(smp): M1 exit — -smp 4 boot verified on 0.40.1`

## M2 — AP LAPIC timer race fix — **DEFERRED**

Rationale: `impldoc/smp_deferred_and_known_issues.md §2.2` documents
that migrating `interrupt.In()` to the per-CPU counter exposes a
second issue — gooos's ISR-hosted syscall design calls `task.Pause()`
while the per-CPU ISR depth is 1 (correct for that design), which
panics as "blocked inside interrupt". Resolving requires a redesign
of the syscall-handler / ISR-depth accounting scheme that is beyond
the scope of this toolchain migration. Listed here as a pointer; the
full design work tracks under the original SMP v2 known-issues.

- [ ] ~~**M2-1. Migrate `interrupt.In()` to read per-CPU `%gs:4` counter only**~~ (deferred)
- [ ] ~~**M2-2. Enable AP LAPIC timer init**~~ (deferred — depends on M2-1)

## M3 — Wave 2 (scheduler=cores promotion) — **DEFERRED**

Rationale: Promoting to `scheduler=cores` requires enabling work
stealing (`stealWork()` call in the scheduler loop), but enabling
stealWork under gooos triggers the M4 AP Ring-3 `iretq` triple-fault
captured in `impldoc/smp_deferred_and_known_issues.md §2.1` because
APs would immediately steal `ring3Wrapper` goroutines — reproduced
during M1 verification in this session. Landing M3 cleanly therefore
depends on M4 being resolved first (or on a stealWork-kernel-only
affinity mechanism that excludes `ring3Wrapper` from stealing — a
non-trivial addition). The Wave 1 patch already ships the dormant
infrastructure (`stealWork`, `apScheduler`) so a future M3 commit
only needs to wire the call site + add the cores-mode bits.

- [ ] ~~**W2-1. Flip `src/target.json` `"scheduler": "tasks"` → `"cores"`**~~ (deferred)
- [ ] ~~**W2-2. Add Wave 2 patch hunks (numCPU, spinlock variables, gcPauseCore, currentCPU)**~~ (deferred)
- [ ] ~~**W2-3. Add `scripts/patch_tinygo_runtime.sh` Wave 2 post-conditions**~~ (deferred)

- [ ] ~~**M3-EXIT-1. New harness `scripts/test_smp_basic.sh` — kernel goroutine distribution**~~ (deferred — depends on W2-2)
- [ ] ~~**M3-EXIT-2. Regression matrix green under `-smp 4`**~~ (deferred — depends on W2-2)

## M4 — Ring-3 on APs — **DEFERRED**

Rationale: per `impldoc/smp_deferred_and_known_issues.md §2.1`,
resolving the AP Ring-3 `iretq` triple-fault requires a QEMU+GDB
hardware-level debugging session — an intermittent fault tied to
per-CPU TSS / GDT / CR3 transitions that cannot reliably be root-
caused by code review alone. Out of scope for this toolchain
migration session.

- [ ] ~~**M4. Debug AP `iretq` triple-fault**~~ (deferred — QEMU+GDB work)

## M5 — SMP-safe GC — **DEFERRED**

Rationale: depends on M3 scheduler=cores landing (GC stop-the-world
only has value when APs actually run goroutines concurrently).
Deferred transitively.

- [ ] ~~**M5. Real `gcPauseCore(cpu)` body + IPI handler**~~ (deferred — depends on M3)

## Closing: README Wave 2 + doc updates + reviewer pass + final audit

- [x] **C-1. README Wave 2 edits (scheduler + SMP progress rows + known-limitations)**
  - Per `impldoc/readme_update_plan.md §Wave 2`: project tagline scheduler mention, Scheduler row, SMP progress row (reflect milestones actually landed), Known limitations updates, SMP verification section
  - Commit: `docs(README): SMP multi-core scheduling status`

- [x] **C-2. `current_impl_doc/` updates**
  - `current_impl_doc/scheduler.md` — document cores-mode scheduler, per-CPU runqueues, work stealing
  - `current_impl_doc/known_issues.md` — remove resolved items; update status of deferred
  - Commit: `docs(impl): update as-built docs for SMP migration`

- [x] **C-3. `impldoc/smp_deferred_and_known_issues.md` update**
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

1. **M2 (AP LAPIC timer race fix)** — `interrupt.In()` per-CPU migration
   conflicts with gooos's ISR-hosted syscall design (`task.Pause()` is
   called while per-CPU ISR depth is 1, panicking as "blocked inside
   interrupt"). Requires syscall-handler / ISR-depth accounting redesign;
   out of scope for this migration. See
   `impldoc/smp_deferred_and_known_issues.md §2.2`.

2. **M3 (scheduler=cores promotion, including W2-1/W2-2/W2-3 and
   M3-EXIT-1/2)** — enabling the `stealWork()` call at the scheduler's
   pop site triggers the M4 AP Ring-3 `iretq` triple-fault (reproduced
   in this session during M1 verification). Landing M3 cleanly depends
   on M4 first, or on adding kernel-only stealWork affinity. The Wave 1
   patch already ships dormant `stealWork` / `apScheduler` / per-CPU
   `runqueues[17]` infrastructure; future wire-up is a narrow change.

3. **M4 (AP Ring-3 iretq triple-fault)** — per
   `impldoc/smp_deferred_and_known_issues.md §2.1`, requires QEMU+GDB
   hardware-level debugging. Out of scope for this session.

4. **M5 (gcPauseCore IPI + stop-the-world GC)** — depends transitively
   on M3. Out of scope.

5. **User-mode promotion to `scheduler=cores`** — `user/target.json`
   stays on `scheduler=tasks`. Deliberate per
   `impldoc/runtime_patches.md §3.9`; user-mode cores promotion will
   happen in a follow-on effort after M5 lands.

6. **Dual-version patch-script fallback** — `scripts/patch_tinygo_runtime.sh`
   retains the 0.33.0 fallback branch with a deprecation warning. The
   plan is to drop this after M3 lands (per
   `impldoc/toolchain_switch_plan.md §2.2`); retained at Wave 1 as a
   transition grace period.

## Reviewer findings

`general-purpose` reviewer subagent ran against this branch on
2026-04-20 and executed the full verification matrix (patch apply ×2,
make build/lint/verify-globals, test_net.sh, test_tcp_phase1..5.sh,
`-smp 4` boot capture). All automated checks PASSed. Classification:

### CRITICAL

none.

### MAJOR (all resolved inline)

1. **Stale `heapLock` bullet in `README.md` toolchain-setup section.**
   Patch dropped heapLock at commit `b350d02`; README text lagged.
   Fixed: README now describes the current comment-only annotation
   near `gc_blocks.go` globals (documents BSP-only-allocates contract
   + M5 `gcPauseCore` plan).
2. **Stale SMP-v2 paragraph in `current_impl_doc/scheduler.md`**
   claimed live work-stealing + per-AP LAPIC timer, contradicting
   Wave 1's actual state (stealWork dormant, AP LAPIC timer disabled
   per M2 deferral). Fixed: paragraph rewritten to describe actual
   state with cross-links to `TODO_SMP3.md` M2/M3 and
   `impldoc/smp_deferred_and_known_issues.md §2.1/§2.2`.
3. **Stale `heapLock` comments inside the gc_blocks.go patch hunk.**
   Fixed: comment block rewritten to describe upstream `gcLock`
   (`task.PMutex`, no-op under `tinygo.unicore`) + BSP-only-allocates
   contract + M5 IPI plan; patch file regenerated.

### MINOR (resolved or accepted)

1. `impldoc/smp_deferred_and_known_issues.md §5` "GC stop-the-world"
   row cited `heapLock protects alloc` — rewrote to describe
   upstream `gcLock task.PMutex` + BSP-only-allocates contract.
2. `impldoc/smp_deferred_and_known_issues.md §5` "schedulerDone race"
   row cited a symbol that doesn't exist in 0.40.1 (`grep
   schedulerDone .../tinygo0.40.1/src/` → 0 matches). Removed the
   row.
3. Off-by-one line citations in `impldoc/tinygo_0_40_1_assessment.md`
   (scheduler_cores.go:13/22/281 vs. actual 12/21/281-291; §5.1 291
   vs. 292; `scheduler_cooperative.go:38-42` vs. 37-41) — accepted
   as "close enough" (reviewer notes they don't affect correctness);
   future lockstep sync if/when the assessment is re-verified.
4. `impldoc/smp_scheduler_design.md §4.4` `runtime_rp2.go:293-299`
   vs. actual 294-299 — accepted (off-by-one).
5. `current_impl_doc/scheduler.md:199` `goroutine_tss.go:77` vs.
   actual 80 — accepted (off-by-one).
6. Stale comment in `runtime_gooos.go:96-97` ("waitForEvents
   provided by wait_other.go (fallback)") — wait_other.go now
   excludes `&& !gooos`; comment kept as harmless historical note.
7. `queue.go Append` lock-rank on `q` then `other` — unreachable
   in current code; latent AB/BA concern deferred to M3 when any
   stealWork-like batch move lands.
8. `current_impl_doc/known_issues.md:256` historical `~/.local/tinygo/`
   reference kept as "Reviewer MINOR notes / Fixed:" context.
9. No new TODO/FIXME/XXX markers introduced by the session (verified
   via diff).
