#!/usr/bin/env bash
# scripts/test_tcp_phase2.sh — TCP Phase TCP-2 verification.
#
# Phase TCP-2 adds active open, retransmission, RTT estimation,
# FIN_WAIT states and the TIME_WAIT reaper. Most of the
# design-doc-specified tests (T2.1, T2.3, T2.6, T2.7) require TAP
# networking with raw-socket privileges so the host can:
#   - listen on a port for the guest to connect to (T2.1),
#   - drop packets to force retransmission (T2.3),
#   - capture pcap of the full FIN handshake (T2.6, T2.7).
#
# This script's executable path is the user-mode sanity check:
#   - Kernel boots and the TCP listener comes up (same as TCP-1).
#   - Round-trip through the echo server still works.
#   - The RTO scanner goroutine doesn't deadlock the kernel.
# The TAP tests are documented inline (block below) but NOT run
# from this script — raw-socket tooling is not guaranteed to be
# present in the current environment.
#
# Exits 0 on PASS of the user-mode sanity path.

set -u

OUT="tmp/serial_tcp2.log"
ECHO_OUT="tmp/tcp2_echo_response.txt"
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

# Round-trip to exercise 3WHS + data + peer-FIN close. This
# implicitly exercises the retxQ push (SYN|ACK) and pop (final
# ACK of 3WHS) paths added in items 2-3, and the CLOSE_WAIT →
# LAST_ACK → CLOSED path (already in Phase TCP-1).
PAYLOAD="tcp2-sanity-$(date +%s)"
printf '%s' "$PAYLOAD" | timeout 5 nc -w 3 127.0.0.1 10080 > "$ECHO_OUT"
ECHO_RC=$?

# Wait for netDiag auto-dump.
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

echo "test_tcp2: pci=$PCI tcp_listen=$TCPLISTEN diag=$DIAG echo_rc=$ECHO_RC"
echo "test_tcp2: echoed='$RECEIVED' expected='$PAYLOAD'"

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

The design-doc tests below need raw-socket privileges and a host-
side TAP interface. They are not run from this script; follow the
steps manually on a machine with root access.

Setup (run once as root):
    ip tuntap add dev tap0 mode tap user "$(id -un)"
    ip addr add 10.0.0.1/24 dev tap0
    ip link set tap0 up

Launch the kernel under TAP instead of slirp:
    sudo qemu-system-x86_64 -cdrom tmp/kernel.iso \
        -serial stdio -no-reboot -no-shutdown \
        -device e1000,netdev=n0 \
        -netdev tap,id=n0,ifname=tap0,script=no,downscript=no

Test T2.1 — 3-way handshake (active open). In a future commit,
`tcpActiveConnect` will be called from sys_connect (TCP-5 item
"sys_connect"). Until then, a kernel self-test goroutine can be
wired up in src/tcp.go (behind a build-tag) to call
`tcpActiveConnect(parseIPv4("10.0.0.1"), 10080)` at boot; start
a host-side listener first:
    nc -l 10080

Test T2.2 — connect timeout. With no host listener on the target
port, the SYN retransmits should follow the exponential back-off
schedule documented in impldoc/net_tcp_timers_and_rtt.md §6.2.
Observe tmp/serial_*.log for repeated SYN emissions + an eventual
tcbFree.

Test T2.3 — data retransmission under forced loss. Use `tc
netem` on tap0 to drop every 3rd TX:
    sudo tc qdisc add dev tap0 root netem loss 33%
then send 20 x 1-KiB segments via nc. Capture with tcpdump and
confirm retransmits appear. (Gated on the echo-server txBuf
refactor documented in item 2's commit message.)

Test T2.4 — RTT estimator convergence. After 100 segments under
the default user-mode configuration, the netDiag dump should
report rtoTicks ≈ 100 (1 s clamped). Observe via:
    grep -A2 'TCP TCBs' tmp/serial_*.log

Test T2.5 — Karn's rule. Force a retransmission (via tc netem
loss) then inspect that the RTT estimator's srttTicks does NOT
move in response to the retransmitted segment's ACK.

Test T2.6 — guest-initiated FIN. Needs a future `sys_close`
wrapper (TCP-5) to call tcpClose on an ESTABLISHED TCB, then
observe FIN_WAIT_1 → FIN_WAIT_2 → TIME_WAIT → CLOSED in both
pcap and the netDiag dump.

Test T2.7 — TIME_WAIT re-ACK. From the host, retransmit the
peer's FIN after TIME_WAIT has been entered; confirm the guest
responds with a pure ACK and timeWaitDeadline resets (visible
via a later netDiag showing the TCB still in TIME_WAIT).

--------------------------------------------------------------------

PREP

exit 0
