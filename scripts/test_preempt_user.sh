#!/usr/bin/env bash
# scripts/test_preempt_user.sh — feature 2.2 user preemption harness.
#
# Boots QEMU, waits for shell prompt, runs `userpreempt` via HMP sendkey.
# The user ELF registers a SIGALRM handler, spawns a tight `for {}` user
# goroutine + a marker-printing sibling. Without kernel-delivered SIGALRM
# preemption the marker goroutine starves; with it, the marker gets its
# turn every quantum.
#
# PASS = ≥ 5 `userpreempt_marker=N` lines in 15s.
#
# Preempt is already enabled (2.1-7 landed const preemptEnabled = true).
# No sed-flip needed like test_preempt_kernel.sh.

set -u

OUT="tmp/serial_preempt_user.log"
MON_SOCK="tmp/test_preempt_user.mon.sock"
CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_user.go.bak"
rm -f "$OUT" "$MON_SOCK" "$BACKUP"

# runUserPreemptProbe auto-launches userpreempt.elf from bspBootDone
# so the harness doesn't need HMP sendkey (which is flaky under
# -smp > 1). Flip it to true for this build, run, revert on exit.
cp "$CONF" "$BACKUP"
restore_config() {
    if [ -f "$BACKUP" ]; then
        mv "$BACKUP" "$CONF"
        make iso >/dev/null 2>&1 || true
    fi
}
sed -i 's/const runUserPreemptProbe = false/const runUserPreemptProbe = true/' "$CONF"

rm -f tmp/kernel.iso
make iso >/dev/null 2>&1 || { restore_config; echo "FAIL: make iso"; exit 1; }

# -smp 4: BSP broadcasts preempt IPIs to APs. userpreempt's
# ring3Wrapper likely migrates to an AP via work-stealing, where
# SIGALRM delivery can actually fire (BSP doesn't self-IPI).
qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -monitor "unix:$MON_SOCK,server,nowait" \
    -display none \
    -no-reboot -no-shutdown \
    -smp 4 &
PID=$!

qemu_cleanup() {
    kill "$PID" 2>/dev/null
    wait "$PID" 2>/dev/null
}
trap 'qemu_cleanup; restore_config' EXIT

# userpreempt.elf auto-launches from bspBootDone under the flipped
# runUserPreemptProbe gate — no sendkey needed.
# Wait up to 15s for markers.
for _ in $(seq 1 150); do
    COUNT=$(grep -cE 'userpreempt_marker=[0-9]+' "$OUT" 2>/dev/null)
    COUNT=${COUNT:-0}
    if [ "$COUNT" -ge 5 ]; then
        break
    fi
    sleep 0.1
done

COUNT=$(grep -cE 'userpreempt_marker=[0-9]+' "$OUT" 2>/dev/null)
COUNT=${COUNT:-0}
echo "test_preempt_user: markers_observed=$COUNT"

if [ "$COUNT" -ge 5 ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL — expected >= 5 markers, saw $COUNT"
echo "--- serial log tail ---"
tail -40 "$OUT"
exit 1
