# 11 — README update plan

The README at `/home/ryo/work/gooos/README.md` currently describes
gooos as a goroutine-scheduled kernel. After Route C M4 / M5 lands,
the README must describe the kernel/user split: kernel threads +
spinlocks + queues in Ring 0; TinyGo goroutines + chan in Ring 3.

**This cycle does NOT edit the README.** The edit lands as either
the final M4 commit or the first M5 commit — whichever feels most
natural at landing time. §11 specifies the diff shape in enough
detail that the landing commit can apply it mechanically.

## Sections to replace (verbatim text from current README)

### Opening paragraph (README.md:3)

```
An experimental x86_64 operating system written in **Go (TinyGo) + GNU assembly**.
The kernel runs on **TinyGo's native goroutine runtime**
(`scheduler=cores`, `gc=conservative`) with live multi-core work-stealing
— service loops are plain `go func()` goroutines, IPC is Go's built-in
`chan`, and Ring 3 processes are goroutines that `iretq` into userspace.
Assembly is used only where the CPU demands it.
```

**Replace with**:

```
An experimental x86_64 operating system written in **Go (TinyGo) + GNU assembly**.
The kernel runs a **gooos-owned kernel-thread scheduler**
(`scheduler=none` on the kernel target, `gc=conservative`) with
per-CPU ready queues, spinlocks, and bounded queues — kernel
services are plain `KernelThread` instances spawned by
`kschedSpawn`, IPC is `KEvent` / `KQueue` / `kschedTimedPark`, and
Ring 3 processes are hosted by kernel threads that `iretq` into
userspace. **Ring-3 user programs keep TinyGo's cooperative
goroutine runtime** (`scheduler=tasks`) — `go`, `chan`, `select`,
`time.Sleep` all work inside a user binary. Assembly is used only
where the CPU demands it.
```

### Scheduler row of the Progress table (README.md:18)

**Replace** the whole "Scheduler" row. Current text:

```
| Scheduler | Done (preemptive) | **TinyGo native goroutines**
  (`scheduler=cores` — multi-core, per-CPU runqueues + work-stealing,
  **preemptive**). BSP's 100 Hz LAPIC timer broadcasts preempt IPI
  vector 0xFB to every AP (feature 2.1); each AP's
  `handlePreemptIPI` checks safe-points then calls `runtime.Gosched()`.
  Ring-3 user goroutines preempted via kernel-delivered SIGALRM
  (feature 2.2, iretq-frame rewrite). TSS.RSP0 updated per-Ring-3-
  goroutine via the `gooosOnResume` hook |
```

**Replace with**:

```
| Scheduler | Done (preemptive) | **gooos-owned kernel-thread
  scheduler** (`scheduler=none` on kernel target, per-CPU ready
  queues + sticky-with-work-stealing, **preemptive**). Each kernel
  service is a `KernelThread` scheduled by `kschedLoop`; kernel
  threads park via `kschedPark` on `KEvent` / `KQueue` and resume
  via `kschedWake`. BSP's 100 Hz LAPIC timer broadcasts preempt
  IPI vector 0xFB to every AP; each AP's `handlePreemptIPI`
  safe-point-checks then calls `kschedYield()`. Ring-3 user
  goroutines preempted via kernel-delivered SIGALRM (feature 2.2,
  iretq-frame rewrite) — unchanged from the pre-Route-C design.
  TSS.RSP0 updated per-Ring-3-host via `tssSetRSP0ForKernelThread`
  on kernel-thread switch. Spinlock hold (`%gs:48 > 0`) disables
  preempt mid-critical-section. GC STW via broadcast freeze IPI
  (vector 0xFD); every kernel thread enters a spin-freeze at the
  next safe point, collector walks the kthread table's stack
  bounds. |
```

### Channel IPC row (README.md:22)

**Replace** the entire row. Current text:

```
| Channel IPC + select | Done | **Native Go `chan` and `select`** in
  Ring 0. `fsReqCh` and per-process `exitCh` are native Go channels
  constructed by the TinyGo runtime |
```

**Replace with**:

```
| Kernel IPC | Done | **gooos-owned `KEvent`/`KQueue` primitives**
  in Ring 0. `fsReqQ` (MPSC `KQueue[*fsRequest]`), per-process
  `ExitEv` (`KEvent`), UDP/TCP socket recv queues (`KQueue[UDPDatagram]`),
  pipe byte buffer (`KQueue[byte]`) all replace the previous
  channel-based IPC. User-side still uses native Go `chan` /
  `select` (see Userspace goroutines row below). |
```

### SMP row (README.md:21) — targeted edits

Keep the row, but the closing paragraph mentions `scheduleTask` /
`migrateAndPause` / `stealWork` inside the TinyGo runtime. Rewrite
that paragraph to cite Route C equivalents:

- `runqueues[cpuID()]` → `kschedQueues[cpuID()]`.
- `stealWork` → `kschedSteal`.
- `scheduleTask` → `kschedPush`.
- `migrateAndPause` (from the patched `scheduler_cores.go`) → no
  direct analog needed; `ring3Wrapper` is a kernel thread spawned
  with the same round-robin target CPU policy, done entirely in
  gooos code (`src/elf.go`'s spawn path).

The sentence about "kernel goroutines and the Ring-3 shell goroutine
routinely migrate to AP 1/3" is kept as descriptive, with
"goroutines" replaced by "kernel threads".

### Userspace goroutines row (README.md:41)

Keep the row intact — it documents the *user-side* runtime, which
Route C does not touch. Optionally add one sentence: "The user-side
runtime is independent of the kernel-side scheduler change; user
binaries still build with `user/target.json` `scheduler=tasks`."

### Architecture diagram (README.md:85..146)

The ASCII diagram explicitly lists the kernel goroutines:

```
    Kernel goroutines (Ring 0)
    ┌──────────────────────┐
    │ go fsTask()          │
    │ go netRxLoop()       │
    │ go timerDispatcher() │
    │ go tcpRTOScannerLoop │
    │ go udpEchoServer     │
    │ go tcpEchoServer     │
    └──────────────────────┘
```

**Replace the "Kernel goroutines (Ring 0)" block** with:

```
    Kernel threads (Ring 0)
    ┌──────────────────────────┐
    │ kschedSpawn("fsTask")    │
    │ kschedSpawn("netRxLoop") │
    │ kschedSpawn("timerDispatcher") │
    │ kschedSpawn("tcpRTOScannerLoop")│
    │ kschedSpawn("udpEchoServer") │
    │ kschedSpawn("tcpEchoServer") │
    └──────────────────────────┘
```

Adjust column widths so the ASCII box lines up. Content unchanged
in spirit; naming rotated.

The "Shell goroutine (Ring 3)" and "External Commands (Ring 3)"
boxes get the label "Kernel thread host → Ring 3 process" instead
of "go ring3Wrapper".

### `time.After` replacement row (README.md:36)

Keep. The `afterTicks` function lives on; §03 / §06 rewire it to
`KEventAfter`, but the row's user-facing description ("`afterTicks(d
uint64) <-chan struct{}` in `src/afterticks.go`") needs a one-line
amendment:

Append: "Post-Route-C the primitive also exposes
`KEventAfter(d uint64) *KEvent` for callers already on a kernel
thread; the channel-returning form is preserved for any remaining
goroutine-context consumers."

### "Where assembly is used" (README.md:69..82)

Append one bullet:

```
- **Kernel-thread context switch** (`kthread_switch.S`): `kschedSwitch`
  / `kschedEnter` — saves callee-saved + RFLAGS, swaps stacks.
  Landed in Route C M0
```

Keep the existing `task_stack_amd64.S` bullet; it remains relevant
for the user-side build.

## Sections to add

### New section: "Kernel scheduler internals" (placed between
"Architecture" and "Repository layout")

Short prose summarizing the §02-§07 design for a reader who wants
to understand the kernel without reading the full design set.
Example content (≤ 40 lines):

```
## Kernel scheduler internals

The gooos kernel runs a cooperative-with-preemption scheduler
over `KernelThread` instances. Each long-lived kernel service —
filesystem serializer, e1000 RX poller, timer wheel, TCP scanner,
UDP/TCP echo demos — is one kernel thread spawned at boot by
`kschedSpawn`. Each Ring-3 process has its own kernel thread
hosting the `ring3Wrapper` that `iretq`s into user space and
serves syscalls on behalf of the user program.

**Per-CPU ready queues** hold runnable threads. Each CPU's
scheduler loop pops the head, context-switches into the thread,
runs until the thread parks (on a `KEvent`, a `KQueue`, or a timer
deadline) or is preempted by the LAPIC-timer IPI. An idle CPU
scans peer queues for stealable work before halting on `hlt`.

**Preemption** is driven by the same 100 Hz BSP LAPIC timer that
predates Route C: `broadcastPreemptIPI` fires on every tick; each
AP's `handlePreemptIPI` safe-point-checks (spinlock held → skip,
else `kschedYield`). Ring-3 SIGALRM delivery via iretq-frame
rewrite is unchanged.

**IPC** uses three primitives: `Spinlock` (unchanged from pre-Route-C),
`KEvent` (single-shot edge-triggered; replaces `chan struct{}`
completions), and `KQueue[T]` (bounded ring buffer; replaces
`chan T, N`). `select`-like multi-waiter semantics are implemented
as bounded polls over the involved primitives.

**GC** uses conservative mark-sweep (unchanged from pre-Route-C).
Stop-the-world fires a new broadcast freeze IPI (vector 0xFD); each
CPU enters a spin-freeze at the next spinlock-release or idle hlt,
and the collector walks every `KernelThread`'s live stack range
directly from the kthread table.

See `current_impl_<next_cycle>/` for the authoritative reference
post-Route-C.
```

(Reference to `current_impl_<next_cycle>/` is a placeholder —
the M5 commit picks an actual date-suffixed directory name.)

### New section: "Kernel / user split" (short)

One or two paragraphs making the kernel-vs-user scheduling
distinction explicit. Can live inside the opening paragraph or as a
sub-section of Architecture.

## Sections to leave alone

These rows / sections need no edit:

- All network-stack rows (Networking stack, Socket API, TCP stack) —
  internal plumbing changes but public surface unchanged.
- Ring-3 stack pool row — the pool semantics are unchanged; §06
  service 8 swaps the backing `chan int` for a `KQueue[int32]`, but
  the user-visible behaviour is the same.
- Userspace conservative GC row — unchanged; user-side GC is
  independent.
- Text editor / Tiny C interpreter rows — user binaries, unaffected.
- Build / Prereq / Run sections — unchanged (the build command set
  stays `make build`, `make iso`, `make run`, `make run-smp`,
  `make run-net`).
- "Known limitations" — unchanged except the channel IPC references
  (the "Sleep granularity" and "Shell job control" bullets stand).
- License / Acknowledgements.

## Diff shape summary

| Edit | Location | Lines touched (approx) |
|------|----------|-----------------------|
| Opening paragraph | README.md:3 | 1 paragraph rewrite |
| Scheduler row | README.md:18 | 1 table row rewrite |
| Kernel IPC row | README.md:22 | 1 table row rewrite (renamed from "Channel IPC + select") |
| SMP row | README.md:21 | Targeted sentence edits within row |
| Architecture diagram | README.md:123..145 | ASCII box replaced |
| "Where assembly is used" | README.md:69..82 | +1 bullet |
| `time.After` row | README.md:36 | +1 sentence |
| New "Kernel scheduler internals" section | Insert after Architecture | ~40 lines added |
| New "Kernel / user split" mini-section | Inside Architecture or opening | ~6 lines added |

Net README grows by about 50 lines.

## Landing mechanics

- Commit subject: `README: describe Route C kernel-thread scheduler`.
- Land as part of M4's final commit OR as M5's first commit
  (implementer's choice; both are fine).
- Do not squash the README commit with an unrelated content commit;
  keep it reviewable standalone.
- Avoid `/**/` or HTML comments in the README — the markdown stays
  plain.

## Verification after landing

- `markdown-link-check` (if the project uses one) clean.
- `grep -n "scheduler=cores" README.md` returns zero hits.
- `grep -n "go func()" README.md` — if any remain, they must be
  user-side context.
- `grep -n "chan " README.md` — same check.
- Visual smoke: render the README in a Markdown viewer; table
  alignment OK; code fences balanced.

## Reviewer gates

- README diff matches reality at M4/M5: **yes** (`scheduler=cores` →
  `scheduler=none`, goroutine → kernel-thread, chan → KEvent /
  KQueue).
- User-side language preserved: **yes** (Userspace goroutines row
  kept).
- No dangling references to removed symbols (e.g. `gooosOnResume`):
  **yes** (all replaced with their Route C analogs).
- Architecture diagram reflects new model: **yes** (box relabelled).
