#!/usr/bin/env bash
# scripts/test_smp_release_gate.sh — DEFERRED 5 / P05 release-gate
# sampler.
#
# Runs each harness in HARNESSES $ITERATIONS times and records the
# per-harness PASS count. Exits 0 iff every harness hits
# ≥ $THRESHOLD_PERCENT %.
#
# Override via env:
#   ITERATIONS=N      (default 50, per Plan-05)
#   THRESHOLD_PERCENT (default 95, per Plan-05)
#   SKIP_HARNESSES='h1 h2'  skip a subset (space-separated basenames)
#
# Output:
#   tmp/release_gate.json  — machine-readable summary.
#   tmp/release_gate_<harness>_<run>.log  — per-run serial logs
#                                            (kept on failure only).

set -u

# §14 §6.2: under uniprocessorKernel the included SMP-distribution
# harnesses SKIP, which the 50-iter sampler interprets as 0 % PASS
# rate. Whole release gate is meaningful only after M7 (Ring-3
# distribution on APs) lands; SKIP at the wrapper level until then.
if grep -q '^const uniprocessorKernel = true' src/preempt_config.go 2>/dev/null; then
    echo "test_smp_release_gate: SKIP under uniprocessorKernel"
    echo "result: SKIP — pending M7 Ring-3-on-AP dispatch (see no_goroutine_kernel_design/14_uniprocessor_kernel.md §6.2)"
    exit 0
fi

# shellcheck source=harness_lib.sh
. "$(dirname "$0")/harness_lib.sh"

ITERATIONS="${ITERATIONS:-50}"
THRESHOLD_PERCENT="${THRESHOLD_PERCENT:-95}"
SKIP_HARNESSES="${SKIP_HARNESSES:-}"
SUMMARY="tmp/release_gate.json"

HARNESSES=(
    "scripts/test_smp_basic.sh"
    "scripts/test_smp_shell_distribution.sh"
    "scripts/test_smp_shell_smpprobe.sh"
    "scripts/test_smp_shell_preempt.sh"
    "scripts/test_sleeptest_shell.sh"
    "scripts/test_goprobe_shell.sh"
    "scripts/test_preempt_kernel.sh"
    "scripts/test_preempt_user.sh"
)

mkdir -p tmp

# Defensive recovery for any harness config left dirty by a prior
# kill -9.
harness_recover_stale_backup "src/preempt_config.go"

skip_set=" $SKIP_HARNESSES "

declare -A pass_count
declare -A fail_count

overall_pass=0

for h in "${HARNESSES[@]}"; do
    hbase=$(basename "$h" .sh)
    if [[ "$skip_set" == *" $hbase "* ]]; then
        echo "skip: $h"
        pass_count["$h"]=0
        fail_count["$h"]=0
        continue
    fi

    echo "=== running $h  ($ITERATIONS iterations) ==="
    p=0
    f=0
    for i in $(seq 1 "$ITERATIONS"); do
        # Each harness manages its own qemu + flag-flip cycle.
        # We just invoke and check exit status. Bound each call
        # via timeout so a stuck harness doesn't hang the sampler.
        if timeout 180 bash "$h" >/dev/null 2>&1; then
            p=$((p+1))
        else
            f=$((f+1))
            # Preserve a failure log if the harness produced one.
            logname="tmp/serial_$(echo "$hbase" | sed 's/^test_//').log"
            if [ -f "$logname" ]; then
                cp "$logname" "tmp/release_gate_${hbase}_run${i}.log" 2>/dev/null || true
            fi
        fi
    done
    pass_count["$h"]=$p
    fail_count["$h"]=$f

    rate=$(( p * 100 / ITERATIONS ))
    echo "$h: pass=$p fail=$f rate=${rate}%"

    if [ "$rate" -lt "$THRESHOLD_PERCENT" ]; then
        overall_pass=1
    fi
done

# Emit JSON summary.
{
    echo "{"
    echo "  \"iterations\": $ITERATIONS,"
    echo "  \"threshold_percent\": $THRESHOLD_PERCENT,"
    echo "  \"results\": {"
    first=1
    for h in "${HARNESSES[@]}"; do
        p=${pass_count["$h"]:-0}
        f=${fail_count["$h"]:-0}
        total=$((p + f))
        if [ "$total" -eq 0 ]; then
            rate=0
        else
            rate=$(( p * 100 / total ))
        fi
        if [ "$first" -eq 0 ]; then
            echo ","
        fi
        first=0
        printf '    "%s": {"pass": %d, "fail": %d, "rate_percent": %d}' "$h" "$p" "$f" "$rate"
    done
    echo
    echo "  },"
    if [ "$overall_pass" -eq 0 ]; then
        echo "  \"overall\": \"PASS\""
    else
        echo "  \"overall\": \"FAIL\""
    fi
    echo "}"
} > "$SUMMARY"

echo
echo "summary -> $SUMMARY"
cat "$SUMMARY"
echo

exit "$overall_pass"
