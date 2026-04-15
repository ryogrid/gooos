# Phase B — Channel Migrations (B3 + B4)

Replace the two custom channels `serialChannel` (`src/serial.go:71`)
and `fsRequestChannel` (`src/fs.go:142`) — and their associated
static pools — with Go-native channels. Detailed implementation
spec for `TODO.md` items **B3** and **B4**.

## 1. Pre-implementation survey

The implementer should confirm these call-site inventories before
editing. They are current as of commit `e89ac20`.

### 1.1 `serialChannel` live callers

Only one:

- `src/serial.go:89` — `chanRecv(serialChannel)` inside
  `serialTaskEntry`.

And one producer:

- `src/serial.go:106` — `chanSend(serialChannel, ...)` inside
  `serialSend`.

But `serialSend` is **never called** from anywhere in `src/`.
`grep -rn "serialSend\b" src/` returns hits only on the definition
and its comment. The 104 occurrences of `serialPrintln` /
`serialPrint` in `src/` all bypass the channel entirely —
`serialPrintln` (`src/serial.go:59`) writes directly to the UART
via busy-wait on `com1LineStat`. The channel path is dead.

### 1.2 `fsRequestChannel` live callers

Five producers (all in `src/fs.go`):

- `src/fs.go:203-213` — `fsSendCreate`
- `src/fs.go:216-225` — `fsSendWrite`
- `src/fs.go:228-239` — `fsSendRead`
- `src/fs.go:242-253` — `fsSendList`
- `src/fs.go:256-264` — `fsSendDelete`

One consumer:

- `src/fs.go:163` — `chanRecv(fsRequestChannel)` inside
  `fsTaskEntry`.

Callers of the `fsSend*` helpers:

- `src/process.go:89` — `fsSendRead(filename)` inside `elfExec`.
- `src/userspace.go` — `sysFsRead`, `sysFsWrite`, `sysFsList`
  handlers call `fsSendRead`, `fsSendWrite`, `fsSendList`.

None are reachable from ISR context — all are syscall handlers
(task context) or boot-time kernel code (`elfExec` runs on the
shell-parent goroutine).

## 2. B3 — `serialChannel` retirement

### 2.1 Decision: delete, do not migrate

The entire `serialChannel` + `serialSend` + `serialTaskEntry`
infrastructure is dead code left over from the original microkernel
PRD (`tasks/prd-goroutine-microkernel.md`). `serialPrintln` and
`serialPrint` in `src/serial.go:52-63` bypass it. Retiring B3 means
deleting the following:

- `src/serial.go:68` — `serialMsgPoolSize` constant.
- `src/serial.go:70-74` — `serialChannel`, `serialMsgs`,
  `serialMsgNext` variables.
- `src/serial.go:76-79` — `serialTaskEntryAddr` `//go:linkname`
  declaration.
- `src/serial.go:81-96` — `serialTaskEntry` function.
- `src/serial.go:98-107` — `serialSend` function.
- `src/main.go:333` — `serialChannel = chanCreate(16)`.
- `src/main.go:334` — `createTask(serialTaskEntryAddr())`.

After B3, `src/serial.go` contains only direct-UART functions
(`serialInit`, `serialPutChar`, `serialPrint`, `serialPrintln`).
No channel, no task, no pool.

### 2.2 Trade-off discussion

**Why not migrate instead of deleting?** The usual argument for a
serialization task is that `serialPrintln` from two concurrent
goroutines could interleave characters. Today's `serialPrintln`
already has that property because it does `serialPutChar` per byte
without any lock. In the two places that call `serialPrintln`
concurrently in Phase B (the pump goroutine in B5 and the shell's
parent `ring3Wrapper` in B9), interleaving is acceptable — we
already accept it today, and complete messages still reach the
log because each `serialPrintln` call is short.

If a future session decides interleaving is unacceptable, adding a
native `chan string` with a `serialPrintWorker` goroutine is a
2-file change. The current deletion does not block that future
work.

### 2.3 Verification

1. `grep -rn "serialChannel\|serialSend\|serialTaskEntry\|serialMsgs\|serialMsgPoolSize" src/`
   returns zero hits.
2. `make build` clean.
3. 10/10 sendkey trials pass.
4. Stress test passes.

### 2.4 Dependencies

None. Can land first in Phase B.

## 3. B4 — `fsRequestChannel` migration

### 3.1 Target shape

Replace `FSRequest` / `FSResponse` static pools and the
`Channel`-backed request bus with a native `chan` and per-request
reply channels.

New types (replace `src/fs.go:80-138` — `FSRequest`, `FSResponse`,
`fsPoolSize`, the pool variables, and `fsRequestChannel`):

```go
// src/fs.go

type fsOp uint8

const (
    fsOpCreate fsOp = iota + 1
    fsOpWrite
    fsOpRead
    fsOpList
    fsOpDelete
)

type fsResponse struct {
    ok    bool
    data  []byte
    names []string
}

type fsRequest struct {
    op    fsOp
    name  string
    data  []byte
    reply chan *fsResponse
}

var fsReqCh = make(chan *fsRequest, 8)
```

The static `fsReqPool` / `fsRespPool` / `fsReqPoolNext` /
`fsRespPoolNext` globals (`src/fs.go:142-147`) are deleted —
each request and response is heap-allocated and lives as long as
the reply channel is reachable.

### 3.2 New `fsTask` body

```go
// src/fs.go

func fsTask() {
    for req := range fsReqCh {
        resp := &fsResponse{}
        switch req.op {
        case fsOpCreate:
            resp.ok = fsCreate(req.name)
        case fsOpWrite:
            resp.ok = fsWrite(req.name, req.data)
        case fsOpRead:
            resp.data = fsRead(req.name)
            resp.ok = resp.data != nil
        case fsOpList:
            resp.names = fsList()
            resp.ok = true
        case fsOpDelete:
            resp.ok = fsDelete(req.name)
        }
        req.reply <- resp
    }
}
```

No `sti()` call (TinyGo's runtime enables interrupts before any
goroutine runs). No `serialPrintln("FS task: started")` unless the
implementer wants to keep that log line (harmless either way).

### 3.3 New `fsSend*` helpers

Each becomes a thin wrapper around `chan` operations:

```go
// src/fs.go

func fsSendCreate(name string) bool {
    reply := make(chan *fsResponse, 1)
    fsReqCh <- &fsRequest{op: fsOpCreate, name: name, reply: reply}
    return (<-reply).ok
}

func fsSendWrite(name string, data []byte) bool {
    reply := make(chan *fsResponse, 1)
    fsReqCh <- &fsRequest{op: fsOpWrite, name: name, data: data, reply: reply}
    return (<-reply).ok
}

func fsSendRead(name string) []byte {
    reply := make(chan *fsResponse, 1)
    fsReqCh <- &fsRequest{op: fsOpRead, name: name, reply: reply}
    return (<-reply).data
}

func fsSendList() []string {
    reply := make(chan *fsResponse, 1)
    fsReqCh <- &fsRequest{op: fsOpList, reply: reply}
    return (<-reply).names
}

func fsSendDelete(name string) bool {
    reply := make(chan *fsResponse, 1)
    fsReqCh <- &fsRequest{op: fsOpDelete, name: name, reply: reply}
    return (<-reply).ok
}
```

The callers (`src/process.go:89` and the `sys_fs_*` handlers in
`src/userspace.go`) do not change — they see the same signatures.

### 3.4 GC residency change

Today, `fsReqPool[fsPoolSize]FSRequest` is a global array — the
request struct lives in `.bss` forever. Under the migration, each
`fsSend*` allocates a new `*fsRequest` on the heap, plus a reply
channel (itself a heap object). After the caller receives, both
become unreachable and are eligible for collection.

This is a behavior change: every FS call now churns through the
allocator. Total per-call allocation: one `fsRequest` struct
(~64 bytes), one `fsResponse` struct (~48 bytes), one channel
buffer (a few dozen bytes). For 10 sendkey trials × ~5 FS ops per
trial = 50 calls → ~8 KB of heap traffic per test run. Well
inside the 4 MiB heap budget and cleanly collected.

### 3.5 Concurrency invariants preserved

The current design serializes all FS access because there is one
`fsTask` consumer and one `fsRequestChannel`. The new design
preserves that: one `fsTask` goroutine, one `fsReqCh`. Multiple
concurrent callers queue on the channel's buffered send; the
reply channels are per-request so no multiplexing confusion.

### 3.6 `src/main.go` changes

Replace `fsRequestChannel = chanCreate(8)` + `createTask(fsTaskEntryAddr())`
at `src/main.go:337-338` with a single line (after B7 lands):

```go
go fsTask()
```

B4 can land before B7 by temporarily keeping `createTask`:

```go
// Intermediate state — B4 landed, B7 not yet.
// Keep createTask wrapper around fsTask for one more commit.
go fsTask()
// Remove when B7 replaces the whole block.
```

Or the implementer can do B4 and B7 in the same commit for `fsTask`
only (still one commit per TODO item; B4's "commit" includes the
`main.go` line change). Either ordering is fine.

### 3.7 Verification

1. `grep -rn "fsRequestChannel\|fsReqPool\|fsRespPool\|FSRequest\b" src/`
   returns only the historical references (if any are left in
   comments; otherwise zero).
2. `make build` clean.
3. 10/10 sendkey trials pass (the harness exercises `ls`, `cat`,
   `hello`, all of which hit `sys_fs_*`).
4. Stress test (`5× ls + cat`) passes — this explicitly exercises
   repeated FS calls to check heap churn doesn't destabilize GC.
5. Add a dev-only kernel function `fsStressTest()` that calls
   `fsSendList()` 1000 times and asserts no panic; enable behind
   `const runFSStress = false` during development.

### 3.8 Dependencies

- B4 has **no predecessors**. Can land alongside or before B3.
- B4 must land **before** B8 (which deletes `src/scheduler.go` —
  `createTask` references go away).
- B4 must land **before** B10 (which deletes `src/channel.go` —
  the legacy `chanSend`/`chanRecv` calls must have stopped).

## 4. Open questions

- **Should `fsTask` handle a graceful shutdown via channel close?**
  Today the loop is infinite. A close-driven exit matters only for
  a future "reboot without panic" feature — not needed in v1.
  Design leaves the loop infinite.
- **Should `fsResponse.data` and `fsResponse.names` share memory
  with `fsTask`'s local allocations?** They do today because
  `fsRead` and `fsList` return slices pointing into kernel memory.
  Under the new model the same is true — the response struct is
  heap-allocated but the slice headers inside still alias kernel
  globals. No change required; document that a caller must treat
  `data`/`names` as valid only until the next FS call that could
  mutate them (same as today).

## 5. Reviewer notes (to be populated after review pass)

(none yet)
