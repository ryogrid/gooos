# Boot and Kernel Initialization — Delta

**Scope:** delta vs. `current_impl_0421_night/01_boot_and_kernel_init.md`. **Extends.** The baseline's step ordering (1–13) is still correct for everything not mentioned here; the three additions below slot into specific numbered steps.

## Summary of Changes Since `a384b1a`

1. `kernelThreadInit()` — new boot call in `src/main.go`, added by `e31b2bc` / refined by `961cb90`. Idempotent no-op in current Phase-4.3 form but cements the call-site so later phases can add per-CPU ready-queue preparation without re-touching boot.
2. `bootActivatePostShellReady()` — new post-shell-ready hook in `src/main.go`, added by `7826548` and extended by `8b75550` / `dfcd404` / `f758f9b`. Called from the Ring-3 side via syscall `#38 sys_shell_ready`. This is the new, explicit boundary between "boot" and "steady-state" — it ends the late initialization window and unblocks preempt fanout.
3. `restoreBSPVirtualWire()` — new BSP LAPIC/PIC virtual-wire reassertion called from `bootActivatePostShellReady()` in the non-IOAPIC path (`dfcd404`). Closes a class of boot transitions where IRQ1 could stop arriving after AP release.

## Current Design (replaces/augments baseline Step 10–13)

### Revised boot-sequence tail

Baseline step 10 ("Services and userspace artifacts") through step 13 ("Final transfer to userspace") are unchanged. The explicit `bspBootDone = 1` in baseline step 11 is now set **inside** `bootActivatePostShellReady()` and therefore occurs **after** shell Ring-3 entry, not before. Concretely:

1. `setupUserspace()` (`src/main.go:599`) launches `sh.elf` via `elfLoad`; APs are already spinning on `bspBootDone == 0`.
2. User shell `main()` in `user/cmd/sh/main.go:26` and `:29` calls `gooos.ShellReady()` immediately after printing the banner.
3. `ShellReady()` (`user/gooos/proc.go:134`) invokes `syscall0(sysShellReady)` — kernel-side handler `sysShellReadyHandler` at `src/userspace.go:617` calls `bootActivatePostShellReady()` (`src/main.go:604`).
4. `bootActivatePostShellReady()` executes once (guarded by `bootPostShellReadyDone`):
   - If `!ioapicActive`, `restoreBSPVirtualWire()` (`src/smp.go:116`) rewrites BSP LVT0=`0x00000700` (ExtINT, unmasked) and LVT1=`0x00000400` (NMI, unmasked); both PICs are unmasked at `pic1Data` / `pic2Data`.
   - `bspBootDone = 1` (`src/main.go:614`) — APs spinning in `apEntry` now progress through the APICID re-latch, LINT0/LINT1 mask, `sti()`, and `markAPSchedulerEntered()` path (see baseline §SMP Boot and `03_smp_preempt_phase_gating.md`).
   - Optional diagnostic launches (`runSMPBasicProbe`, `runSMPShellPreemptProbe`, `runPreemptProbe`) kick off here (lines 616–636).
   - `preemptPhaseAdvance(preemptPhaseSchedReady)` (`src/main.go:638`) advances the phase gate — see `03_smp_preempt_phase_gating.md` for the full state machine.

### `kernelThreadInit()` call-site

Inserted in `src/main.go:367`, immediately before `smpInit()` (baseline step 7). Body in `src/kernel_thread.go:58` is currently a no-op comment (`961cb90`); it exists so Phase 4.4 can add per-CPU stack preparation at exactly this point without re-wiring boot.

## Current Implementation Details

- **Entry point(s):** `main()` at `src/main.go`; key additions are lines `367` (`kernelThreadInit`), `594–600` (final `setupUserspace` transfer), and the `bootActivatePostShellReady` definition at `604–639`.
- **Guard variable:** `bootPostShellReadyDone uint32` at `src/main.go:602` — single-shot latch so repeat shell re-execs cannot re-trigger the BSP virtual-wire restore or re-advance the preempt phase.
- **Virtual-wire restore:** only fires when `ioapicActive == false`. In IOAPIC mode the existing IO-APIC routing carries IRQ1 and this path is intentionally skipped.
- **Syscall #38 round-trip:** Ring-3 `gooos.ShellReady()` → `syscall0(sysShellReady)` → `sysShellReadyHandler` (`src/userspace.go:614–620`) → `bootActivatePostShellReady()`. See `05_syscalls_and_shell_ready.md` for the syscall-table delta.
- **AP scheduler-entered marking:** `apEntry` in `src/smp.go:326` calls `markAPSchedulerEntered()` once per AP after `sti()` and before `apSchedulerEntry()`. This count is what `preemptPhase` uses to transition `SchedReady → Operational` — see `03_smp_preempt_phase_gating.md`.

## Diff-from-Baseline Notes

- Baseline Step 11 ("SMP scheduler release: `bspBootDone = 1`") is **moved** from pre-userspace main-thread bottom to inside `bootActivatePostShellReady()`. Order is preserved — the write still precedes any observation that depends on it — but the trigger is now event-driven (shell-ready syscall) rather than sequential.
- Baseline Step 12 ("Optional preempt probes gated by constants in `src/preempt_config.go`") now also happens inside `bootActivatePostShellReady()`. See `04_scheduler_and_kernel_thread.md` for the updated, enlarged flag set.
- `flaky_kbdproblem_fix/00_index.md` is a planning corpus (design docs) for the remaining instability; not an implementation event. Referenced here so coding agents know it exists.

## Open Questions / Known Gaps

- `bootActivatePostShellReady()` runs in the syscall ISR context of the shell's very first `int 0x80` to #38. It does heavy work (serial prints, spawn of diagnostic goroutines, phase-lock acquire). No reported faults, but the combination of "late boot transitions + first-ever ISR from Ring 3" is the same narrow window that exposed the original keyboard-IRQ race — see `smp_preempt_problem/README.md §3` for the open hypothesis.
- IOAPIC path (`ioapicActive == true`) is untested by the virtual-wire-restore mitigation; the current QEMU profile runs the non-IOAPIC path. If an IOAPIC deployment ever exhibits a similar late-boot IRQ loss, a symmetric restore for the IO-APIC RTEs will need to be added.
