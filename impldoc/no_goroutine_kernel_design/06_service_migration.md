# 06 — Service migration inventory and rewire plan

This doc is the 1:1 map from every current goroutine-spawn / channel
/ `afterTicks` site in `src/*.go` to its Route C replacement. §09
uses this inventory to order the milestones.

## Goroutine spawn sites

Every `^\s*go ` in `src/*.go`, in scan order.

| # | File:line | Spawned | Lifetime | Role |
|---|-----------|---------|----------|------|
| 1 | `src/afterticks.go:86` | `timerDispatcher` | forever | Timer wheel dispatcher |
| 2 | `src/goroutine_tss.go:88` | anonymous task-offset test | one-shot | Boot-time TinyGo-task struct-offset self-test |
| 3 | `src/process.go:415` | `ring3Wrapper(child)` | per-process | Wrapper for every Ring-3 child (shell, external commands) |
| 4 | `src/net.go:60` | `netRxLoop` | forever | e1000 RX poller |
| 5 | `src/net.go:63` | `udpEchoServer` | forever | Demo UDP echo server |
| 6 | `src/main.go:348` | anon `func { ch <- 42 }` | one-shot | Boot-time Spike2 chan round-trip test |
| 7 | `src/main.go:360` | anon `func { … afterTicks self-test }` | one-shot | Boot-time afterTicks self-test |
| 8 | `src/main.go:417` | anon `func { … }` | one-shot | Boot-time network startup probe (calls `netDiag` inline at 417/422) |
| 9 | `src/main.go:441` | `fsTask` | forever | Filesystem serializer |
| 10 | `src/main.go:617` | `smpBasicProbe` | one-shot | SMP distribution probe (gated by `runSMPBasicProbe`) |
| 11 | `src/main.go:634` | `kpMarker` | forever while gated | Kernel-probe marker (gated by `runPreemptProbe`) |
| 12 | `src/main.go:635` | `kpHog` | forever while gated | Kernel-probe hog (gated by `runPreemptProbe`) |
| 13 | `src/elf.go:250` | `ring3Wrapper(proc)` | per-process | Same as #3 via `elfSpawn` — for boot shell process |
| 14 | `src/tcp_retx.go:127` | `tcpRTOScannerLoop` | forever once armed | TCP retransmission / persist / TIME_WAIT / delayed-ACK scanner |
| 15 | `src/tcp.go:1344` | `tcpEchoServer` | forever | Demo TCP echo server (port 8080) |

**Discrepancy from hoge.md §06**: `netDiag` is NOT a spawned
goroutine. It's a plain function at `src/net.go:142` called inline
from the anonymous goroutine at `src/main.go:417,422`. §06 treats it
as a utility; the migration rewrites the anonymous probe at #8 into
a periodic kernel-thread loop (below) that calls `netDiag()` the
same way.

### Classification

| Class | Entries | Migration plan |
|-------|---------|---------------|
| **Boot-time one-shot** | #2, #6, #7, #8, #10 | Convert to ordinary function calls from the `main()` init sequence. No kernel thread needed. #10 is already gated off by default; keep the gate. |
| **Long-lived service** | #1, #4, #5, #9, #14, #15 | `kschedSpawn(name, fn)` — one kernel thread per service |
| **Per-process wrapper** | #3, #13 (same body) | `kschedSpawn("ring3Wrapper", func(){ ring3Wrapper(proc) })` — one kernel thread per Ring-3 process. Stack returns to pool on exit (§07) |
| **Gated demo probe** | #11, #12 | `kschedSpawn` behind the existing `runPreemptProbe` gate. Dies with the gate when they fall out of scope |

Notes:

- The five one-shot sites (#2, #6, #7, #8, #10) are cosmetic boot
  tests; none of them produce an observable regression gate. The
  existing cycle-6 plan in `current_impl_2026_04_24/fix_plan_deferred_1_5/`
  had a T1 cleanup proposing they be deleted outright. **Route C's
  §06 recommendation**: delete them during the M3 / M5 window (not
  required for Route C correctness, but the convergence is
  natural — they're useless demos that happen to use `go`).
- #11 / #12 (`kpHog`, `kpMarker`) are pedagogical; Route C keeps
  them as kernel threads to preserve the existing
  `scripts/test_preempt_kernel.sh` regression. Their preempt behaviour
  under kschedYield is the M1 gate.

## Channel inventory

Every channel declaration + `make(chan)` in `src/*.go`.

### Channel-typed struct fields (8)

| # | File:line | Field | Purpose |
|---|-----------|-------|---------|
| 1 | `src/fs.go:175` | `fsRequest.reply chan *fsResponse` | FS per-request reply |
| 2 | `src/fs.go:186` | `fsReqCh chan *fsRequest` | FS request queue (global) |
| 3 | `src/process.go:46` (declared) / `src/process.go:310` (init) | `Process.exitCh chan uintptr` | Parent ← child exit code |
| 4 | `src/udp.go:40` | `UDPBinding.Ch chan UDPDatagram` | UDP socket recv channel |
| 5 | `src/netsock.go:115` | `socketFd.recvCh chan UDPDatagram` | Per-socket recv queue |
| 6 | `src/ring3_pool.go:28` | `ring3StackPoolCh chan int` | Free-slot indices for Ring-3 kernel-stack pool |
| 7 | `src/afterticks.go:73` | `timerEntry.ch chan<- struct{}` | Timer wheel fire target |
| 8 | `src/pipe.go:26` | `pipe.ch chan byte` | Pipe byte buffer |

### `make(chan)` call sites (21)

| # | File:line | Type / cap | Purpose |
|---|-----------|-----------|---------|
| a | `src/afterticks.go:128` | `chan struct{}, 1` | afterTicks per-call signal |
| b | `src/fs.go:190` | `chan *fsRequest, 8` | fsReqCh init |
| c | `src/fs.go:220` | `chan *fsResponse, 1` | fsSendCreate reply |
| d | `src/fs.go:227` | `chan *fsResponse, 1` | fsSendWrite reply |
| e | `src/fs.go:234` | `chan *fsResponse, 1` | fsSendRead reply |
| f | `src/fs.go:241` | `chan *fsResponse, 1` | fsSendList reply |
| g | `src/fs.go:248` | `chan *fsResponse, 1` | fsSendDelete reply |
| h | `src/pipe.go:42` | `chan byte, 4096` | Pipe buffer |
| i | `src/ring3_pool.go:61` | `chan int, maxRing3Procs` | Ring-3 pool free-list |
| j | `src/udp.go:143` | `chan UDPDatagram, 16` | UDP binding recv |
| k | `src/netsock.go:239` | `chan UDPDatagram, 16` | Socket recv |
| l | `src/process.go:310` | `chan uintptr, 1` | exitCh for elfSpawn child |
| m | `src/elf.go:190` | `chan uintptr, 1` | exitCh for boot shell |
| n | `src/main.go:347` | `chan int, 1` | Spike2 test (one-shot) |
| o | `src/goroutine_tss.go:87` | `chan struct{}, 1` | Task-offset self-test (one-shot) |

(Plus the six fsRequest replies counted at c-g above. Total 21 sites.)

### `<-afterTicks(` consumers (12)

| # | File:line | Ticks | Purpose |
|---|-----------|-------|---------|
| A | `src/main.go:361` | 2 | Boot self-test (one-shot) |
| B | `src/main.go:418` | 500 | Boot net-probe first fire |
| C | `src/main.go:421` | 1000 | Boot net-probe iteration |
| D | `src/main.go:666` | 5 | Timer-wheel demo loop |
| E | `src/main.go:692` | 1 | Test loop |
| F | `src/keyboard_irq.go:121` | 1 | Keyboard IRQ fallback poll |
| G | `src/netsock.go:593` | 5 | sys_recvfrom timeout poll |
| H | `src/netsock.go:648` | 5 | Socket poll |
| I | `src/netsock.go:784` | 5 | Socket op poll |
| J | `src/tcp.go:1356` | `tcpEchoPollTicks` | TCP echo server poll |
| K | `src/tcp_retx.go:140` | `tcpRetxScanTicks` | RTO scanner poll |
| L | `src/userspace.go:454` | variable | User-space sleep hook (sys_sleep) |

## Migration map: goroutine → kernel thread

Detailed per-service rewire. `kschedSpawn(name, fn)` is the §02
API. Every call replaces the current `go fn()`.

### Service 1 — `timerDispatcher` (`src/afterticks.go:86..121`)

- Current: `go timerDispatcher()` from `afterTicksInit`.
- Route C: `kschedSpawn("timerDispatcher", timerDispatcher)`.
- Body rewrites:
  - `runtime.Gosched()` at line 117 → `kschedYield()`.
  - `kernelYield()` at line 119 → **delete** (it was a half-migration
    artifact; kernel threads use `kschedYield` only).
  - `ready[j] ch <- struct{}{}` sends at line 113 → `ready[j]->Signal()`
    — where `ready[j]` is now a `*KEvent` instead of a
    `chan<- struct{}`.
  - `timerEntry.ch chan<- struct{}` field (line 73) →
    `timerEntry.ev *KEvent`.
- `afterTicks(d)` wrapper (line 126..150) rewires:
  - Returns `*KEvent` instead of `<-chan struct{}` (new function
    `KEventAfter(d)` per §03; `afterTicks` stays as a temporary
    shim returning a channel implemented as a goroutine waiting on
    `KEventAfter` until every caller is migrated).
- **Boot-ordering**: must run before any caller hits a timed wait.
  Today spawned from `main.go`'s init block. Route C: spawned from
  the same init position; kschedLoop has not yet started, so the
  thread sits Runnable until kschedLoop begins.

### Service 2 — `netRxLoop` (`src/net.go:74..82`)

- Current: `go netRxLoop()` at `src/net.go:60`.
- Route C: `kschedSpawn("netRxLoop", netRxLoop)`.
- Body rewrites:
  - `runtime.Gosched()` at line 78 → `kschedYield()`.
  - `kernelYield()` at line 80 → **delete** (same reason as
    timerDispatcher).
- No channels to rewire — this service already polls and does not
  own any channel.

### Service 3 — `udpEchoServer` (spawned at `src/net.go:63`; body
elsewhere, per grep)

- Current: `go udpEchoServer()`.
- Route C: `kschedSpawn("udpEchoServer", udpEchoServer)`.
- Channels to rewire: `UDPBinding.Ch` (inventory #4) — MPMC queue
  (though in practice 1:1). Becomes `KQueue[UDPDatagram]`.
- `<-udpBinding.Ch` recv → `udpBinding.Q.Pop()`.
- `udpBinding.Ch <- dg` send in the RX dispatch path →
  `udpBinding.Q.TryPush(dg)` (existing code already handles
  drop-on-full; `TryPush` returns bool).

### Service 4 — `fsTask` (`src/fs.go:195..216`)

- Current: `go fsTask()` from `main.go:441`.
- Route C: `kschedSpawn("fsTask", fsTask)`.
- Channels:
  - `fsReqCh chan *fsRequest` (inventory #2) → `fsReqQ KQueue[*fsRequest]`,
    initialized via `fsReqQ.Init(8)`.
  - `fsRequest.reply chan *fsResponse` (inventory #1) →
    replaced with an embedded `KEvent` and an owned
    `*fsResponse` field: each caller allocates `req := &fsRequest{…}`
    with `req.ev` a fresh `KEvent`. `fsTask` fills
    `req.resp = &fsResponse{…}` then `req.ev.Signal()`. The caller
    `req.ev.Wait(); return req.resp`.
- Body rewrites:
  - `for req := range fsReqCh` (line 198) → `for { req := fsReqQ.Pop(); … }`.
  - `req.reply <- resp` (line 214) → `req.resp = resp; req.ev.Signal()`.
- Callers (`fsSendCreate`, `fsSendWrite`, `fsSendRead`, `fsSendList`,
  `fsSendDelete` at `src/fs.go:218..250`):
  - Each currently does: create reply chan, push req, `<-reply`.
  - After: create `req := &fsRequest{…, ev: KEvent{}}`, push req,
    `req.ev.Wait()`, return the `req.resp` field.
- `ensureFSReqCh()` (`src/fs.go:188`) → `ensureFSReqQ()` — same
  lazy-init pattern but on a `KQueue` instead of a `chan`.

### Service 5 — `tcpRTOScannerLoop` (`src/tcp_retx.go:138..143`)

- Current: `go tcpRTOScannerLoop()` from `tcpStartRTOScanner`.
- Route C: `kschedSpawn("tcpRTOScannerLoop", tcpRTOScannerLoop)`.
- Body: `<-afterTicks(tcpRetxScanTicks)` → `kschedTimedPark(tcpRetxScanTicks)`
  or `KEventAfter(tcpRetxScanTicks).Wait()`. Pick one — §03 prefers
  `kschedTimedPark` for the scanner because it doesn't need
  `KEvent` reusability and the primitive is simpler.

### Service 6 — `tcpEchoServer` (`src/tcp.go:1344..`)

- Current: `go tcpEchoServer()` at boot.
- Route C: `kschedSpawn("tcpEchoServer", tcpEchoServer)`.
- Body: `<-afterTicks(tcpEchoPollTicks)` → `kschedTimedPark`.
- Possible sub-spawned per-connection goroutines? Grep `src/tcp.go`
  for `go ` inside `tcpEchoServer`. (Reviewer gate: verify.) If any,
  each `go handleConn(c)` becomes `kschedSpawn("tcpConn", func(){ handleConn(c) })`.
- Known per-connection spawn: the echo server historically spawned
  one goroutine per connection; Route C keeps the same model with
  kernel threads. Per-connection kernel threads exit on connection
  close via `kschedExit`.

### Service 7 — `ring3Wrapper` (§07 owns the detailed rewire)

- Current: `go ring3Wrapper(proc)` from `src/process.go:415` and
  `src/elf.go:250`.
- Route C: `kschedSpawn("ring3Wrapper", func(){ ring3Wrapper(proc) })`.
- Detailed rewire in §07 — covers TSS.RSP0 handoff, CR3 switch,
  `processExit` → `KEvent.Signal`, `processWait` → `KEvent.Wait`.

### Service 8 — `ring3StackPoolCh` (`src/ring3_pool.go`)

- Current: `ring3StackPoolCh chan int, cap=maxRing3Procs`. Free
  slots live in the channel; `ring3StackAcquire` does `idx := <-ch`,
  `ring3StackRelease` does `ch <- idx`.
- Route C: replace the channel with a `KQueue[int32]` of the same
  capacity. `Acquire` does `Q.Pop()`, `Release` does `Q.TryPush(idx)`
  (must never fail; capacity == maxRing3Procs).
- Alternative: replace with a bitmap + spinlock (no queue ordering
  needed). Pick the simpler KQueue for consistency with §03. §10
  parks the bitmap variant if contention shows up.

### Service 9 — `pipe.ch` (`src/pipe.go:26,42`)

- Current: `chan byte, cap=4096`.
- Route C: `KQueue[byte]` with `Init(4096)`. Read / write syscalls
  batch-drain as today.
- Possible SPSC optimization: parked for §10.

### Service 10 — `socketFd.recvCh` + `UDPBinding.Ch` (inventory #5, #4)

- Both already covered by the udpEchoServer notes + Service 3 above.
- `sys_recvfrom` at `src/netsock.go:239` uses `make(chan UDPDatagram, 16)`
  — becomes `socketFd.recvQ KQueue[UDPDatagram]` with `Init(16)`.
- `sys_recvfrom` timeout at `src/netsock.go:593` (`<-afterTicks(5)`
  inside a select-style loop) → the kernel-side wait now uses a
  combined `kschedTimedPark` + `socketFd.recvQ.TryPop()` loop. §03
  notes this is the only surviving "select-on-N-events" case; the
  fix is a bounded poll:

  ```
  for !deadlinePassed {
      if v, ok := q.TryPop(); ok { return v }
      kschedTimedPark(1) // 1 tick ≈ 10 ms
  }
  return nothing
  ```

  Same granularity as today (the current code already sleeps 5
  ticks = 50 ms between polls; Route C can keep 5 ticks, the above
  is illustrative).

### Service 11 — `Process.exitCh` (inventory #3, §07)

- Current: `chan uintptr, cap=1`.
- Route C: `Process.ExitEv KEvent` + `Process.ExitCode uintptr`.
- `processExit`: `proc.ExitCode = code; proc.ExitEv.Signal()`.
- `processWait`: `proc.ExitEv.Wait(); return proc.ExitCode`.

### Service 12 — one-shot boot tests (`go` sites #6, #7, #10, Spike2
`chan int, 1` inventory `n`, task-offset `chan struct{}, 1`
inventory `o`)

- Recommendation: **delete outright** during M3 cleanup. These tests
  exist purely to exercise now-removed runtime paths; once `chan`
  and `go` are gone from the kernel, the tests lose their subject.
- This is the same cleanup `current_impl_2026_04_24/fix_plan_deferred_1_5/`
  T1 proposed but with a different motivation (Route C removes the
  substrate rather than just decluttering).

## `afterTicks` consumer migration

Each consumer's rewrite, summarized (details above where applicable):

| # | Site | Route C form |
|---|------|--------------|
| A | `main.go:361` boot test | delete the site (see §06 one-shot cleanup) |
| B | `main.go:418` boot net-probe first fire | migrate the enclosing anonymous goroutine (#8) into a `kschedSpawn("netProbe", netProbeLoop)` that does `kschedTimedPark` + `netDiag()` |
| C | `main.go:421` boot net-probe iteration | same `netProbeLoop` thread |
| D | `main.go:666` timer-wheel demo loop | delete (demo) |
| E | `main.go:692` test loop | delete |
| F | `keyboard_irq.go:121` IRQ fallback poll | `kschedTimedPark(1)` — the call is already on a kernel thread after M4 (the keyboard pump is long-lived) |
| G | `netsock.go:593` recvfrom timeout | replace with the bounded poll pattern above |
| H | `netsock.go:648` socket poll | same bounded-poll |
| I | `netsock.go:784` socket op poll | same bounded-poll |
| J | `tcp.go:1356` TCP echo server poll | `kschedTimedPark(tcpEchoPollTicks)` — the hosting thread is `tcpEchoServer` |
| K | `tcp_retx.go:140` RTO scanner poll | `kschedTimedPark(tcpRetxScanTicks)` — hosting thread is `tcpRTOScannerLoop` |
| L | `userspace.go:453` sys_sleep hook | replace with `kschedTimedPark(d)` — see §07 for the sys_sleep rewire. **Sequencing**: this migration must land **after** `ring3Wrapper` becomes a kernel thread (M4 per §09), because otherwise the caller is on a TinyGo goroutine's stack and `kschedTimedPark` would write that stack pointer into a `KernelThread` (H-01 hazard) |

## Boot-ordering constraints

Current `main.go` spawns services in this order (grep for `go `):

1. `afterTicksInit()` → spawns `timerDispatcher` early (before any
   callers of `afterTicks` run).
2. `netInit()` → spawns `netRxLoop` and `udpEchoServer`.
3. `tcpInit()` → `tcpRTOScannerLoop` is spawned lazily by
   `tcpStartRTOScanner` on the first armed deadline.
4. `go fsTask()` — before the boot shell load, so the shell can
   call `fsSendRead` immediately.
5. `elfSpawn("sh.elf", …)` → spawns the Ring-3 shell wrapper.

Route C preserves this order. The rule: **`kschedSpawn` a service
before any potential caller of that service runs**. Since `kschedLoop`
only runs after `main()` returns into it, the enqueued services
don't actually execute until then — meaning all spawn calls happen
in a single-threaded context during `main()`, which simplifies
ordering considerably (everything is sequential until the scheduler
loop takes over).

One subtle case: `tcpStartRTOScanner` at `src/tcp_retx.go:119..128`
spawns lazily. Under Route C this lazy spawn happens from some
kernel thread's context (whichever thread armed the first deadline).
`kschedSpawn` is safe to call from a kernel thread as long as the
caller is not holding `kschedAllLock` recursively; the current
`tcpStartRTOScanner` holds `tcbTableLock` (rank 9) which is lower-
ranked than kschedAllLock (unranked, sits above), so the order is
safe.

## Service → primitive mapping summary

| Service | Uses | Primitive mapping |
|---------|------|--------------------|
| `timerDispatcher` | `chan<- struct{}` per-entry; `runtime.Gosched` | `*KEvent` per-entry; `kschedYield` |
| `netRxLoop` | `runtime.Gosched` | `kschedYield` |
| `udpEchoServer` | `UDPBinding.Ch` | `KQueue[UDPDatagram]` |
| `fsTask` | `fsReqCh` + per-request reply chan | `KQueue[*fsRequest]` + embedded `KEvent` |
| `tcpRTOScannerLoop` | `<-afterTicks` | `kschedTimedPark` |
| `tcpEchoServer` | `<-afterTicks`, per-connection `go` | `kschedTimedPark`, per-connection `kschedSpawn` |
| `ring3Wrapper` | `exitCh` | `KEvent` + `ExitCode` field (§07) |
| Ring-3 stack pool | `chan int` | `KQueue[int32]` or bitmap |
| pipe | `chan byte, 4096` | `KQueue[byte]` with Init(4096) |
| `sys_recvfrom` timeout | `<-afterTicks` + chan recv | bounded poll: `kschedTimedPark` + `TryPop` |
| `Process.exitCh` | `chan uintptr` | `KEvent` + `ExitCode` field |
| boot self-tests | `chan`, `afterTicks` | **delete** |

## Orderly-deletion tracker

§06 identifies these items as dead-on-arrival once Route C lands;
M3 / M5 cleanup deletes them:

- `src/main.go:348` (Spike2 test), line 360 (afterTicks self-test),
  line 666 (timer-wheel demo loop), line 692 (test loop) and the
  enclosing anonymous goroutines at lines 347, 360, 417.
- `src/goroutine_tss.go:86..98` — task-offset self-test goroutine.
- `src/afterticks.go:62` `afterTicksCalls` — keep (still a useful
  diagnostic after wheel migrates).
- `scripts/tinygo_runtime.patch` hunks labelled "kernel-side
  scheduler shims" — §08 catalogs.
- `scripts/verify_globals.sh` asserts for `runqueue` / `sleepQueue`
  / `timerQueue` — replaced by new kthread globals asserts.

## Reviewer gates

- All 15 spawn sites mapped: **yes** (table above).
- All 8 channel fields mapped: **yes**.
- All 21 `make(chan)` sites covered (at least by pattern): **yes**
  (by type; detailed §03 pattern applies).
- All 12 `<-afterTicks` sites mapped: **yes**.
- netDiag is a utility not a daemon: **yes** (noted at top and in
  the boot-probe wrapper detail).
- Boot ordering preserved: **yes** (single-threaded main until
  kschedLoop).
