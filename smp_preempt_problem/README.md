# SMP Preempt/Input Stability Status

This note summarizes the latest mitigations already landed in the tree, the behavior observed on the most recent `make run-smp` runs, and the current repair direction.

It is intentionally split into **confirmed facts** and **working hypotheses**. The goal is to capture the current state accurately without overstating root-cause confidence.

## Scope

The current problem is no longer just "keyboard input sometimes fails under SMP".

The latest runs show four related symptoms:

1. The first interactive keyboard input after `make run-smp` succeeds more often than before, but it is still not fully reliable.
2. When input does succeed, `gochan` can either complete normally or hang.
3. When `smpprobe` runs successfully, its workers can still all report `cpuID=0`, even though all APs came online during boot.
4. A bare `0x...` value can appear next to the shell prompt, which makes the console trace harder to interpret.

## Recently Landed Mitigations

### 1. Keyboard reads no longer depend on a dedicated pump goroutine

The old `keyboardPump -> keyboardCh -> stdin reader` path has been removed.

The current path is:

- IRQ1 stores keyboard events into the lock-free IRQ ring.
- Blocking stdin readers drain that ring directly through `keyboardReadEventBlocking()`.

Relevant files:

- `src/keyboard_irq.go`
- `src/fd.go`
- `src/userspace.go`
- `src/keyboard.go`

**Why this matters:** the previous design depended on a Go-channel wakeup path that was vulnerable to cross-CPU timing and parking behavior. The new path keeps keyboard delivery off the Go channel and much closer to the IRQ source.

### 2. BSP-side keyboard polling fallback was added

If no keyboard IRQ has been observed yet, the BSP can poll the PS/2 controller status/data ports and feed the same IRQ ring through `pollKeyboardFallback()`.

Relevant files:

- `src/keyboard.go`
- `src/pit.go`

**Why this matters:** this gives the system a second path when IRQ1 delivery disappears after SMP boot transitions.

### 3. BSP virtual-wire state is restored after shell-ready handoff

After `sys_shell_ready`, the kernel now reasserts the BSP-side PIC pass-through configuration in the non-IOAPIC path through `restoreBSPVirtualWire()`.

Relevant files:

- `src/main.go`
- `src/smp.go`

**Why this matters:** one working theory was that late boot transitions could leave legacy IRQ delivery in a degraded state after the shell became interactive.

### 4. Diagnostics were extended to distinguish IRQ vs poll fallback

`netDiag()` now reports whether the keyboard path has seen:

- a real IRQ (`kbdIRQ:seen`)
- only the polling fallback (`kbdIRQ:poll`)
- neither (`kbdIRQ:never`)

Relevant file:

- `src/net.go`

## Confirmed Current Status

Based on the latest reported run:

- Boot reaches the shell consistently enough to continue interactive investigation.
- AP startup still completes: the run reported `SMP: 4 cores online` and printed AP-online messages.
- Keyboard behavior is **improved but not solved**. This is a real improvement over the previous baseline, not a complete fix.
- `gochan` is **not deterministically broken**. It can complete successfully in some runs.
- `smpprobe` is **not proving multi-core user-process distribution reliably**. In the latest successful sample, every worker printed `cpuID=0`.
- The console output is still noisy enough that at least one prompt-adjacent `0x...` line appeared without clear context.

## Working Hypotheses

These are not yet fully proven, but they are the most useful current hypotheses.

### A. Keyboard input was only the first visible failure surface

The direct IRQ-ring reader, poll fallback, and virtual-wire restore appear to have improved the "cannot type at all" ratio, but they do not explain:

- `gochan` hanging after successful input
- `smpprobe` staying on CPU 0

That strongly suggests the remaining instability is centered in the post-shell SMP/runtime path, not just in keyboard delivery.

### B. The shell-ready transition is still a critical boundary

`bootActivatePostShellReady()` is now part of the keyboard fix story, but the same transition is also where:

- APs are released into steady-state scheduling
- post-boot wake/preempt behavior becomes relevant
- user processes start to exercise `Spawn`, `Wait`, and Ring-3 resume paths repeatedly

This makes the shell-ready boundary the most likely common point between the keyboard improvement and the remaining SMP issues.

### C. The `smpprobe` result suggests AP wake/scheduling is still incomplete after boot

`smpprobe` showing only `cpuID=0` despite all APs being online means at least one of these is still wrong or unstable:

1. APs are online but not actually draining runnable Ring-3 work after shell handoff.
2. `schedulerWake` / idle wakeup timing is insufficient for the spawned user processes.
3. Work stealing is live in theory but not effective in this execution window.
4. A runtime invariant around secondary-core participation is still inconsistent with the current SMP configuration.

The last point is especially important because `scripts/tinygo_runtime.patch` still keeps `secondaryCoresStarted` on a historically conservative path, with comment text that reflects an older M3-era safety argument. That does **not** mean `stealWork()` is currently disabled; the scheduler patch does call `stealWork()` when the local queue is empty. The open question is whether the remaining `secondaryCoresStarted`/GC assumption still matches the current "live work stealing" runtime behavior closely enough.

### D. `gochan` hanging may be a process-lifecycle or user-runtime symptom

`gochan` itself is small and normally straightforward:

- buffered channels
- short-lived goroutines
- a simple `select`

If it hangs only intermittently under SMP, the more likely suspects are:

- `processWait()` / `processExit()` synchronization
- Ring-3 resume metadata (`TSS.RSP0`, CR3 handoff, current process bookkeeping)
- the userspace runtime wait path under SMP timing

### E. The bare `0x...` prompt noise is probably a logging/serialization problem

The current tree still has many split `serialPrint()` sequences in boot and SMP code paths.

The latest boot log already shows line interleaving such as mixed AP/LAPIC output, so the prompt-adjacent `0x...` line is most likely:

- an incomplete fragment from another serial message, or
- a valid message whose prefix/suffix was interleaved away

That means console cleanliness is not just cosmetic here; it directly affects diagnosis quality.

## Near-Term Repair Direction

### 1. Build a deterministic repro harness first

The next harness should capture, per run:

- keyboard usable vs unusable
- `gochan` complete vs hang
- `smpprobe` distributed vs all-on-CPU0
- prompt-adjacent stray output

This should avoid flaky HMP `sendkey` dependency and reuse the existing shell/autorun style already present in the tree where possible.

### 2. Make critical SMP serial output line-atomic

The goal is not a full logging redesign. The immediate goal is to make the boot/AP/prompt-adjacent messages trustworthy enough for diagnosis.

Likely targets include:

- AP online logs
- LAPIC calibration logs
- BSP/AP boot identity logs
- any prompt-time diagnostic path that can emit raw hex fragments

### 3. Audit the post-shell scheduler/runtime boundary

Focus areas:

- `bootActivatePostShellReady()`
- AP idle/wake path
- `schedulerWake`
- `waitForEvents`
- `stealWork`
- `secondaryCoresStarted` assumptions in the TinyGo runtime patch

Primary objective: explain why `smpprobe` workers can remain on CPU 0 after a nominal 4-core boot.

### 4. Audit the `gochan` hang path separately from keyboard input

The `gochan` issue should be treated as a second-order SMP/runtime problem, not as a keyboard bug.

Focus areas:

- `src/process.go`
- `src/goroutine_tss.go`
- user runtime wait/scheduling behavior

### 5. Keep docs and repro artifacts aligned with the investigation

Once the next fixes land, this note should be updated so that:

- landed mitigations stay distinguishable from hypotheses
- the latest observed behavior is preserved
- future debugging starts from the current state instead of rediscovering it

## Main Code Surfaces To Revisit

- `src/keyboard.go`
- `src/keyboard_irq.go`
- `src/fd.go`
- `src/userspace.go`
- `src/main.go`
- `src/process.go`
- `src/smp.go`
- `src/serial.go`
- `src/net.go`
- `src/goroutine_tss.go`
- `scripts/tinygo_runtime.patch`
- `scripts/test_smp_basic.sh`
- `scripts/test_smp_shell_smpprobe.sh`

## Bottom Line

The latest mitigations appear to have **improved keyboard usability under `make run-smp`**, but the system is **not yet stable**.

The evidence now points to a broader post-shell SMP/runtime issue:

- keyboard delivery is better,
- but AP participation in user-process work is still unreliable,
- `gochan` still flakes,
- and serial interleaving still obscures the real execution trace.

The next step should therefore treat this as a **combined SMP scheduling + process lifecycle + observability problem**, not as a keyboard-only bug.
