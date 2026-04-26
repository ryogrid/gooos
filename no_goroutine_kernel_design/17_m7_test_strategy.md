# 17 — M7 test strategy

Companion to `15_userspace_smp_on_aps.md` (design) and
`16_m7_execution_plan.md` (work order). This file
specifies the new gating harness and the rewrites for the
5 SKIP-gated SMP-distribution scripts.

## 1. New harness — `scripts/test_ring3_distribution.sh`

### Purpose

Gate M7 on observable Ring-3 distribution across APs.
Mirrors `scripts/test_smp_shell_preempt.sh`'s autorun
pattern (no HMP `sendkey` injection) so the test is
deterministic under `-smp 4`.

### Mechanism

Set `runSMPShellPreemptProbe = true` (auto-loads cpuhog +
markerprint at `bspBootDone`); boot `qemu -smp 4`; capture
serial log for 15 s; count distinct cpuIDs from
`marker <iter> cpu=<N>` lines. PASS if ≥ 2 distinct N
values observed.

### Spec

```bash
#!/usr/bin/env bash
# scripts/test_ring3_distribution.sh — M7 Ring-3 distribution gate.
#
# Asserts that under userspaceSMP=true (M7 default), the
# auto-loaded markerprint.elf observes ≥ 2 distinct cpuIDs
# in its `marker <iter> cpu=<N>` output within 15 s. M6's
# uniprocessor-kernel invariants stay unaffected because
# this test does NOT touch the keyboard path.
#
# PASS: ≥ 2 distinct cpuIDs in markerprint output.
# FAIL: only cpuID=0 observed (all Ring-3 work on BSP).

set -u

CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_ring3_dist.go.bak"
OUT="tmp/serial_ring3_dist.log"

. "$(dirname "$0")/harness_lib.sh"
harness_recover_stale_backup "$CONF"

# §M7 §6.2 SKIP gate (mirrors the 5 deferred harnesses).
if grep -q '^const userspaceSMP = false' "$CONF" 2>/dev/null; then
    echo "test_ring3_distribution: SKIP under userspaceSMP=false"
    echo "result: SKIP — flip src/preempt_config.go uniprocessorKernel/userspaceSMP to enable M7"
    exit 0
fi

rm -f "$OUT" "$BACKUP"
cp "$CONF" "$BACKUP"
restore_config() {
    if [ -f "$BACKUP" ]; then
        mv "$BACKUP" "$CONF"
        rm -f tmp/kernel.iso
    fi
}
cleanup() {
    if [ -n "${PID:-}" ]; then
        kill "$PID" 2>/dev/null
        wait "$PID" 2>/dev/null
    fi
    restore_config
}
trap cleanup EXIT

sed -i 's/const runSMPShellPreemptProbe = false/const runSMPShellPreemptProbe = true/' "$CONF"

make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }

qemu-system-x86_64 -cdrom tmp/kernel.iso -serial "file:$OUT" \
    -display none -no-reboot -no-shutdown -smp 4 &
PID=$!
sleep 15
kill "$PID" 2>/dev/null
wait "$PID" 2>/dev/null
PID=""

DISTINCT=$(grep -oE 'marker [0-9]+ cpu=[0-9]+' "$OUT" 2>/dev/null \
    | grep -oE 'cpu=[0-9]+' | sort -u | wc -l)

echo "test_ring3_distribution: distinct_cpus=$DISTINCT"

if [ "$DISTINCT" -ge 2 ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL — Ring-3 work observed on only $DISTINCT cpu(s)"
echo "--- log tail ---"
tail -40 "$OUT"
exit 1
```

### Decision rule

PASS bar matches `15_*.md` §10: ≥ 9/10 iterations PASS in
a 10-iter outer wrapper if ever needed (M6.fix-1's
`scripts/test_shell_post_exec_prompt.sh` shape). For
M7's gate we run a single 15 s observation per CI run;
the bar is "≥ 2 distinct cpuIDs in one observation".

## 2. Re-purposed harness assertions (Step 5 of M7)

### `scripts/test_smp_basic.sh`

**Pre-M6 assertion**: `smp_basic_cpu=N` (N>0) from
kernel-goroutine `smpBasicProbe`.

**M7 re-purpose**: enable `runSMPProbeShellTest = true`
(autorun runs `smpprobe.elf` via the existing shell
autorun mechanism), assert `cpu=N` (N>0) appears in
smpprobe's worker output. The smpprobe binary itself
already prints worker cpuIDs; M7 just guarantees the
workers run on APs.

SKIP gate change: `^const userspaceSMP = false`.

### `scripts/test_smp_shell_distribution.sh`

**Pre-M6 assertion**: count distinct `smp_basic_cpu=N`
values via the kernel-goroutine probe.

**M7 re-purpose**: count distinct `cpu=N` values from
smpprobe.elf worker output (or fall back to cpuhog/
markerprint output if smpprobe is not in the autorun).
Assert ≥ 2 distinct.

SKIP gate change: `^const userspaceSMP = false`.

### `scripts/test_smp_shell_preempt.sh`

**Existing assertion**: ≥ 5 `marker <iter>` serial lines
within 15 s under cpuhog + markerprint co-runtime.

**M7 re-purpose**: unchanged. Under M7 the assertion
finally passes because cpuhog (one AP) and markerprint
(another AP) actually run in parallel, so markerprint
emits its 5+ markers without being starved.

SKIP gate change: `^const userspaceSMP = false`.

### `scripts/test_smp_shell_smpprobe.sh`

**Existing assertion**: smpprobe.elf workers cpuID
distribution + completion message + follow-up shell
command.

**M7 re-purpose**: unchanged behavior; passes once Ring-3
spawn distributes onto APs.

SKIP gate change: `^const userspaceSMP = false`.

### `scripts/test_smp_release_gate.sh`

**Existing assertion**: 50× sampler over 8 harnesses;
each must hit ≥ 95 % PASS rate.

**M7 re-purpose**: unchanged. Re-enable
test_smp_basic, test_smp_shell_distribution,
test_smp_shell_preempt, test_smp_shell_smpprobe in the
HARNESSES list (or rely on their internal SKIP gates).
Add `test_ring3_distribution` to the list.

SKIP gate change: `^const userspaceSMP = false`.

## 3. M6 invariant harnesses (must remain green)

These are unchanged by M7 and must continue to PASS:

- `scripts/test_run_smp_keyboard.sh` — boot shell on BSP,
  HMP `sendkey h e l p ret`, 10-iter. ≥ 9/10 helpRan,
  0/10 PF, ≥ 9/10 M9.
- `scripts/test_shell_post_exec_prompt.sh` — boot shell
  on BSP, exec hello, 10-iter. ≥ 8/10 helloPrinted,
  0/10 panics. M6.fix-1 invariant.
- `scripts/test_kthread_smoke.sh` — service tier on BSP.
- `scripts/test_ps.sh` — single-CPU keyboard via HMP.
- `scripts/test_net.sh` — service-tier net kthreads on
  BSP.
- `scripts/test_tcp_phase[1-5].sh` —
  TCP service kthreads on BSP.
- `scripts/test_tcp_longidle.sh 15` — afterTicks +
  TCP timer wheel.

## 4. Failure modes to watch

If `test_ring3_distribution.sh` reports
`distinct_cpus=1`, the most likely M7 regressions:

1. **`kschedSpawnRing3Wrapper` round-robin is gated off**
   (Step 4 reverted) — exec'd children all land on BSP.
   Fix: re-apply Step 4.
2. **`apSchedulerEntry` doesn't dispatch Ring-3 tier**
   (Step 3 reverted) — APs idle; spawn pushes to AP queue
   but nothing pops. Fix: re-apply Step 3.
3. **`gooosWakeupCPU` not delivering** — APs miss the
   wake IPI. Check `wakeFirstSeen[ap]` in `netDiag`
   output (the `wake:NNNN` line).
4. **`kschedPushRing3` writing the wrong queue** — push
   to `kschedQueues[ap]` (service tier) by mistake.
   `kschedLoopRing3Only` doesn't pop service-tier; the
   host sits forever.

If any of these, revert via `git revert HEAD` per
`16_m7_execution_plan.md`'s rollback rule.

If `test_run_smp_keyboard.sh` regresses (drops below
9/10), the most likely M7 regression:

1. **Boot shell ended up on AP** — `kschedSpawnRing3WrapperOnBSP`
   round-robin slipped past the `, 0)` pin. Fix: confirm
   line 67 of `src/kthread_ring3.go` (post-Step-4) reads
   `kschedPushRing3(t, 0)` exactly.
2. **BSP combined pump not driving Ring-3 tier** — boot
   shell is on `kschedQueuesRing3[0]` but the `elf.go`
   pump only calls `kschedLoopOnce()`. Fix: confirm
   Step 3's combined-pump edit landed.

If `test_shell_post_exec_prompt.sh` regresses, the most
likely cause:

1. **Cross-CPU `proc.Exited` not visible to parent**.
   Under M7 parent (boot shell) on BSP and child on AP;
   parent polls `proc.Exited` via `kschedTimedPark(1)`.
   If child's write isn't observed, the parent loops
   forever. Verify `procLock` semantics around
   `proc.Exited = 1` (`src/process.go:571`); spinlock
   release provides the memory barrier; should work.
2. **Child stuck on AP queue** — same as Ring-3
   distribution failure mode 4 above.

## 5. Reviewer sub-agent verifies test strategy

The Step 7 reviewer pass (per `16_*.md`) must confirm
that:

- The new harness is wired into `Makefile`'s test target
  (or matches the existing convention; check
  `Makefile`'s `test:` rule for the pattern).
- All 5 re-purposed harnesses have updated SKIP gates
  pointing to `userspaceSMP`.
- `scripts/test_smp_release_gate.sh`'s HARNESSES list
  includes `test_ring3_distribution`.
- The M6 invariant harnesses' line counts and structure
  are unchanged (no accidental edits).
