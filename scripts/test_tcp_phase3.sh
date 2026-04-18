#!/usr/bin/env bash
# scripts/test_tcp_phase3.sh — TCP Phase TCP-3 verification.
#
# Phase TCP-3 adds flow-control bookkeeping: SWS-avoided receive-
# window advertisement, snd-window update guard, persist timer,
# delayed-ACK timer. The RFC-specified end-to-end tests T3.1–T3.6
# observe pcap traces of window values and ACK timing — these
# need TAP networking + tcpdump + a configurable host peer to
# back-pressure the guest.
#
# This script's executable path is the user-mode sanity check
# (same as phase2.sh). The flow-control code paths that are
# dormant in v1 (persist probe emission, piggyback-on-outbound)
# are explicitly called out in the TAP narrative at the tail.
#
# Exits 0 on PASS of the user-mode sanity path.

set -u

OUT="tmp/serial_tcp3.log"
ECHO_OUT="tmp/tcp3_echo_response.txt"
rm -f "$OUT" "$ECHO_OUT"

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

# --- User-mode sanity (non-privileged) ---

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

PAYLOAD="tcp3-sanity-$(date +%s)"
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

echo "test_tcp3: pci=$PCI tcp_listen=$TCPLISTEN diag=$DIAG echo_rc=$ECHO_RC"
echo "test_tcp3: echoed='$RECEIVED' expected='$PAYLOAD'"

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

All T3.* tests require TAP networking + tcpdump + a configurable
host-side peer. See the TAP setup block in
scripts/test_tcp_phase2.sh for common commands.

Test T3.1 — advertised rcv window shrinks. Run a host-side nc
that doesn't read from the guest; observe that the guest's
Window field in pcap drops from 8192 toward 0 as its rxBuf fills.

Test T3.2 — zero-window persist. Continuing from T3.1, observe
that after a silence window the persist timer fires a 1-byte
probe (exponential back-off: 1 s / 2 s / 4 s / ... capped at 60
s). NOTE: v1's echo server does not stage bytes in txBuf, so
the probe path emits only when the peer has queued data — this
is dormant until the echo-server / sys_tcp_send refactor
documented in TCP-2 item 2.

Test T3.3 — window reopen cancels persist. After the host
reads some bytes, the next outbound segment from guest should
carry the freshly-recovered Window value; the persist timer
disarms (`persistDeadline` clears).

Test T3.4 — SWS avoidance. Drain rxBuf byte-at-a-time from the
host side; the guest's advertised Window does NOT grow by 1 on
every read — it sits at lastAdvWin until growth reaches
min(mssEff, cap/2).

Test T3.5 — delayed ACK. Send a single segment and observe
pcap: the guest should emit an ACK ~200 ms later (scaffolding
only — current state machine sends immediate ACKs for
correctness; the delack field + scanner fire path are in
place, but the handler hasn't been switched from immediate
ACK to deferred ACK. Enabling is a one-line tweak in
tcpHandleEstablished once the echo path routes through txBuf).

Test T3.6 — every-other-segment accelerated ACK. Same
conditions as T3.5 but two segments arrive back-to-back; the
"ACK every second full-sized segment" rule (RFC 1122 §4.2.3.2)
should collapse the delay. Also dormant until delayed ACK is
switched on.

--------------------------------------------------------------------

PREP

exit 0
