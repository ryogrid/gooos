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

- [ ] **2. Boot-phase gating**
  - `src/smp.go`: `var bspBootDone uint32`; AP spins on it
    with `pause()` then calls `apSchedulerEntry()`.
  - `src/main.go`: `bspBootDone = 1` before `setupUserspace()`.
  - Verify: `-smp 4` boots; shell works; `ls` works;
    `test_sendkey.sh 1` PASS under `-smp 4`.

- [ ] **3. VGA console spinlock wrapping**
  - `src/vga.go`: wrap `vgaConsolePrint` + `vgaConsoleClear`
    with `vgaLock` (top-level only; NOT inner helpers).
  - Verify: `make build` clean; no deadlock under `-smp 4`.

- [ ] **4. Serial port lock**
  - `src/serial.go`: add `serialLock` spinlock around
    `serialPutChar`.
  - Verify: serial output not interleaved under `-smp 4`.

- [ ] **5. elfSpawn AP wakeup IPI**
  - `src/process.go:elfSpawn`: send IPI to wake an idle AP
    after `go ring3Wrapper(child)`.
  - Verify: `smpprobe` shows workers on different cpuIDs.

- [ ] **6. IOAPIC type-2 override parsing**
  - `src/smp.go:parseMADT`: handle type-2 entries.
  - `src/ioapic.go`: apply override for IRQ0.
  - Re-enable `ioapicInit()` in `src/main.go`.
  - Verify: `afterTicks: OK` under `-smp 4` with IOAPIC.
  - If PIT still fails: leave IOAPIC disabled, document.

- [ ] **7. Regression matrix**
  - All harnesses under `-smp 4`: test_sendkey x3,
    test_gochan, test_goprobe, test_tinyc, test_pipe_matrix.
  - `smpprobe` shows multiple cpuIDs.
  - Verify: all PASS, `pf=0`.

- [ ] **8. README.md update**
  - SMP progress row → AP scheduler participation.
  - Mention `smpprobe`.

## Deferred Items

(Append here if anything slips out of scope.)

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
