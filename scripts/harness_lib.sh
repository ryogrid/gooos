#!/usr/bin/env bash
# scripts/harness_lib.sh — common setup/teardown helpers for test
# harnesses that mutate src/preempt_config.go.
#
# Source at the top of a harness. Provides:
#   harness_recover_stale_backup  — if a prior kill -9 left a backup
#     at tmp/preempt_config_*.go.bak, restore it before doing
#     anything else. Idempotent: safe to call multiple times.
#
# Motivation (G3 in TODO_FIX.md): a harness that was killed with
# SIGKILL bypasses its trap EXIT handler, leaving preempt_config.go
# with a flag flipped on and a backup file orphaned in tmp/. The
# next build picks up the flipped flag silently. This helper
# rescues that state.

harness_recover_stale_backup() {
    local conf="${1:-src/preempt_config.go}"
    shopt -s nullglob
    local backups=(tmp/preempt_config_*.go.bak)
    shopt -u nullglob
    if [ "${#backups[@]}" -eq 0 ]; then
        return 0
    fi
    # If CONF has any "= true" run*Test flag, a prior run leaked.
    if grep -qE 'const (runPreempt|runUserPreempt|runSMP|runGoprobeTest|runSleeputestTest|runYieldtestTest).*= true' "$conf"; then
        # Use the most recently modified backup to restore.
        local newest
        newest=$(ls -t "${backups[@]}" 2>/dev/null | head -n1)
        if [ -n "$newest" ] && [ -f "$newest" ]; then
            echo "harness_lib: restoring $conf from leaked backup $newest"
            cp "$newest" "$conf"
        fi
    fi
    # Remove all stale backups regardless; a clean slate is desired.
    for b in "${backups[@]}"; do
        rm -f "$b"
    done
}
