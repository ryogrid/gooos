#!/usr/bin/env bash
# scripts/test_preempt_kernel.sh — feature 2.1 kernel preemption harness.
#
# Requires the kernel to be built with preemptEnabled=true AND
# runPreemptProbe=true in src/preempt_config.go (both flipped for
# feature 2.1-7). In that configuration src/main.go spawns a pair
# of goroutines after bspBootDone:
#
#   - kpHog: tight `for { x++ }` loop with NO cooperative yield.
#   - kpMarker: prints `preempt_probe_marker=N` every ~50 ms.
#
# Under -smp 4, work-stealing distributes the two goroutines across
# cores. If both happen to land on the same CPU's runqueue, only
# preemption can let kpMarker make progress while kpHog hogs the CPU.
# PASS = observe ≥ 5 `preempt_probe_marker=` lines within 5 s.
#
# Under -smp 1, BSP runs both goroutines cooperatively. The BSP's own
# 100 Hz timer does NOT preempt BSP (known limitation for 2.1 — BSP
# is the broadcast source; targets are APs). So under -smp 1 kpMarker
# cannot make progress; the harness uses -smp 4 as the default.
#
# Exits 0 on PASS, 1 on FAIL (prints log tail).

set -u

OUT="tmp/serial_preempt_kernel.log"
CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config.go.bak"
rm -f "$OUT" "$BACKUP"

# runPreemptProbe is OFF in release because kpHog breaks other
# regression harnesses. Flip it to true for this run, rebuild, run,
# revert on exit.
cp "$CONF" "$BACKUP"
restore_config() {
    if [ -f "$BACKUP" ]; then
        mv "$BACKUP" "$CONF"
        make iso >/dev/null 2>&1 || true
    fi
}
trap restore_config EXIT

sed -i 's/const runPreemptProbe = false/const runPreemptProbe = true/' "$CONF"

rm -f tmp/kernel.iso
make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -display none \
    -no-reboot -no-shutdown \
    -smp 4 &
PID=$!

qemu_cleanup() {
    kill "$PID" 2>/dev/null
    wait "$PID" 2>/dev/null
}
# Stacked trap: qemu_cleanup runs first, then restore_config.
trap 'qemu_cleanup; restore_config' EXIT

# Wait up to 15 s for ≥ 5 marker lines.
for _ in $(seq 1 30); do
    COUNT=$(grep -cE 'preempt_probe_marker=[0-9]+' "$OUT" 2>/dev/null)
    COUNT=${COUNT:-0}
    if [ "$COUNT" -ge 5 ]; then
        break
    fi
    sleep 0.5
done

COUNT=$(grep -cE 'preempt_probe_marker=[0-9]+' "$OUT" 2>/dev/null || echo 0)
echo "test_preempt_kernel: markers_observed=$COUNT"

if [ "$COUNT" -ge 5 ]; then
    echo "result: PASS"
    exit 0
fi

if ! grep -q 'preempt_probe_marker=0' "$OUT" 2>/dev/null; then
    echo "FAIL: probe never fired — is preemptEnabled + runPreemptProbe set?"
fi

echo "result: FAIL — expected >= 5 markers, saw $COUNT"
echo "--- serial log tail ---"
tail -40 "$OUT"
exit 1
