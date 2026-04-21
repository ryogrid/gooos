# Network Stack: e1000 Driver to Userspace Socket API

## Stack Layers and Main Files

- Device/PCI: `src/pci.go`, `src/e1000.go`, `src/e1000_irq.go`
- L2 dispatch: `src/net.go`, `src/ethernet.go`, `src/netbuf.go`, `src/netstats.go`
- ARP: `src/arp.go`
- IPv4/ICMP: `src/ipv4.go`, `src/icmp.go`
- UDP: `src/udp.go`
- TCP: `src/tcp.go`, `src/tcp_segment.go`, `src/tcp_flow.go`, `src/tcp_cc.go`, `src/tcp_rtt.go`, `src/tcp_retx.go`
- Socket ABI/syscalls: `src/netsock.go`

## NIC Driver Layer (`src/e1000.go`)

Key design constants:

- RX descriptors: `e1000NumRxDesc = 64`
- TX descriptors: `e1000NumTxDesc = 32`
- descriptor size: 16 bytes
- buffer size per descriptor: 2048 bytes

Driver flow:

1. BAR0 MMIO mapping with cache-disabled flags.
2. reset + control/status programming.
3. MAC read from `RAL0/RAH0`.
4. RX/TX ring and DMA buffers setup.
5. RX polling via `e1000TryReceive()`.
6. TX enqueue via `e1000Send()`.

IRQ path:

- `handleE1000IRQ` in `src/e1000_irq.go` handles NIC interrupt cause bookkeeping.

## Network Core (`src/net.go`)

`netInit()`:

- sets default IPv4 config (`10.0.2.15/24`, gateway `10.0.2.2`)
- emits gratuitous ARP
- starts `netRxLoop`
- starts UDP echo server and TCP init

`netRxLoop()`:

- repeatedly calls `drainRxRing()`
- increments diagnostic counters
- yields via `runtime.Gosched()`

`ethernetDispatch(frame)`:

- validates frame length bounds
- parse header
- destination filtering (`isForUs`)
- demux by EtherType to ARP/IPv4 handlers

## Buffer Pool (`src/netbuf.go`)

- pool size: 128 buffers
- buffer size: 2048 bytes
- allocation bitmap: `netBufFree [2]uint64`
- lock: `netBufLock`

Used for bounded allocation and instrumentation (`BufAllocFail`).

## ARP (`src/arp.go`)

- ARP cache entries: `arpCacheSize = 16`
- cache operations under `arpLock`
- ARP resolve path supports timeout and gateway/next-hop interactions

## UDP (`src/udp.go`)

- bind table capacity: `udpMaxBinds = 8`
- checksum validation supports RFC 768 checksum-disabled receive (`wire checksum == 0`)
- receive demux by destination port into bound channel
- send path builds UDP header + pseudo-header checksum and calls IPv4 transmit

## TCP (`src/tcp.go` and companion files)

### Core structures

- `TCB` table capacity: `tcbMax = 16`
- listener slots: `tcpMaxListeners = 4`
- per-listener queue depth: `tcpAcceptQueueDepth = 8`
- TX/RX ring buffers: 8192 bytes each

### State machine

`tcpState*` enum includes RFC 793 states:

- CLOSED
- LISTEN
- SYN_SENT
- SYN_RECEIVED
- ESTABLISHED
- FIN_WAIT_1
- FIN_WAIT_2
- CLOSE_WAIT
- CLOSING
- LAST_ACK
- TIME_WAIT

### Companion logic

- segmentation: `src/tcp_segment.go`
- flow control / SWS avoidance: `src/tcp_flow.go`
- congestion control: `src/tcp_cc.go`
- RTT/RTO estimation: `src/tcp_rtt.go`
- retransmission/timer scanner: `src/tcp_retx.go`

## Socket Syscall Layer (`src/netsock.go`)

Supported socket families/types:

- `AF_INET + SOCK_DGRAM` (UDP)
- `AF_INET + SOCK_STREAM` (TCP)

Primary syscall numbers:

- UDP/socket control: 22..27
- TCP socket flow: 28..33

`socketFd` discriminates behavior using `kind`:

- UDP
- TCP idle
- TCP listener
- TCP connected

Close paths branch per kind, including listener queue drain and TCB close/free transitions.

## Locking and Concurrency Notes

- UDP bind table lock: `udpLock`
- TCB table lock: `tcbTableLock`
- listener lock: `tcpListenLock`
- timer lock(s) in TCP timing paths

Current design assumes controlled scheduling and careful lock ordering; socketFd internals are not independently locked and rely on surrounding process execution model.

## Network Invariants

1. RX buffers cannot be consumed without explicit copy on handoff where required.
2. Port bind uniqueness is enforced per protocol table.
3. TCP state transitions are table-driven with bounded TCB/listener resources.
4. Socket close must detach kernel data structures to avoid stale references.

## Known Risk Areas

- Edge cases around long-lived zero-window/persist and rare retransmission timing remain sensitive.
- Some behavior remains benchmarked against QEMU/slirp assumptions.
- Concurrency assumptions in socket FD internals would need rework under fully parallel multi-CPU syscall execution.
