# 01 — Problem Statement

## The symptom

Bring up the guest the normal way (the three-terminal procedure
documented in `README.md` under "Path D / Path E"):

```
make run-net
```

Wait for the Ring-3 shell prompt. Leave the guest idle for more
than ~15 seconds, then from a **host** terminal run:

```
nc -w 3 127.0.0.1 10080
hello
```

Expected: the string `hello` echoes back (the kernel runs an
in-kernel TCP echo server on guest port 8080, forwarded to host
port 10080).

Actual on current HEAD of `tcp-take2`: nc closes with no reply
after its 3-second timeout. The serial log shows the e1000 ISR
firing for each retransmitted SYN, but no frame is ever handed
to the Ethernet dispatcher, and the guest never emits a reply.

If instead you fire nc within ~5 seconds of the shell prompt
appearing, the echo round-trips correctly. The bug is purely
**time-dependent on how long the guest has been idle post-Ring-3
startup**.

## The reproducer

`scripts/test_tcp_latetiming.sh` automates the above. It:

1. Boots the guest (building `tmp/kernel.iso` if needed).
2. Polls the serial log until `ring3Wrapper: jumping to Ring 3`
   appears (max ~30 s).
3. Sleeps an additional 15 seconds.
4. Fires `timeout 5 nc -w 3 127.0.0.1 10080` with a
   timestamped payload.
5. Prints `result: PASS` (exit 0) if the payload round-trips,
   otherwise `result: FAIL` (exit 1) with a serial log tail.

On current HEAD this script prints:

```
test_latetiming: nc_rc=... echoed='' expected='late-timing-...'
result: FAIL
```

The empty `echoed` is the visible symptom. The fix session's
primary external success criterion is making this script PASS.

## Regression gates that must stay green

The five phase scripts under `scripts/` exercise the TCP stack
end to end at tight timing (each does its nc within a few seconds
of Ring-3 coming up):

- `scripts/test_tcp_phase1.sh` — three-way handshake + FIN
- `scripts/test_tcp_phase2.sh` — data transfer with in-order
  delivery
- `scripts/test_tcp_phase3.sh` — retransmission and RTO
- `scripts/test_tcp_phase4.sh` — flow control and cwnd collapse
  on RTO
- `scripts/test_tcp_phase5.sh` — userspace `tcpecho` + `tcpcli`
  demo round-trip

All five PASS on current HEAD. Any fix must keep all five PASSing.

## Success criteria

- `bash scripts/test_tcp_latetiming.sh` exits 0.
- `bash scripts/test_tcp_phase{1..5}.sh` all exit 0.
- `make build && make lint && make verify-globals` clean.
- The "periodic netDiag only fires once or twice" symptom
  (described in `02_evidence_and_hypotheses.md`) goes away —
  i.e. netDiag snapshots keep appearing in the serial log for
  the full duration of the test.

## What "fixed" looks like from the outside

- Manual `nc 127.0.0.1 10080` works at any time post-boot, not
  only within the first few seconds.
- The serial log shows `netDiag` snapshots at a steady cadence
  (piggybacked on `netRxLoop` in the current diagnostic build)
  for tens of seconds, not two or three and then silence.
- `e1000IRQCount` and `NetRxLoopWakes` continue to advance
  together — the ISR fires **and** the goroutine drains.

## Out-of-scope symptoms (do NOT chase these here)

- QEMU window keyboard does not react — this is the
  `-serial stdio` routing quirk documented in README Path D,
  not a bug.
- `tcpcli` hanging if `tcpecho` was not started first — that's
  user error, the demo expects the order in README.
- `Buf alloc fails: 1` on netDiag — normal, one-shot during ARP
  gratuitous send right after `netInit`.

All three of the above came up during the investigation and
turned out to be red herrings.
