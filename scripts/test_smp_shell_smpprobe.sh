#!/usr/bin/env bash
# scripts/test_smp_shell_smpprobe.sh — deterministic SMP shell smpprobe probe.
#
# Enables runSMPProbeShellTest, boots -smp 4, and validates that:
#   1) shell autorun executes `smpprobe`,
#   2) smpprobe emits worker cpu lines and completes,
#   3) shell executes a follow-up command (`echo POST_SMPPROBE_OK`),
#   4) processWait foreground diagnostics are present.

set -u

OUT="tmp/serial_smp_shell_smpprobe.log"
CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_smp_shell_smpprobe.go.bak"

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

sed -i 's/const runSMPProbeShellTest = false/const runSMPProbeShellTest = true/' "$CONF"
if ! grep -q 'const runSMPProbeShellTest = true' "$CONF"; then
    restore_config
    echo "FAIL: could not enable runSMPProbeShellTest"
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

# Wait <= 60 s for all required markers.
for _ in $(seq 1 600); do
    HAS_START=$(grep -c 'smpprobe: spawning' "$OUT" 2>/dev/null || true)
    HAS_WORKER=$(grep -cE '^worker-[0-9]+: cpuID=' "$OUT" 2>/dev/null || true)
    HAS_DONE=$(grep -c '^smpprobe: done$' "$OUT" 2>/dev/null || true)
    HAS_POST=$(grep -c 'POST_SMPPROBE_OK' "$OUT" 2>/dev/null || true)
    HAS_FG=$(grep -c 'SHELLPROBE: fg_after_wait' "$OUT" 2>/dev/null || true)
    if [ "${HAS_START:-0}" -ge 1 ] && [ "${HAS_WORKER:-0}" -ge 1 ] && \
       [ "${HAS_DONE:-0}" -ge 1 ] && [ "${HAS_POST:-0}" -ge 1 ] && \
       [ "${HAS_FG:-0}" -ge 1 ]; then
        break
    fi
    sleep 0.1
done

HAS_START=$(grep -c 'smpprobe: spawning' "$OUT" 2>/dev/null || true)
HAS_WORKER=$(grep -cE '^worker-[0-9]+: cpuID=' "$OUT" 2>/dev/null || true)
HAS_DONE=$(grep -c '^smpprobe: done$' "$OUT" 2>/dev/null || true)
HAS_POST=$(grep -c 'POST_SMPPROBE_OK' "$OUT" 2>/dev/null || true)
HAS_FG=$(grep -c 'SHELLPROBE: fg_after_wait' "$OUT" 2>/dev/null || true)

echo "test_smp_shell_smpprobe: start=${HAS_START:-0} worker=${HAS_WORKER:-0} done=${HAS_DONE:-0} post=${HAS_POST:-0} fg=${HAS_FG:-0}"

if [ "${HAS_START:-0}" -ge 1 ] && [ "${HAS_WORKER:-0}" -ge 1 ] && \
   [ "${HAS_DONE:-0}" -ge 1 ] && [ "${HAS_POST:-0}" -ge 1 ] && \
   [ "${HAS_FG:-0}" -ge 1 ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL"
echo "--- serial log tail ---"
tail -60 "$OUT"
exit 1
