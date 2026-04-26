#!/usr/bin/env bash
# scripts/test_wget_parse_rejections.sh — automated wget
# parseURL-rejection harness.
#
# Boots gooos under QEMU (no -smp, no -net), waits for the
# shell prompt, then types three URL inputs whose rejection
# is purely deterministic (no host server, no network):
#
#   §6  HTTPS reject:    wget https://e/x → "wget: only http:// supported"
#   §7  Hostname reject: wget http://e/x  → "wget: hostname not supported"
#   §8  Empty basename:  wget http://1.2.3.4/ → "wget: URL has no basename"
#
# Each URL is sent character-by-character via QEMU HMP's
# `sendkey`, followed by `ret` to fire the command. Serial
# output is collected to a file and grep'd for the expected
# rejection message.
#
# Mirrors the pattern in scripts/test_run_smp_keyboard.sh:54–75.
#
# Exits 0 on PASS (all three rejections matched), 1 on FAIL.

set -u

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

OUT="tmp/wget_parse_rejections.log"
MON="tmp/wget_parse_rejections.mon"
rm -f "$OUT" "$MON"

cleanup() {
    if [ -n "${QPID:-}" ]; then
        kill "$QPID" 2>/dev/null
        wait "$QPID" 2>/dev/null
    fi
    rm -f "$MON"
}
trap cleanup EXIT

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -monitor "unix:$MON,server,nowait" \
    -display none -no-reboot -no-shutdown &
QPID=$!

# Wait up to 20 s for the shell prompt.
for _ in $(seq 1 200); do
    grep -q "Type 'help' for available commands" "$OUT" 2>/dev/null && break
    sleep 0.1
done
sleep 2

python3 - "$MON" <<'PY' 2>/dev/null
import socket, sys, time

# QEMU `sendkey` token map for printable ASCII used in URLs.
TOKENS = {
    ' ':  'spc',
    '/':  'slash',
    '.':  'dot',
    ':':  'shift-semicolon',
    '-':  'minus',
    '_':  'shift-minus',
}
# Letters a-z and digits 0-9 are sent as their literal name.

def char_to_keys(ch):
    if ch.isalpha() and ch.islower():
        return [ch]
    if ch.isalpha() and ch.isupper():
        return ['shift-' + ch.lower()]
    if ch.isdigit():
        return [ch]
    if ch in TOKENS:
        return [TOKENS[ch]]
    raise ValueError("unsupported char in URL: " + repr(ch))

def send_line(s, line):
    for ch in line:
        for k in char_to_keys(ch):
            s.sendall(('sendkey ' + k + '\n').encode())
            time.sleep(0.05)
    s.sendall(b'sendkey ret\n')
    time.sleep(2.5)  # let the command run + serial flush

s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sys.argv[1])
s.settimeout(2)
try:
    s.recv(4096)
except Exception:
    pass

# §6 HTTPS reject
send_line(s, 'wget https://e/x')
# §7 hostname reject
send_line(s, 'wget http://e/x')
# §8 empty basename reject
send_line(s, 'wget http://1.2.3.4/')

s.close()
PY

sleep 3
kill "$QPID" 2>/dev/null
wait "$QPID" 2>/dev/null
QPID=""

HTTPS_REJECT=$(grep -c "wget: only http:// supported" "$OUT" 2>/dev/null)
HOST_REJECT=$(grep -c "wget: hostname not supported" "$OUT" 2>/dev/null)
BASENAME_REJECT=$(grep -c "wget: URL has no basename" "$OUT" 2>/dev/null)

[ -z "$HTTPS_REJECT" ] && HTTPS_REJECT=0
[ -z "$HOST_REJECT" ] && HOST_REJECT=0
[ -z "$BASENAME_REJECT" ] && BASENAME_REJECT=0

echo "test_wget_parse_rejections: https=$HTTPS_REJECT host=$HOST_REJECT basename=$BASENAME_REJECT"

if [ "$HTTPS_REJECT" -gt 0 ] && [ "$HOST_REJECT" -gt 0 ] && [ "$BASENAME_REJECT" -gt 0 ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL — log tail:"
tail -40 "$OUT"
exit 1
