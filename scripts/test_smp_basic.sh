#!/usr/bin/env bash
# scripts/test_smp_basic.sh — M3-7 SMP goroutine distribution test.
#
# Boots the kernel under QEMU with -smp 4 and waits for evidence
# that a kernel goroutine ran on a non-BSP core. Two signals count:
#   1. smpBasicProbe() in src/main.go emits `smp_basic_cpu=N` once
#      per tick; we pass iff N != 0 is seen at least once.
#   2. ring3Wrapper's first print carries its cpuID; cpuID != 0
#      is additional evidence that the shell-spawn goroutine got
#      stolen to an AP.
#
# Exits 0 on PASS, 1 on FAIL (prints the log tail).

set -u

OUT="tmp/serial_smp_basic.log"
rm -f "$OUT"

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -display none \
    -no-reboot -no-shutdown \
    -smp 4 &
PID=$!

# Wait up to 20s for PASS evidence. Check every 0.5s.
got_kernel=""
got_ring3=""
for _ in $(seq 1 40); do
    if [ -f "$OUT" ]; then
        if [ -z "$got_kernel" ] && grep -qE '^smp_basic_cpu=[1-9]' "$OUT"; then
            got_kernel=1
        fi
        if [ -z "$got_ring3" ] && grep -qE '^ring3Wrapper: cpuID=[1-9]' "$OUT"; then
            got_ring3=1
        fi
        if [ -n "$got_kernel" ] && [ -n "$got_ring3" ]; then
            break
        fi
    fi
    sleep 0.5
done

sleep 0.5

kill "$PID" 2>/dev/null
wait "$PID" 2>/dev/null

if ! [ -f "$OUT" ]; then
    echo "FAIL: serial log not written"
    exit 1
fi

echo "test_smp_basic: kernel_on_ap=${got_kernel:-0} ring3_on_ap=${got_ring3:-0}"

if [ -n "$got_kernel" ] || [ -n "$got_ring3" ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL — all observed cpuIDs were 0"
echo "--- serial log tail ---"
tail -40 "$OUT"
exit 1
