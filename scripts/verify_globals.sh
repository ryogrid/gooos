#!/usr/bin/env bash
# verify_globals.sh — assert TinyGo runtime globals that hold GC
# roots are placed inside the [_globals_start, _globals_end) range
# that findGlobals scans.
#
# A TinyGo upgrade or linker-script change that pushes one of
# these symbols outside the range would silently make the
# conservative collector miss live Task pointers and cause use-
# after-free of goroutine state. See impldoc/deferred_hygiene.md
# §4.
#
# Exits 0 on success, non-zero with a diagnostic on violation.

set -euo pipefail

KERNEL=${1:-tmp/kernel.bin}

if [[ ! -f "$KERNEL" ]]; then
    echo "verify-globals: $KERNEL does not exist" >&2
    exit 1
fi

start=$(nm "$KERNEL" | awk '$3 == "_globals_start" { print $1 }')
end=$(nm   "$KERNEL" | awk '$3 == "_globals_end"   { print $1 }')

if [[ -z "$start" || -z "$end" ]]; then
    echo "verify-globals: missing _globals_start/_globals_end in $KERNEL" >&2
    exit 1
fi

start_dec=$((16#$start))
end_dec=$((16#$end))

# Runtime symbols whose contents include *task.Task pointers.
# Only globals that actually appear in nm output are checked
# (TinyGo dead-code-eliminates unused ones; that's not an error).
pattern='^runtime[.](runqueue|runqueues|sleepQueue|timerQueue)$'

bad=0
checked=0
while read -r addr type name; do
    [[ -z "$addr" || -z "$name" ]] && continue
    a=$((16#$addr))
    if (( a < start_dec || a >= end_dec )); then
        printf 'verify-globals: %s @ 0x%s (%s) outside [_globals_start, _globals_end) [0x%s, 0x%s)\n' \
            "$name" "$addr" "$type" "$start" "$end" >&2
        bad=1
    fi
    checked=$((checked + 1))
done < <(nm "$KERNEL" | awk -v p="$pattern" '$2 ~ /^[bBdDrR]$/ && $3 ~ p { print $1, $2, $3 }')

if (( checked == 0 )); then
    echo "verify-globals: no runtime queue symbols found in $KERNEL" >&2
    exit 1
fi

if (( bad == 0 )); then
    echo "verify-globals: OK ($checked symbols inside [0x$start, 0x$end))"
fi

exit $bad
