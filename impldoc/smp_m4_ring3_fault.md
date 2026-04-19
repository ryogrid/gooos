# M4 — AP Ring-3 `iretq` Triple-Fault Investigation Playbook

**Scope.** QEMU + GDB-driven root-cause analysis and fix for the AP Ring-3 `iretq` triple-fault that today blocks every gooos SMP milestone that would distribute Ring-3 goroutines. Covers reproducer, QEMU/GDB recipe, hypothesis-to-fix matrix, fix-site guidance, verification harness, and rollback. **Does not** cover the toolchain/scheduler-mode changes (M3) or the AP LAPIC timer race (M2); those are sibling docs.

**Cross-links.**
- Deferred-item charter: `TODO_SMP3.md §"Deferred further"` item 3.
- Primary symptom writeup: `impldoc/smp_deferred_and_known_issues.md §2.1`.
- Milestone entry in this batch: `impldoc/smp_unblock_overview.md` (read me first).
- Unified schedule: `impldoc/smp_unblock_milestones_and_verification.md §M4`.
- M3 (scheduler=cores promotion) depends on M4 resolving: `impldoc/smp_m3_cores_promotion.md`.
- Existing AP bring-up design: `impldoc/smp_percpu_and_sync.md §5`, `impldoc/smp_kernel_scheduler.md §7`, `impldoc/smp_ap_safety_overview.md §2`.

---

## 1. Symptom (current as of `smp-take3` HEAD)

From `impldoc/smp_deferred_and_known_issues.md §2.1`:

> When an AP steals a `ring3Wrapper` goroutine via `stealWork()` and calls `jumpToRing3` → `iretq`, the AP silently triple-faults. No serial output, no panic — the CPU simply resets.
>
> - `ring3Wrapper` debug prints confirm: "cpuID=2, stackAcquired, jumping to Ring 3" — then silence.
> - Single-CPU and `-smp 4` with stealWork disabled: shell works perfectly on BSP.
> - `-smp 4` with stealWork enabled: shell goroutine stolen by AP → triple fault.

**Live reproduction during the 0.40.1 migration** (M1 exit verification, captured in commit message of `d0cba8e`): the 0.40.1 patch inadvertently wired the stealWork call into the scheduler's pop site. Boot under `-smp 4` produced:

```
ring3Wrapper: cpuID=1
ring3Wrapper: stackAcquired
ring3Wrapper: jumping to Ring 3
```

— then silence, no shell prompt. Commit `d0cba8e fix(smp): disable stealWork call under Wave 1 tasks mode` reverts the wire-up to keep M1 green.

---

## 2. Reproducer

### 2.1 Enable the fault

Re-wire the `stealWork()` call that `d0cba8e` disabled. In the patched TinyGo tree (`~/.local/tinygo0.40.1/src/runtime/scheduler_cooperative.go`, around **line 247-254**), the current text reads:

```go
t := runqueues[gooosCpuID()].Pop()
// stealWork is intentionally NOT called here under Wave 1 (tasks
// mode). Work stealing triggers the AP Ring-3 iretq triple-fault
// documented in impldoc/smp_deferred_and_known_issues.md §2.1
// because APs may steal ring3Wrapper goroutines and crash on
// their per-CPU TSS transition. Enabling stealWork is deferred
// to milestone M3/M4 after the underlying Ring-3 AP fault is
// resolved. APs therefore idle in waitForEvents under -smp N.
if t == nil {
```

Replace with:

```go
t := runqueues[gooosCpuID()].Pop()
if t == nil {
    // M4 repro: enable stealWork to trigger the AP Ring-3
    // iretq triple-fault under -smp 4.
    t = stealWork()
}
if t == nil {
```

Then regenerate the patch file and rebuild:

```
cd /home/ryo/work/tinygo
git diff > /home/ryo/work/gooos/scripts/tinygo_runtime.patch
cd /home/ryo/work/gooos
make build
make iso
```

### 2.2 Revert cleanly

After the investigation (whether a fix lands or not), restore the Wave 1 safe state:

```
cd /home/ryo/work/tinygo
git checkout -- src/runtime/scheduler_cooperative.go
cd /home/ryo/work/gooos
git checkout -- scripts/tinygo_runtime.patch
bash scripts/patch_tinygo_runtime.sh
make build
make iso
```

`scripts/patch_tinygo_runtime.sh` is idempotent and re-applies the Wave 1 patch verbatim.

---

## 3. QEMU + GDB Recipe

### 3.1 Launch QEMU with GDB stub

```
qemu-system-x86_64 \
  -cdrom tmp/kernel.iso \
  -serial file:tmp/m4_serial.log \
  -no-reboot -no-shutdown \
  -display none \
  -smp 4 \
  -d int,cpu_reset,guest_errors \
  -D tmp/m4_qemu.log \
  -s -S
```

Flag breakdown:

| Flag | Purpose |
|---|---|
| `-s` | GDB server on `localhost:1234`. |
| `-S` | Freeze at reset until GDB connects. |
| `-d int,cpu_reset,guest_errors` | Log interrupt/exception deliveries, CPU resets (= triple-fault), guest-state errors to the `-D` file. |
| `-D tmp/m4_qemu.log` | QEMU diagnostic log (reset reason + last exception frames will appear here). |
| `-smp 4` | BSP + 3 APs, same as `make run-smp`. |

Start QEMU as a background process so GDB can attach:

```
bash -c 'qemu-system-x86_64 -cdrom tmp/kernel.iso -serial file:tmp/m4_serial.log -no-reboot -no-shutdown -display none -smp 4 -d int,cpu_reset,guest_errors -D tmp/m4_qemu.log -s -S &'
```

### 3.2 GDB session

In a second shell:

```
gdb -q tmp/kernel.bin
```

GDB prompt commands (paste block):

```
set architecture i386:x86-64
set disassembly-flavor att
target remote localhost:1234

# Symbol-level breakpoints (kernel + patched TinyGo runtime).
break ring3Wrapper
break jumpToRing3
# To watch ISR entries, a read-watchpoint on the per-CPU counter
# catches every `incl %gs:4` (src/isr.S:111). The global counter
# at gooos_in_interrupt_depth is a .bss symbol; `break *<dataSym>`
# is invalid. Use rwatch instead:
rwatch *(unsigned int*)gooos_in_interrupt_depth

# Break on any triple-fault (QEMU translates to cpu_reset).
# There is no direct CPU-reset hook in gdbstub; rely on
# pre-iretq breakpoints + the -d cpu_reset log to pinpoint.

continue
```

When a breakpoint trips, dump the AP's critical registers:

```
# Which CPU are we on?
info registers
# Check task register (TR)
info registers   # look for `tr` line; compare to perCPUTSS[cpuID] base
# Check segment descriptors
info all-registers   # gdt, tr, cs, ss
# Per-CPU GS base (IA32_GS_BASE)
p/x $gs_base         # may need `monitor info registers` via QEMU monitor

# Dump the per-CPU TSS for this CPU (assume cpuID=1 for example)
p/x &perCPUTSS[1]
x/32xw &perCPUTSS[1]          # show the first 128 bytes of TSS
# Offset 4 = RSP0 (low 32 bits), offset 8 = RSP0 high; check they're non-zero.

# GDT base/limit for this CPU
x/20xg &perCPUGDT[1]          # show 10 entries of 8 bytes each

# If we're in ring3Wrapper just before jumpToRing3:
# Verify CR3 == proc.pml4 for this ring3 process
p/x proc.pml4
# Compare to $cr3 via QEMU monitor
monitor info registers        # prints CR3 / GDT base / TR / ...
```

### 3.3 Monitor-level diagnostics (optional — usually unneeded)

GDB's `monitor <cmd>` prefix forwards to QEMU's monitor via the gdbstub, so `monitor info registers` / `monitor info mem` from inside the GDB session already gives CR3 / GDT / TR / IDT access. Add a separate monitor socket **only** if you want to query the monitor from a second shell concurrently with the GDB session. In that case launch QEMU with:

```
-monitor telnet:localhost:4444,server,nowait
```

Then `(echo 'info registers'; echo 'info mem') | nc -q 1 localhost 4444` captures the same state from a separate shell.

---

## 4. Hypothesis → Confirm/Refute Matrix

Sourced from `impldoc/smp_deferred_and_known_issues.md §2.1 (a-e)`. Work top-to-bottom; first row that confirms gets the fix treatment.

| # | Hypothesis | Confirm via | Refute via | Expected fix site |
|---|---|---|---|---|
| a (**leading**) | AP's TR (Task Register) does not point at `&perCPUTSS[cpuID]`. `selectorTSS` at `src/gdt.go:201` is a single compile-time constant; `ltr(selectorTSS)` must resolve against the per-CPU GDT that `lgdtReload` at `src/gdt.go:197` just loaded. | `monitor info registers` after line 201 runs on each AP; `tr` selector should equal `selectorTSS`; the descriptor at that GDT offset on the **per-CPU** GDT must have base = `&perCPUTSS[cpuIdx][0]`. Dump with `x/2gx &perCPUGDT[cpuIdx][gdtTSSLow]`. | `tr` base matches `&perCPUTSS[cpuIdx][0]` → row refuted. | `src/gdt.go:197-201` — ensure `lgdtReload` and `ltr` are sequenced such that `ltr` reads the fresh descriptor. If an interrupt fires between lines 197 and 201, the AP's GDTR points at the new per-CPU GDT but TR still points at the BSP's stale descriptor. Wrap the two-instruction window inside `cli` / `sti` if not already (check surrounding code at `src/gdt.go` call site and `src/smp.go` apEntry). |
| b (**unlikely — descriptor already built fresh**) | TSS type byte stale: 0xB (busy) instead of 0x9 (available) after `ltr`. | Dump byte 5 (access byte) of `perCPUGDT[cpuID][gdtTSSLow]` via `x/1bx`. Bits 0-3 = type; must be 9. | `src/gdt.go:166-178` already constructs the TSS descriptor fresh with `low \|= uint64(0x89) << 40` (P=1, DPL=0, Type=9 "available TSS64"). The copy-from-BSP-GDT path that would leave 0xB stale does not exist in the current code. Expected result: type = 9 → refuted. | If (against expectation) type != 9, audit what other code path overwrites `perCPUGDT[cpuIdx][gdtTSSLow]` after line 177. |
| c | RSP0 in AP TSS does not point at a valid kernel stack. | `x/1xg &perCPUTSS[cpuID]+4` (RSP0 is at byte offset 4, 8 bytes long). Value should be in the heap range occupied by `ring3StackPool` or a per-task kernel stack top. | RSP0 points into a valid mapped kernel page with Ring-0 access → refuted. | `src/goroutine_tss.go` `gooosOnResume` + `src/gdt.go:141` `tssSetRSP0` — verify that on an AP, `tssSetRSP0` writes `perCPUTSS[cpuID]` (not `perCPUTSS[0]`), i.e. `idx := cpuID()` resolves correctly when called from the AP. |
| d | CR3 on the AP does not match `proc.pml4` for the stolen `ring3Wrapper` goroutine. | `monitor info registers` after the `swapgs` / before `iretq`; compare CR3 to `proc.pml4`. | CR3 == proc.pml4 → refuted. | `src/goroutine_tss.go` `gooosOnResume` — the CR3 swap path must run on the same CPU that then executes `iretq`. If gooosOnResume is invoked inside `task_stack_amd64.go::resume()` but a scheduler reschedule happens between the CR3 write and the `iretq`, the AP could execute iretq with a stale CR3. Check that gooosOnResume runs **immediately** before the swapTask to the ring3 stack. |
| e | User CS/SS selectors (0x1B / 0x23) do not resolve in the AP's per-CPU GDT. | Read gdt descriptors at offsets 0x18 and 0x20 from the TR-pointed GDT base; verify Ring-3 code+data attributes set. | Selectors resolve correctly → refuted. | `src/gdt.go:155-158` — verify `gdtUserCode`/`gdtUserData` entries are copied into every `perCPUGDT[cpuIdx]`. |

### 4.1 Evidence capture template

For each row you confirm or refute, capture into `tmp/m4_evidence_<cpuID>_<timestamp>.txt`:

```
== Hypothesis (a|b|c|d|e) — cpuID=N ==
TR selector:           0x....
TR base:               0x....   (expected &perCPUTSS[N][0] = 0x....)
TSS type byte:         0x..     (expected 0x09 or 0x0B)
RSP0:                  0x....   (expected a ring3StackPool stack top)
CR3:                   0x....   (expected proc.pml4 = 0x....)
GDT[user_code]:        0x........   (attrs bits set?)
GDT[user_data]:        0x........
QEMU -D log excerpt:
  <copy relevant `check_exception` / `cpu_reset` lines>
```

Append the evidence file verbatim to the M4 fix commit's message so the next reviewer can audit.

---

## 5. Fix Design (evidence-driven, **no presumed fix**)

**Important pre-investigation note.** `src/gdt.go:166-178` **already** builds the TSS descriptor fresh with `low |= uint64(0x89) << 40` (P=1, DPL=0, Type=9 "available 64-bit TSS"). The 0.40.1-migration reviewer pass confirmed this. Therefore hypothesis (b) — "TSS type byte stale after ltr" — is expected to **refute**, and the leading candidate is hypothesis (a): **the `lgdtReload` / `ltr` sequencing at `src/gdt.go:197-201`**.

Specifically, `lgdtReload` at line 197 loads the new per-CPU GDT via `lgdt`, then reloads segment registers. Between line 197 and `ltr(selectorTSS)` at line 201 there are two Go statements (a `wrmsr` at line 198 and no-ops). If an interrupt fires in that window on an AP — LAPIC timer, IPI, PIT-via-broadcast — the ISR prologue reads `%gs:offset` and other per-CPU state using the new GDT but with TR still pointing at the BSP's descriptor; when the CPU subsequently tries to transition to Ring 3 via `iretq`, it references a TR descriptor that doesn't match its own per-CPU TSS.

**Do not write a fix until the evidence-capture pass (§4.1) confirms the specific root cause.** The evidence table's "Expected fix site" column for the confirmed row is the authoritative change site. Rough sketches per hypothesis:

- **If (a) confirms:** wrap the `lgdtReload` / `wrmsr` / `ltr` sequence (`src/gdt.go:197-201`) in `cli` / `sti` to suppress interrupts during the GDT-TR transition. The change is 2-3 lines in `src/gdt.go` plus any required stubs in `src/stubs.S`.
- **If (b) confirms** (unexpected): audit what path overwrites `perCPUGDT[cpuIdx][gdtTSSLow]` after line 177.
- **If (c) confirms:** audit `src/goroutine_tss.go` `gooosOnResume` and `src/gdt.go:141` `tssSetRSP0` — verify `cpuID()` returns the correct index on the AP.
- **If (d) confirms:** ensure `gooosOnResume` runs **atomically** relative to the subsequent `iretq` (no scheduler reschedule between CR3 write and Ring-3 transition).
- **If (e) confirms:** audit `perCPUGDT[cpuIdx]` initialisation to ensure user CS/SS entries were actually copied at runtime (the `:157-158` copy statements ran).

**Pivot rule.** The implementation agent should NOT preview a fix from this doc. Run the evidence pass first; let the evidence dictate the fix site.

---

## 6. Verification

### 6.1 New harness: `scripts/test_smp_ring3.sh`

Wraps `smpprobe.elf` under `-smp 4`. Expected output: worker goroutines report ≥ 2 distinct cpuIDs.

Skeleton (to be written as a sibling of `scripts/test_smp_basic.sh`):

```bash
#!/usr/bin/env bash
# scripts/test_smp_ring3.sh — Ring-3 goroutine distribution under -smp 4.
# PASSes when smpprobe.elf workers report ≥ 2 distinct cpuIDs after
# M4 + M3 land.
set -euo pipefail
cd "$(dirname "$0")/.."
rm -f tmp/m4_test_serial.log

# Launch QEMU directly with & so $! captures the QEMU PID (not a
# wrapper bash subshell). The stdin-to-QEMU path for smpprobe
# input is handled by whatever existing harness convention the
# repo uses for keystroke injection; see scripts/test_sendkey.sh
# for the pattern.
qemu-system-x86_64 -cdrom tmp/kernel.iso \
    -serial file:tmp/m4_test_serial.log \
    -no-reboot -no-shutdown -display none -smp 4 \
    -monitor null &
QEMU_PID=$!

# Bounded wait for smpprobe to emit its cpuID report (max 30 s).
for i in $(seq 1 30); do
    if grep -q 'smpprobe: worker cpuID=' tmp/m4_test_serial.log 2>/dev/null; then
        break
    fi
    kill -0 "$QEMU_PID" 2>/dev/null || break  # QEMU exited early
    sleep 1
done

kill "$QEMU_PID" 2>/dev/null || true
wait "$QEMU_PID" 2>/dev/null || true

# Verify ≥ 2 distinct cpuIDs observed.
distinct=$(grep -oE 'worker cpuID=[0-9]+' tmp/m4_test_serial.log | sort -u | wc -l)
echo "test_smp_ring3: distinct_cpuids=$distinct"
if (( distinct >= 2 )); then
    echo "result: PASS"
    exit 0
else
    echo "result: FAIL — only $distinct distinct cpuID(s) observed (expected ≥ 2)"
    exit 1
fi
```

**Note on keystroke injection.** `smpprobe.elf` takes arguments from the shell command line. The harness above assumes the kernel auto-runs `smpprobe` at boot (via a temporary boot-probe hook in `src/main.go` gated by `const runSmpprobeProbe = true`). If the implementation agent prefers to inject keystrokes into a running shell instead, follow the pattern in `scripts/test_sendkey.sh` (PS/2 keyboard scancode injection via the QEMU monitor `sendkey` command).

(The shell script must respect the gooos `tmp/` convention and avoid unbounded polling — the `seq 1 30` loop enforces a 30-second ceiling per memory entry `feedback_background_bash.md`.)

### 6.2 Regression matrix under `-smp 4`

After the M4 fix lands AND M3 wires `stealWork()`:

```
bash scripts/test_smp_ring3.sh                # PASS (new)
bash scripts/test_sendkey.sh 1                # PASS (Ring-3 shell under -smp 4)
bash scripts/test_pipe_matrix.sh              # PASS
bash scripts/test_gochan.sh                   # PASS
bash scripts/test_goprobe.sh                  # PASS
bash scripts/test_net.sh                      # PASS (no TCP regression)
bash scripts/test_tcp_phase5.sh               # PASS
```

Every harness above that currently PASSes under `-smp 1` must still PASS under `-smp 4` with the fix in place.

### 6.3 Build/lint gates

- `make build` clean.
- `make lint` clean (no new ISR-unsafe constructs in any gdt.go / smp.go edits).
- `make verify-globals` clean.

---

## 7. Commit Plan

One `fix(smp): …` commit per item; land in this order.

| # | Subject | Files |
|---|---|---|
| 1 | `fix(smp): <root cause per hypothesis>` — the actual fix identified by the evidence pass | `src/gdt.go` and/or `src/percpu.go` and/or `src/smp.go` — exact set depends on confirmed hypothesis |
| 2 | `test(smp): add scripts/test_smp_ring3.sh` | `scripts/test_smp_ring3.sh` |
| 3 | `docs(smp): M4 resolution — update deferred/known issues` | `impldoc/smp_deferred_and_known_issues.md §2.1` (mark resolved; keep evidence link) |

Commits 1 and 2 must be bisectable independently; `test_smp_ring3.sh` without fix #1 should still exit with a clear `result: FAIL` line.

---

## 8. Rollback

If the investigation session cannot localise the fault within a reasonable time budget (two engineering days):

1. `git checkout -- src/runtime/scheduler_cooperative.go` in `/home/ryo/work/tinygo/` (revert the repro-enable edit).
2. `git checkout -- scripts/tinygo_runtime.patch` in `/home/ryo/work/gooos/` (restore Wave 1 patch).
3. `bash scripts/patch_tinygo_runtime.sh` (re-apply Wave 1).
4. `make build && make iso` (verify Wave 1 state rebuilds green).
5. Append a dated subsection to `impldoc/smp_deferred_and_known_issues.md §2.1` under "**Investigation attempts**" listing:
   - Date and session ID.
   - Evidence file paths under `tmp/` (copied to the commit message before they disappear).
   - Hypotheses confirmed / refuted.
   - Why the session was abandoned.
6. Open an issue (`gh issue create`) only with explicit user instruction.

---

## 9. Deliverables

1. `src/gdt.go` / `src/percpu.go` / `src/smp.go` edits that make `scripts/test_smp_ring3.sh` PASS.
2. `scripts/test_smp_ring3.sh` harness committed.
3. `impldoc/smp_deferred_and_known_issues.md §2.1` updated to "Resolved" with commit hash.
4. Full regression matrix green under `-smp 4`.
5. No `git push`; no branch operations; no `master` merge without explicit user instruction.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
