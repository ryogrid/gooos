# Multi-core Shell Scheduling + Preemptive Switching (feature 2.3)

## Scope

Specify the integration-level verification that confirms shell-spawned commands (i) distribute across CPU cores concurrently and (ii) cannot starve each other under a hostile CPU-bound workload. The bulk of the mechanism falls out of features 2.1 (kernel preemption) and 2.2 (user-goroutine preemption); this feature adds the **harness + acceptance criteria** that demonstrate both in combination, plus the narrow glue required to exercise them.

Out of scope: new scheduler mechanism — 2.3 does not add kernel scheduler code. If a test exposes a missing mechanism, escalate to 2.1 / 2.2 rather than patching in 2.3.

## Cross-links

- `preempt_shell_overview.md` — dependency DAG: 2.3 depends on 2.1 for anti-starvation gate; 2.2 is an orthogonal probe.
- `preempt_kernel_goroutines.md` — 2.1 mechanism details.
- `preempt_user_goroutines.md` — 2.2 mechanism details.
- `preempt_shell_milestones_and_verification.md` — 2.3's Entry/Exit rolled into the unified table.
- Existing work-stealing: wired at commit `aa5bb91` (`src/ipi.go:46-55 gooosWakeupCPU` + `scheduler_cores.go:217-219 stealWork` pop-site). Distribution assertion is already satisfied today; this feature codifies the test.

## 1. Current State

- Shell commands spawn as independent processes (one `ring3Wrapper` kernel goroutine per `elfSpawn` call at `src/process.go:235,342`). Each ring3Wrapper is a kernel-schedulable task; the kernel runqueues are per-CPU (`runqueues[numCPU]` in `scheduler_cores.go`); `stealWork()` distributes them.
- Today `smpprobe.elf` (`user/cmd/smpprobe/main.go`) demonstrates distribution empirically: parent spawns 4 workers that each call `sys_getcpuid` and typically report distinct cpuIDs (0..3) under `-smp 4`. This confirms sub-gate (a) but is not automated.
- Nothing today covers sub-gate (b): if a worker spins on `for {}`, it holds its kernel CPU until it yields at a syscall. Sibling ring3Wrappers on the same CPU's runqueue wait forever. After 2.1 lands, kernel preemption rotates them; after 2.2 lands, the hostile process's own user-goroutines also keep time-slicing internally.
- `scripts/test_smp_basic.sh` (landed via the M3 batch) tests kernel-goroutine distribution, not shell-spawned user processes. It is complementary, not a replacement.

## 2. Sub-gates

2.3 decomposes into two **independent** assertions. Each must be separately falsifiable; failure isolation tells us which feature is broken.

### Sub-gate (a): Distribution

**Assertion.** Under `-smp 4`, shell-spawned processes observe ≥ 2 distinct `sys_getcpuid` values across workers spawned by one parent.

**Probe.** `scripts/test_smp_shell_distribution.sh` runs `smpprobe` from the shell and greps serial log for cpuID lines.

**Depends on.** Nothing new. Already satisfied by `aa5bb91`.

**Pass condition.** `grep -cE 'worker-[0-9]+: cpuID=[1-3]' /tmp/smp_distribution.log >= 2`.

Including this sub-gate in 2.3 is defensive: a future regression in `stealWork` or `gooosWakeupCPU` would break shell distribution silently; 2.3's harness makes the regression fail visibly.

### Sub-gate (b): Anti-starvation

**Assertion.** Under `-smp 4`, if one shell-spawned process runs a pure `for {}` compute loop, other processes on the same CPU's runqueue still make progress.

**Probe.** `scripts/test_smp_shell_preempt.sh`:

1. Boot to shell under `-smp 4`.
2. Spawn a new user ELF `cpuhog.elf` (added in 2.3's commit list, §4) — runs `for { /* no syscalls */ }`.
3. Spawn `markerprint.elf` — loops `for i := 0; i < 20; i++ { println("marker " + i); sleep 100ms }`.
4. After 3 s, check serial log for ≥ 5 "marker" lines.

**Depends on.** Feature 2.1 (kernel preemption). Without 2.1, whichever of `cpuhog` or `markerprint` lands on the other's CPU-hosted runqueue will still suffer starvation until the hog does something yield-inducing (it doesn't).

**Why "≥ 5" and not "all 20"?** IPI latency + preempt granularity (100 Hz timer = 10 ms quantum) means ~5 markers per second is the floor under adversarial conditions. 5 within 3 s gives ~60% margin over the floor.

**Failure isolation table:**

| Observation | Likely cause |
| --- | --- |
| 0 markers | 2.1 not wired: preempt IPI not firing or ISR not rescheduling. |
| 1–4 markers | 2.1 partially wired: preemptDisable stuck in a spinlock somewhere; or quantum too coarse. |
| ≥ 5 markers | Sub-gate (b) PASS. |

### Sub-gate (c): Intra-process fairness (orthogonal probe for 2.2)

**Assertion.** Within a single shell-spawned user process containing multiple goroutines, a pure `for {}` user goroutine does not starve sibling goroutines in the same process.

**Probe.** `scripts/test_preempt_user.sh` (already listed under 2.2 §7 commit 7; cross-referenced here for completeness).

**Depends on.** Feature 2.2 (mechanism B signal delivery).

**Why separate from (b)?** (b) tests *inter-process* fairness; (c) tests *intra-process*. A world where 2.1 lands but 2.2 does not has (b) PASS and (c) FAIL. Keeping them separate lets a future implementation-session reader diagnose partial landings.

## 3. Harness Scripts

### 3.1 `scripts/test_smp_shell_distribution.sh`

```bash
#!/bin/bash
# Boots -smp 4, auto-runs `smpprobe` via QEMU monitor sendkey,
# verifies ≥ 2 distinct cpuIDs appear.
set -e
make iso >/dev/null
LOG=$(mktemp /home/ryo/work/gooos/tmp/test_smp_dist.XXXXXX.log)
make run-smp >"$LOG" 2>&1 &
QEMU_PID=$!
trap "kill $QEMU_PID 2>/dev/null" EXIT
# ... sendkey sequence to run "smpprobe" ...
# bounded poll per workflow_bounded_polling.md
for i in $(seq 1 20); do
    if grep -qE 'worker-3: cpuID' "$LOG"; then break; fi
    sleep 1
done
COUNT=$(grep -oE 'cpuID=[0-9]+' "$LOG" | sort -u | wc -l)
if [ "$COUNT" -lt 2 ]; then
    echo "FAIL: only $COUNT distinct cpuIDs observed"
    exit 1
fi
echo "PASS: $COUNT distinct cpuIDs"
```

Full script shipped in commit §4.

### 3.2 `scripts/test_smp_shell_preempt.sh`

Structure mirrors 3.1; after the shell is ready, issues `cpuhog &` (depends on 2.4 `&` syntax, see Entry criteria §5) followed by `markerprint` and greps for marker count.

**Fallback if 2.4 hasn't landed at test-authoring time:** use `smpprobe &`-style manual orchestration by spawning the hog from a wrapper program that calls `sys_spawn(cpuhog)` and then loops printing markers itself. See §4 commit 3 note.

### 3.3 `scripts/test_preempt_user.sh`

Defined in `preempt_user_goroutines.md §7 commit 7`. Listed here for visibility.

## 4. Commit-per-edit Plan

1. `feat(user): add cpuhog user ELF` — `user/cmd/cpuhog/main.go` (new), trivial `for {}` loop. Register in `user/Makefile:21 CMDS`. `scripts/embed_elfs.sh` picks it up automatically.
2. `feat(user): add markerprint user ELF` — `user/cmd/markerprint/main.go` (new), 20-iter println loop with `gooos.Sleep(100)` between. Register in Makefile.
3. `test(smp): add test_smp_shell_distribution.sh harness` — sub-gate (a) automation.
4. `test(smp): add test_smp_shell_preempt.sh harness` — sub-gate (b) automation. **Depends on 2.1 landed.**

Total: 4 commits. 2.3 adds no new kernel or runtime code — only user ELFs and scripts.

## 5. Entry Criteria

- For sub-gate (a): only work-stealing (commit `aa5bb91`) on HEAD.
- For sub-gate (b): feature 2.1 landed (preempt-enabled kernel).
- For sub-gate (c): feature 2.2 landed (sys_sigaction + handler).
- For the 2.4-dependent `&` syntax in `test_smp_shell_preempt.sh`: either 2.4 landed OR the fallback single-spawn wrapper described in §3.2.

## 6. Exit Criteria

- `scripts/test_smp_shell_distribution.sh` PASS (≥ 2 distinct cpuIDs).
- `scripts/test_smp_shell_preempt.sh` PASS under `-smp 4` (≥ 5 markers within 3 s).
- `scripts/test_preempt_user.sh` PASS (cross-referenced from 2.2).
- No QEMU panic, no triple-fault in any run.
- Existing regression harnesses (`test_net.sh`, `test_tcp_phase{1..5}.sh`, `test_smp_basic.sh`, `test_gochan.sh`, `test_pipe_matrix.sh`) remain PASS.

## 7. Per-File Edits

- `user/cmd/cpuhog/main.go` (NEW) — `package main\n...\nfunc main() { for {} }`.
- `user/cmd/markerprint/main.go` (NEW) — 20-iter println/sleep.
- `user/Makefile:21` — append `cpuhog markerprint` to `CMDS`.
- `scripts/test_smp_shell_distribution.sh` (NEW).
- `scripts/test_smp_shell_preempt.sh` (NEW).

Nothing under `/home/ryo/work/gooos/src/` changes.

## 8. Rollback

Trivial: `git revert` each commit. cpuhog/markerprint are user ELFs — removing them from `CMDS` and deleting `user/cmd/cpuhog`/`user/cmd/markerprint` restores the baseline. Harness scripts are standalone; deletion does not affect any build.

## 9. Risks

- **False-pass on sub-gate (b) if cpuhog happens to land on its own core**. Under `-smp 4` with only 2 active processes, the scheduler may place cpuhog on CPU 3 and markerprint on CPU 0 — no contention, markers fire regardless of preemption. Mitigation: the harness pins `-smp 1` as well; under single-core, cpuhog and markerprint *must* share. PASS = ≥ 5 markers under `-smp 1` AND under `-smp 4`.
- **Quantum drift**. If 2.1 lands with quantum > 100 ms, the marker count may dip below 5 in a 3 s window. Mitigation: 2.1's quantum is documented as 10 ticks @ 100 Hz = 100 ms; 2.3's 5-marker floor already assumes this. If 2.1 changes the quantum, 2.3's threshold updates in the same commit series.
- **Spurious FAIL from serial-log buffering**. QEMU serial output is line-buffered through `serialPrintln`. If the harness checks too early (< 1 s), early markers may not have flushed. Mitigation: bounded polling loop with 1 s steps up to 20 s (per `workflow_bounded_polling.md` memory entry).

## 10. Deliverables

- 4 commits per §4.
- 2 new user ELFs: `cpuhog`, `markerprint`.
- 2 new scripts: `test_smp_shell_distribution.sh`, `test_smp_shell_preempt.sh`.

## Reviewer MINOR notes

*Populated during the mandatory reviewer pass.*
