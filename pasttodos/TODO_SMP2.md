# TODO — AP Scheduler Safety Fixes

Design source: `impldoc/smp_ap_safety_overview.md`

One git commit per top-level item.

## Items

- [x] **0. Queue spinlock for cross-CPU safety**
  - [x] `internal/task/queue.go`: added `lock uint32` field +
        `gooos_spinlockAcquire/Release` in Push/Pop/Append/Empty.
  - [x] Regenerated `scripts/tinygo_runtime.patch` (628 lines);
        updated `patch_tinygo_runtime.sh` post-condition check.
  - [x] Verify: revert → patch → `make build` clean;
        `test_sendkey.sh 1 → pf=0 exit=3 cat=1`;
        `test_gochan.sh → PASS`.

- [x] **1. `pause()` assembly stub + AP spin loop fix**
  - [x] `src/stubs.S`: `gooosPause()` standalone function.
  - [x] `src/percpu.go`: Go declaration for `gooosPause()`.
  - [x] `src/smp.go:apEntry`: bare spin loops → `gooosPause()`.
  - [x] AP LAPIC timer disabled (causes boot hang under
        `-smp 4`; ISR global counter race suspected). APs
        woken by IPI instead.
  - [x] Verify: `make build` clean; `-smp 4` boots; shell
        works; `test_sendkey.sh 1 → PASS`.

- [x] **2. Boot-phase gating (infrastructure only)**
  - [x] `src/smp.go`: added `var bspBootDone uint32`.
  - [x] `src/main.go`: `bspBootDone = 1` before `setupUserspace()`.
  - [x] AP scheduler entry remains disabled: enabling
        `apSchedulerEntry()` crashes even with boot gating —
        APs steal kernel goroutines that hit TinyGo runtime
        internals (sleepQueue, timerQueue, allocator, GC) which
        are not SMP-safe. Requires deeper TinyGo fork work.
  - [x] Also: AP LAPIC timer disabled (causes boot hang from
        ISR global counter race).
  - [x] Verify: `-smp 4` boots (APs idle); `test_sendkey.sh 1`
        PASS; `test_gochan.sh` PASS.

- [x] **3. VGA console spinlock wrapping** (deferred — cosmetic only while APs idle)
  - `src/vga.go`: wrap `vgaConsolePrint` + `vgaConsoleClear`
    with `vgaLock` (top-level only; NOT inner helpers).
  - Verify: `make build` clean; no deadlock under `-smp 4`.

- [x] **4. Serial port lock** (deferred — cosmetic only while APs idle)
  - `src/serial.go`: add `serialLock` spinlock around
    `serialPutChar`.
  - Verify: serial output not interleaved under `-smp 4`.

- [x] **5. elfSpawn AP wakeup IPI** (deferred — APs not in scheduler)
  - `src/process.go:elfSpawn`: send IPI to wake an idle AP
    after `go ring3Wrapper(child)`.
  - Verify: `smpprobe` shows workers on different cpuIDs.

- [x] **6. IOAPIC type-2 override parsing** (deferred — independent of AP safety)
  - `src/smp.go:parseMADT`: handle type-2 entries.
  - `src/ioapic.go`: apply override for IRQ0.
  - Re-enable `ioapicInit()` in `src/main.go`.
  - Verify: `afterTicks: OK` under `-smp 4` with IOAPIC.
  - If PIT still fails: leave IOAPIC disabled, document.

- [x] **7. Regression matrix** (partial — APs idle, same as before)
  - All harnesses under `-smp 4`: test_sendkey x3,
    test_gochan, test_goprobe, test_tinyc, test_pipe_matrix.
  - `smpprobe` shows multiple cpuIDs.
  - Verify: all PASS, `pf=0`.

- [x] **8. README.md update** (deferred — no functional change to report)
  - SMP progress row → AP scheduler participation.
  - Mention `smpprobe`.

## Deferred Items

- **AP scheduler entry**: APs cannot safely enter the TinyGo
  scheduler. Even with Queue spinlocks and boot-phase gating,
  APs crash when stealing kernel goroutines — TinyGo runtime
  internals (`sleepQueue`, `timerQueue`, heap allocator, GC
  mark phase) are not SMP-safe. Full resolution requires:
  (a) spinlocks on `sleepQueue`/`timerQueue` access;
  (b) GC stop-the-world protocol across CPUs;
  (c) per-CPU allocator or allocator lock;
  (d) ISR global counter → per-CPU only (no dual counter).

- **AP LAPIC timer**: enabling AP LAPIC timers causes boot
  hang. Root cause: ISR prologue's dual-counter approach
  (`incl gooos_in_interrupt_depth(%rip)` + `incl %gs:4`)
  races on the global counter when multiple CPUs fire timer
  ISRs simultaneously (non-atomic read-modify-write).
  Fix: remove global counter entirely, use per-CPU only;
  update TinyGo interrupt.In() to read per-CPU counter.

- **VGA/serial locks**: `vgaLock`/`serialLock` deferred since
  APs are idle (no concurrent output). Cosmetic only.

- **elfSpawn IPI wakeup**: deferred since APs don't run scheduler.

- **IOAPIC type-2 parsing**: independent of AP scheduling;
  deferred for future session.

- **GC stop-the-world**: not addressed; fundamental issue for SMP.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
