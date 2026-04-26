#!/usr/bin/env bash
# scripts/test_ring3_distribution.sh — M7 Ring-3 distribution gate.
#
# Asserts that under userspaceSMP=true (M7 default), the
# auto-loaded markerprint.elf observes ≥ 2 distinct cpuIDs
# in its `marker <iter> cpu=<N>` output within 15 s. M6's
# uniprocessor-kernel invariants stay unaffected because
# this test does NOT touch the keyboard path.
#
# PASS: markerprint runs on at least one AP (cpu != 0).
#   Under M7 a Ring-3 process is dispatched to one AP queue
#   and stays there for its lifetime (process migration is
#   M8+). So a single markerprint instance only emits cpu=N
#   for one N. The success signal is N != 0 — Ring-3 was
#   dispatched off BSP.
# FAIL: all markerprint output observed on cpu=0 (uniprocessor
#   M6 fallback, AP dispatch broken, or M7 disabled).

set -u

CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_ring3_dist.go.bak"
OUT="tmp/serial_ring3_dist.log"

# shellcheck source=harness_lib.sh
. "$(dirname "$0")/harness_lib.sh"
harness_recover_stale_backup "$CONF"

# §M7 §6.2 SKIP gate (mirrors the 5 deferred harnesses).
if grep -q '^const userspaceSMP = false' "$CONF" 2>/dev/null; then
    echo "test_ring3_distribution: SKIP under userspaceSMP=false"
    echo "result: SKIP — flip src/preempt_config.go userspaceSMP=true to enable M7"
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
# smpBasicProbe is the launcher for cpuhog+markerprint
# (src/main.go:742-752). Without runSMPBasicProbe=true the
# launcher never fires and no `marker <iter> cpu=<N>` lines
# appear in serial.
sed -i 's/const runSMPBasicProbe = false/const runSMPBasicProbe = true/' "$CONF"

make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }

qemu-system-x86_64 -cdrom tmp/kernel.iso -serial "file:$OUT" \
    -display none -no-reboot -no-shutdown -smp 4 &
PID=$!
sleep 15
kill "$PID" 2>/dev/null
wait "$PID" 2>/dev/null
PID=""

CPUS=$(grep -oE 'marker [0-9]+ cpu=[0-9]+' "$OUT" 2>/dev/null \
    | grep -oE 'cpu=[0-9]+' | sort -u | tr '\n' ' ')
COUNT=$(grep -cE '^marker [0-9]+ cpu=' "$OUT" 2>/dev/null)
COUNT=${COUNT:-0}

echo "test_ring3_distribution: marker_count=$COUNT cpus_observed=[$CPUS]"

# §15 §10: PASS iff markerprint emitted markers AND at least
# one of those was on cpu != 0 (Ring-3 dispatched off BSP onto
# an AP).
if [ "$COUNT" -ge 1 ] && echo "$CPUS" | grep -qE 'cpu=[1-9]'; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL — markerprint did not run on any AP (cpus=[$CPUS], count=$COUNT)"
echo "--- log tail ---"
tail -40 "$OUT"
exit 1
