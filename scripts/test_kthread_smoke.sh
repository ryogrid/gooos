#!/usr/bin/env bash
# scripts/test_kthread_smoke.sh — Route C M0 smoke test.
#
# Flips runKthreadSmoke to true, rebuilds, boots. Success criteria:
#   1. "SMOKE: kthread smoke test starting" appears.
#   2. The serial log contains at least 5 'A' chars AND 5 'B' chars
#      inside the smoke window (between "starting" and "SMOKE: OK").
#   3. "SMOKE: OK" appears — meaning both smoke threads reached
#      kschedExit and kschedLoop returned cleanly.
#
# After the smoke window, normal boot continues; the harness does
# not care whether the boot finishes. A 40-second timeout covers
# init + the smoke window comfortably.

set -u

CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_kthread_smoke.go.bak"
TIMEOUT="${TIMEOUT:-40}"
OUT="tmp/kthread_smoke.log"

mkdir -p tmp

cp "$CONF" "$BACKUP"
restore_config() {
    if [ -f "$BACKUP" ]; then
        mv "$BACKUP" "$CONF"
        rm -f tmp/kernel.iso tmp/kernel.bin
    fi
}
trap restore_config EXIT

sed -i 's/const runKthreadSmoke = false/const runKthreadSmoke = true/' "$CONF"
if ! grep -q 'const runKthreadSmoke = true' "$CONF"; then
    echo "FAIL: could not enable runKthreadSmoke"
    exit 1
fi

rm -f tmp/kernel.iso tmp/kernel.bin
echo "building kthread-smoke ISO..."
if ! make iso >/dev/null 2>&1; then
    echo "FAIL: make iso"
    exit 1
fi

rm -f "$OUT"
qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -display none \
    -no-reboot -no-shutdown \
    -smp 4 &
pid=$!

deadline=$(( $(date +%s) + TIMEOUT ))
while [ "$(date +%s)" -lt "$deadline" ]; do
    if grep -q 'SMOKE: OK' "$OUT" 2>/dev/null; then
        break
    fi
    sleep 1
done
kill -9 "$pid" 2>/dev/null
wait "$pid" 2>/dev/null

# Extract the smoke window (from "starting" to "SMOKE: OK").
window=$(awk '/SMOKE: kthread smoke test starting/{flag=1; next}
              /SMOKE: OK/{print; flag=0; next}
              flag{print}' "$OUT" 2>/dev/null)

a_count=$(printf '%s' "$window" | tr -cd 'A' | wc -c)
b_count=$(printf '%s' "$window" | tr -cd 'B' | wc -c)
ok_line=$(grep -c 'SMOKE: OK' "$OUT" 2>/dev/null || true)
start_line=$(grep -c 'SMOKE: kthread smoke test starting' "$OUT" 2>/dev/null || true)
: "${a_count:=0}" "${b_count:=0}" "${ok_line:=0}" "${start_line:=0}"

echo "smoke: start=$start_line A=$a_count B=$b_count ok=$ok_line"
echo "log: $OUT"

if [ "$start_line" -lt 1 ] || [ "$ok_line" -lt 1 ]; then
    echo "result: FAIL (smoke banners missing)"
    exit 1
fi
if [ "$a_count" -lt 5 ] || [ "$b_count" -lt 5 ]; then
    echo "result: FAIL (expected ≥5 A and ≥5 B, got A=$a_count B=$b_count)"
    exit 1
fi
echo "result: PASS"
exit 0
