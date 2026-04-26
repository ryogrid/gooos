#!/usr/bin/env bash
# Test goprobe specifically via HMP

OUT="/tmp/goprobe_hmp_test.log"
MON_SOCK="/tmp/goprobe_hmp_test.mon.sock"
rm -f "$OUT" "$MON_SOCK"

cd /home/ryo/work/gooos

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

# Start QEMU in foreground with timeout
timeout 120 qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -monitor "unix:$MON_SOCK,server,nowait" \
    -display none \
    -no-reboot -no-shutdown \
    -smp 4 &
PID=$!

sleep 1

# Wait for shell
for i in $(seq 1 300); do
    [ -f "$OUT" ] && grep -q "Type 'help'" "$OUT" 2>/dev/null && break
    sleep 0.1
done

sleep 5

# Send goprobe via Python
python3 << 'PY'
import socket, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect("/tmp/goprobe_hmp_test.mon.sock")
s.recv(4096)
for c in "goprobe": s.sendall(f"sendkey {c}\n".encode()); time.sleep(0.3)
s.sendall(b"sendkey ret\n")
s.close()
PY

# Wait for goprobe
sleep 30

# Analyze results
echo "=== goprobe output ==="
grep "^goprobe:" "$OUT" 2>/dev/null || echo "(no goprobe output)"

echo ""
if grep -q "goprobe: ALL TESTS PASS" "$OUT" 2>/dev/null; then
    echo "result: PASS"
    exit 0
else
    echo "result: FAIL or incomplete"
    echo ""
    echo "=== Crash check ==="
    grep -E "(panic|page fault|#DE:)" "$OUT" 2>/dev/null || echo "(no crashes)"
    exit 1
fi
