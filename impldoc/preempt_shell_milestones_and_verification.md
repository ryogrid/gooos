# Preempt + Shell Batch — Unified Milestones & Verification

## Scope

Unified Entry/Exit gates and verification matrix across the five features in this batch: 2.1 (kernel-goroutine preemption), 2.2 (user-goroutine preemption), 2.3 (multi-core shell scheduling + preempt verification), 2.4 (shell `&` + `sys_waitpid`), 2.5 (`ps` + `sys_listprocs`). Each per-feature design doc also carries Entry/Exit in its own §5/§6; this doc is the integration view.

Extends, does not replace, the existing `impldoc/smp_unblock_milestones_and_verification.md` (which covered the 0.40.1 migration + work-stealing batch).

## Cross-links

- `preempt_shell_overview.md` — dependency DAG + Design Decisions.
- Per-feature: `preempt_kernel_goroutines.md`, `preempt_user_goroutines.md`, `shell_multicore_preempt.md`, `shell_background_jobs.md`, `shell_ps_command.md`.
- Prior batch: `impldoc/smp_unblock_milestones_and_verification.md`.
- README-update plan: `preempt_shell_readme_update_plan.md`.

## 1. Dependency DAG (brief)

```
          2.1 kernel preempt
          /              \
         v                v
   2.3 shell multi-core   2.2 user preempt
     (anti-starvation)      (weakly depends on 2.1 for composition)
         |                       |
         +---- 2.3 sub-gate (c) -+  (orthogonal probe for 2.2)

   2.4 shell &       2.5 ps
      (independent)      (independent)
```

Full DAG in `preempt_shell_overview.md §3`.

## 2. Per-feature Entry / Exit gates

### 2.1 Kernel goroutine preemption

- **Entry.** `smp-take4` HEAD (scheduler=cores + stealWork live at `aa5bb91`); M4 Ring-3 fault-fix in history (`5aea173`); existing regression matrix green under `-smp 1` and `-smp 4`.
- **Exit.** `scripts/test_preempt_kernel.sh` PASS under `-smp 1` and `-smp 4` (≥ 5 markers in 5 s). `grep -n 'preemptEnabled' src/preempt_config.go` shows `true`. No `blocked inside interrupt` panic. Full regression matrix remains green.

### 2.2 User goroutine preemption

- **Entry.** 2.1 landed OR independent gate `userPreemptEnabled` added. Patched TinyGo runtime rebuilt against latest `scripts/tinygo_runtime.patch`.
- **Exit.** `scripts/test_preempt_user.sh` PASS under `-smp 1` (≥ 5 markers in 5 s from a `for{}`-sibling goroutine). No Ring-3 triple-fault. Full regression matrix green.

### 2.3 Multi-core shell scheduling + preempt verification

- **Entry.** (a) requires nothing new (baseline `aa5bb91`). (b) requires 2.1 landed. (c) requires 2.2 landed. Harness can ship with partial gate coverage, documenting which sub-gates are skipped.
- **Exit.** `scripts/test_smp_shell_distribution.sh` PASS (sub-gate a). `scripts/test_smp_shell_preempt.sh` PASS under both `-smp 1` and `-smp 4` (sub-gate b). `scripts/test_preempt_user.sh` PASS (sub-gate c). No panics.

### 2.4 Shell `&` + `sys_waitpid`

- **Entry.** `smp-take4` HEAD. No dependency on 2.1/2.2/2.5.
- **Exit.** `scripts/test_shell_background.sh` PASS under `-smp 1` and `-smp 4`. Interactive: `hello &` prompt returns immediately + completion line fires within 1–2 s; `hello && ls` rejects as syntax error. 17th concurrent `&` reverts to foreground gracefully. Pipeline-`&` (`ls | wc &`) backgrounds the whole pipeline, one completion line per stage.

### 2.5 `ps` + `sys_listprocs`

- **Entry.** `smp-take4` HEAD. No dependency on 2.1/2.2/2.4.
- **Exit.** `scripts/test_ps.sh` PASS under `-smp 1` and `-smp 4`. `ps` shows header + ≥ 1 row. `unsafe.Sizeof(ProcInfo{}) == 64` at build time. Full regression matrix green.

## 3. QEMU invocation matrix

Every harness must be run under **both** `-smp 1` and `-smp 4`. Specific configurations:

| SMP count | Rationale |
| --- | --- |
| `-smp 1` | Baseline cooperative + new mechanism; no AP scheduler in the mix. Sub-gate (b) of 2.3 is *harder* here (single core = forced sharing), making it the primary anti-starvation test. |
| `-smp 4` | Production-target. All features under their intended concurrency. Distribution sub-gate (a) of 2.3 is primary here. |
| `-smp 8` | Spot-check — run once after 2.1 lands to confirm no "only works at 4 CPUs" regression. Not required per-commit. |
| `-smp 16` (maxCPUs) | End-of-batch sanity only. Most harnesses skip; a single boot-to-shell + `smpprobe` pass suffices. |

Invocation pattern (per `Makefile:124-125`): `make run-smp` sets `-smp 4`; override via `make run SMP=N` if the Makefile supports it (else manual `-smp N` QEMU flag).

## 4. Harness list

All new scripts under `/home/ryo/work/gooos/scripts/`:

| Harness | Feature | Run-under | Pass criterion |
| --- | --- | --- | --- |
| `test_preempt_kernel.sh` | 2.1 | `-smp 1`, `-smp 4` | ≥ 5 markers in 5 s from a starved kernel goroutine. |
| `test_preempt_user.sh` | 2.2, 2.3 sub-gate (c) | `-smp 1` | ≥ 5 markers in 5 s from a starved user goroutine. |
| `test_smp_shell_distribution.sh` | 2.3 sub-gate (a) | `-smp 4` | ≥ 2 distinct cpuIDs across shell-spawned workers. |
| `test_smp_shell_preempt.sh` | 2.3 sub-gate (b) | `-smp 1`, `-smp 4` | ≥ 5 markers in 3 s while a sibling process runs `cpuhog`. |
| `test_shell_background.sh` | 2.4 | `-smp 1`, `-smp 4` | `&` spawns returns prompt immediately; completion line observed within 3 s; `&&` rejected. |
| `test_ps.sh` | 2.5 | `-smp 1`, `-smp 4` | `ps` output has header + ≥ 1 row; row includes shell; no panics. |

All harnesses must follow the bounded-polling pattern per `workflow_bounded_polling.md`:

```bash
for i in $(seq 1 N); do
    if grep -q 'EXPECTED_PATTERN' "$LOG"; then break; fi
    sleep 1
done
```

Never use unbounded `while`/`until` loops (per `feedback_background_bash.md`).

## 5. Regression matrix (must stay green)

After each commit in each feature's per-doc commit-per-edit plan AND at the end of the batch, these existing harnesses must PASS under `-smp 1` and (where they are SMP-aware) `-smp 4`:

| Harness | `-smp 1` | `-smp 4` | Coverage |
| --- | --- | --- | --- |
| `scripts/test_net.sh` | ✓ | ✓ | UDP/DHCP baseline. |
| `scripts/test_tcp_phase1.sh` | ✓ | ✓ | TCP state machine basics. |
| `scripts/test_tcp_phase2.sh` | ✓ | ✓ | TCP timers/RTT. |
| `scripts/test_tcp_phase3.sh` | ✓ | ✓ | TCP flow control. |
| `scripts/test_tcp_phase4.sh` | ✓ | ✓ | TCP congestion control. |
| `scripts/test_tcp_phase5.sh` | ✓ | ✓ | TCP full socket API. |
| `scripts/test_gochan.sh` | ✓ | ✓ | Channel pipelines. |
| `scripts/test_pipe_matrix.sh` | ✓ | ✓ | Shell pipeline IO. |
| `scripts/test_smp_basic.sh` | skip | ✓ | Kernel goroutine distribution. |
| `scripts/test_goprobe.sh` | ✓ | ✓ | Channel + goroutine smoke. |
| `scripts/test_sendkey.sh` | ✓ | ✓ | QEMU sendkey driver (shell smoke). |

An in-progress feature that breaks any of these is a stop condition (see `preempt_shell_overview.md §Stop conditions`).

## 6. End-of-batch audit

Run after the last `docs(impldoc): fold reviewer findings` commit (or after commit 8+ in §7 of each per-feature doc, if review pass is clean):

- `git status -- src/ user/ scripts/ Makefile` — clean; no stale artifacts.
- `git log --oneline master..HEAD | wc -l` — matches expected commit count (sum of §3-§5 commit counts across the 5 feature docs + this doc set's 8 `docs(impldoc)` commits + any `fold reviewer findings` commit).
- `make build && make lint && make verify-globals` — clean.
- `scripts/patch_tinygo_runtime.sh` — re-applies cleanly to a fresh `~/.local/tinygo0.40.1/` tree; second invocation prints `already-applied:`.
- Full regression matrix PASS under `-smp 1` and `-smp 4`.
- Harness-list from §4 — every harness PASS at the `-smp N` configurations listed.
- `grep -rnE 'TODO|FIXME|XXX' src/ user/ scripts/ impldoc/ | diff - <baseline>` — no NEW markers introduced by this batch.

## 7. Interaction with prior SMP work

This batch operates on top of the SMP unblock batch (`impldoc/smp_unblock_*`). Two specific interactions:

### 7.1 M2 partial status

`impldoc/smp_deferred_and_known_issues.md §2.2` documents the AP LAPIC timer deferral. 2.1 explicitly **does not** land the AP timer (kept in the `## Future: per-CPU AP timer` section of `preempt_kernel_goroutines.md`). If the implementation session later decides to land the AP timer, that is a separate milestone and requires its own Entry/Exit gates — this batch's verification does not cover AP-timer correctness.

### 7.2 M4 Ring-3 fault fix

Commit `5aea173` fixed the AP Ring-3 `iretq` triple-fault. 2.2's mechanism B rewrites the `iretq` frame in-place at the end of syscall dispatch. The two must be tested together:

- `scripts/test_smp_ring3.sh` (landed at M4) must remain PASS after 2.2 lands.
- A reviewer audit (bullet (c)/(h) of `preempt_shell_overview.md §Reviewer brief`) explicitly verifies that 2.2's frame-rewrite does not regress M4's invariants.

## 8. Stop conditions

Per-feature stop conditions are in each feature doc's §12 (Risks). Batch-level:

- Any existing regression harness stops PASSing after a feature commit lands — revert the offending commit, diagnose, do not "fix forward".
- QEMU triple-faults / kernel panics under any `-smp N` after a feature commit — capture the serial log, run one QEMU + GDB diagnostic pass, then ask.
- `scripts/patch_tinygo_runtime.sh` fails to re-apply idempotently on a freshly-unpacked TinyGo tree — patch regen required before any further commits in the affected feature.

## 9. Deliverables

Nothing in this doc — it is verification and gate documentation only. The actual deliverables are listed in each feature doc's §Deliverables section.

## Reviewer MINOR notes

*Populated during the mandatory reviewer pass.*
