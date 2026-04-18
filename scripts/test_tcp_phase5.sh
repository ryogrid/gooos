#!/usr/bin/env bash
# scripts/test_tcp_phase5.sh — TCP Phase TCP-5 end-to-end verification.
#
# Automated path verifies:
#   - Kernel boots with both UDP + TCP hostfwds alive.
#   - Kernel TCP echo (Path D, hostfwd 10080 → 8080) round-trips.
#   - Phase 1-4 regression: UDP echo (Path A, hostfwd 9999 → 7).
#   - tcpecho.elf / tcpcli.elf are present in the in-memory fs.
#
# Manual path (documented inline at the tail): launch
# tcpecho.elf from the shell and round-trip through the
# userspace TCP echo (Path E, hostfwd 10081 → 8081).

set -u

OUT="tmp/serial_tcp5.log"
ECHO_OUT="tmp/tcp5_echo_response.txt"
UDP_OUT="tmp/tcp5_udp_response.txt"
rm -f "$OUT" "$ECHO_OUT" "$UDP_OUT"

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
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

for _ in $(seq 1 200); do
    grep -q '=== end ===' "$OUT" 2>/dev/null && break
    sleep 0.1
done

kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null

TCPRECV="$(cat "$ECHO_OUT" 2>/dev/null || true)"
UDPRECV="$(cat "$UDP_OUT" 2>/dev/null || true)"

PCI=$(grep -c '^PCI: found e1000' "$OUT")
TCPLISTEN=$(grep -c '^TCP: listener port=8080' "$OUT")
UDPLISTEN=$(grep -c '^UDP echo: listening' "$OUT")
DIAG=$(grep -c '=== Network Diagnostics ===' "$OUT")
TCPECHO_FS=$(grep -c 'tcpecho.elf:' "$OUT")
TCPCLI_FS=$(grep -c 'tcpcli.elf:' "$OUT")
: "${PCI:=0}"; : "${TCPLISTEN:=0}"; : "${UDPLISTEN:=0}"; : "${DIAG:=0}"
: "${TCPECHO_FS:=0}"; : "${TCPCLI_FS:=0}"

echo "test_tcp5: pci=$PCI tcp_listen=$TCPLISTEN udp_listen=$UDPLISTEN diag=$DIAG tcp_rc=$TCP_RC udp_rc=$UDP_RC tcpecho_fs=$TCPECHO_FS tcpcli_fs=$TCPCLI_FS"
echo "test_tcp5: TCP echoed='$TCPRECV' expected='$TCPPAYLOAD'"
echo "test_tcp5: UDP echoed='$UDPRECV' expected='$UDPPAYLOAD'"

if (( PCI >= 1 && TCPLISTEN >= 1 && UDPLISTEN >= 1 && DIAG >= 1 &&
      TCPECHO_FS >= 1 && TCPCLI_FS >= 1 )) &&
   [ "$TCPRECV" = "$TCPPAYLOAD" ] &&
   [ "$UDPRECV" = "$UDPPAYLOAD" ]; then
    echo "result: PASS"
else
    echo "result: FAIL"
    tail -80 "$OUT"
    exit 1
fi

cat <<'PREP'

--- Manual verification (userspace echo / client) -----------------

Path E — userspace tcpecho.elf:
1. Launch the kernel in interactive mode:
       make run-net
2. At the gooos shell prompt:
       tcpecho &
   Serial output shows "tcpecho: starting userspace echo on
   TCP port 8081".
3. From the host:
       echo hello | nc -w 3 127.0.0.1 10081
   Expected output: hello

Active-open from the guest (tcpcli):
1. On the host, start a listener before launching the guest:
       nc -l 10080
2. Launch the kernel (run-net).
3. At the gooos shell:
       tcpcli 10.0.2.2 10080 hi-from-gooos
   The nc listener receives "hi-from-gooos" (note: under QEMU
   slirp, 10.0.2.2 is the host's virtual gateway).

These paths are manual because the test script cannot yet
drive shell stdin through the guest's PS/2 input synchronously
with the hostfwd socket. Full automation is a reviewer-pass
follow-up.

--------------------------------------------------------------------

PREP

exit 0
