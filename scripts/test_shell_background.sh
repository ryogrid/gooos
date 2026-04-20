#!/usr/bin/env bash
# scripts/test_shell_background.sh — feature 2.4 shell & harness.
#
# Boots the kernel, waits for shell prompt, sends the keystrokes
# `hello &` via HMP monitor sendkey, then verifies:
#   - The shell's immediate job-notification line appears
#     (`[<id>] <pid> hello`) within 3 s.
#   - The completion line (`[<id>] <pid> done exit=0 hello`) appears
#     within 5 s after that.
#
# Exits 0 on PASS, 1 on FAIL (prints log tail).

set -u

OUT="tmp/serial_shell_bg.log"
MON_SOCK="tmp/test_shell_bg.mon.sock"
rm -f "$OUT" "$MON_SOCK"

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

# Runs under -smp 1. Sendkey under -smp > 1 is a pre-existing harness
# flake (same reason test_tcp_phase5.sh runs default-smp).
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

# Type `hello &` and press ENTER.
# QEMU sendkey: shift-7 → & on US layout.
# A throwaway backspace absorbs any swallowed first keystroke (same
# precaution as test_tcp_phase5.sh and test_ps.sh).
hmp_send_many \
    'sendkey backspace' \
    'sendkey h' \
    'sendkey e' \
    'sendkey l' \
    'sendkey l' \
    'sendkey o' \
    'sendkey spc' \
    'sendkey shift-7' \
    'sendkey ret'

# Give the keys time to be consumed before we start polling.
sleep 2

# The shell's backgrounding notification is `[<id>] <pid> hello`.
# The reap-poll emits `[<id>] <pid> done exit=<N> hello` on the next prompt.
got_spawn=""
got_done=""
for _ in $(seq 1 80); do
    if [ -z "$got_spawn" ] && grep -qE '^\[[0-9]+\] [0-9]+ hello$' "$OUT"; then
        got_spawn=1
    fi
    if [ -z "$got_done" ] && grep -qE '^\[[0-9]+\] [0-9]+ done exit=[0-9]+ hello$' "$OUT"; then
        got_done=1
    fi
    if [ -n "$got_spawn" ] && [ -n "$got_done" ]; then
        break
    fi
    sleep 0.2
done

# Poke the shell with a newline to trigger the reap poll if the done
# line is still pending (reap fires at prompt boundary).
hmp_send_many 'sendkey ret'

for _ in $(seq 1 30); do
    if [ -z "$got_done" ] && grep -qE '^\[[0-9]+\] [0-9]+ done exit=[0-9]+ hello$' "$OUT"; then
        got_done=1
    fi
    if [ -n "$got_done" ]; then
        break
    fi
    sleep 0.2
done

echo "test_shell_background: spawn=${got_spawn:-0} done=${got_done:-0}"

if [ -n "$got_spawn" ] && [ -n "$got_done" ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL"
echo "--- serial log tail ---"
tail -40 "$OUT"
exit 1
