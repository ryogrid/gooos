# Late-timing RX Stall — Handoff Package

You are picking up an **unfixed** bug in gooos's kernel TCP stack.
Phase TCP-1..TCP-5 functional work is complete and all
`scripts/test_tcp_phase{1..5}.sh` regression gates PASS, but a
manual `make run-net` + host `nc 127.0.0.1 10080` issued more than
~15 seconds after guest boot receives no echo. A previous session
has already investigated, ruled out several initial hypotheses,
left diagnostic instrumentation in the tree, and committed a
root-cause writeup. Your job is to **design and implement the
fix**; the investigation is done.

This directory is the self-contained handoff. Read the files below
in order — you should not need the previous session's chat
transcript.

## Read order

1. [`01_problem_statement.md`](01_problem_statement.md) — exact
   symptom, the reproducer script, and what "fixed" looks like
   from the outside.
2. [`02_evidence_and_hypotheses.md`](02_evidence_and_hypotheses.md)
   — numbered evidence captured, what's been ruled out, and the
   leading hypothesis. Also lists four candidate fix approaches.
3. [`03_gooos_design_map.md`](03_gooos_design_map.md) — which of
   gooos's ~60 design docs are authoritative for this bug, which
   to skip (they reference deleted subsystems), and a one-line
   evolution order so you can read older docs without getting
   confused.
4. [`04_investigation_next_steps.md`](04_investigation_next_steps.md)
   — concrete first actions, plus watch-outs from prior sessions.

## Quick start

```
# 1. Reproduce the bug (expected FAIL on current HEAD).
bash scripts/test_tcp_latetiming.sh

# 2. Prove you haven't broken the green regression gates.
bash scripts/test_tcp_phase1.sh
bash scripts/test_tcp_phase2.sh
bash scripts/test_tcp_phase3.sh
bash scripts/test_tcp_phase4.sh
bash scripts/test_tcp_phase5.sh

# 3. Read 01, 02, then 04. 03 is reference material.
```

## Non-goals

- **Do not rewrite the e1000 driver.** The NIC is working; ISR
  fires correctly, MMIO reads succeed post-stall. The bug is
  upstream of it.
- **Do not touch the TCP state machine or sockets.** This bug
  manifests below the IPv4 layer — the frame never reaches
  `ethernetDispatch`.
- **Do not clean up the WIP diagnostics** in commit `fe627b5`
  until your fix lands and the latetiming script PASSes. The
  serial-print instrumentation is still load-bearing for
  verifying the fix.
- **Do not chase CR3 / conservative-GC / IMS corruption.** All
  three were ruled out by the prior session; see
  `02_evidence_and_hypotheses.md` for refutations.

## Scope

- **Kernel-side fix only.** Userspace TCP APIs and the `tcpecho`
  demo are correct.
- **User-mode QEMU only** (hostfwd). TAP / root-privilege setups
  are out of scope for this bug.
- **Single-core BSP.** SMP AP paths are not involved.

## Key commits in the investigation trail

```
fe627b5  wip(net): late-timing RX-stall diagnosis — RDH stuck at 0
45ebfd0  docs(net): document late-timing RX stall as top-priority follow-up
2abec07  chore(net): late-timing RX-stall diagnosis — root cause isolated
```

All three are on branch `tcp-take2`. The WIP commit adds diagnostic
prints that you should leave in place until verification.
