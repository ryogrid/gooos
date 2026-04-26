#!/usr/bin/env bash
# scripts/test_sleeptest_control.sh — S1 control sampler.
#
# Like test_sleeptest_longrun.sh but flips ONLY runSleeputestTest
# (not runSleepAudit). Used once to verify that the P02 crash
# pattern is not caused by the Option D trace-ring writes.
# Per 06_next_cycle.md §Caveats (reviewer S1).

set -u

# shellcheck source=harness_lib.sh
. "$(dirname "$0")/harness_lib.sh"

CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_control.go.bak"
ITERATIONS="${ITERATIONS:-20}"
TIMEOUT_PER_RUN="${TIMEOUT_PER_RUN:-90}"
SUMMARY="tmp/sleep_s1_control_summary.json"

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
    echo "FAIL: runSleepAudit must be false for control run"
    exit 1
fi

rm -f tmp/kernel.iso
echo "building control ISO (runSleepAudit=false)..."
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
    out="tmp/sleep_control_run_${i}.log"
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
  "label": "S1 control (runSleepAudit=false, pre-revert)",
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
