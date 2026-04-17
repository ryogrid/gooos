#!/usr/bin/env bash
# scripts/test_net.sh — User-mode networking smoke test.
#
# Boots the kernel under QEMU with an emulated Intel 82540EM NIC
# attached to slirp user-mode networking (no TAP / root required).
# Verifies:
#   1. PCI discovery prints the expected markers.
#   2. MAC read + link-up prints.
#   3. NET layer initialises and sends gratuitous ARP.
#   4. ICMP echo-reply self-test passes.
#   5. netbuf pool lifecycle test passes.
#   6. UDP echo server listens on port 7 (hostfwd → host 9999).
#   7. Host `nc -u 127.0.0.1 9999` round-trips a payload through
#      the kernel echo server.
#   8. netDiag auto-dump prints the expected `=== Network
#      Diagnostics ===` block before QEMU is killed.
#
# Exits 0 on PASS, 1 on FAIL (with the log tail printed for diagnosis).

set -u

OUT="tmp/serial_net.log"
ECHO_OUT="tmp/udp_echo_response.txt"
rm -f "$OUT" "$ECHO_OUT"

# Build the ISO if missing (CI / fresh checkout).
if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -display none \
    -no-reboot -no-shutdown \
    -device e1000,netdev=n0 \
    -netdev user,id=n0,hostfwd=udp::9999-:7 &
PID=$!

# Wait up to 20 s for the UDP echo listener to come up.
for _ in $(seq 1 200); do
    grep -q 'UDP echo: listening' "$OUT" 2>/dev/null && break
    sleep 0.1
done

if ! grep -q 'UDP echo: listening' "$OUT" 2>/dev/null; then
    echo "FAIL: UDP echo server did not come up within 20 s"
    kill "$PID" 2>/dev/null
    wait "$PID" 2>/dev/null
    tail -40 "$OUT"
    exit 1
fi

# Round-trip a payload via host 9999 → guest 7.
PAYLOAD="hello-gooos-$(date +%s)"
printf '%s' "$PAYLOAD" | timeout 3 nc -u -w 2 127.0.0.1 9999 > "$ECHO_OUT"
ECHO_RC=$?

# Let the netDiag auto-dump (~5 s after boot) finish.
for _ in $(seq 1 200); do
    grep -q '=== end ===' "$OUT" 2>/dev/null && break
    sleep 0.1
done

kill "$PID" 2>/dev/null
wait "$PID" 2>/dev/null

RECEIVED="$(cat "$ECHO_OUT" 2>/dev/null || true)"

# Serial output uses CRLF — anchor on line start only, not end.
PCI=$(grep -c '^PCI: found e1000' "$OUT")
MAC=$(grep -c '^e1000: MAC=' "$OUT")
LINK=$(grep -c '^e1000: link up' "$OUT")
NETINIT=$(grep -c '^NET: initialized' "$OUT")
ARPGRAT=$(grep -c '^ARP: sent gratuitous' "$OUT")
ICMP=$(grep -c '^TEST: icmp echo reply PASS' "$OUT")
NETBUF=$(grep -c '^TEST: netbuf lifecycle PASS' "$OUT")
UDPLISTEN=$(grep -c '^UDP echo: listening' "$OUT")
DIAG=$(grep -c '=== Network Diagnostics ===' "$OUT")
: "${PCI:=0}"; : "${MAC:=0}"; : "${LINK:=0}"; : "${NETINIT:=0}"
: "${ARPGRAT:=0}"; : "${ICMP:=0}"; : "${NETBUF:=0}"
: "${UDPLISTEN:=0}"; : "${DIAG:=0}"

echo "test_net: pci=$PCI mac=$MAC link=$LINK netinit=$NETINIT arp_grat=$ARPGRAT icmp=$ICMP netbuf=$NETBUF udp_listen=$UDPLISTEN diag=$DIAG echo_rc=$ECHO_RC"
echo "test_net: echoed='$RECEIVED' expected='$PAYLOAD'"

if (( PCI >= 1 && MAC >= 1 && LINK >= 1 && NETINIT >= 1 &&
      ARPGRAT >= 1 && ICMP >= 1 && NETBUF >= 1 &&
      UDPLISTEN >= 1 && DIAG >= 1 )) &&
   [ "$RECEIVED" = "$PAYLOAD" ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL"
tail -60 "$OUT"
exit 1
