#!/usr/bin/env bash
# scripts/test_smp_shell_preempt.sh — feature 2.3 sub-gate (b).
#
# Boots -smp 4 and auto-launches cpuhog.elf + markerprint.elf at
# bspBootDone via runSMPShellPreemptProbe. This bypasses shell-driving
# through HMP sendkey, which is a known flaky path under -smp > 1 and
# caused false negatives in this harness.
#
# With kernel-goroutine preemption (feature 2.1 landed), the cpuhog
# process hogging one AP cannot starve markerprint on the same runqueue
# — the preempt IPI forces a reschedule.
#
# PASS = ≥ 5 `marker <N>` serial lines within 15 s (markerprint's
# default output is `marker <iter> cpu=<N>` — see user/cmd/markerprint).
#
# Exits 0 on PASS, 1 on FAIL (prints log tail).

set -u

OUT="tmp/serial_smp_shell_preempt.log"
CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_smp_shell_preempt.go.bak"

# shellcheck source=harness_lib.sh
. "$(dirname "$0")/harness_lib.sh"
harness_recover_stale_backup "$CONF"

rm -f "$OUT" "$BACKUP"

cp "$CONF" "$BACKUP"
restore_config() {
    if [ -f "$BACKUP" ]; then
        mv "$BACKUP" "$CONF"
        rm -f tmp/kernel.iso
    fi
}

sed -i 's/const runSMPShellPreemptProbe = false/const runSMPShellPreemptProbe = true/' "$CONF"
if ! grep -q 'const runSMPShellPreemptProbe = true' "$CONF"; then
    restore_config
    echo "FAIL: could not enable runSMPShellPreemptProbe"
    exit 1
fi

rm -f tmp/kernel.iso
make iso >/dev/null 2>&1 || { restore_config; echo "FAIL: make iso"; exit 1; }

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -display none \
    -no-reboot -no-shutdown \
    -smp 4 &
PID=$!

cleanup() {
    kill "$PID" 2>/dev/null
    wait "$PID" 2>/dev/null
}
trap 'cleanup; restore_config' EXIT

# Wait ≤ 15 s for ≥ 5 marker lines.
for _ in $(seq 1 150); do
    COUNT=$(grep -cE '^marker [0-9]+ cpu=' "$OUT" 2>/dev/null)
    COUNT=${COUNT:-0}
    if [ "$COUNT" -ge 5 ]; then
        break
    fi
    sleep 0.1
done

COUNT=$(grep -cE '^marker [0-9]+ cpu=' "$OUT" 2>/dev/null)
COUNT=${COUNT:-0}
echo "test_smp_shell_preempt: markers_observed=$COUNT"

if [ "$COUNT" -ge 5 ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL — expected >= 5 markers, saw $COUNT"
echo "--- serial log tail ---"
tail -40 "$OUT"
exit 1
