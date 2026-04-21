# Problem model and evidence targets

## Symptom summary

1. Boot/preempt behavior is non-deterministic under `-smp 4` (`tasks/TODO.md` notes flapping in preempt-focused harnesses).
2. `test_smp_shell_preempt.sh` remains unstable despite sendkey bypasses, showing starvation-like behavior (`markerprint` often stalls at marker 0/1 while `cpuhog` continues).
3. AP LAPIC timer remains deferred in `apEntry()` due known boot-hang risk (`src/smp.go` comment block around `lapicTimerInit()`).
4. Preempt routing depends on APICID latching and BSP-driven timer fanout (`src/ipi.go`, `src/lapic_timer.go`), with unstable-target fallback behavior.
5. Shell-driving via HMP sendkey is itself known flaky under SMP in existing scripts, so test failures can be harness-path failures, not always kernel/shell correctness failures.

## Code-grounded evidence map

| Area | Current implementation evidence | Why it matters |
| --- | --- | --- |
| AP timer path | `src/smp.go`: AP-side `lapicTimerInit()` intentionally disabled | Startup model is asymmetric (BSP timer + IPI fanout), increasing sensitivity to IPI target correctness and APICID stability |
| Preempt trigger | `src/lapic_timer.go`: preempt fanout runs only when `preemptEnabled && bspBootDone != 0 && idx == 0` | A single CPU drives cross-core preempt timing; early/unstable periods can amplify races |
| IPI target selection | `src/ipi.go`: `broadcastPreemptIPI()` skips APs with APICID=0 and falls back to shorthand broadcast when no target sent | Transient APICID zero state can suppress or distort delivery behavior |
| ISR preempt gating | `src/goroutine_irq.go`: `handlePreemptIPI()` gates on interrupt/syscall depth and preempt-disable | Correctness depends on precise depth bookkeeping and safe-point timing |
| Shell command execution | `user/cmd/sh/main.go`: external commands run via `gooos.Exec` (`sys_exec -> elfExec -> processWait`) | `smpprobe` correctness and shell recovery depend on this synchronous path |
| Foreground keyboard ownership | `src/fd.go` + `src/process.go`: `consoleStdin.Read` only for foreground proc; `processWait` transfers ownership child->parent | Shell liveness after command completion depends on reliable foreground restoration |
| `smpprobe` behavior | `user/cmd/smpprobe/main.go`: parent spawns workers (`Spawn`) and waits (`Wait`) | Exercises process lifecycle + scheduler distribution + command return path |
| Harness confounder | scripts (`test_ps.sh`, `test_shell_background.sh`, comments in SMP scripts): sendkey under SMP documented flaky | Must separate true product failures from keystroke injection failures |

## Root-cause hypotheses to prove/disprove

1. **H1 (startup phase coupling):** preempt fanout starts while AP target state is still unstable, causing inconsistent preempt delivery and flapping harness outcomes.
2. **H2 (target selection instability):** APICID transient zero and fallback-broadcast behavior cause unpredictable IPI coverage under load.
3. **H3 (probe-path contamination):** probe-only warmup/diagnostic logic in common timer path perturbs scheduling behavior.
4. **H4 (command-path verification gap):** current SMP shell tests do not deterministically exercise the real shell command path for `smpprobe` because they avoid sendkey by bypassing shell input.
5. **H5 (shell continuity edge):** foreground ownership restoration around blocking exec/wait has edge windows that become visible under SMP/preempt stress.

## Evidence targets for implementation

1. Explicit startup/preempt phase markers with monotonic transitions (no backslide).
2. Stable preempt-target snapshot metrics (target count, per-CPU eligibility, no oscillation once operational).
3. Deterministic `smpprobe` command-path markers proving:
   1. command started from shell path,
   2. command completed,
   3. shell prompt returned and accepted follow-up command.
4. Separation of primary correctness gates (non-sendkey deterministic) from supplemental interactive sendkey evidence.
