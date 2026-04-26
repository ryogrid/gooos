#!/usr/bin/env bash
# Simple multi-boot validation: checks if shell boots and reaches prompt

set -u

SAMPLE_SIZE="${1:-5}"
PASS_COUNT=0

for i in $(seq 1 $SAMPLE_SIZE); do
    echo "Boot $i/$SAMPLE_SIZE..." >&2
    
    # Quick boot check - just verify shell reaches prompt
    timeout 12 qemu-system-x86_64 -cdrom tmp/kernel.iso \
        -serial stdio -no-reboot -no-shutdown -smp 4 \
        2>&1 | grep -q "gooos shell v0.1"
    
    if [ $? -eq 0 ]; then
        ((PASS_COUNT++))
        echo "  ✓ Shell reached prompt" >&2
    else
        echo "  ✗ Shell did not reach prompt" >&2
    fi
done

echo ""
echo "Results: $PASS_COUNT/$SAMPLE_SIZE boots successful"

if [ $PASS_COUNT -eq $SAMPLE_SIZE ]; then
    echo "Result: PASS (100% boot success)"
    exit 0
else
    SUCCESS_RATE=$((PASS_COUNT * 100 / SAMPLE_SIZE))
    echo "Result: $SUCCESS_RATE% success rate"
    exit 1
fi
