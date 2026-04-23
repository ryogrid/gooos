#!/usr/bin/env bash
# scripts/test_sleeptest_shell.sh — deterministic sleeptest validation

set -u

OUT="tmp/serial_sleeptest_shell.log"
CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_sleeptest.go.bak"
rm -f "$OUT" "$BACKUP"

cp "$CONF" "$BACKUP"
restore_config() {
    if [ -f "$BACKUP" ]; then
        mv "$BACKUP" "$CONF"
        rm -f tmp/kernel.iso
    fi
}

sed -i 's/const runSleeputestTest = false/const runSleeputestTest = true/' "$CONF"
if ! grep -q 'const runSleeputestTest = true' "$CONF"; then
    restore_config
    echo "FAIL: could not enable runSleeputestTest"
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

# Wait for markers
for _ in $(seq 1 600); do
    HAS_BEGIN=$(grep -c 'sleeptest: begin' "$OUT" 2>/dev/null || true)
    HAS_S1=$(grep -c 'sleeptest: Sleep 1 OK' "$OUT" 2>/dev/null || true)
    HAS_S2=$(grep -c 'sleeptest: Sleep 2 OK' "$OUT" 2>/dev/null || true)
    HAS_S3=$(grep -c 'sleeptest: Sleep 3 OK' "$OUT" 2>/dev/null || true)
    HAS_PASS=$(grep -c 'sleeptest: ALL SLEEPS PASS' "$OUT" 2>/dev/null || true)
    
    if [ "${HAS_BEGIN:-0}" -ge 1 ] && [ "${HAS_S1:-0}" -ge 1 ] && \
       [ "${HAS_S2:-0}" -ge 1 ] && [ "${HAS_S3:-0}" -ge 1 ] && \
       [ "${HAS_PASS:-0}" -ge 1 ]; then
        break
    fi
    sleep 0.1
done

HAS_BEGIN=$(grep -c 'sleeptest: begin' "$OUT" 2>/dev/null || true)
HAS_S1=$(grep -c 'sleeptest: Sleep 1 OK' "$OUT" 2>/dev/null || true)
HAS_S2=$(grep -c 'sleeptest: Sleep 2 OK' "$OUT" 2>/dev/null || true)
HAS_S3=$(grep -c 'sleeptest: Sleep 3 OK' "$OUT" 2>/dev/null || true)
HAS_PASS=$(grep -c 'sleeptest: ALL SLEEPS PASS' "$OUT" 2>/dev/null || true)

echo "test_sleeptest_shell: begin=${HAS_BEGIN:-0} s1=${HAS_S1:-0} s2=${HAS_S2:-0} s3=${HAS_S3:-0} pass=${HAS_PASS:-0}"

if [ "${HAS_BEGIN:-0}" -ge 1 ] && [ "${HAS_S1:-0}" -ge 1 ] && \
   [ "${HAS_S2:-0}" -ge 1 ] && [ "${HAS_S3:-0}" -ge 1 ] && \
   [ "${HAS_PASS:-0}" -ge 1 ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL"
echo "--- serial log tail ---"
tail -60 "$OUT"
exit 1

