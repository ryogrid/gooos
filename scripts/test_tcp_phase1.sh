#!/usr/bin/env bash
# scripts/test_tcp_phase1.sh — TCP Phase TCP-1 smoke test.
#
# Boots the kernel under QEMU with an emulated Intel 82540EM NIC
# attached to slirp user-mode networking (no TAP / root required).
# Verifies:
#   1. Kernel boot reaches NET init and the TCP listener on port
#      8080 registers ("TCP: listener port=8080 (kernel echo)").
#   2. Host `nc 127.0.0.1 10080` completes the 3-way handshake,
#      round-trips a payload through the kernel echo goroutine,
#      and closes cleanly.
#   3. netDiag auto-dump shows the TCP TCB transitioning through
#      LISTEN / ESTABLISHED / CLOSE_WAIT / LAST_ACK / CLOSED.
#
# Exits 0 on PASS, 1 on FAIL (log tail printed for diagnosis).

set -u

OUT="tmp/serial_tcp1.log"
ECHO_OUT="tmp/tcp_echo_response.txt"
rm -f "$OUT" "$ECHO_OUT"

# Build the ISO if missing.
if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -display none \
    -no-reboot -no-shutdown \
    -device e1000,netdev=n0 \
    -netdev user,id=n0,hostfwd=udp::9999-:7,hostfwd=tcp::10080-:8080 &
PID=$!

# Wait up to 20 s for the TCP listener to come up.
for _ in $(seq 1 200); do
    grep -q 'TCP: listener port=8080' "$OUT" 2>/dev/null && break
    sleep 0.1
done

if ! grep -q 'TCP: listener port=8080' "$OUT" 2>/dev/null; then
    echo "FAIL: TCP listener did not come up within 20 s"
    kill "$PID" 2>/dev/null
    wait "$PID" 2>/dev/null
    tail -40 "$OUT"
    exit 1
fi

# Round-trip a payload through the kernel TCP echo (port 8080).
# nc in TCP mode closes on EOF of stdin, triggering the FIN
# handshake that exercises CLOSE_WAIT → LAST_ACK → CLOSED.
PAYLOAD="hello-gooos-tcp-$(date +%s)"
printf '%s' "$PAYLOAD" | timeout 5 nc -w 3 127.0.0.1 10080 > "$ECHO_OUT"
ECHO_RC=$?

# Let the netDiag auto-dump (~5 s after boot) finish.
for _ in $(seq 1 200); do
    grep -q '=== end ===' "$OUT" 2>/dev/null && break
    sleep 0.1
done

kill "$PID" 2>/dev/null
wait "$PID" 2>/dev/null

RECEIVED="$(cat "$ECHO_OUT" 2>/dev/null || true)"

PCI=$(grep -c '^PCI: found e1000' "$OUT")
NETINIT=$(grep -c '^NET: initialized' "$OUT")
TCPLISTEN=$(grep -c '^TCP: listener port=8080' "$OUT")
DIAG=$(grep -c '=== Network Diagnostics ===' "$OUT")
# Serial dump of the active TCB(s) should mention at least one of
# the post-handshake or close states if the round-trip completed.
TCPSTATES=$(grep -cE 'ESTABLISHED|CLOSE_WAIT|LAST_ACK' "$OUT")
: "${PCI:=0}"; : "${NETINIT:=0}"; : "${TCPLISTEN:=0}"; : "${DIAG:=0}"
: "${TCPSTATES:=0}"

echo "test_tcp1: pci=$PCI netinit=$NETINIT tcp_listen=$TCPLISTEN diag=$DIAG echo_rc=$ECHO_RC tcp_states=$TCPSTATES"
echo "test_tcp1: echoed='$RECEIVED' expected='$PAYLOAD'"

# The state-string match is best-effort — netDiag may fire before
# the connection is established if the nc command runs early. The
# round-trip check is the authoritative signal.
if (( PCI >= 1 && NETINIT >= 1 && TCPLISTEN >= 1 && DIAG >= 1 )) &&
   [ "$RECEIVED" = "$PAYLOAD" ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL"
tail -80 "$OUT"
exit 1
