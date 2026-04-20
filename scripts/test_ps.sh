#!/usr/bin/env bash
# scripts/test_ps.sh — feature 2.5 ps-command harness.
#
# Boots the kernel under QEMU, waits for the shell prompt, sends the
# keystrokes `p s ENTER` via QEMU HMP monitor sendkey, then greps the
# serial log for:
#   - the ps column header (`  PID  PPID  S  CPU    TICKS  NAME`)
#   - at least one row with a PID value (the ps process itself, or
#     the shell)
#
# Exits 0 on PASS, 1 on FAIL (prints the log tail).

set -u

OUT="tmp/serial_ps.log"
MON_SOCK="tmp/test_ps.mon.sock"
rm -f "$OUT" "$MON_SOCK"

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

# Runs under -smp 1. Keyboard-IRQ delivery from QEMU HMP sendkey has a
# pre-existing latency quirk under -smp > 1 (see test_tcp_phase5.sh which
# also runs only at default smp). The ps command itself works fine under
# -smp 4 when invoked manually; the harness keystroke injection is the
# limiting factor.
qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -monitor "unix:$MON_SOCK,server,nowait" \
    -display none \
    -no-reboot -no-shutdown &
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
    echo "FAIL: shell prompt did not appear within 20 s "
    tail -40 "$OUT"
    exit 1
fi

# Longer settle for -smp > 1: keyboard IRQ delivery races the boot-time
# netDiag prints, and the shell's ReadLine may not be parked yet.
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

hmp_send_many \
    'sendkey backspace' \
    'sendkey p' \
    'sendkey s' \
    'sendkey ret'

# Expect the header line + ≥ 1 data row within 5 s.
got_header=""
got_row=""
for _ in $(seq 1 50); do
    if [ -z "$got_header" ] && grep -q "PID  PPID  S  CPU" "$OUT"; then
        got_header=1
    fi
    # A row has a numeric PID followed by spaces + state char R/S/Z/?
    if [ -z "$got_row" ] && grep -qE '^ *[0-9]+ +[0-9]+ +[RSZ?] ' "$OUT"; then
        got_row=1
    fi
    if [ -n "$got_header" ] && [ -n "$got_row" ]; then
        break
    fi
    sleep 0.1
done

echo "test_ps: header=${got_header:-0} row=${got_row:-0}"

if [ -n "$got_header" ] && [ -n "$got_row" ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL"
echo "--- serial log tail ---"
tail -40 "$OUT"
exit 1
