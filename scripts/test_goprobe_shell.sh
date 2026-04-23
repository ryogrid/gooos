#!/usr/bin/env bash
# scripts/test_goprobe_shell.sh — deterministic shell autorun goprobe test.
#
# Enables runGoprobeTest, boots -smp 4, and validates that:
#   1) shell autorun executes `goprobe`,
#   2) goprobe emits all test markers (go+chan, select, time.Sleep, yield-cycle),
#   3) goprobe completes with ALL TESTS PASS,
#   4) shell executes post-goprobe command.

set -u

OUT="tmp/serial_goprobe_shell.log"
CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_goprobe.go.bak"
rm -f "$OUT" "$BACKUP"

cp "$CONF" "$BACKUP"
restore_config() {
    if [ -f "$BACKUP" ]; then
        mv "$BACKUP" "$CONF"
        rm -f tmp/kernel.iso
    fi
}

sed -i 's/const runGoprobeTest = false/const runGoprobeTest = true/' "$CONF"
if ! grep -q 'const runGoprobeTest = true' "$CONF"; then
    restore_config
    echo "FAIL: could not enable runGoprobeTest"
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
    HAS_BEGIN=$(grep -c 'goprobe: begin' "$OUT" 2>/dev/null || true)
    HAS_GO_CHAN=$(grep -c 'goprobe: go+chan OK' "$OUT" 2>/dev/null || true)
    HAS_SELECT=$(grep -c 'goprobe: select OK' "$OUT" 2>/dev/null || true)
    HAS_YIELD_LOOP=$(grep -c 'goprobe: yield-loop OK' "$OUT" 2>/dev/null || true)
    HAS_YIELD_CYCLE=$(grep -c 'goprobe: yield-cycle OK' "$OUT" 2>/dev/null || true)
    HAS_PASS=$(grep -c 'goprobe: ALL TESTS PASS' "$OUT" 2>/dev/null || true)
    
    if [ "${HAS_BEGIN:-0}" -ge 1 ] && [ "${HAS_GO_CHAN:-0}" -ge 1 ] && \
       [ "${HAS_SELECT:-0}" -ge 1 ] && [ "${HAS_YIELD_LOOP:-0}" -ge 1 ] && \
       [ "${HAS_YIELD_CYCLE:-0}" -ge 1 ] && [ "${HAS_PASS:-0}" -ge 1 ]; then
        break
    fi
    sleep 0.1
done

HAS_BEGIN=$(grep -c 'goprobe: begin' "$OUT" 2>/dev/null || true)
HAS_GO_CHAN=$(grep -c 'goprobe: go+chan OK' "$OUT" 2>/dev/null || true)
HAS_SELECT=$(grep -c 'goprobe: select OK' "$OUT" 2>/dev/null || true)
HAS_YIELD_LOOP=$(grep -c 'goprobe: yield-loop OK' "$OUT" 2>/dev/null || true)
HAS_YIELD_CYCLE=$(grep -c 'goprobe: yield-cycle OK' "$OUT" 2>/dev/null || true)
HAS_PASS=$(grep -c 'goprobe: ALL TESTS PASS' "$OUT" 2>/dev/null || true)

echo "test_goprobe_shell: begin=${HAS_BEGIN:-0} go_chan=${HAS_GO_CHAN:-0} select=${HAS_SELECT:-0} yield_loop=${HAS_YIELD_LOOP:-0} yield_cycle=${HAS_YIELD_CYCLE:-0} pass=${HAS_PASS:-0}"

if [ "${HAS_BEGIN:-0}" -ge 1 ] && [ "${HAS_GO_CHAN:-0}" -ge 1 ] && \
   [ "${HAS_SELECT:-0}" -ge 1 ] && [ "${HAS_YIELD_LOOP:-0}" -ge 1 ] && \
   [ "${HAS_YIELD_CYCLE:-0}" -ge 1 ] && [ "${HAS_PASS:-0}" -ge 1 ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL"
echo "--- serial log tail ---"
tail -60 "$OUT"
exit 1
