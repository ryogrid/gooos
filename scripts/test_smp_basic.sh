#!/usr/bin/env bash
# scripts/test_smp_basic.sh — M3-7 SMP goroutine distribution test.
#
# Boots the kernel under QEMU with -smp 4 and waits for evidence
# that a kernel goroutine ran on a non-BSP core. Two signals count:
#   1. smpBasicProbe() in src/main.go emits `smp_basic_cpu=N` once
#      per tick; we pass iff N != 0 is seen at least once.
#   2. ring3Wrapper's first print carries its cpuID; cpuID != 0
#      is additional evidence that the shell-spawn goroutine got
#      stolen to an AP.
#
# Exits 0 on PASS, 1 on FAIL (prints the log tail).

set -u

# §14 §6.2: under uniprocessorKernel the kernel runs as a
# uniprocessor on BSP and no kthread runs on any AP — the
# `smp_basic_cpu=N` distribution assertion is structurally
# false. Re-purposing for Ring-3 distribution is M7 follow-up.
if grep -q '^const uniprocessorKernel = true' src/preempt_config.go 2>/dev/null; then
    echo "test_smp_basic: SKIP under uniprocessorKernel"
    echo "result: SKIP — pending M7 Ring-3-on-AP dispatch (see no_goroutine_kernel_design/14_uniprocessor_kernel.md §6.2)"
    exit 0
fi

OUT="tmp/serial_smp_basic.log"
CONF="src/preempt_config.go"
BACKUP="tmp/preempt_config_smp_basic.go.bak"

# shellcheck source=harness_lib.sh
. "$(dirname "$0")/harness_lib.sh"
harness_recover_stale_backup "$CONF"

rm -f "$OUT"
rm -f "$BACKUP"

cp "$CONF" "$BACKUP"
restore_config() {
    if [ -f "$BACKUP" ]; then
        mv "$BACKUP" "$CONF"
        rm -f tmp/kernel.iso
    fi
}

trap restore_config EXIT

sed -i 's/const runSMPBasicProbe = false/const runSMPBasicProbe = true/' "$CONF"
if ! grep -q 'const runSMPBasicProbe = true' "$CONF"; then
    echo "FAIL: could not enable runSMPBasicProbe"
    exit 1
fi

make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }

qemu-system-x86_64 \
    -cdrom tmp/kernel.iso \
    -serial "file:$OUT" \
    -display none \
    -no-reboot -no-shutdown \
    -smp 4 &
PID=$!

# Wait up to 20s for PASS evidence. Check every 0.5s.
for _ in $(seq 1 40); do
    if [ -f "$OUT" ] && grep -qE 'smp_basic_cpu=[1-9]|cpuID=[1-9]' "$OUT"; then
        break
    fi
    sleep 0.5
done

sleep 0.5

kill "$PID" 2>/dev/null
wait "$PID" 2>/dev/null

if ! [ -f "$OUT" ]; then
    echo "FAIL: serial log not written"
    exit 1
fi

AP_HITS=$(grep -oE 'smp_basic_cpu=[0-9]+' "$OUT" 2>/dev/null |
    grep -oE '=[1-9]' | sort -u | wc -l)
R3_HITS=$(grep -oE 'cpuID=[1-9]' "$OUT" 2>/dev/null | sort -u | wc -l)

echo "test_smp_basic: ap_kernel_cpus=$AP_HITS ring3_ap_hits=$R3_HITS"

if [ "$AP_HITS" -ge 1 ] || [ "$R3_HITS" -ge 1 ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL — all observed cpuIDs were 0"
echo "--- serial log tail ---"
tail -40 "$OUT"
exit 1
