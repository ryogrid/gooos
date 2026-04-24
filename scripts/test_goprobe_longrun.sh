#!/usr/bin/env bash
# scripts/test_goprobe_longrun.sh — DEFERRED 3 follow-up / I-2 sampler.
#
# Same shape as scripts/test_sleeptest_longrun.sh but targets the
# goprobe.elf autorun path via runGoprobeTest=true. Answers
# "is the P02 spawn-time regression sleeptest-specific or general
# to user-process spawn?" per
# current_impl_2026_04_24/fix_plan_deferred_1_5/06_next_cycle.md §I-2.
#
# ITERATIONS defaults to 50. TIMEOUT_PER_RUN defaults to 90 s.

set -u

# shellcheck source=harness_lib.sh
. "$(dirname "$0")/harness_lib.sh"

CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_goprobe_longrun.go.bak"
ITERATIONS="${ITERATIONS:-50}"
TIMEOUT_PER_RUN="${TIMEOUT_PER_RUN:-90}"
SUMMARY="tmp/goprobe_longrun_summary.json"

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

sed -i 's/const runGoprobeTest = false/const runGoprobeTest = true/' "$CONF"
if ! grep -q 'const runGoprobeTest = true' "$CONF"; then
    echo "FAIL: could not enable runGoprobeTest"
    exit 1
fi

rm -f tmp/kernel.iso
echo "building goprobe longrun ISO..."
if ! make iso >/dev/null 2>&1; then
    echo "FAIL: make iso"
    exit 1
fi

pass=0
fail=0
fail_nobegin=0
fail_before_pass=0

for i in $(seq 1 "$ITERATIONS"); do
    out="tmp/goprobe_longrun_run_${i}.log"
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
        if grep -q 'goprobe: ALL TESTS PASS' "$out" 2>/dev/null; then
            break
        fi
        sleep 1
    done
    kill -9 "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null

    begin=$(grep -c 'goprobe: begin' "$out" 2>/dev/null || true)
    allpass=$(grep -c 'goprobe: ALL TESTS PASS' "$out" 2>/dev/null || true)
    : "${begin:=0}" "${allpass:=0}"

    if [ "$allpass" -ge 1 ]; then
        pass=$((pass+1))
        verdict="PASS"
    else
        fail=$((fail+1))
        if [ "$begin" -lt 1 ]; then
            fail_nobegin=$((fail_nobegin+1))
            verdict="FAIL(nobegin)"
        else
            fail_before_pass=$((fail_before_pass+1))
            verdict="FAIL(midrun)"
        fi
    fi

    echo "run $i/$ITERATIONS: $verdict  (begin=$begin pass=$allpass)"
done

rate=$(( pass * 100 / ITERATIONS ))
cat >"$SUMMARY" <<EOF
{
  "iterations": $ITERATIONS,
  "pass": $pass,
  "fail": $fail,
  "rate_percent": $rate,
  "breakdown": {
    "fail_nobegin": $fail_nobegin,
    "fail_midrun": $fail_before_pass
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
