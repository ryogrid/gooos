# Running the networking demos

gooos talks UDP/IP/Ethernet AND TCP/IP to the host through the
emulated Intel 82540EM NIC. Five end-to-end paths are manually
verifiable.

**Before you start — two easy-to-trip-over gotchas:**

1. **The gooos shell lives on the serial line, not the QEMU
   window.** `make run-net` launches QEMU with `-serial stdio`.
   That means the gooos shell prompt, boot log, and all stdout
   appear in **the terminal where you ran `make run-net`** —
   _not_ in the VGA window QEMU pops up. The VGA window shows a
   few boot banners and then sits quietly; it is not the
   interactive console. Type commands in the terminal.
   (Keystrokes typed in the QEMU window are delivered to the
   kernel via PS/2 IRQ and do reach the shell, but the echoed
   output still goes to serial, so typing there looks like
   nothing is happening.)
2. **Host-side `nc` needs a *second* terminal.** Terminal 1 is
   occupied by `make run-net` / the gooos shell. Open a second
   host shell and run `nc` from there. Also wait for the serial
   log in terminal 1 to show `TCP: listener port=8080 (kernel
   echo)` (Path D) or `tcpecho: starting userspace echo on TCP
   port 8081` (Path E) before invoking `nc` — hitting a port
   before the listener is up will just RST-close the connection
   and `nc` exits silently.

The five paths:

| Path | Listener | Host-side port | What it exercises |
|---|---|---|---|
| A | Kernel-builtin UDP echo | `127.0.0.1:9999` (hostfwd → guest 7) | `e1000` RX → `netRxLoop` **kernel kthread (BSP-pinned per M6/M7 R1)** → `ethernetDispatch` → `ipv4Handle` → `udpHandle` → kernel echo path → `ipv4Send` → `e1000Transmit` TX |
| B | Userspace `udpecho.elf` | `127.0.0.1:19999` (hostfwd → guest 17) | Path A's RX half + `socketFd.recvQ` (`udpDgramQueue` MPSC) → `sys_recvfrom` → Ring-3 `UDPRecvFrom` → `UDPSendTo` → `sys_sendto` → `udpSend` → TX. Under M7, `udpecho.elf` itself runs on an AP — its `KEvent.Wait` parks on the AP, BSP `netRxLoop` `Signal`s, cross-CPU wake via `gooosWakeupCPU` IPI `0xFC` |
| C | Userspace `dhcp.elf` | Broadcast to `255.255.255.255:67` via `sys_sendto_bcast` / QEMU slirp's built-in DHCP server at `10.0.2.2` | Full DORA, `sys_net_config` lease apply, `/network.conf` persistence. Under M7, `dhcp.elf` runs on an AP; the broadcast send is BSP-mediated through the same `udpSend` path. |
| D | Kernel-builtin TCP echo | `127.0.0.1:10080` (hostfwd → guest 8080) | `ipv4Handle` → `tcpHandle` → TCB state machine (LISTEN → SYN_RECEIVED → ESTABLISHED → CLOSE_WAIT → LAST_ACK → CLOSED) + `tcpEchoServer` **kernel kthread (BSP-pinned)** + `tcpSendSegment` → `ipv4Send` → TX |
| E | Userspace `tcpecho.elf` | `127.0.0.1:10081` (hostfwd → guest 8081) | Path D's state machine + `sys_accept` → Ring-3 `TCPAccept` → `TCPRecv` / `TCPSendAll` → `sys_tcp_send`/`sys_tcp_recv` → `tcpTCBDrainTX` → TX. Under M7, `tcpecho.elf` runs on an AP and parks on `KEvent.Wait`; segment arrival on BSP `netRxLoop` triggers cross-CPU wake. |

> **M7 routing summary** (per
> [`../no_goroutine_kernel_design/15_userspace_smp_on_aps.md`](../no_goroutine_kernel_design/15_userspace_smp_on_aps.md)):
> all hardware IRQs (PIT, PS/2 keyboard, e1000) land on **BSP only**
> via PIC pass-through. The protocol-handling kthreads
> (`netRxLoop`, `udpEcho`, `tcpRTOScanner`, `tcpEcho`,
> `netDiagLoop`) are BSP-pinned per R1. AP-resident user
> processes do their socket I/O via `KEvent` park/wake; the
> BSP service kthread that handles the IRQ → protocol
> demux → `KEvent.Signal` wakes the AP via wake-IPI `0xFC`.
> e1000 IRQ steering to APs is **deferred to M8**
> (`15_userspace_smp_on_aps.md` §12).

## Communication flow (ASCII)

```
  Host terminal                   QEMU process                    gooos guest (Ring 3 + Ring 0)
  =============                   ============                    ============================

  nc -u 127.0.0.1 9999  ──────►   ┌─────────────┐                 ┌───────────────────────────┐
  (Path A: kernel UDP echo)        │  slirp NAT  │                 │  Ring 3 userspace         │
                                   │  hostfwd    │                 │  ┌─────────────────────┐  │
  nc -u 127.0.0.1 19999 ──────►   │  9999 →  7  │                 │  │ udpecho.elf         │  │
  (Path B: userland UDP echo)      │ 19999 → 17  │                 │  │ dhcp.elf            │  │
                                   │ 10080 → 8080│                 │  │ tcpecho.elf         │  │
  nc    127.0.0.1 10080 ──────►   │ 10081 → 8081│                 │  │ tcpcli.elf          │  │
  (Path D: kernel TCP echo)        └──────┬──────┘                 │  └──────┬──────────────┘  │
                                          │                        │         │                 │
  nc    127.0.0.1 10081 ──────►           │                        │         │ syscall (int 0x80)
  (Path E: userland TCP echo)             │                        │         │
       ▲                                  │ virtual Ethernet       │         ▼                 │
       │                                  │ frames (L2)            │  ┌─────────────────────┐  │
       │                                  ▼                        │  │ Ring 0 kernel       │  │
       │                          ┌───────────────┐                │  │                     │  │
       │                          │ QEMU e1000    │ ◄──MMIO BAR0─  │  │  netsock.go         │  │
       │                          │ device model  │    PCI cfg     │  │  ├── socketFd +     │  │
       │                          │ (DMA rings +  │    IRQ 11      │  │  │   udpBindings[]  │  │
       │                          │  INTx# line)  │ ──────────────►│  │  ├── udp.go         │  │
       │                          └───────┬───────┘                │  │  ├── tcp.go (TCB,   │  │
       │                                  │                        │  │  │   listener,     │  │
       │                                  │                        │  │  │   state machine)│  │
       │                                  │                        │  │  ├── tcp_retx.go   │  │
       │                                  │                        │  │  ├── tcp_rtt.go    │  │
       │                                  │                        │  │  ├── tcp_flow.go   │  │
       │                                  │                        │  │  ├── tcp_cc.go     │  │
       │                                  │                        │  │  ├── ipv4.go       │  │
       │                                  │                        │  │  ├── arp.go        │  │
       │                                  │                        │  │  ├── ethernet.go   │  │
       │                                  │                        │  │  └── e1000.go      │  │
       │                                  │                        │  └─────────────────────┘  │
       │                                  │                        │                           │
       └──────────────────────────────────┴────────────────────────┴───────────────────────────┘
                         (reply path takes the same wire in reverse)
```

Lock-ordering ranks consulted along the RX path are 5 (`netBufLock`)
→ 6 (`arpLock`) → 7 (`udpLock`) → 8 (`statsLock`), plus 9
(`tcbTableLock`) / 10 (`tcpListenLock`) / 11 (`tcpTimerLock`) along
the TCP paths. Rank 12 (`timerListLock`, the `afterTicks` timer
wheel) sits above all of them and is acquired from timer-wait
sites independent of the RX pipeline. See `src/spinlock.go`.

## A. Kernel-builtin UDP echo (port 7)

No shell commands needed — the kernel auto-starts `udpEchoServer` on
port 7 during `netInit` at boot. From a second host terminal:

```
$ make run-net            # terminal 1: boots gooos, shell prompt on stdio

$ echo -n 'hello-from-host' | nc -u -w 2 127.0.0.1 9999    # terminal 2
hello-from-host
```

Success: `nc` prints the same bytes it sent. The guest's serial log
records the RX packet and TX reply in the `netDiag` counter block.
`netDiag` fires an initial dump ~5 s after boot and then every ~10 s
periodically via the `afterTicks` timer-wheel.

## B. Userspace UDP echo (port 17)

In the gooos shell (terminal 1, `make run-net`):

```
$ udpecho
udpecho: starting userspace echo on UDP port 17
```

This blocks — `udpecho.elf` is a Ring-3 program that loops
`UDPRecvFrom` → `UDPSendTo`. From a second host terminal:

```
$ echo -n 'hello-from-userland' | nc -u -w 2 127.0.0.1 19999
hello-from-userland
```

Round-trip exercises the complete stack from the slirp hostfwd
through the kernel RX dispatcher, up through `sys_recvfrom` into
Ring 3, back out through `sys_sendto`.

## C. DHCP (obtain IP / netmask / gateway / DNS)

In the gooos shell:

```
$ dhcp
dhcp: starting DHCP client
dhcp: MAC = 52:54:00:12:34:56
dhcp: DISCOVER sent, waiting for OFFER...
dhcp: OFFER received: IP = 10.0.2.15
dhcp: REQUEST sent, waiting for ACK...
ARP: sent gratuitous announcement for 10.0.2.15

dhcp: network configured:
  IP      = 10.0.2.15
  Netmask = 255.255.255.0
  Gateway = 10.0.2.2
  DNS     = 10.0.2.3
  Lease   = 86400 seconds
  Server  = 10.0.2.2
```

The client runs the full RFC 2131 DORA against QEMU slirp's built-in
DHCP server (hard-wired at `10.0.2.2`), pushes the lease into the
kernel stack via `sys_net_config`, sends a gratuitous ARP announcing
the new `yiaddr`, and writes the result to `/network.conf`. Inspect
it afterwards:

```
$ cat network.conf
# Network configuration (DHCP)
ip=10.0.2.15
netmask=255.255.255.0
gateway=10.0.2.2
dns=10.0.2.3
lease=86400
server=10.0.2.2
```

A `netDiag` dump now shows `DNS: 10.0.2.3`, confirming the kernel
global was updated by `sys_net_config(ncSetDNS, …)`.

## D. Kernel-builtin TCP echo (port 8080)

No gooos-shell commands needed — `tcpInit` registers the
listener and spawns `tcpEchoServer` during `netInit` at boot.

```
# Terminal 1 — boot gooos (leave this running):
$ make run-net
...
PCI: found e1000 at 00:03.0 ...
e1000: link up
NET: initialized IP=10.0.2.15 gw=10.0.2.2
...
TCP: listener port=8080 (kernel echo)       <-- wait for this

# Terminal 2 — from any host shell, round-trip a payload:
$ echo -n 'hello-tcp' | nc -w 3 127.0.0.1 10080
hello-tcp
```

If `nc` exits with no output, check terminal 1 for the "TCP:
listener port=8080" line — it takes a second or two after the
VGA banner. Running `nc` before that will just RST-close.

This exercises the full 3-way handshake (SYN → SYN|ACK → ACK),
the echo data path, and the close handshake (peer FIN → our
ACK → our FIN → peer ACK → CLOSED). Path D works regardless of
how long the guest has been idle before `nc` is invoked — the
`afterTicks` timer-wheel keeps `netRxLoop` progressing for
arbitrarily long idle windows (see
`current_impl_doc/known_issues.md` §"afterTicks single-dispatcher
timer wheel" for the history).

## E. Userspace TCP echo (port 8081)

**Note on shell behaviour:** the gooos shell currently has
no background-job (`&`) support — `user/cmd/sh/main.go`
always `Spawn`s then immediately `Wait`s. So `tcpecho`
runs as a *blocking* foreground command: the shell prompt
won't come back, but the echo loop inside `tcpecho.elf`
services incoming TCP connections regardless (the accept
loop and per-connection goroutines run as Ring-3 goroutines
inside the blocked process). That's enough to demo Path E —
just close QEMU when you're done with the demo.

In the gooos shell (terminal 1, `make run-net`):

```
$ tcpecho
tcpecho: starting userspace echo on TCP port 8081
            (prompt does NOT return — this is expected)
```

`tcpecho.elf` is a Ring-3 program that loops
`TCPAccept` → per-connection goroutine → `TCPRecv` / `TCPSendAll`
→ close on peer FIN. With `tcpecho` blocking the shell, from
a second host terminal:

```
$ echo -n 'hello-userland-tcp' | nc -w 3 127.0.0.1 10081
hello-userland-tcp
```

Round-trip exercises sys_accept → sys_tcp_send → sys_tcp_recv
into Ring 3 and back through `tcpTCBDrainTX` to the wire.

Guest-initiated active open (reach a host listener):

**Important:** pick a host port that is **not** in
`make run-net`'s hostfwd list (10080 / 10081 / 9999 / 19999 —
all claimed by QEMU). Using one of those ports double-binds
them: if `nc -l` runs first, QEMU fails to bring up the
hostfwd; if QEMU runs first, `nc -l` can't bind. The example
below uses `5555`, but any unused port above 1024 works.

```
# On the host, start a listener on 5555 (not in the hostfwd list):
$ nc -l 5555

# In the gooos shell:
$ tcpcli 10.0.2.2 5555 hi-from-gooos
tcpcli: <- hi-from-gooos
```

Under QEMU slirp, `10.0.2.2` is the host's virtual gateway, so
the guest's SYN reaches the listener on the host directly
(slirp NATs the connection to `127.0.0.1:5555`). This
exercises `tcpActiveConnect` → SYN_SENT → ESTABLISHED plus the
`tcpcli.elf` FIN-from-our-side close.

## Packet capture (optional)

Add `-object filter-dump,id=d,netdev=n0,file=tmp/net.pcap` to the
QEMU invocation (edit the `run-net` Makefile target or run the
command manually). Open the pcap in Wireshark to see the actual
frames — useful when debugging a path A/B/C/D/E failure. The DORA
exchange and the TCP state-machine transitions are especially
readable this way.

## Automated smoke tests

`scripts/test_net.sh` (invokable via `make test-net`) exercises
path A non-interactively — boots the ISO in headless QEMU, greps
the boot-time markers, and round-trips a payload through the
hostfwd 9999→7. Phase-5 paths (B and C) are currently
hand-verified only.

The TCP phases have dedicated harnesses under `scripts/`:

| Script | Covers |
|---|---|
| `test_tcp_phase1.sh` | passive open + kernel echo + FIN close |
| `test_tcp_phase2.sh` | active open / TIME_WAIT reap / RTO back-off (TAP notes inline) |
| `test_tcp_phase3.sh` | flow control / zero-window persist / delayed ACK / SWS |
| `test_tcp_phase4.sh` | slow start / cwnd collapse / fast retransmit |
| `test_tcp_phase5.sh` | Phase-5 end-to-end — kernel echo round-trip + UDP regression + tcpecho/tcpcli ELF presence |
| `test_tcp_latetiming.sh` | late-timing RX-stall reproducer (nc 15 s after Ring-3 boot). Expected PASS on HEAD; regression gate for the `afterTicks` timer-wheel fix. |
| `test_tcp_longidle.sh <seconds>` | parametrised idle-window variant of latetiming. Verified at 15 / 20 / 30 / 60 / 120 / 300 s. |
