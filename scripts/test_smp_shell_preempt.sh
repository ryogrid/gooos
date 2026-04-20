#!/usr/bin/env bash
# scripts/test_smp_shell_preempt.sh — feature 2.3 sub-gate (b).
#
# Boots -smp 4, spawns cpuhog.elf + markerprint.elf as separate
# processes via the shell (using HMP sendkey). With kernel-goroutine
# preemption (feature 2.1 landed), the cpuhog process hogging one AP
# cannot starve markerprint on the same runqueue — the preempt IPI
# forces a reschedule.
#
# PASS = ≥ 5 `marker <N>` serial lines within 15 s (markerprint's
# default output is `marker <iter> cpu=<N>` — see user/cmd/markerprint).
#
# Exits 0 on PASS, 1 on FAIL (prints log tail).

set -u

OUT="tmp/serial_smp_shell_preempt.log"
MON_SOCK="tmp/test_smp_shell_preempt.mon.sock"
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

# Wait for shell prompt (≤ 20 s).
for _ in $(seq 1 200); do
    grep -q "Type 'help' for available commands" "$OUT" 2>/dev/null && break
    sleep 0.1
done

if ! grep -q "Type 'help' for available commands" "$OUT" 2>/dev/null; then
    echo "FAIL: shell prompt did not appear within 20 s"
    tail -40 "$OUT"
    exit 1
fi

sleep 5

hmp_send_many() {
    python3 - "$MON_SOCK" "$@" <<'PY'
import socket, sys, time
sock_path = sys.argv[1]
cmds = sys.argv[2:]
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sock_path)
s.settimeout(2)
try:
    s.recv(4096)
except Exception:
    pass
for c in cmds:
    s.sendall((c + '\n').encode())
    time.sleep(0.3)
time.sleep(0.3)
s.close()
PY
}

# Type `cpuhog &` then `markerprint`.
hmp_send_many \
    'sendkey backspace' \
    'sendkey c' \
    'sendkey p' \
    'sendkey u' \
    'sendkey h' \
    'sendkey o' \
    'sendkey g' \
    'sendkey spc' \
    'sendkey shift-7' \
    'sendkey ret'

sleep 2

hmp_send_many \
    'sendkey m' \
    'sendkey a' \
    'sendkey r' \
    'sendkey k' \
    'sendkey e' \
    'sendkey r' \
    'sendkey p' \
    'sendkey r' \
    'sendkey i' \
    'sendkey n' \
    'sendkey t' \
    'sendkey ret'

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
