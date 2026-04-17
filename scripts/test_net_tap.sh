#!/usr/bin/env bash
# scripts/test_net_tap.sh — TAP-mode integration test (root required).
#
# Brings up a dedicated tap0 with host IP 10.0.0.1/24, boots the
# kernel with `-netdev tap,ifname=tap0,script=no,downscript=no` so
# the guest and host share a virtual L2 segment (guest=10.0.0.2),
# then exercises the full IP stack end-to-end:
#
#   1. `ping -c 5 10.0.0.2` — proves ICMP echo-reply on real traffic.
#   2. `echo hi | nc -u -w2 10.0.0.2 7` — proves UDP echo against
#      the kernel echo server without the slirp hostfwd shim.
#
# NOT part of the per-phase CI gate; it depends on TAP/CAP_NET_ADMIN,
# which many environments do not grant. Run manually when developing
# the net stack on a machine that has those privileges.
#
# This script hard-codes 10.0.0.0/24 for the TAP segment, which differs
# from the static IP compiled into the kernel (10.0.2.15). It is
# therefore EXPECTED TO FAIL on the current kernel until a runtime IP
# reconfiguration path exists. Kept as a template for future work;
# see TODO_NET1.md / impldoc/net_dhcp_client.md.

set -u

if [ "$(id -u)" -ne 0 ]; then
    echo "test_net_tap: requires root (needed for ip tuntap)."
    exit 1
fi

OUT="tmp/serial_net_tap.log"
rm -f "$OUT"

# Build the ISO if missing.
if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

cleanup() {
    kill "${PID-}" 2>/dev/null || true
    wait "${PID-}" 2>/dev/null || true
    ip link set tap0 down 2>/dev/null || true
    ip tuntap del dev tap0 mode tap 2>/dev/null || true
}
trap cleanup EXIT

ip tuntap add dev tap0 mode tap user "$(id -un)" || true
ip addr flush dev tap0 2>/dev/null || true
ip addr add 10.0.0.1/24 dev tap0
ip link set tap0 up

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -display none \
    -no-reboot -no-shutdown \
    -device e1000,netdev=n0 \
    -netdev tap,id=n0,ifname=tap0,script=no,downscript=no &
PID=$!

# Wait up to 20 s for NET init.
for _ in $(seq 1 200); do
    grep -q 'NET: initialized' "$OUT" 2>/dev/null && break
    sleep 0.1
done

# Note: kernel IP is 10.0.2.15 — see the warning at the top of this
# file. Override TARGET if you rebuild with a 10.0.0.0/24 kernel IP.
TARGET="${GOOOS_NET_TAP_TARGET:-10.0.2.15}"

PING_OK=0
if ping -c 5 -W 2 "$TARGET" >/dev/null 2>&1; then
    PING_OK=1
fi

UDP_OUT="$(printf 'hi' | timeout 3 nc -u -w 2 "$TARGET" 7 || true)"

echo "test_net_tap: target=$TARGET ping_ok=$PING_OK udp='$UDP_OUT'"

if (( PING_OK == 1 )) && [ "$UDP_OUT" = "hi" ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL (see $OUT)"
tail -40 "$OUT" 2>/dev/null || true
exit 1
