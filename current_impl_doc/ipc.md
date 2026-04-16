# Inter-Goroutine Communication (IPC)

gooos does not implement any bespoke IPC primitive. Every
message-passing channel is a **native Go `chan`** constructed
by the TinyGo runtime; every service is a goroutine. The
scheduler, block/unblock, and select logic live in TinyGo's
`runtime/scheduler_*.go` — we wire gooos state into those
primitives rather than building alternatives.

## Channel Topology

```mermaid
graph LR
    IRQ1[IRQ1 handler<br/>handleKeyboard<br/>//go:nosplit] -->|uint32 event<br/>SPSC ring @ .bss| Ring[gooosKbdRing 64]
    Ring -->|drain in pump| Pump[keyboardPump goroutine]
    Pump -->|ch <- event| KBCh[(keyboardCh<br/>chan uint32, 16)]
    KBCh --> RKR[sysReadKeyHandler<br/>editor foreground]
    KBCh --> RKL[readKeyboardLine<br/>line-buffered stdin]

    Handlers[any syscall / service] --> FSCh[(fsReqCh<br/>chan *fsRequest, 8)]
    FSCh --> FSTask[fsTask goroutine]
    FSTask -->|req.reply <- resp| Replies[per-request<br/>chan *fsResponse, 1]
    Replies --> Handlers

    Pool[ring3StackPoolInit] -->|push 32 slot IDs| R3Ch[(ring3StackPoolCh<br/>chan int, 32)]
    R3Ch -->|acquire| Wrapper[ring3Wrapper]
    Wrapper -->|release on exit| R3Ch

    Parent[elfExec parent] -->|<-child.exitCh| ExitCh[(child.exitCh<br/>chan uintptr, 1)]
    Child[processExit] -->|exitCh <- code| ExitCh

    Pipe1[pipeWriter] -->|ch <- b| PipeCh[(chan byte, 4096)]
    PipeCh --> Pipe2[pipeReader]
```

Every arrow is a TinyGo `chan` send or receive. The
scheduler's `runqueue` / `sleepQueue` / `timerQueue` handle all
blocking.

## Package-Level Channels

| Name | Type | Buffer | Source | Purpose |
|---|---|---|---|---|
| `keyboardCh` | `chan uint32` | 16 | `src/keyboard_irq.go:31` | drained by `keyboardPump`; fed from the `.bss` SPSC ring that the IRQ1 handler writes |
| `fsReqCh` | `chan *fsRequest` | 8 | `src/fs.go:186` | serializes every FS op through the single `fsTask` goroutine |
| `ring3StackPoolCh` | `chan int` | 32 | `src/ring3_pool.go:28` | free list of pre-allocated Ring-3 kernel stacks |

Every other channel is **per-request / per-process**:

- `fsRequest.reply`: `chan *fsResponse, 1` — created per FS
  op so the caller gets its own reply without races.
- `Process.exitCh`: `chan uintptr, 1` — one per spawned
  process; the parent blocks on `<-child.exitCh`.
- Pipe backing channel: `chan byte, 4096` — one per pipe.
- `afterTicks(d)` return channel: `chan struct{}, 1` — one
  per timer.

## Keyboard Input Path

```mermaid
sequenceDiagram
    participant CPU
    participant IRQ as IRQ1 handler<br/>handleKeyboard
    participant Ring as .bss SPSC ring<br/>gooosKbdRing[64]
    participant Pump as keyboardPump goroutine
    participant Ch as keyboardCh
    participant H as consumer (editor or shell)

    CPU->>IRQ: PS/2 interrupt, scancode on port 0x60
    IRQ->>IRQ: inb(0x60), picSendEOI(1)
    IRQ->>IRQ: track shift/ctrl/alt make/break<br/>consume 0xE0 prefix
    IRQ->>IRQ: translate to ASCII<br/>Ctrl+letter → 0x01..0x1A
    IRQ->>Ring: write event (head++)<br/>drop if full (h - t ≥ 64)
    IRQ-->>CPU: iretq

    loop forever
        Pump->>Ring: tail slot available?
        alt empty
            Pump->>Pump: runtime.Gosched<br/>if still empty: sti+hlt
        else
            Pump->>Ring: read event (tail++)
            Pump->>Ch: ch <- event
        end
    end

    H->>Ch: <-keyboardCh
    Ch-->>H: event
```

Key design: **the ISR never touches a Go `chan`.** Channel
sends under `//go:nosplit` are not safe (TinyGo's runtime may
alloc during hash ops). The lock-free SPSC ring + pump goroutine
bridges IRQ context to goroutine context cleanly.

## Filesystem Service

```mermaid
sequenceDiagram
    participant Caller as syscall handler
    participant Req as fsRequest
    participant Ch as fsReqCh (buffered 8)
    participant Task as fsTask goroutine
    participant Reply as req.reply (buffered 1)

    Caller->>Req: reply = make(chan *fsResponse, 1)
    Caller->>Req: build req{op, name, data, reply}
    Caller->>Ch: fsReqCh <- req
    Task->>Ch: <-fsReqCh
    Task->>Task: dispatch op (Create/Write/Read/List/Delete)
    Task->>Reply: req.reply <- resp
    Caller->>Reply: <-reply
    Reply-->>Caller: resp
```

A single `fsTask` goroutine serializes all FS access — no lock
on the 32-entry `FileSystem` array needed. Wrappers in
`src/fs.go:211+` (`fsSendCreate`, `fsSendRead`, `fsSendWrite`,
`fsSendList`, `fsSendDelete`) hide the channel dance from
handlers.

## Pipes (`src/pipe.go`)

```mermaid
flowchart LR
    Writer[writer process<br/>pipeWriter.Write] -->|ch <- b<br/>byte at a time| Buf[(chan byte, 4096)]
    Buf -->|<-ch| Reader[reader process<br/>pipeReader.Read]

    WCl[writer close<br/>wrRefs--] -->|wrRefs == 0| CloseCh[close ch]
    CloseCh -->|EOF| Reader
    RCl[reader close<br/>rdRefs--] -->|rdRefs == 0| MarkClosed[rdClosed = true]
    MarkClosed -->|next send sees flag| EPIPE[pipeWriter.Write → fdErrPipe]
```

- **Backing**: one 4 KiB `chan byte` per pipe.
- **Refcounted ends**: `pipe.rdRefs` and `pipe.wrRefs` (one per
  fd holding that end). `fdAddRef` bumps on fd inheritance; the
  end closes only when all refs go to 0.
- **Writer-close → reader EOF**: the last `pipeWriter.Close()`
  calls `close(ch)`, so the reader's `<-ch` sees channel closed.
- **Reader-close → writer EPIPE**: the last `pipeReader.Close()`
  sets `rdClosed = true`; the writer's next `Write` sees it
  and returns `fdErrPipe`.

Pipes are the backbone of `cmd1 | cmd2 | ... | cmdN` N-stage
pipelines. Each stage runs in its own process (with its own
PML4) and connects via `Dup2` onto fd 0 / fd 1.

## Process Exit Synchronization

```mermaid
sequenceDiagram
    participant Shell as parent (sh)
    participant EC as child.exitCh (buf 1)
    participant Child as ring3Wrapper goroutine

    Shell->>Shell: elfSpawn(filename)<br/>creates Process{exitCh: make(chan uintptr, 1)}
    Shell->>Child: go ring3Wrapper(child)
    Shell->>EC: code = <-child.exitCh (block)
    Note over Child: user code runs...
    Child->>Child: sys_exit(code) → processExit
    Child->>EC: child.exitCh <- code
    EC-->>Shell: unblocked, code in hand
    Child->>Child: taskPause (goroutine dies)
    Shell->>Shell: use code (fg transfer, etc.)
```

Buffered capacity 1 so the child's send never blocks — even if
the parent is still unwinding housekeeping.

## `afterTicks` — Timer Replacement for `time.After`

`src/afterticks.go`:

```go
func afterTicks(d uint64) <-chan struct{} {
    ch := make(chan struct{}, 1)
    go func() {
        deadline := pitTicks + d
        for pitTicks < deadline {
            runtime.Gosched()
        }
        ch <- struct{}{}
    }()
    return ch
}
```

```mermaid
sequenceDiagram
    participant Caller
    participant Worker as afterTicks worker goroutine
    participant PIT as pitTicks (100 Hz)
    participant Ch as timer ch (buf 1)

    Caller->>Worker: go func() { poll + send }
    Caller->>Ch: <-afterTicks(d)<br/>(blocked)
    loop until pitTicks ≥ deadline
        Worker->>PIT: read pitTicks
        Worker->>Worker: runtime.Gosched<br/>(lets other goroutines run)
    end
    Worker->>Ch: ch <- struct{}{}
    Ch-->>Caller: unblocked
    Worker->>Worker: goroutine exits
```

Why not `time.After`? The TinyGo `time` package (through
`reflect`) pulls in SSE-using code that we keep disabled.
`afterTicks` is a 10-line replacement that uses only `pitTicks`
(a plain uint64) and `runtime.Gosched`.

The `sysSleepHandler` uses `afterTicks` too — if it used
`time.Sleep`, it would route through the kernel's patched
`sleepTicks` (a `sti; hlt; cli` busy loop), stalling every
other goroutine.

## Select Usage

Native `select` is available and used internally by the
scheduler, but gooos service code rarely needs it. The one
notable call site is Ring-3 `sys_read` on a pipe: the
`pipeReader.Read` wrapper uses a `select` over `pipe.ch` and a
close-detection path (simplified; see `src/pipe.go`).

Userspace programs — `gochan.elf` in particular — exercise
`select` directly from Ring 3, proving the end-to-end path
works.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
