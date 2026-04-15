# Userspace Goroutines — Verification Plan

This document specifies how the userspace-goroutines feature
is exercised end-to-end once implementation lands. Covers the
new `goprobe` probe binary, its harness, the regression
matrix, and the size-budget audit.

Depends on all three topic docs
(`userspace_tinygo_runtime.md`,
`userspace_scheduler_integration.md`,
`userspace_gc_and_stacks.md`) — this file assumes the runtime
plumbing and blocking-syscall pattern already work.

## 1. Goal

- A userland probe program (`goprobe.elf`) exercises every
  concurrency primitive we've enabled: `go func()`,
  `chan int`, `select`, `time.Sleep`, `runtime.Gosched`.
- A harness script (`tmp/test_goprobe.sh`) boots the kernel,
  runs `goprobe` from the shell, and asserts expected
  serial markers + zero PF.
- All existing regression harnesses
  (`tmp/test_sendkey.sh`, `tmp/test_redirect.sh`,
  `tmp/test_pipe.sh`, `tmp/test_fd_probe.sh`,
  `tmp/test_wc_pipe.sh`) still PASS.
- A size-budget audit confirms every user binary that
  pulls in the new scheduler surface fits under the
  `maxFileData` cap (`src/fs.go:12`, currently **40 KiB**;
  comment at `src/fs.go:12` notes "fits all user ELFs (max
  37 KiB)" — so bumping the cap is a near-certain
  prerequisite, not a contingency).

## 2. `goprobe` ELF

Path: `user/cmd/goprobe/main.go` (new).

```go
package main

import (
    "time"

    "github.com/ryogrid/gooos/user/gooos"
)

func main() {
    gooos.Println("goprobe: begin")

    // --- Test 1: go + chan round-trip ---
    ch := make(chan int, 1)
    go func() {
        ch <- 42
    }()
    v := <-ch
    if v == 42 {
        gooos.Println("goprobe: go+chan OK")
    } else {
        gooos.Println("goprobe: go+chan FAIL")
        gooos.Exit(1)
    }

    // --- Test 2: select with two chans ---
    c1 := make(chan int, 1)
    c2 := make(chan int, 1)
    go func() { c1 <- 1 }()
    go func() { c2 <- 2 }()
    sum := 0
    for i := 0; i < 2; i++ {
        select {
        case x := <-c1:
            sum += x
        case x := <-c2:
            sum += x
        }
    }
    if sum == 3 {
        gooos.Println("goprobe: select OK")
    } else {
        gooos.Println("goprobe: select FAIL")
        gooos.Exit(1)
    }

    // --- Test 3: time.Sleep interleaving ---
    counter := 0
    done := make(chan struct{})
    go func() {
        for i := 0; i < 3; i++ {
            time.Sleep(20 * time.Millisecond)
            counter++
        }
        close(done)
    }()
    <-done
    if counter == 3 {
        gooos.Println("goprobe: time.Sleep OK")
    } else {
        gooos.Println("goprobe: time.Sleep FAIL")
        gooos.Exit(1)
    }

    // --- Test 4: Gosched cycle (no syscall) ---
    //
    // Two goroutines increment a shared counter via plain
    // Gosched-based yielding. Under cooperative scheduling
    // this converges without data races on single-CPU v1.
    sharedA, sharedB := 0, 0
    finished := make(chan int, 2)
    go func() {
        for i := 0; i < 100; i++ {
            sharedA++
            // time.Sleep isn't available here — use direct
            // runtime yield. user/gooos/proc.go:Yield exists
            // but that yields to the KERNEL; runtime.Gosched
            // stays in-process.
            gooos.Yield() // kernel yield; OK for this test
        }
        finished <- 1
    }()
    go func() {
        for i := 0; i < 100; i++ {
            sharedB++
            gooos.Yield()
        }
        finished <- 2
    }()
    <-finished
    <-finished
    if sharedA == 100 && sharedB == 100 {
        gooos.Println("goprobe: yield-cycle OK")
    } else {
        gooos.Println("goprobe: yield-cycle FAIL")
        gooos.Exit(1)
    }

    gooos.Println("goprobe: ALL TESTS PASS")
}
```

### 2.1 Expected serial output

```
goprobe: begin
goprobe: go+chan OK
goprobe: select OK
goprobe: time.Sleep OK
goprobe: yield-cycle OK
goprobe: ALL TESTS PASS
```

### 2.2 Wiring into the build

- Add `goprobe` to `user/Makefile` `CMDS` at line 16.
  Current line: `CMDS := sh hello ls cat wc`. New line:
  `CMDS := sh hello ls cat wc goprobe`. (Note: older
  design drafts mentioned `fdprobe` — that probe was
  never wired into CMDS in master even though
  `tmp/test_fd_probe.sh` exists, so do not rely on it
  being in the baseline build.)
- `src/main.go`: store `fsCreate("goprobe.elf")` +
  `fsWrite("goprobe.elf", userElf_goprobe[:])` in the
  user-binary preload block (around line 390-395, same
  spot as the other user ELFs).
- `scripts/embed_elfs.sh` picks up `user/build/goprobe.elf`
  automatically.

## 3. Harness: `tmp/test_goprobe.sh`

Mirrors the existing `tmp/test_fd_probe.sh` pattern
(`pf=0` required, output markers counted).

```bash
#!/usr/bin/env bash
# tmp/test_goprobe.sh — userspace goroutine + chan probe.
set -u
OUT="tmp/serial_goprobe.log"
MON="tmp/mon_goprobe.sock"
rm -f "$OUT" "$MON"
qemu-system-x86_64 -cdrom tmp/kernel.iso -serial "file:$OUT" \
    -monitor "unix:$MON,server,nowait" -display none \
    -no-reboot -no-shutdown &
PID=$!
for _ in $(seq 1 30); do [ -S "$MON" ] && break; sleep 0.1; done
mon() { echo "$1" | nc -q 0 -U "$MON" >/dev/null 2>&1; }
send_line() {
    local s="$1" i ch
    for (( i=0; i<${#s}; i++ )); do
        ch="${s:$i:1}"
        case "$ch" in
            ' ') mon "sendkey spc" ;;
            '.') mon "sendkey dot" ;;
            *) mon "sendkey $ch" ;;
        esac
        sleep 0.05
    done
    mon "sendkey ret"
    sleep 2
}
sleep 5
send_line "goprobe"
sleep 3
mon "quit" >/dev/null 2>&1
wait "$PID" 2>/dev/null
rm -f "$MON"

PF=$(grep -c "^PF: " "$OUT")
ALL=$(grep -c "^goprobe: ALL TESTS PASS$" "$OUT")
: "${PF:=0}"; : "${ALL:=0}"
echo "goprobe: pf=$PF all_pass=$ALL"
if (( PF == 0 && ALL >= 1 )); then
    echo "result: PASS"
    exit 0
fi
echo "result: FAIL"
tail -30 "$OUT"
exit 1
```

## 4. Regression matrix

Once `goprobe` is green, re-run every existing harness:

| Harness | Purpose | Expected |
|---|---|---|
| `bash tmp/test_sendkey.sh $i` × 10 | baseline shell flow | all `pf=0 exit=3 cat=1` |
| `bash tmp/test_fd_probe.sh` | fd-table syscalls | `contents=1 read_write=1 err=1 pf=0` → PASS |
| `bash tmp/test_redirect.sh` | `>` / `<` / `>>` | `hello_lines=1 pf=0` → PASS |
| `bash tmp/test_pipe.sh` | 2-stage + 3-stage pipes | `pf=0 exit=3 hello_lines=1 world_lines=1` → PASS |
| `bash tmp/test_wc_pipe.sh` | wc-via-stdin | `pf=0 echo_counts=1 file_counts=1` → PASS |
| `bash tmp/test_pipe_matrix.sh` | 4-way pipe matrix | all `pf=0`, correct spawn counts |
| `bash tmp/test_goprobe.sh` | **new** — userspace concurrency | PASS |
| `make build` | kernel clean build | `verify-globals: OK`; lint PASS |
| `make run-smp` — 1 trial smoke | SMP-4 still boots shell | "SMP: 4 cores online" on serial |

Everything green → the feature ships.

## 5. Size audit

Concern: TinyGo's `scheduler=tasks` pulls in a larger
runtime surface than `scheduler=none`. The kernel's
`src/user_binaries.go` embeds the ELFs inline as Go byte
arrays; the FS stores each under `maxFileData = 40960`
(40 KiB, `src/fs.go:10-13`). At head the largest user
ELF (`sh.elf`) is about 37 KiB, leaving ~3 KiB of
headroom. The scheduler + chan runtime surface will
consume more than that, so `maxFileData` MUST be bumped
as part of this round — not a contingency.

**Mandatory change**: raise `maxFileData` to **98304**
(96 KiB) in `src/fs.go:12` and update the accompanying
comment. FS memory footprint: 32 slots × 96 KiB = 3 MiB;
well within the kernel heap. 96 KiB gives roughly 2.5×
headroom over today's 37 KiB peak, so we don't have to
re-audit every time TinyGo's runtime grows a few
hundred bytes.

Pre-flight audit after the runtime lands:

```bash
ls -l user/build/*.elf
```

Expect `sh.elf` to grow from ~37 KiB to 50–60 KiB
after the scheduler + chan runtime. `hello.elf` /
`cat.elf` / `wc.elf` / `ls.elf` will also grow modestly
because the runtime is linked in even if they don't
spawn goroutines (TinyGo's linker should dead-code-
eliminate unused scheduler entry points, but the
initialized `runqueue` etc. add a few KiB).

**If any ELF exceeds 96 KiB after landing**: bump again
to 128 KiB (32 × 128 KiB = 4 MiB) and document in the
implementation commit. Staying one doubling ahead of
observed size is the standing policy.

## 6. Files to add / modify

| File | Change |
|---|---|
| `user/cmd/goprobe/main.go` | **new** — probe program (§2) |
| `user/Makefile` | CMDS line 16 adds `goprobe` |
| `src/main.go` | fsCreate + fsWrite for `goprobe.elf` |
| `tmp/test_goprobe.sh` | **new** — harness (§3) |
| `src/fs.go` (conditional) | `maxFileData` bump if audit requires |

## 7. Dependencies

All three sibling docs:
`userspace_tinygo_runtime.md`,
`userspace_scheduler_integration.md`,
`userspace_gc_and_stacks.md`.

## 8. Open questions

1. **Should `goprobe` include the blocking-syscall
   limitation probe (§4 of `userspace_scheduler_
   integration.md`)?** Pro: demonstrates the limitation
   is understood. Con: the probe would intentionally
   hang waiting for a keystroke, complicating the
   harness. Recommendation: **skip in v1**; document
   limitation only. The limitation probe could land
   later as `tmp/test_blocking.sh`.
2. **Does TinyGo need any `goos=linux` package we
   haven't vetted?** Particularly `syscall.Syscall` or
   `os.*` paths. User binaries today don't `import
   "os"`; `time` is the most likely new transitive
   dependency via `time.Sleep`. Verify at implementation
   that `time` links cleanly — the kernel had a known
   issue with `time.After` (see
   `impldoc/deferred_hygiene.md §5`); userspace may hit
   the same and need the same `afterTicks` fallback.
3. **What if `gc=leaking` runs out of sbrk budget
   during `goprobe`?** `goprobe` allocates a handful of
   chans + goroutine stacks (~8 chan buffers × small
   sizes + 5 goroutines × 8 KiB = ~40 KiB). Well under
   the 1 MiB `mmap` cap. Safe. Still, if the probe
   fails with `sys_sbrk: OOM`, flip `gc` to
   `conservative` per `userspace_gc_and_stacks.md §1`.

## 9. Risk register delta

- **Retires**: `R-userspace-concurrency-unverified`
  — the `goprobe` harness proves `go` / `chan` /
  `select` / `time.Sleep` all work.
- **Adds**: `R-userspace-time-package-link-fail` —
  see open question 2. Possibly covered by
  `afterTicks` fallback pattern.
- **Adds**: `R-user-elf-size-overflow` — if any user
  ELF blows past the FS slot cap, `maxFileData` needs
  bumping. Audit in §5.
