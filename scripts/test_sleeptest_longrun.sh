#!/usr/bin/env bash
# scripts/test_sleeptest_longrun.sh — DEFERRED 3 / P03 audit sampler.
#
# Builds gooos with runSleepAudit=true + runSleeputestTest=true,
# then boots QEMU -smp 4 N times and classifies each run as
# PASS (all three Sleep N OK + ALL SLEEPS PASS) or FAIL. Collects
# a per-run serial log under tmp/sleep_audit_<run>.log. Emits a
# terse JSON-ish summary to stdout and keeps all logs around for
# post-analysis.
#
# ITERATIONS defaults to 50 (plan's audit target). Override via
# env: ITERATIONS=20 bash scripts/test_sleeptest_longrun.sh.
# TIMEOUT_PER_RUN defaults to 90 s.

set -u

# shellcheck source=harness_lib.sh
. "$(dirname "$0")/harness_lib.sh"

CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_longrun.go.bak"
ITERATIONS="${ITERATIONS:-50}"
TIMEOUT_PER_RUN="${TIMEOUT_PER_RUN:-90}"
SUMMARY="tmp/sleep_longrun_summary.json"

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

sed -i 's/const runSleepAudit = false/const runSleepAudit = true/' "$CONF"
sed -i 's/const runSleeputestTest = false/const runSleeputestTest = true/' "$CONF"
if ! grep -q 'const runSleepAudit = true' "$CONF"; then
    echo "FAIL: could not enable runSleepAudit"
    exit 1
fi
if ! grep -q 'const runSleeputestTest = true' "$CONF"; then
    echo "FAIL: could not enable runSleeputestTest"
    exit 1
fi

rm -f tmp/kernel.iso
echo "building audit ISO..."
if ! make iso >/dev/null 2>&1; then
    echo "FAIL: make iso"
    exit 1
fi

pass=0
fail=0
s1only=0
s2only=0
s3only=0
nobegin=0

for i in $(seq 1 "$ITERATIONS"); do
    out="tmp/sleep_audit_run_${i}.log"
    rm -f "$out"
    qemu-system-x86_64 \
        -cdrom tmp/kernel.iso \
        -serial "file:$out" \
        -display none \
        -no-reboot -no-shutdown \
        -smp 4 &
    pid=$!

    # Wait up to TIMEOUT_PER_RUN for ALL SLEEPS PASS; kill qemu afterwards.
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
    s3=$(grep -c 'sleeptest: Sleep 3 OK' "$out" 2>/dev/null || true)
    allpass=$(grep -c 'sleeptest: ALL SLEEPS PASS' "$out" 2>/dev/null || true)
    : "${begin:=0}" "${s1:=0}" "${s2:=0}" "${s3:=0}" "${allpass:=0}"

    if [ "$allpass" -ge 1 ]; then
        pass=$((pass+1))
        verdict="PASS"
    else
        fail=$((fail+1))
        if [ "$begin" -lt 1 ]; then
            nobegin=$((nobegin+1))
            verdict="FAIL(nobegin)"
        elif [ "$s3" -ge 1 ]; then
            verdict="FAIL(postS3)"
        elif [ "$s2" -ge 1 ]; then
            s2only=$((s2only+1))
            verdict="FAIL(afterS2)"
        elif [ "$s1" -ge 1 ]; then
            s1only=$((s1only+1))
            verdict="FAIL(afterS1)"
        else
            verdict="FAIL(beforeS1)"
        fi
    fi

    echo "run $i/$ITERATIONS: $verdict  (begin=$begin s1=$s1 s2=$s2 s3=$s3)"
done

rate=$(( pass * 100 / ITERATIONS ))
cat >"$SUMMARY" <<EOF
{
  "iterations": $ITERATIONS,
  "pass": $pass,
  "fail": $fail,
  "rate_percent": $rate,
  "breakdown": {
    "fail_nobegin": $nobegin,
    "fail_afterS1": $s1only,
    "fail_afterS2": $s2only
  },
  "threshold_percent": 95
}
EOF

echo
echo "summary written to $SUMMARY:"
cat "$SUMMARY"
echo

if [ "$rate" -ge 95 ]; then
    echo "result: PASS (rate=${rate}%)"
    exit 0
fi

echo "result: FAIL (rate=${rate}% < 95% threshold)"
exit 1
