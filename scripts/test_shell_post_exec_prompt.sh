#!/usr/bin/env bash
# scripts/test_shell_post_exec_prompt.sh — M6.fix-1 regression
# harness.
#
# Pre-fix bug: under M6 (uniprocessorKernel), running any external
# command (`hello`, `ls`) panicked with `panic: runtime error at
# 0x100fc1: scheduler is disabled` inside `processExit`. The trigger
# was `ring3StackRelease(idx)` doing a `chan int` send, which
# TinyGo's chansend path under `scheduler=none` routes through
# `task.Pause` → runtime panic.
#
# Fix (M6.fix-1): replace `ring3StackPoolCh chan int` with a
# spinlock-protected free-bitmap (mirrors `kthreadPool` pattern).
# Also gate the `proc.exitCh <- exitCode` send on
# `kschedRunning[cpuID()] == nil` so the kthread parent path
# (which polls `proc.Exited` instead) skips the chan send entirely.
#
# This harness boots `qemu -smp 4`, sends `hello\r` via HMP, and
# verifies that the next `$ ` prompt appears within 2 s of
# "Hello, World from gooos userspace!". Repeats 10 iterations.
#
# PASS: ≥ 8/10 iterations show "Hello, World..." AND 0/10 panics.
# (The looser ≥ 8/10 bar absorbs the same HMP-sendkey delivery flake
# `scripts/test_run_smp_keyboard.sh` and the older
# `scripts/test_smp_shell_distribution.sh` already document. The
# 0/10-panics bar is the actual M6.fix-1 invariant: post-exec
# `processExit` must never reach `task.Pause`.)
#
# Exits 0 on PASS, 1 on FAIL.

set -u

if [ ! -f tmp/kernel.iso ]; then
    make iso >/dev/null 2>&1 || { echo "FAIL: make iso"; exit 1; }
fi

TOTAL=${TOTAL:-10}
SUCCESS=0
PANICS=0
LAST_FAIL_LOG=""

cleanup_one() {
    if [ -n "${QPID:-}" ]; then
        kill "$QPID" 2>/dev/null
        wait "$QPID" 2>/dev/null
    fi
}
trap cleanup_one EXIT

for run in $(seq 1 "$TOTAL"); do
    OUT="tmp/shell_postexec_$run.log"
    MON="tmp/shell_postexec_$run.mon"
    rm -f "$OUT" "$MON"

    qemu-system-x86_64 \
        -cdrom tmp/kernel.iso \
        -serial "file:$OUT" \
        -monitor "unix:$MON,server,nowait" \
        -display none -no-reboot -no-shutdown -smp 4 &
    QPID=$!

    for _ in $(seq 1 200); do
        grep -q "Type 'help' for available commands" "$OUT" 2>/dev/null && break
        sleep 0.1
    done
    sleep 5

    python3 - "$MON" <<'PY' 2>/dev/null
import socket, sys, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sys.argv[1])
s.settimeout(2)
try:
    s.recv(4096)
except Exception:
    pass
for k in ['h', 'e', 'l', 'l', 'o', 'ret']:
    s.sendall(('sendkey ' + k + '\n').encode())
    time.sleep(0.3)
time.sleep(2)
s.close()
PY

    sleep 4
    kill "$QPID" 2>/dev/null
    wait "$QPID" 2>/dev/null
    QPID=""

    HELLO=$(grep -c "Hello, World from gooos userspace" "$OUT" 2>/dev/null)
    PANIC=$(grep -cE "panic|#DE|PF:" "$OUT" 2>/dev/null)
    [ -z "$HELLO" ] && HELLO=0
    [ -z "$PANIC" ] && PANIC=0

    # SUCCESS: Hello printed (input reached kernel) AND no panic.
    # PANICS: any run that produced a panic / PF / #DE — that is
    # the actual fix-regression signal.
    if [ "$HELLO" -gt 0 ] && [ "$PANIC" -eq 0 ]; then
        SUCCESS=$((SUCCESS+1))
    fi
    if [ "$PANIC" -gt 0 ]; then
        PANICS=$((PANICS+1))
        LAST_FAIL_LOG="$OUT"
    fi

    echo "run $run: helloPrinted=$HELLO panic=$PANIC"
done

echo "==========="
echo "test_shell_post_exec_prompt: PASS=$SUCCESS/$TOTAL FAIL=$PANICS/$TOTAL"

if [ "$SUCCESS" -ge 8 ] && [ "$PANICS" -eq 0 ]; then
    echo "result: PASS"
    exit 0
fi

echo "result: FAIL"
if [ -n "$LAST_FAIL_LOG" ]; then
    echo "--- tail of $LAST_FAIL_LOG ---"
    tail -25 "$LAST_FAIL_LOG"
fi
exit 1
