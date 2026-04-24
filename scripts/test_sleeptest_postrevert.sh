#!/usr/bin/env bash
# scripts/test_sleeptest_postrevert.sh — S2 post-revert baseline sampler.
#
# Same harness as test_sleeptest_control.sh (flips ONLY runSleeputestTest,
# leaves runSleepAudit=false) but runs on the tree AFTER the P02 revert
# (commit 94886c1 / TODO_SCHED/optG.revert). Compare PASS rate to the S1
# pre-revert control (tmp/sleep_s1_control_summary.json: 25 % / N=20).
# A meaningful jump (e.g. >>50 % PASS) confirms P02 as the regression
# source per 06_next_cycle.md §Final state.

set -u

# shellcheck source=harness_lib.sh
. "$(dirname "$0")/harness_lib.sh"

CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_postrevert.go.bak"
ITERATIONS="${ITERATIONS:-50}"
TIMEOUT_PER_RUN="${TIMEOUT_PER_RUN:-90}"
SUMMARY="tmp/sleep_s2_postrevert_summary.json"

harness_recover_stale_backup "$CONF"
mkdir -p tmp

cp "$CONF" "$BACKUP"
restore_config() {
    if [ -f "$BACKUP" ]; then
        mv "$BACKUP" "$CONF"
        rm -f tmp/kernel.iso
    fi
}
trap restore_config EXIT

sed -i 's/const runSleeputestTest = false/const runSleeputestTest = true/' "$CONF"
if ! grep -q 'const runSleeputestTest = true' "$CONF"; then
    echo "FAIL: could not enable runSleeputestTest"
    exit 1
fi
if grep -q 'const runSleepAudit = true' "$CONF"; then
    echo "FAIL: runSleepAudit must be false for post-revert baseline run"
    exit 1
fi

rm -f tmp/kernel.iso
echo "building post-revert ISO (runSleepAudit=false)..."
if ! make iso >/dev/null 2>&1; then
    echo "FAIL: make iso"
    exit 1
fi

pass=0
fail=0
nobegin=0
before_s1=0
after_s1=0
after_s2=0

for i in $(seq 1 "$ITERATIONS"); do
    out="tmp/sleep_s2_run_${i}.log"
    rm -f "$out"
    qemu-system-x86_64 \
        -cdrom tmp/kernel.iso \
        -serial "file:$out" \
        -display none \
        -no-reboot -no-shutdown \
        -smp 4 &
    pid=$!

    deadline=$(( $(date +%s) + TIMEOUT_PER_RUN ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        if grep -q 'sleeptest: ALL SLEEPS PASS' "$out" 2>/dev/null; then
            break
        fi
        sleep 1
    done
    kill -9 "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null

    begin=$(grep -c 'sleeptest: begin' "$out" 2>/dev/null || true)
    s1=$(grep -c 'sleeptest: Sleep 1 OK' "$out" 2>/dev/null || true)
    s2=$(grep -c 'sleeptest: Sleep 2 OK' "$out" 2>/dev/null || true)
    allpass=$(grep -c 'sleeptest: ALL SLEEPS PASS' "$out" 2>/dev/null || true)
    : "${begin:=0}" "${s1:=0}" "${s2:=0}" "${allpass:=0}"

    if [ "$allpass" -ge 1 ]; then
        pass=$((pass+1))
        verdict="PASS"
    else
        fail=$((fail+1))
        if [ "$begin" -lt 1 ]; then
            nobegin=$((nobegin+1))
            verdict="FAIL(nobegin)"
        elif [ "$s1" -lt 1 ]; then
            before_s1=$((before_s1+1))
            verdict="FAIL(beforeS1)"
        elif [ "$s2" -lt 1 ]; then
            after_s1=$((after_s1+1))
            verdict="FAIL(afterS1)"
        else
            after_s2=$((after_s2+1))
            verdict="FAIL(afterS2)"
        fi
    fi

    echo "run $i/$ITERATIONS: $verdict  (begin=$begin s1=$s1 s2=$s2 pass=$allpass)"
done

rate=$(( pass * 100 / ITERATIONS ))
cat >"$SUMMARY" <<EOF
{
  "label": "S2 post-revert baseline (runSleepAudit=false)",
  "iterations": $ITERATIONS,
  "pass": $pass,
  "fail": $fail,
  "rate_percent": $rate,
  "breakdown": {
    "fail_nobegin": $nobegin,
    "fail_beforeS1": $before_s1,
    "fail_afterS1": $after_s1,
    "fail_afterS2": $after_s2
  }
}
EOF

echo
echo "summary -> $SUMMARY:"
cat "$SUMMARY"
echo

if [ "$rate" -ge 95 ]; then
    exit 0
fi
exit 1
