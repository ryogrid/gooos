# SMP v2 — Verification Plan

Per-phase test criteria, concurrency stress probes, and
regression matrix for validating every work plan item from
`smp_overview.md §4`.

## 1. Build-Clean Criterion

Every commit must satisfy:

```
$ make build    # exits 0, no warnings
$ make lint     # exits 0 (ISR-safety walker)
$ make verify-globals  # exits 0
```

No new `TODO`, `FIXME`, or `XXX` markers in product code.

## 2. QEMU Configuration

All SMP v2 tests run under:

```
$ make run-smp   # equivalent to: qemu-system-x86_64 -smp 4 ...
```

The `-smp 4` flag gives 1 BSP + 3 APs. Serial log is the
primary evidence channel. All automated harnesses read from the
serial log file.

For stress tests, also run with `-smp 8` and `-smp 16` to
exercise higher core counts.

## 3. Phase 0 — Foundation Tests

### Item 1: Per-CPU Storage (GS base)

| Test | Method | Pass Criterion |
|---|---|---|
| BSP cpuID | Serial output at boot | `"BSP cpuID=0"` in serial log |
| AP cpuID | Serial output per AP | `"AP N cpuID=N+1"` for each AP |
| GS base isolation | Each CPU reads own PerCPU block | No cross-CPU data corruption |
| Regression | `test_sendkey.sh 1` | `pf=0 exit=3 cat=1` |

### Item 2: Spinlock Primitive

| Test | Method | Pass Criterion |
|---|---|---|
| BSP acquire/release | Boot-time probe: acquire, release, re-acquire | No deadlock; serial `"spinlock: OK"` |
| Contention smoke | BSP acquires; interrupt fires; ISR does NOT try to acquire same lock (interrupts disabled) | No deadlock |
| Regression | `test_sendkey.sh 1` | `pf=0 exit=3 cat=1` |

### Item 3: Per-CPU GDT + TSS

| Test | Method | Pass Criterion |
|---|---|---|
| AP GDT load | Serial output per AP | `"AP N: GDT+TSS loaded"` |
| TSS.RSP0 per-CPU | Ring-3 shell runs on BSP | `test_sendkey.sh 1` PASS |
| Regression | All existing harnesses | No regression |

### Item 4: Per-CPU Interrupt Depth

| Test | Method | Pass Criterion |
|---|---|---|
| BSP ISR depth | Timer ISR fires; `interrupt.In()` == true inside handler | Serial confirms |
| AP ISR depth | LAPIC timer on AP; per-CPU counter increments | No cross-CPU corruption |
| Regression | `test_sendkey.sh 1` | `pf=0 exit=3 cat=1` |

## 4. Phase 1 — Kernel SMP Tests

### Item 5: LAPIC Register Definitions + EOI

| Test | Method | Pass Criterion |
|---|---|---|
| Build | `make build` | Clean |

### Item 6: LAPIC Timer Calibration + Per-AP Init

| Test | Method | Pass Criterion |
|---|---|---|
| Calibration | Serial output | `"LAPIC timer: N ticks/10ms"` (N > 0) |
| Per-AP fire | Each AP prints periodic heartbeat | At least 3 heartbeats per AP in 1 second |
| Regression | `test_sendkey.sh 1` | PASS |

### Item 7: IOAPIC Discovery + Redirection Table

| Test | Method | Pass Criterion |
|---|---|---|
| IOAPIC found | Serial output | `"IOAPIC: base=0xFEC00000, max_redir=N"` |
| IRQ routing | Keyboard + timer still work after IOAPIC takeover | `test_sendkey.sh 1` PASS |
| PIC disabled | PIC masked; IOAPIC drives IRQs | No spurious PIC interrupts |

### Item 8: Per-CPU Runqueues + systemStack

| Test | Method | Pass Criterion |
|---|---|---|
| BSP regression | Single-CPU behavior unchanged | `test_sendkey.sh 1` PASS |
| Per-CPU Pop | Serial: scheduler on CPU N pops from runqueues[N] | At least 1 pop per CPU |

### Item 9: Spinlock-Protected Queue

| Test | Method | Pass Criterion |
|---|---|---|
| Channel ops | `test_gochan.sh` under `-smp 4` | PASS |
| No deadlock | 10 × `test_sendkey.sh` trials | All `pf=0 exit=3 cat=1` |

### Item 10: Cross-CPU Wakeup in chan.go

| Test | Method | Pass Criterion |
|---|---|---|
| Chan ping-pong | Goroutine on CPU 0 sends to chan; receiver on CPU 1 wakes | Serial shows cross-CPU wakeup |
| IPI delivery | Serial confirms IPI sent + received | Wakeup latency < 1 ms |

### Item 11: AP Scheduler Spawn

| Test | Method | Pass Criterion |
|---|---|---|
| APs running | Serial: `"AP N: scheduler running"` for each AP | All APs enter scheduler loop |
| Goroutine distribution | Boot-time probe: 4 goroutines, each prints cpuID | At least 2 distinct CPUs observed |
| Shell works | `test_sendkey.sh 1` under `-smp 4` | PASS |

### Item 12: Shared Data Audit Fixes

| Test | Method | Pass Criterion |
|---|---|---|
| All harnesses | Run full matrix under `-smp 4` | All PASS |
| No data races | 10 × `test_sendkey.sh` trials under `-smp 4` | All `pf=0 exit=3 cat=1` |
| Atomic pitTicks | Serial: pitTicks monotonically increases | No backward jumps |

### Item 13: IPI Send Primitive + Wakeup Vector

| Test | Method | Pass Criterion |
|---|---|---|
| IPI smoke | BSP sends IPI to each AP; AP handler acks | Serial: `"IPI: AP N received"` per AP |
| Wakeup latency | Measure time from IPI send to AP serial output | < 1 ms |

### Item 14: Timer-Based Preemption

| Test | Method | Pass Criterion |
|---|---|---|
| Yield test | CPU-bound goroutine (tight loop) + cooperating goroutine on same CPU | Both make progress within 100 ms |
| No starvation | 4 CPU-bound goroutines, 1 per CPU + 1 shell | Shell still responds to keyboard |

## 5. Phase 2 — User SMP Tests

### Item 15: Ring-3 Goroutines on APs

| Test | Method | Pass Criterion |
|---|---|---|
| Shell on AP | `test_sendkey.sh 1` under `-smp 4` | PASS |
| User goroutines | `test_goprobe.sh` under `-smp 4` | PASS (all probes) |
| Cross-CPU exec | `test_pipe_matrix.sh` under `-smp 4` | All 4 cases PASS |
| cpuID trace | Temporarily add cpuID to gooosOnResume serial | Ring-3 goroutines seen on APs |

### Item 16: TLB Shootdown

| Test | Method | Pass Criterion |
|---|---|---|
| Rapid exec | Shell: `ls; ls; ls; ...` × 20 under `-smp 4` | No page faults on stale TLB |
| Pipe storm | `test_pipe_matrix.sh` × 5 under `-smp 4` | All PASS |
| Serial verify | No `"PF:"` lines in serial log | `pf=0` |

### Item 17: processExit Cross-CPU Cleanup

| Test | Method | Pass Criterion |
|---|---|---|
| Concurrent exit | `test_gochan.sh` under `-smp 4` | PASS |
| No deadlock | `test_pipe_matrix.sh` × 10 under `-smp 4` | All PASS, no hangs |
| Memory stable | Serial: no `"allocPage: out of memory"` after 20 execs | Stable memory |

## 6. Concurrency Stress Probes

### 6.1 Counter Equality Test

New boot-time kernel probe (gated by build flag, off in
release):

```go
func smpCounterStress() {
    var counters [maxCPUs]uint64
    var done uint32
    for cpu := 0; cpu < activeCPUs; cpu++ {
        go func(c int) {
            for atomic.LoadUint32(&done) == 0 {
                counters[c]++
            }
        }(cpu)
    }
    // Wait 1 second
    sleepTicks(100) // 100 ticks = 1 second at 100 Hz
    atomic.StoreUint32(&done, 1)
    // Check: all counters > 0 (every CPU ran goroutines)
    // Check: max/min ratio < 5 (rough balance)
}
```

### 6.2 Channel Ping-Pong Stress

```go
func smpChannelStress() {
    ch := make(chan int)
    go func() {
        for i := 0; i < 1000; i++ {
            ch <- i
        }
        close(ch)
    }()
    count := 0
    for range ch {
        count++
    }
    // Check: count == 1000
}
```

### 6.3 Multi-Process Spawn/Exit Stress

Shell script (`tmp/test_smp_stress.sh`):

```bash
# Run 10 rapid exec cycles under -smp 4
for i in $(seq 1 10); do
    send_line "ls"
    sleep 0.5
    send_line "hello"
    sleep 0.5
done
# Check: pf=0 in serial log
```

## 7. Regression Matrix

All existing test harnesses must PASS under `-smp 4`:

| Harness | Pass Criterion |
|---|---|
| `test_sendkey.sh` × 10 | All `pf=0 exit=3 cat=1` |
| `test_fd_probe.sh` | `contents=1 read_write=1 err=1 pf=0` |
| `test_redirect.sh` | `hello_lines=1 pf=0` |
| `test_pipe.sh` | `pf=0 exit=3 hello_lines=1 world_lines=1` |
| `test_wc_pipe.sh` | `echo_counts=1 file_counts=1 pf=0` |
| `test_pipe_matrix.sh` | All 4 cases `pf=0` |
| `test_goprobe.sh` | `pf=0 begin=1 go_chan=1 select=1 time_sleep=1 yield=1 all=1` |
| `test_gochan.sh` | `pf=0 sq=1/1/1/1/1 alpha=1 beta=1 fin=1` |
| `test_tinyc.sh` | `pf=0 s45=2 fib55=1 forsum=1` |
| `test_edit.sh` | `pf=0 hello=1` |

Harnesses currently use the default QEMU config (single CPU).
For SMP v2, add a parallel run under `-smp 4` (modify each
harness to accept an optional `SMP=4` env var, or create a
wrapper `test_smp_matrix.sh` that re-runs each harness with
`-smp 4`).

## 8. Diagnostic Instrumentation

For debugging during implementation (not shipped in release):

| Instrument | Purpose | Gating |
|---|---|---|
| `cpuID()` trace in `gooosOnResume` | Confirm Ring-3 goroutines migrate across CPUs | `const smpTrace = true` |
| IPI send/receive serial log | Confirm IPI delivery | `const ipiTrace = true` |
| Spinlock hold-time warning | Detect long critical sections | `const lockTrace = true` |
| Per-CPU scheduler pop/steal counts | Verify work distribution | `const schedTrace = true` |

All gated by compile-time constants; dead-code eliminated when
false.

## 9. Pass/Fail Summary per Phase

| Phase | Pass When |
|---|---|
| Phase 0 | Items 1-4 build clean; BSP regression harnesses green; APs report cpuID on serial |
| Phase 1 | APs enter scheduler; goroutines run on multiple CPUs; counter stress probe shows distribution; all harnesses green under `-smp 4` |
| Phase 2 | Ring-3 commands run on APs; TLB shootdown prevents stale faults; processExit clean; all harnesses green under `-smp 4` |
| Phase 3 | README updated; reviewer CRITICAL=0 MAJOR=0; no new TODO/FIXME markers |

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
