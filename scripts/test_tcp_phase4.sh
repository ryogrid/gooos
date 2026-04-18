#!/usr/bin/env bash
# scripts/test_tcp_phase4.sh — TCP Phase TCP-4 verification.
#
# Phase TCP-4 adds RFC 5681 congestion control: slow start,
# congestion avoidance, fast retransmit / recovery, and the
# RTO-triggered cwnd collapse. The design-doc tests T4.1-T4.5
# want to observe cwnd / ssthresh trajectories in netDiag +
# pcap while under induced loss. All of those are TAP-only
# (tc netem to drop packets, iperf-style flooding).
#
# This script runs the same user-mode sanity baseline as
# phase2.sh / phase3.sh. The CC bookkeeping is in place and
# initialised on every connection; it just isn't exercised by
# the single-segment round-trip, which is why there's no
# user-mode verification of the CC specifics.
#
# Exits 0 on PASS of the user-mode sanity path.

set -u

OUT="tmp/serial_tcp4.log"
ECHO_OUT="tmp/tcp4_echo_response.txt"
rm -f "$OUT" "$ECHO_OUT"

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -display none \
    -no-reboot -no-shutdown \
    -device e1000,netdev=n0 \
    -netdev user,id=n0,hostfwd=tcp::10080-:8080 &
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

PAYLOAD="tcp4-sanity-$(date +%s)"
printf '%s' "$PAYLOAD" | timeout 5 nc -w 3 127.0.0.1 10080 > "$ECHO_OUT"
ECHO_RC=$?

for _ in $(seq 1 200); do
    grep -q '=== end ===' "$OUT" 2>/dev/null && break
    sleep 0.1
done

kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null

RECEIVED="$(cat "$ECHO_OUT" 2>/dev/null || true)"
PCI=$(grep -c '^PCI: found e1000' "$OUT")
TCPLISTEN=$(grep -c '^TCP: listener port=8080' "$OUT")
DIAG=$(grep -c '=== Network Diagnostics ===' "$OUT")
: "${PCI:=0}"; : "${TCPLISTEN:=0}"; : "${DIAG:=0}"

echo "test_tcp4: pci=$PCI tcp_listen=$TCPLISTEN diag=$DIAG echo_rc=$ECHO_RC"
echo "test_tcp4: echoed='$RECEIVED' expected='$PAYLOAD'"

if (( PCI >= 1 && TCPLISTEN >= 1 && DIAG >= 1 )) &&
   [ "$RECEIVED" = "$PAYLOAD" ]; then
    echo "result: PASS"
else
    echo "result: FAIL"
    tail -80 "$OUT"
    exit 1
fi

cat <<'PREP'

--- TAP-mode tests (prepared, not executed) -----------------------

All T4.* tests need TAP + tc netem. See
scripts/test_tcp_phase2.sh for setup commands.

Test T4.1 — slow-start ramp. Send ~64 KiB of data through the
guest (needs the TCP-5 sys_tcp_send path OR a test-only echo
variant that stages bytes in txBuf). Observe pcap: first burst
2 × MSS, second 4 × MSS, doubling each RTT until ssthresh.

Test T4.2 — slow-start → CA transition. After a loss event
sets ssthresh, subsequent ACKs should grow cwnd linearly (one
MSS per RTT) rather than exponentially. Grep netDiag for
cwnd / ssthresh at the transition.

Test T4.3 — fast retransmit. Use tc netem loss 10% on tap0;
send 100 × 1-KiB segments. Observe pcap: retransmits appear
BEFORE the 1-second RTO floor, triggered by 3 dup-ACKs.

Test T4.4 — fast-recovery cwnd math. Immediately after fast
retransmit fires, cwnd should equal ssthresh + 3*mss (observed
via netDiag row).

Test T4.5 — RTO collapse. Block all outbound TX for > RTO;
observe netDiag: cwnd collapses to mssEff, ssthresh =
max(prior-flight/2, 2*mss).

All observations currently require the TCP-5 data-TX path.
Under the v1 kernel echo, the CC bookkeeping is initialised
on every ESTABLISHED TCB but isn't driven by meaningful data
transmission.

--------------------------------------------------------------------

PREP

exit 0
