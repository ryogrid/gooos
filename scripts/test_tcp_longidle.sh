#!/usr/bin/env bash
# scripts/test_tcp_longidle.sh — extended-idle variant of
# scripts/test_tcp_latetiming.sh. Takes the idle duration in
# seconds as $1 (default 15).
#
# Used to verify the afterTicks timer-wheel fix
# (current_impl_doc/known_issues.md §"afterTicks single-dispatcher
# timer wheel") holds for arbitrarily long idle windows. The
# companion scripts/test_tcp_latetiming.sh hard-codes 15 s; this
# one parametrises so you can probe 30 s / 60 s / 120 s / 300 s.
#
# Exits 0 on PASS, 1 on FAIL.
set -u

IDLE="${1:-15}"
OUT="tmp/serial_tcp_longidle_${IDLE}.log"
ECHO_OUT="tmp/tcp_longidle_${IDLE}_echo.txt"
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

# Bounded wait for Ring 3 to come up (max ~30 s). Post-M4.1
# the banner is "ring3WrapperKT:" (kthread-hosted); pre-M4.1
# was "ring3Wrapper:" (goroutine-hosted). Match both.
for i in $(seq 1 300); do
    kill -0 "$PID" 2>/dev/null || { echo "FAIL: QEMU died during boot"; exit 1; }
    grep -qE 'ring3Wrapper(KT)?: jumping to Ring 3' "$OUT" 2>/dev/null && break
    sleep 0.1
done

if ! grep -qE 'ring3Wrapper(KT)?: jumping to Ring 3' "$OUT" 2>/dev/null; then
    echo "FAIL: Ring 3 shell did not come up within 30 s"
    kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null
    tail -30 "$OUT"
    exit 1
fi

echo "longidle[$IDLE s]: ring3 up, idling..."
sleep "$IDLE"

if ! kill -0 "$PID" 2>/dev/null; then
    echo "FAIL: QEMU died before nc attempt (idle=$IDLE)"
    tail -30 "$OUT"
    exit 1
fi

PAYLOAD="longidle-${IDLE}s-$(date +%s)"
printf '%s' "$PAYLOAD" | timeout 5 nc -w 3 127.0.0.1 10080 > "$ECHO_OUT"
NC_RC=$?

sleep 1

kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null

RECEIVED="$(cat "$ECHO_OUT" 2>/dev/null || true)"
echo "test_longidle[$IDLE s]: nc_rc=$NC_RC echoed='$RECEIVED' expected='$PAYLOAD'"

if [ "$RECEIVED" = "$PAYLOAD" ]; then
    echo "result[$IDLE s]: PASS"
    exit 0
fi

echo "result[$IDLE s]: FAIL"
tail -40 "$OUT"
exit 1
