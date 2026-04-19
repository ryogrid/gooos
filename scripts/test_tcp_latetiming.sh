#!/usr/bin/env bash
# scripts/test_tcp_latetiming.sh — reproduces the late-timing RX stall.
#
# Expected result on current HEAD: FAIL.
# After the scheduler-starvation fix lands, this script should PASS.
#
# Boots the kernel, waits for Ring-3 shell, then waits an additional
# 15 seconds before firing nc against the kernel TCP echo on port
# 10080. That 15-second gap is long enough for the post-Ring-3
# goroutine-scheduling stall (see pasttodos/TODO_NET3.md "Known issue
# — late-timing RX stall") to kick in — netRxLoop stops being
# scheduled, e1000 ISR still fires but no frame is ever drained from
# the RX ring, nc receives no echo.
#
# Exits 0 on PASS (round-trip echo confirmed), 1 on FAIL.

set -u

OUT="tmp/serial_tcp_latetiming.log"
ECHO_OUT="tmp/tcp_latetiming_echo.txt"
rm -f "$OUT" "$ECHO_OUT"

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -display none \
    -no-reboot -no-shutdown \
    -device e1000,netdev=n0 \
    -netdev user,id=n0,hostfwd=tcp::10080-:8080 &
PID=$!

# Bounded wait for Ring 3 to come up (max ~30 s).
for i in $(seq 1 300); do
    kill -0 "$PID" 2>/dev/null || { echo "FAIL: QEMU died during boot"; exit 1; }
    grep -q 'ring3Wrapper: jumping to Ring 3' "$OUT" 2>/dev/null && break
    sleep 0.1
done

if ! grep -q 'ring3Wrapper: jumping to Ring 3' "$OUT" 2>/dev/null; then
    echo "FAIL: Ring 3 shell did not come up within 30 s"
    kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null
    tail -30 "$OUT"
    exit 1
fi

# Let the guest run for 15 s post-Ring-3. This is the window during
# which the scheduler stall manifests.
sleep 15

# Confirm QEMU is still alive before the round-trip attempt.
if ! kill -0 "$PID" 2>/dev/null; then
    echo "FAIL: QEMU died before nc attempt"
    tail -30 "$OUT"
    exit 1
fi

PAYLOAD="late-timing-$(date +%s)"
printf '%s' "$PAYLOAD" | timeout 5 nc -w 3 127.0.0.1 10080 > "$ECHO_OUT"
NC_RC=$?

# Give the scanner one more tick in case the echo just barely made it.
sleep 1

kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null

RECEIVED="$(cat "$ECHO_OUT" 2>/dev/null || true)"
echo "test_latetiming: nc_rc=$NC_RC echoed='$RECEIVED' expected='$PAYLOAD'"

if [ "$RECEIVED" = "$PAYLOAD" ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL"
echo "--- serial log tail ---"
tail -60 "$OUT"
exit 1
