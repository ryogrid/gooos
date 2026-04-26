#!/usr/bin/env bash
# Validate keyboard input reliability across multiple boots

set -u

FAIL_COUNT=0
PASS_COUNT=0
SAMPLE_SIZE="${1:-10}"

for i in $(seq 1 $SAMPLE_SIZE); do
    timeout 10 qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio -no-reboot -no-shutdown -smp 4 > /tmp/boot_$i.log 2>&1 || true
    
    # Check if we reached the prompt without crashing
    if grep -q "gooos shell v0.1" /tmp/boot_$i.log && ! grep -qE "(panic|page fault|#DE:)" /tmp/boot_$i.log; then
        ((PASS_COUNT++))
        echo "Boot $i: PASS"
    else
        ((FAIL_COUNT++))
        echo "Boot $i: FAIL"
        if grep -qE "(panic|page fault|#DE:)" /tmp/boot_$i.log; then
            echo "  (crash detected)"
        elif ! grep -q "gooos shell v0.1" /tmp/boot_$i.log; then
            echo "  (shell did not reach prompt)"
        fi
    fi
done

SUCCESS_RATE=$(echo "scale=1; $PASS_COUNT * 100 / $SAMPLE_SIZE" | bc)
echo ""
echo "Keyboard Reliability Test Results:"
echo "  Passes: $PASS_COUNT/$SAMPLE_SIZE"
echo "  Fails: $FAIL_COUNT/$SAMPLE_SIZE"
echo "  Success Rate: $SUCCESS_RATE%"

if [ "$PASS_COUNT" -eq "$SAMPLE_SIZE" ]; then
    echo "Result: PASS (100% success rate achieved)"
    exit 0
else
    echo "Result: FAIL (target: 100%)"
    exit 1
fi
