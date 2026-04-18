# Networking Stack — TCP Test Plan

Comprehensive verification strategy for the TCP phase of the
gooos networking stack across all five TCP sub-phases.

Parent doc: [`net_tcp_overview.md`](net_tcp_overview.md).
Extends: [`net_test_plan.md`](net_test_plan.md) (the Phase 1-4
test plan whose structure this document mirrors).

---

## 1. Test Environment

### 1.1 QEMU Configurations

The existing configurations from `net_test_plan.md §1.1` are
reused unchanged. Two host-forward additions extend
Configuration B for TCP:

**Configuration B-TCP — User-mode + TCP hostfwd** (v1 default):

```bash
qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio \
  -no-reboot -no-shutdown \
  -device e1000,netdev=n0 \
  -netdev user,id=n0,\
hostfwd=udp::9999-:7,\
hostfwd=udp::19999-:17,\
hostfwd=tcp::10080-:8080,\
hostfwd=tcp::10081-:8081
```

- Guest TCP port 8080 → host 10080: kernel TCP echo (Path D).
- Guest TCP port 8081 → host 10081: userspace TCP echo (Path E).
- UDP paths (9999 / 19999) preserved for Phase 1-5 regression.

**Configuration C — TAP networking** (unchanged from
`net_test_plan.md §1.1`): required for bidirectional flows
(host-initiated SYNs with full seq/ack visibility) and
`iperf3` high-throughput tests.

**Configuration D — pcap capture**: add
`dump=file:tmp/tcp.pcap` to the `-netdev` line; analyse with
`tcpdump -r tmp/tcp.pcap` or Wireshark.

### 1.2 Makefile Target

`run-net` in the current `Makefile` is extended; no new target
is needed:

```makefile
run-net: $(KERNEL_ISO) check-multiboot
	$(QEMU) -cdrom $(KERNEL_ISO) -serial stdio \
	  -no-reboot -no-shutdown \
	  -device e1000,netdev=n0 \
	  -netdev user,id=n0,hostfwd=udp::9999-:7,hostfwd=udp::19999-:17,hostfwd=tcp::10080-:8080,hostfwd=tcp::10081-:8081
```

### 1.3 Host-side Tooling

| Tool | Purpose | Invocation |
|---|---|---|
| `nc` (netcat) | 3-way handshake + small byte transfers | `nc 127.0.0.1 10080` |
| `curl` | HTTP GET against userspace server (Path E extension) | `curl http://127.0.0.1:10081/` |
| `iperf3` | Throughput regression under loss (Config C) | `iperf3 -c 10.0.0.2 -p 8080` |
| `tcpdump` | Packet capture + state-machine verification | `tcpdump -r tmp/tcp.pcap -n tcp` |
| `python3 -c "..."` | Crafting malformed segments for negative tests | See §6 |
| `hping3` (optional) | Low-level SYN/RST injection | Config C, root required |

### 1.4 Automation Scripts

All scripts go under `tmp/` (scratch, per `CLAUDE.md`):

```
tmp/test_tcp_phase1.sh       # passive open + kernel echo round-trip
tmp/test_tcp_phase2.sh       # active open + retransmission
tmp/test_tcp_phase3.sh       # flow control / zero window
tmp/test_tcp_phase4.sh       # congestion control under loss
tmp/test_tcp_phase5.sh       # userspace API end-to-end
tmp/test_tcp_regression.sh   # runs all of the above + Phase 1-5 regression
```

Each script:
1. Boots the kernel under Configuration B-TCP with a timeout
   (`timeout 60 qemu ...`).
2. Runs the host-side test (e.g., `echo "hello" | nc 127.0.0.1
   10080 | grep hello`).
3. Greps the serial log for expected state-machine
   transitions (e.g., `TCP: 8080: SYN_RECEIVED → ESTABLISHED`).
4. Exits 0 on pass, 1 on fail with a summary line on stderr.

### 1.5 Serial-Log Verification

The kernel prints per-state transitions via `netDiag`
extensions (see `net_tcp_work_plan.md` Phase TCP-1 tasks):

```
TCP: 0:0 → 10.0.2.2:45678 LISTEN
TCP: 0:8080 → 10.0.2.2:45678 SYN_RECEIVED
TCP: 0:8080 → 10.0.2.2:45678 ESTABLISHED
TCP: 0:8080 → 10.0.2.2:45678 CLOSE_WAIT
TCP: 0:8080 → 10.0.2.2:45678 LAST_ACK
TCP: 0:8080 → 10.0.2.2:45678 CLOSED
```

Each test script greps for the expected sequence.

---

## 2. Phase TCP-1 Tests — Passive Open + Kernel Echo

### T1.1 PCI + net stack still boot under TCP init

| Item | Detail |
|---|---|
| **Precondition** | `Config B-TCP` |
| **Action** | Boot kernel |
| **Expected serial** | Existing `PCI: found e1000` + `NET: initialized` log lines AND `TCP: init complete, 16 TCBs, 4 listeners` |
| **Failure** | Any triple-fault or panic → revert `tcpInit()` call site |

### T1.2 `ipv4Handle` dispatches proto=6

| Item | Detail |
|---|---|
| **Precondition** | Kernel booted |
| **Action** | Host sends `hping3 -S 10.0.2.15 -p 8080` (no listener yet) |
| **Expected pcap** | Guest replies with `RST` (per RFC 793 §3.4) |
| **Failure** | Guest drops silently → `case ipProtoTCP:` missing from `src/ipv4.go:184-213` |

### T1.3 LISTEN creation

| Item | Detail |
|---|---|
| **Precondition** | Kernel has `tcpEchoServer` goroutine started on port 8080 |
| **Action** | `netDiag` dumps after boot |
| **Expected serial** | `TCP listener: port=8080 pid=0 backlog=8` |
| **Failure** | No listener in dump → `tcpListenerAlloc` or goroutine not wired |

### T1.4 3-way handshake (passive)

| Item | Detail |
|---|---|
| **Precondition** | Listener on 8080 |
| **Action** | `nc 127.0.0.1 10080` (hostfwd → guest 8080) |
| **Expected serial** | `SYN_RECEIVED → ESTABLISHED` transition line |
| **Expected pcap** | Three segments — SYN, SYN\|ACK, ACK |
| **Failure** | SYN|ACK missing → listener match or `tcpSendSegment` bug; ACK followed by RST → ISN or seq bookkeeping bug |

### T1.5 Small echo round-trip

| Item | Detail |
|---|---|
| **Precondition** | Handshake complete |
| **Action** | `echo "hello-gooos" | nc 127.0.0.1 10080` |
| **Expected host output** | `hello-gooos` echoed back |
| **Expected serial** | `TCP: echoed 12 B on port 8080` |
| **Failure** | No echo → `rxBuf` or `txBuf` wiring; wrong bytes → rbWrite/rbRead bug |

### T1.6 FIN close (peer-initiated)

| Item | Detail |
|---|---|
| **Precondition** | ESTABLISHED |
| **Action** | `nc` closes (Ctrl+D) |
| **Expected serial** | `ESTABLISHED → CLOSE_WAIT → LAST_ACK → CLOSED` |
| **Expected pcap** | FIN, ACK, FIN\|ACK, ACK (four segments) |
| **Failure** | No transition past CLOSE_WAIT → `tcpShutdownWrite` or kernel-server auto-close missing |

### T1.7 RST on stray segment

| Item | Detail |
|---|---|
| **Precondition** | Connection closed, TCB freed |
| **Action** | Replay a captured ACK via `python3` raw socket (Config C) |
| **Expected pcap** | Guest responds with RST |
| **Failure** | Silent drop → `tcpHandle` §7 RST-on-no-match branch missing |

### T1.8 Multiple simultaneous connections

| Item | Detail |
|---|---|
| **Precondition** | LISTEN on 8080 |
| **Action** | 4 parallel `nc` from host |
| **Expected serial** | 4 distinct ESTABLISHED transitions |
| **Expected behaviour** | All 4 echo independently |
| **Failure** | Connections interfere → TCB 4-tuple lookup bug |

### T1.9 Accept-queue overflow (RST)

| Item | Detail |
|---|---|
| **Precondition** | LISTEN with backlog 8 |
| **Action** | 10 simultaneous SYN floods (Config C, host-initiated via `hping3 --flood -S -p 8080 -c 10`) |
| **Expected pcap** | First 8 get SYN\|ACK; last 2 get RST |
| **Failure** | All get SYN\|ACK → accept-queue cap not enforced |

### T1.10 TCB exhaustion (RST)

| Item | Detail |
|---|---|
| **Precondition** | 16 active connections |
| **Action** | 17th incoming SYN |
| **Expected pcap** | RST to the 17th |
| **Expected serial** | `TCP: TCB pool exhausted; rejecting SYN` |
| **Failure** | Silent drop or kernel panic → `tcbAlloc` nil-check missing |

---

## 3. Phase TCP-2 Tests — Active Open + Retransmission

### T2.1 3-way handshake (active)

| Item | Detail |
|---|---|
| **Precondition** | Guest `tcpcli.elf` runs; host listens `nc -l 10080` (Config C — IP 10.0.0.1) |
| **Action** | Guest `tcpcli 10.0.0.1 10080 "hi"` |
| **Expected serial** | `SYN_SENT → ESTABLISHED` |
| **Expected pcap** | SYN (guest) → SYN\|ACK (host) → ACK (guest) |
| **Failure** | Guest stuck in SYN_SENT → `tcpActiveConnect` or RTO path |

### T2.2 Connect timeout

| Item | Detail |
|---|---|
| **Precondition** | No host listener |
| **Action** | `tcpcli 10.0.0.1 9999` |
| **Expected serial** | Three SYN retransmits at ~1 s / 3 s / 7 s intervals, then `tcpcli: connect timeout` |
| **Expected behaviour** | Exit code nonzero within ~11 s |
| **Failure** | Infinite loop → connect timer goroutine leak |

### T2.3 Data retransmission (RTO)

| Item | Detail |
|---|---|
| **Precondition** | ESTABLISHED via Config C; configure TAP to drop every 3rd TX via `iptables` or `tc netem loss` |
| **Action** | Guest sends 20 × 1 KiB segments |
| **Expected pcap** | Dropped segments retransmitted; RTO back-off visible |
| **Failure** | Segments lost forever → RTO timer not re-arming |

### T2.4 RTT estimator converges

| Item | Detail |
|---|---|
| **Precondition** | ESTABLISHED |
| **Action** | 100 segments with Config B-TCP (sub-ms RTT) |
| **Expected serial** | `netDiag` reports `rtoTicks=100` (clamped to 1 s floor) |
| **Failure** | RTO oscillates wildly → `tcpRTTUpdate` arithmetic error |

### T2.5 Karn's rule

| Item | Detail |
|---|---|
| **Precondition** | Force a retransmit via Config C packet drop |
| **Action** | Observe next non-retransmit RTT sample |
| **Expected serial** | No RTT update on the retransmitted segment's ACK |
| **Failure** | `srttTicks` jumps erratically → Karn guard missing |

### T2.6 FIN close (guest-initiated)

| Item | Detail |
|---|---|
| **Precondition** | ESTABLISHED |
| **Action** | Guest calls `TCPShutdown(fd, SHUT_WR)` |
| **Expected serial** | `ESTABLISHED → FIN_WAIT_1 → FIN_WAIT_2 → TIME_WAIT`; 60 s later `TIME_WAIT → CLOSED` |
| **Failure** | TCB never freed → TIME_WAIT timer leak |

### T2.7 TIME_WAIT holds the port

| Item | Detail |
|---|---|
| **Precondition** | Connection just entered TIME_WAIT |
| **Action** | Host retries from same ephemeral port within 60 s |
| **Expected pcap** | Guest responds with pure ACK (TIME_WAIT re-ACK path) |
| **Failure** | New SYN|ACK → TIME_WAIT handling broken |

---

## 4. Phase TCP-3 Tests — Flow Control

### T3.1 Advertised rcv window shrinks

| Item | Detail |
|---|---|
| **Precondition** | Kernel echo but with `tcpcli` that does NOT read data |
| **Action** | Host sends 8 KiB without reading from guest |
| **Expected pcap** | Guest `Window` drops from 8192 to 0 |
| **Failure** | Window stays full → `rcvWnd` bookkeeping bug |

### T3.2 Zero-window persist

| Item | Detail |
|---|---|
| **Precondition** | Continue from T3.1 |
| **Action** | Observe pcap for 30 s |
| **Expected pcap** | Periodic 1-byte probes from host (host side persist) OR guest resumes when user reads |
| **Failure** | No probes from either side → persist timer not arming |

### T3.3 Window reopen

| Item | Detail |
|---|---|
| **Precondition** | `rcvWnd == 0` |
| **Action** | `tcpcli` calls `TCPRecv(fd, buf[:4096])` once |
| **Expected pcap** | Next segment from guest carries `Window = 4096` |
| **Failure** | Window stays 0 → SWS avoidance holding too aggressively |

### T3.4 SWS avoidance

| Item | Detail |
|---|---|
| **Precondition** | rcvBuf 90% full |
| **Action** | `tcpcli` reads 10 bytes |
| **Expected pcap** | Window advertisement does NOT increase by 10 (SWS holds) |
| **Failure** | Window grows by 10 → §5.1 tcpAdvertiseWin not applied |

### T3.5 Delayed ACK

| Item | Detail |
|---|---|
| **Precondition** | ESTABLISHED; peer sends 1 × 500-byte segment |
| **Action** | Observe pcap for 300 ms |
| **Expected pcap** | Pure ACK from guest at ~200 ms mark |
| **Failure** | ACK immediately (0 ms) → delack timer never started |

### T3.6 Delayed-ACK accelerated

| Item | Detail |
|---|---|
| **Precondition** | ESTABLISHED; MSS 536 |
| **Action** | Peer sends 2 × 536-byte segments back-to-back |
| **Expected pcap** | ACK sent immediately (within 1 ms), NOT 200 ms |
| **Failure** | 200 ms delay → "every other segment" rule not implemented |

---

## 5. Phase TCP-4 Tests — Congestion Control

### T4.1 Slow-start ramp

| Item | Detail |
|---|---|
| **Precondition** | ESTABLISHED, Config B-TCP, no loss |
| **Action** | Guest sends 64 KiB via `TCPSendAll` |
| **Expected pcap** | First burst: 2 × MSS; second burst: 4 × MSS; doubling each RTT |
| **Failure** | Constant burst size → slow-start logic not engaging |

### T4.2 Slow-start → CA transition

| Item | Detail |
|---|---|
| **Precondition** | First RTO fire has set `ssthresh` |
| **Action** | Re-transmit the same 64 KiB |
| **Expected serial** | `cwnd` growth linearises once `cwnd >= ssthresh` |
| **Failure** | Continued exponential growth → `if t.cwnd < t.ssthresh` branch missing |

### T4.3 Fast retransmit (3-dup-ACK)

| Item | Detail |
|---|---|
| **Precondition** | Config C with `tc netem loss 10%`; send 100 × 1 KiB |
| **Action** | Observe pcap |
| **Expected pcap** | Retransmissions triggered before RTO (earlier than 1 s) |
| **Failure** | Only RTO-driven retransmits visible → dup-ACK counter not incrementing |

### T4.4 Fast recovery cwnd math

| Item | Detail |
|---|---|
| **Precondition** | Fast retransmit just fired |
| **Action** | Grep serial `netDiag` output |
| **Expected serial** | `cwnd == ssthresh + 3*mss` immediately after FR |
| **Failure** | `cwnd` collapsed to 1 MSS → RTO path accidentally taken |

### T4.5 RTO collapse

| Item | Detail |
|---|---|
| **Precondition** | No data for > RTO |
| **Action** | Block all outbound; wait for RTO fire |
| **Expected serial** | `cwnd = mssEff`, `ssthresh = max(flight/2, 2*mss)` |
| **Failure** | `cwnd` unchanged → RTO path missing CC update |

### T4.6 Throughput regression (iperf3)

| Item | Detail |
|---|---|
| **Precondition** | Config C + guest `iperf3` server at port 5201 (post-v1; if not available, skip) |
| **Action** | Host `iperf3 -c 10.0.0.2 -t 10` |
| **Expected** | Non-zero throughput; no kernel crash |
| **Failure** | Kernel panic → stress exposed a deadlock or buffer-overflow |

*Note*: T4.6 is aspirational — requires a userspace iperf3
port or minimal server in the codebase. OK to defer to a
follow-up test harness.

---

## 6. Phase TCP-5 Tests — Userspace API

### T5.1 `TCPSocket` + `TCPListen` + `TCPAccept`

| Item | Detail |
|---|---|
| **Precondition** | `tcpecho.elf` embedded in kernel |
| **Action** | From the shell: `tcpecho 8081 &` |
| **Expected serial** | `TCP listener: port=8081 pid=<pid>` |
| **Failure** | `TCPListen` returns error → `sys_listen` wiring |

### T5.2 End-to-end userspace echo

| Item | Detail |
|---|---|
| **Precondition** | T5.1 passed |
| **Action** | `echo hello | nc 127.0.0.1 10081` |
| **Expected host output** | `hello` echoed |
| **Expected serial** | `tcpecho: echoed 6B` |
| **Failure** | No echo → check `TCPRecv` / `TCPSend` loop in `user/cmd/tcpecho/main.go` |

### T5.3 `TCPConnect` from userspace

| Item | Detail |
|---|---|
| **Precondition** | Config C with host `nc -l 10080` |
| **Action** | `tcpcli 10.0.0.1 10080 "ping"` |
| **Expected** | `ping` appears on host `nc` |
| **Failure** | `TCPConnect` returns error → `sys_connect` wiring |

### T5.4 `TCPRecv` timeout

| Item | Detail |
|---|---|
| **Precondition** | ESTABLISHED, no peer data |
| **Action** | Userspace calls `TCPRecv(fd, buf, 100 /* 1 s */)` |
| **Expected** | Returns `< 0` (timeout) after ~1 s |
| **Failure** | Returns 0 or blocks forever → timeout path missing |

### T5.5 `TCPShutdown(SHUT_WR)`

| Item | Detail |
|---|---|
| **Precondition** | ESTABLISHED |
| **Action** | Userspace `TCPShutdown(fd, SHUT_WR)` |
| **Expected serial** | `FIN_WAIT_1` transition; subsequent `TCPSend` returns error |
| **Failure** | FIN never sent → `tcpShutdownWrite` wiring |

### T5.6 Kind safety (UDP fd rejects TCP syscalls)

| Item | Detail |
|---|---|
| **Precondition** | Create a UDP socket via existing `Socket()` |
| **Action** | Call `TCPSend(udpFd, ...)` |
| **Expected** | Returns `< 0` (fdErrBad) |
| **Failure** | Kernel accepts → missing kind check in `sys_tcp_send` |

### T5.7 Phase 1-5 regression

| Item | Detail |
|---|---|
| **Precondition** | All TCP tests passing |
| **Action** | `scripts/test_net.sh` (existing Phase 1-4 harness) + TODO_NET2 Part C flow |
| **Expected** | All UDP tests still pass; `dhcp.elf` still completes DORA |
| **Failure** | UDP broken → TCP refactor touched UDP hot path |

### T5.8 README demo paths

| Item | Detail |
|---|---|
| **Precondition** | `impldoc/net_tcp_work_plan.md` Phase TCP-5 complete |
| **Action** | Manually follow README Paths D and E |
| **Expected** | Paths work end-to-end as documented |
| **Failure** | README instructions drift from code → Path D/E update missed |

---

## 7. Fuzz & Stress

### T6.1 Random malformed segment fuzz

Craft 100 packets with random bits flipped in the TCP header
(via `python3 scapy`). Expected: zero kernel panics; invalid
segments counted in `netStats.TcpInvalid`.

### T6.2 Option-parsing fuzz

Craft SYN segments with malformed options (length=0, length
running off end, unknown kind). Expected: no infinite loop;
`tcpParseOptions` returns `ok=false` and the SYN is processed
with default MSS = 536.

### T6.3 TIME_WAIT churn

Loop: host opens + closes 100 connections back-to-back.
Expected: all 100 enter TIME_WAIT; all are freed within 60 s
plus churn; the 16-TCB cap holds throughout.

### T6.4 4-hour soak (manual)

Leave `tcpecho.elf` running; host loops `nc | cat | nc` for
4 hours. Expected: no memory leak observable via `netDiag`;
no kernel panic.

---

## 8. Pass / Fail Criteria Summary

A Phase TCP-N test suite passes when:

- Every `TN.m` test above passes under its stated QEMU
  configuration.
- No kernel panic, triple fault, or hang in any test.
- `netStats` counters match the expected event counts (e.g.,
  `TcpRetx` non-zero in Phase TCP-2, `TcpDupAck` non-zero in
  Phase TCP-4).
- Phase 1-5 regression (`scripts/test_net.sh` + TODO_NET2 Part
  C) continues to pass.

A single fuzz-test panic is a CRITICAL regression.

---

## 9. Open Questions

1. **Test-harness language**: Bash for glue + Python scapy for
   packet crafting. Alternative: pure Go harness. Recommendation:
   stay with Bash/Python (matches existing `tmp/test_*.sh`
   style).
2. **iperf3 port**: whether to port a minimal iperf3-compatible
   receiver to gooos userspace. Recommendation: defer to post-v1;
   `nc` + `dd` cover throughput spot-checks.
3. **Fuzz reproducibility**: fixed seed vs random. Recommendation:
   both — fixed seed for CI, random for soak.

---

## 10. Relationship to Other Documents

- **`impldoc/net_test_plan.md`**: Phase 1-4 test plan whose
  structure this document mirrors. Every Configuration A-D
  listed there applies to TCP too.
- **`net_tcp_overview.md §6`**: phase breakdown that the §2-§6
  test sections correspond to.
- **`net_tcp_state_machine.md §12`**: verification criteria
  that map 1:1 to tests T1.4 / T1.6 / T2.1 / T2.6 here.
- **`net_tcp_flow_and_congestion.md §10`**: flow-control tests
  listed there are expanded in §4 of this document.
- **`pasttodos/TODO_NET2.md` Part C**: Phase 5 verification
  style this document follows.
