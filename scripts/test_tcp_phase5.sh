#!/usr/bin/env bash
# scripts/test_tcp_phase5.sh — TCP Phase TCP-5 end-to-end verification.
#
# Automated path verifies:
#   - Kernel boots with both UDP + TCP hostfwds alive.
#   - Kernel TCP echo (Path D, hostfwd 10080 → 8080) round-trips.
#   - Userspace tcpecho.elf (Path E, hostfwd 10081 → 8081) round-trips.
#     The shell is driven via the QEMU HMP monitor's sendkey command
#     so the test can type "tcpecho" + Enter without a human.
#   - Phase 1-4 regression: UDP echo (Path A, hostfwd 9999 → 7).
#   - tcpecho.elf / tcpcli.elf are present in the in-memory fs.
#
# tcpcli.elf (active-open from the guest) is still manual-only —
# it needs a host listener the test framework would have to
# juggle against the e1000 hostfwds. Documented at the tail.

set -u

OUT="tmp/serial_tcp5.log"
ECHO_OUT="tmp/tcp5_echo_response.txt"
ECHO_E_OUT="tmp/tcp5_user_echo_response.txt"
UDP_OUT="tmp/tcp5_udp_response.txt"
MON_SOCK="tmp/tcp5.mon.sock"
rm -f "$OUT" "$ECHO_OUT" "$ECHO_E_OUT" "$UDP_OUT" "$MON_SOCK"

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -monitor "unix:$MON_SOCK,server,nowait" \
    -display none \
    -no-reboot -no-shutdown \
    -device e1000,netdev=n0 \
    -netdev user,id=n0,hostfwd=udp::9999-:7,hostfwd=tcp::10080-:8080,hostfwd=tcp::10081-:8081 &
PID=$!

for _ in $(seq 1 200); do
    grep -q 'TCP: listener port=8080' "$OUT" 2>/dev/null && break
    sleep 0.1
done

if ! grep -q 'TCP: listener port=8080' "$OUT" 2>/dev/null; then
    echo "FAIL: TCP listener did not come up within 20 s"
    kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null
    tail -40 "$OUT"
    exit 1
fi

# Path D: kernel TCP echo.
TCPPAYLOAD="tcp5-kernel-$(date +%s)"
printf '%s' "$TCPPAYLOAD" | timeout 5 nc -w 3 127.0.0.1 10080 > "$ECHO_OUT"
TCP_RC=$?

# Path A regression: UDP echo.
UDPPAYLOAD="tcp5-udp-$(date +%s)"
printf '%s' "$UDPPAYLOAD" | timeout 3 nc -u -w 2 127.0.0.1 9999 > "$UDP_OUT"
UDP_RC=$?

# Path E: userspace tcpecho.elf. Drive the gooos shell via
# QEMU HMP sendkey so the test is self-contained.
#
# hmp_send_many: push a sequence of monitor commands into the
# HMP socket in one connection. OpenBSD nc doesn't cooperate
# well with HMP's line-editing echo (commands come back as
# character-by-character repaints), so a short Python client
# that opens the socket once and sends the whole batch is more
# reliable than nc -U for this purpose.
hmp_send_many() {
    # Arguments are monitor commands, one per argv slot.
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

# Wait for the shell prompt marker ("Type 'help' ..."). If it
# never appears, Ring-3 isn't up or the kernel panicked.
for _ in $(seq 1 200); do
    grep -q "Type 'help' for available commands" "$OUT" 2>/dev/null && break
    sleep 0.1
done

if ! grep -q "Type 'help' for available commands" "$OUT" 2>/dev/null; then
    echo "FAIL: shell prompt did not appear within 20 s"
    kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null
    tail -40 "$OUT"
    exit 1
fi

# Settle so the shell's ReadLine is parked on the keyboard
# channel before we start injecting keys. The boot-time
# testAfterTicks print lands AFTER the shell prompt, so give it
# a beat to arrive and the shell to flush.
sleep 3

# Send a dummy backspace first; QEMU -display none sometimes
# swallows the first post-boot keystroke silently, and a
# leading backspace is idempotent for an empty ReadLine
# buffer.
hmp_send_many \
    'sendkey backspace' \
    'sendkey t' \
    'sendkey c' \
    'sendkey p' \
    'sendkey e' \
    'sendkey c' \
    'sendkey h' \
    'sendkey o' \
    'sendkey ret'

for _ in $(seq 1 100); do
    grep -q 'tcpecho: starting' "$OUT" 2>/dev/null && break
    sleep 0.1
done

if ! grep -q 'tcpecho: starting' "$OUT" 2>/dev/null; then
    echo "FAIL: tcpecho.elf did not start within 10 s after sendkey"
    kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null
    tail -40 "$OUT"
    exit 1
fi

E_PAYLOAD="tcp5-user-$(date +%s)"
printf '%s' "$E_PAYLOAD" | timeout 5 nc -w 3 127.0.0.1 10081 > "$ECHO_E_OUT"
E_RC=$?

for _ in $(seq 1 200); do
    grep -q '=== end ===' "$OUT" 2>/dev/null && break
    sleep 0.1
done

kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null

TCPRECV="$(cat "$ECHO_OUT" 2>/dev/null || true)"
UDPRECV="$(cat "$UDP_OUT" 2>/dev/null || true)"
ERECV="$(cat "$ECHO_E_OUT" 2>/dev/null || true)"

PCI=$(grep -c '^PCI: found e1000' "$OUT")
# §M7: under userspaceSMP=true the boot shell prints "$ " before
# the TCP/UDP service kthreads emit their "listening" lines,
# producing "$ TCP: listener port=8080" / "$ UDP echo: listening".
# Allow the optional shell-prompt prefix.
TCPLISTEN=$(grep -cE '(^|^\$ )TCP: listener port=8080' "$OUT")
UDPLISTEN=$(grep -cE '(^|^\$ )UDP echo: listening' "$OUT")
DIAG=$(grep -c '=== Network Diagnostics ===' "$OUT")
TCPECHO_FS=$(grep -c 'tcpecho.elf:' "$OUT")
TCPCLI_FS=$(grep -c 'tcpcli.elf:' "$OUT")
TCPECHO_START=$(grep -c 'tcpecho: starting' "$OUT")
: "${PCI:=0}"; : "${TCPLISTEN:=0}"; : "${UDPLISTEN:=0}"; : "${DIAG:=0}"
: "${TCPECHO_FS:=0}"; : "${TCPCLI_FS:=0}"; : "${TCPECHO_START:=0}"

echo "test_tcp5: pci=$PCI tcp_listen=$TCPLISTEN udp_listen=$UDPLISTEN diag=$DIAG tcp_rc=$TCP_RC udp_rc=$UDP_RC tcpecho_fs=$TCPECHO_FS tcpcli_fs=$TCPCLI_FS tcpecho_start=$TCPECHO_START e_rc=$E_RC"
echo "test_tcp5: Path D echoed='$TCPRECV' expected='$TCPPAYLOAD'"
echo "test_tcp5: Path E echoed='$ERECV' expected='$E_PAYLOAD'"
echo "test_tcp5: UDP    echoed='$UDPRECV' expected='$UDPPAYLOAD'"

if (( PCI >= 1 && TCPLISTEN >= 1 && UDPLISTEN >= 1 && DIAG >= 1 &&
      TCPECHO_FS >= 1 && TCPCLI_FS >= 1 && TCPECHO_START >= 1 )) &&
   [ "$TCPRECV" = "$TCPPAYLOAD" ] &&
   [ "$ERECV" = "$E_PAYLOAD" ] &&
   [ "$UDPRECV" = "$UDPPAYLOAD" ]; then
    echo "result: PASS"
else
    echo "result: FAIL"
    tail -100 "$OUT"
    exit 1
fi

cat <<'PREP'

--- Manual verification (tcpcli active-open) ----------------------

Active-open from the guest (tcpcli) is still manual-only:
1. On the host, start a listener on a port NOT in the run-net
   hostfwd list (10080/10081/9999/19999 are all claimed by
   QEMU). For example:
       nc -l 5555
2. Launch the kernel (run-net).
3. At the gooos shell:
       tcpcli 10.0.2.2 5555 hi-from-gooos
   The nc listener receives "hi-from-gooos" (note: under QEMU
   slirp, 10.0.2.2 is the host's virtual gateway).

--------------------------------------------------------------------

PREP

exit 0
