# TODO_M6 — Uniprocessor kernel milestone

Tracker for the execution cycle defined by
`no_goroutine_kernel_design/14_uniprocessor_kernel.md` and
driven by `hoge.md`. One TODO item per commit; tick `[x]`
only after the corresponding commit lands.

Branch: `smp-no-goroutine-in-kernel`. Starting HEAD:
`0cd9095 add doc: uni core kernel design`.

## Steps

- [x] Bootstrap — create this tracker, run baseline smoke tests
- [x] Step 0 — add `scripts/test_run_smp_keyboard.sh` harness
- [x] Step 1 — add `const uniprocessorKernel = true`
- [x] Step 2 — pin every kthread spawn to BSP (§3.2/§3.3/§3.6)
- [x] Step 3 — APs idle in kernel mode (§3.1/§3.4/§3.5)
- [x] Step 4 — re-enable net services on BSP (§3.7/§3.8)
- [x] Step 5 — SMP-distribution tests SKIP/re-purposed (§6.2)
- [x] Step 6 — lock-rank doc + RR counter cleanup (§4)
- [x] Reviewer sub-agent pass (`hoge.md` §5)
- [x] README + impldoc refresh (`hoge.md` §6)
- [x] Final sweep — grep TODO/FIXME/XXX/HACK + verification

## Baseline (HEAD `0cd9095`)

- `scripts/test_kthread_smoke.sh`: PASS (A=5 B=5 ok=1)
- `scripts/test_ps.sh`: PASS (header=1 row=1)
- 10-iter `qemu -smp 4` HMP `sendkey h e l p ret`
  (measured in M6 bisection at `193e205`):
  0/10 helpRan, 0/10 PF (with M6 partial fix), 0/10 M9-drained.
  This is the regression that §14 fixes.

## Per-step measurements

- **Step 0** (HEAD `aad1a04`, no §14 code yet): pre-§14
  baseline measurement on the new harness reproduced the
  expected failure. Sample run 1: `PF: addr=0x670C1333
  rip=0x40105072` (Ring-3 user-space PF — keyboard input
  reaching shell with corrupt state). Confirms §14 motivation.
- **Step 2** (HEAD `fc83121` + Step 2 edits, all kthread
  spawns BSP-pinned): 10-iter — helpRan=0/10, M8=3/10,
  M9=0/10, PF=6/10. PARTIAL as expected per §7 (net services
  still gated by `runMinimalKthreads=true`; boot shell does
  not exec before keystroke; functional behavior identical
  to baseline). Structural fix lands at Step 3.
- **Step 3** (HEAD `82a123b` + Step 3 edits, APs idle in
  kernel mode): **10-iter helpRan=10/10, M8=10/10, M9=10/10,
  PF=0/10. ✅ §14 §8 PASS bar met.** Both Bug A (cross-CPU
  PF) and Bug B (parked shell never drains) eliminated by the
  apSchedulerEntry idle-loop change. The §14 hypothesis is
  confirmed.
- **Step 4** (HEAD `67f6f40` + Step 4 edits, net services
  back on BSP, runMinimalKthreads=false default,
  pitWakeAPs gated): keyboard 10-iter helpRan=10/10,
  M8=10/10, M9=10/10, PF=0/10. ✅ Holding the §14 §8 bar.
  `scripts/test_net.sh` PASS (UDP echo round-trip, ARP,
  ICMP, netbuf lifecycle).
- **Step 6** (HEAD `e49d47f` + Step 6 doc edits): keyboard
  10-iter helpRan=10/10, M8=10/10, M9=10/10, PF=0/10. ✅
  Doc-only changes do not regress correctness.
- **Reviewer pass** (HEAD `f1aa3fe`, general-purpose
  sub-agent): PASS-with-MINOR. 0 BLOCKING. Every
  invariant U1..U10 is upheld by the code; §14 §8
  verification matrix confirmed (keyboard PASS measured;
  5 SMP scripts SKIP-gated; `make build` / `make lint` /
  `make verify-globals` clean; K5 preserved — user-side
  build untouched). The only MINOR is the README refresh,
  which is the next workflow step.
- **Final §8 verification matrix** (HEAD `3113e3b`):
  - `scripts/test_run_smp_keyboard.sh`: 10/10 helpRan,
    10/10 M8, 10/10 M9, 0/10 PF — **PASS** (above bar).
  - `scripts/test_kthread_smoke.sh`: PASS (A=5 B=5 ok=1).
  - `scripts/test_ps.sh`: PASS (header=1 row=1).
  - `scripts/test_net.sh`: PASS (UDP echo round-trip).
  - `scripts/test_tcp_longidle.sh 15`: PASS.
  - `scripts/test_tcp_phase[1-5].sh`: 5/5 PASS.
  - `make build` / `make lint` / `make verify-globals`:
    clean (`verify-globals: OK (4 symbols ...)`).
  - `make -C user all`: clean ("Nothing to be done").
  - 5 SMP-distribution scripts: SKIP under flag.
  - `grep -rIn 'TODO\|FIXME\|XXX\|HACK' src/ scripts/`:
    only 3 pre-existing references to TODO_FIX.md /
    TODO_SMP4.md inside source comments — **none added
    by this M6 cycle**.

## Deferred

- **`pipe.ch chan byte` (Ring-3 pipe fds)** — pre-existing
  known issue from `12_implementation_notes.md` § Open
  issues + risks. Not addressed in M6.fix-1 because no
  pipe-using user binary is currently in the test matrix;
  the fix pattern (chan → spinlock-protected primitive)
  is identical to M6.fix-1's `ring3StackRelease` change.
  Wire when the first pipe-using program lands in M7+.

## Post-cycle fixes

- **M6.fix-1** (commit `<TBD>`): `ring3StackRelease`
  panicked under scheduler=none because its `chan int`
  send routes through TinyGo's `task.Pause` path. Symptom:
  no `$ ` prompt after `hello`/`ls`. Fix: replaced
  `ring3StackPoolCh chan int` with a spinlock-protected
  free bitmap (mirrors `kthreadPool` pattern); also
  flag-gated `proc.exitCh <- exitCode` send to skip the
  send when parent is a kthread (parent uses `proc.Exited`
  poll instead). New regression harness:
  `scripts/test_shell_post_exec_prompt.sh` — 10/10 PASS,
  0 panics. All §8 gates re-confirmed green.
