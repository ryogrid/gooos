#!/usr/bin/env bash
# scripts/test_smp_stability_sample.sh — one-sample SMP shell timing harness.
#
# Purpose:
#   Drive the real shell at human-ish cadence under `-smp 4`, then classify
#   one run as:
#     - gochan finished vs hung
#     - smpprobe distributed vs cpu0-only
#     - prompt-adjacent stray `$ 0x...` output seen vs not seen
#
# This is intentionally a sampling harness, not a hard PASS/FAIL regression.
# The underlying issue appears timing-sensitive, so the script preserves long
# post-boot and between-command delays instead of using an in-kernel autorun.

set -u

OUT="tmp/serial_smp_stability_sample.log"
MON_SOCK="tmp/smp_stability_sample.mon.sock"
rm -f "$OUT" "$MON_SOCK"

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -monitor "unix:$MON_SOCK,server,nowait" \
    -display none \
    -no-reboot -no-shutdown \
    -smp 4 &
PID=$!

cleanup() {
    kill "$PID" 2>/dev/null
    wait "$PID" 2>/dev/null
}
trap cleanup EXIT

hmp_send_many() {
    python3 - "$MON_SOCK" "$@" <<'PY'
import os
import socket
import sys
import time

sock_path = sys.argv[1]
cmds = sys.argv[2:]
delay = float(os.environ.get("HMP_KEY_DELAY", "0.45"))
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sock_path)
s.settimeout(2)
try:
    s.recv(4096)
except Exception:
    pass
for c in cmds:
    s.sendall((c + '\n').encode())
    time.sleep(delay)
time.sleep(delay)
s.close()
PY
}

# Wait for shell banner / prompt area to appear.
for _ in $(seq 1 300); do
    grep -q "Type 'help' for available commands" "$OUT" 2>/dev/null && break
    sleep 0.1
done

if ! grep -q "Type 'help' for available commands" "$OUT" 2>/dev/null; then
    echo "FAIL: shell did not appear within 30 s"
    tail -40 "$OUT"
    exit 1
fi

# Timing-sensitive issue: preserve a longer, human-ish pause after shell boot
# so late boot diagnostics and initial ReadLine parking can settle.
sleep 5

# For this timing-sensitive harness, default to human-like typing without an
# automatic leading backspace. Existing HMP tests use a backspace guard to
# absorb a swallowed first key, but here that extra keypress can perturb the
# exact input path under investigation. Re-enable only when explicitly needed.
if [ "${HMP_LEADING_BACKSPACE:-0}" = "1" ]; then
    hmp_send_many 'sendkey backspace'
    sleep 1
fi

hmp_send_many \
    'sendkey g' \
    'sendkey o' \
    'sendkey c' \
    'sendkey h' \
    'sendkey a' \
    'sendkey n' \
    'sendkey ret'

GOCHAN_START=0
GOCHAN_FINISH=0
for _ in $(seq 1 200); do
    if [ "$GOCHAN_START" -eq 0 ] && grep -q '^gochan: pipeline demo' "$OUT" 2>/dev/null; then
        GOCHAN_START=1
    fi
    if [ "$GOCHAN_FINISH" -eq 0 ] && grep -q '^gochan: finished$' "$OUT" 2>/dev/null; then
        GOCHAN_FINISH=1
        break
    fi
    sleep 0.1
done

SMP_START=0
SMP_DONE=0
WORKERS=0
NONZERO_CPUS=0

if [ "$GOCHAN_FINISH" -eq 1 ]; then
    # Preserve another human-scale think time before the next command.
    sleep 4

    if [ "${HMP_LEADING_BACKSPACE:-0}" = "1" ]; then
        hmp_send_many 'sendkey backspace'
        sleep 1
    fi

    hmp_send_many \
        'sendkey s' \
        'sendkey m' \
        'sendkey p' \
        'sendkey p' \
        'sendkey r' \
        'sendkey o' \
        'sendkey b' \
        'sendkey e' \
        'sendkey ret'

    for _ in $(seq 1 300); do
        if [ "$SMP_START" -eq 0 ] && grep -q '^smpprobe: spawning ' "$OUT" 2>/dev/null; then
            SMP_START=1
        fi
        if [ "$SMP_DONE" -eq 0 ] && grep -q '^smpprobe: done$' "$OUT" 2>/dev/null; then
            SMP_DONE=1
            break
        fi
        sleep 0.1
    done

    WORKERS=$(grep -cE '^worker-[0-9]+: cpuID=' "$OUT" 2>/dev/null || true)
    NONZERO_CPUS=$(grep -cE '^worker-[0-9]+: cpuID=[1-9]' "$OUT" 2>/dev/null || true)
fi

# Give the shell a final idle window so prompt-adjacent noise, if any, lands.
sleep 2

PROMPT_HEX=$(grep -cE '^\$ 0x[0-9A-Fa-f]+' "$OUT" 2>/dev/null || true)

echo "test_smp_stability_sample: gochan_start=$GOCHAN_START gochan_finish=$GOCHAN_FINISH smp_start=$SMP_START smp_done=$SMP_DONE workers=${WORKERS:-0} nonzero_cpus=${NONZERO_CPUS:-0} prompt_hex=${PROMPT_HEX:-0}"

if [ "$GOCHAN_START" -eq 0 ]; then
    echo "result: FAIL (gochan did not start; command injection likely failed)"
    echo "--- serial log tail ---"
    tail -60 "$OUT"
    exit 1
fi

if [ "$GOCHAN_FINISH" -eq 0 ]; then
    echo "result: OBSERVED gochan_hang"
    exit 0
fi

if [ "$SMP_START" -eq 0 ]; then
    echo "result: FAIL (smpprobe did not start after completed gochan)"
    echo "--- serial log tail ---"
    tail -60 "$OUT"
    exit 1
fi

if [ "$SMP_DONE" -eq 0 ]; then
    echo "result: OBSERVED smpprobe_hang"
    exit 0
fi

if [ "${NONZERO_CPUS:-0}" -ge 1 ]; then
    echo "result: OBSERVED smpprobe_distributed"
else
    echo "result: OBSERVED smpprobe_all_cpu0"
fi
