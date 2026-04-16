#!/usr/bin/env bash
# verify_globals_user.sh — user-binary equivalent of
# scripts/verify_globals.sh. Asserts any TinyGo runtime globals
# that hold GC roots (runqueue, sleepQueue, timerQueue) live
# inside the [_globals_start, _globals_end) window that the
# conservative collector's findGlobals() scans.
#
# Takes an ELF path as argument; run once per user binary from
# user/Makefile's link rule.
#
# Differs from the kernel version in one way: a user ELF with
# zero matched queue symbols is OK (TinyGo dead-code-eliminates
# unused ones; smaller user programs may not retain any).
#
# Exits 0 on success, non-zero with a diagnostic on violation.

set -euo pipefail

ELF=${1:?usage: verify_globals_user.sh <user.elf>}

if [[ ! -f "$ELF" ]]; then
    echo "verify-globals-user: $ELF does not exist" >&2
    exit 1
fi

start=$(nm "$ELF" | awk '$3 == "_globals_start" { print $1 }')
end=$(nm   "$ELF" | awk '$3 == "_globals_end"   { print $1 }')

if [[ -z "$start" || -z "$end" ]]; then
    echo "verify-globals-user: missing _globals_start/_globals_end in $ELF" >&2
    exit 1
fi

start_dec=$((16#$start))
end_dec=$((16#$end))

# Runtime globals whose contents include *task.Task pointers.
pattern='^runtime[.](runqueue|sleepQueue|timerQueue)$'

bad=0
checked=0
while read -r addr type name; do
    [[ -z "$addr" || -z "$name" ]] && continue
    a=$((16#$addr))
    if (( a < start_dec || a >= end_dec )); then
        printf 'verify-globals-user: %s @ 0x%s (%s) outside [_globals_start, _globals_end) [0x%s, 0x%s) in %s\n' \
            "$name" "$addr" "$type" "$start" "$end" "$ELF" >&2
        bad=1
    fi
    checked=$((checked + 1))
done < <(nm "$ELF" | awk -v p="$pattern" '$2 ~ /^[bBdDrR]$/ && $3 ~ p { print $1, $2, $3 }')

if (( checked == 0 )); then
    # Tolerated: user program DCE'd all TinyGo runtime queues. The
    # bracket symbols still define a valid (possibly empty) scan
    # range for findGlobals.
    echo "verify-globals-user: $ELF — no runtime queues (OK, DCE)"
    exit 0
fi

if (( bad == 0 )); then
    echo "verify-globals-user: $ELF OK ($checked symbols inside [0x$start, 0x$end))"
fi

exit $bad
