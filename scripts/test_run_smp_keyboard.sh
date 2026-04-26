#!/usr/bin/env bash
# scripts/test_run_smp_keyboard.sh — M6 uniprocessor-kernel
# milestone gating harness.
#
# Mirrors the manual `make run-smp` workflow: boots the kernel
# under `qemu -smp 4`, waits for the shell prompt, sends
# `h e l p ret` via QEMU HMP `sendkey`, and checks the serial
# log for:
#   - the `help` command echo + the shell's "Built-in commands:"
#     header (the M9 drain proxy)
#   - no PF / panic / #DE in the log
#   - the M8 marker firing (keyboard ISR ran at all)
# Repeats N=10 iterations and decides PASS by the bar specified
# in `no_goroutine_kernel_design/14_uniprocessor_kernel.md` §8:
#
#   PASS: helpRan ≥ 9/10, PF == 0/10, M9 fired ≥ 9/10
#
# Pre-§14 baseline: 0/10 helpRan, 0..5/10 PF, 0/10 M9.
# Post-§14 target: ≥ 9/10 helpRan, 0/10 PF, ≥ 9/10 M9.
#
# Exits 0 on PASS, 1 on FAIL (prints per-run summary + the log
# tail of one failing run).

set -u

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

TOTAL=${TOTAL:-10}
SUCCESS=0
PANICS=0
M8FIRED=0
M9FIRED=0
LAST_FAIL_LOG=""

cleanup_one() {
    if [ -n "${QPID:-}" ]; then
        kill "$QPID" 2>/dev/null
        wait "$QPID" 2>/dev/null
    fi
}
trap cleanup_one EXIT

for run in $(seq 1 "$TOTAL"); do
    OUT="tmp/run_smp_kbd_$run.log"
    MON="tmp/run_smp_kbd_$run.mon"
    rm -f "$OUT" "$MON"

    qemu-system-x86_64 \
        -cdrom tmp/kernel.iso \
        -serial "file:$OUT" \
        -monitor "unix:$MON,server,nowait" \
        -display none -no-reboot -no-shutdown -smp 4 &
    QPID=$!

    # Wait up to 20 s for the shell prompt.
    for _ in $(seq 1 200); do
        grep -q "Type 'help' for available commands" "$OUT" 2>/dev/null && break
        sleep 0.1
    done
    sleep 5

    python3 - "$MON" <<'PY' 2>/dev/null
import socket, sys, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sys.argv[1])
s.settimeout(2)
try:
    s.recv(4096)
except Exception:
    pass
for k in ['h', 'e', 'l', 'p', 'ret']:
    s.sendall(('sendkey ' + k + '\n').encode())
    time.sleep(0.3)
time.sleep(0.5)
s.close()
PY

    sleep 6
    kill "$QPID" 2>/dev/null
    wait "$QPID" 2>/dev/null
    QPID=""

    HELP=$(grep -c "Built-in commands" "$OUT" 2>/dev/null)
    PF=$(grep -cE "PF:|panic|#DE" "$OUT" 2>/dev/null)
    M8=$(grep -c "M8 handleKeyboard" "$OUT" 2>/dev/null)
    M9=$(grep -c "M9 pump:drained" "$OUT" 2>/dev/null)

    [ -z "$HELP" ] && HELP=0
    [ -z "$PF" ]   && PF=0
    [ -z "$M8" ]   && M8=0
    [ -z "$M9" ]   && M9=0

    [ "$HELP" -gt 0 ] && SUCCESS=$((SUCCESS+1))
    [ "$PF" -gt 0 ]   && PANICS=$((PANICS+1)) && LAST_FAIL_LOG="$OUT"
    [ "$M8" -gt 0 ]   && M8FIRED=$((M8FIRED+1))
    [ "$M9" -gt 0 ]   && M9FIRED=$((M9FIRED+1))

    echo "run $run: helpRan=$HELP M8=$M8 M9=$M9 PF=$PF"
done

echo "==========="
echo "test_run_smp_keyboard: helpRan=$SUCCESS/$TOTAL M8=$M8FIRED/$TOTAL M9=$M9FIRED/$TOTAL PF=$PANICS/$TOTAL"

# §14 §8 PASS bar.
if [ "$SUCCESS" -ge 9 ] && [ "$PANICS" -eq 0 ] && [ "$M9FIRED" -ge 9 ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL"
if [ -n "$LAST_FAIL_LOG" ]; then
    echo "--- tail of $LAST_FAIL_LOG ---"
    tail -30 "$LAST_FAIL_LOG"
fi
exit 1
