# Channel IPC and Service Tasks

## Channel Implementation (`src/channel.go`)

### Channel Struct
```go
type Channel struct {
    buf        [256]uintptr   // ring buffer (chanMaxSlots = 256)
    readIdx    int
    writeIdx   int
    count      int
    capacity   int             // 0 = unbuffered (rendezvous)
    senderWQ   WaitQueue
    receiverWQ WaitQueue
    used       bool
}
```

- **Pool**: `chanPool [32]Channel` (static, `chanPoolSize = 32`)
- **`chanCreate(capacity)`**: reuses freed slots first, then appends. Returns `*Channel`
- **`chanFree(ch)`**: marks slot unused, resets all fields

### Operations

| Function | Blocking | Safe from ISR | Description |
|---|---|---|---|
| `chanSend(ch, val)` | Yes | No | Blocks if buffer full; wakes one receiver |
| `chanRecv(ch)` | Yes | No | Blocks if buffer empty; wakes one sender |
| `chanTrySend(ch, val)` | No | Yes | Returns false if full; used by keyboard IRQ |
| `chanRecvReady(ch)` | No | Yes | True if count > 0 |
| `chanSendReady(ch)` | No | Yes | True if space available |

### Unbuffered Channels (capacity = 0)
Rendezvous semantics: sender deposits in `buf[0]`, blocks until receiver takes it. Both sides coordinate via WaitQueues.

### Select Multiplexer
```go
func selectWait(cases *[8]SelectCase, n int) (index int, val uintptr)
```
- Phase 1: poll all cases for readiness (first match wins)
- Phase 2: register on all wait queues, block
- Phase 3: on wakeup, deregister from all queues, retry
- Handles spurious wakeups by looping

### Channel ID Registration (for Userspace)
- `chanRegister(ch)` -> sequential uint64 ID
- `chanLookup(id)` -> `*Channel` or nil
- Max 32 registered channels (`chanIDTableSize`)
- Currently: ID 0 = `userKeyboardChannel`

## Keyboard Service (`src/keyboard.go`)

### IRQ Handler (`handleKeyboard`, vector 33)
- Reads scancode from port `0x60`
- Sends EOI to PIC
- Ignores key releases (bit 7 set)
- Translates via `scancodeToASCII[128]` table (lowercase a-z, 0-9, punctuation `-=[]\;',./`, space)
- Packs event: `uintptr(scancode) | (uintptr(ascii) << 8)`
- Publishes to **both** `keyboardChannel` and `userKeyboardChannel` via `chanTrySend` (non-blocking, drops if full)

### Channels
- `keyboardChannel`: capacity 16, for kernel consumers (currently unused — `keyboardConsumerTask` not spawned)
- `userKeyboardChannel`: capacity 16, registered as channel ID 0 for userspace `sys_read`

### Notable Limitations
- No shift key / uppercase support
- No arrow keys, Tab, Ctrl+C
- Backspace (`0x0E`) and Enter (`0x1C`) handled as special scancodes

## Serial Service (`src/serial.go`)

### Direct API (interrupt-safe)
- `serialPutChar(c)` — polls transmit ready, writes to COM1
- `serialPrint(s)` / `serialPrintln(s)` — byte-by-byte output

### Task-Based API
- `serialChannel`: capacity 16
- `serialTaskEntry()`: kernel task that loops `chanRecv(serialChannel)`, writes received string to COM1
- `serialSend(msg)`: stores msg in 16-slot static pool, sends pointer via `chanSend` (blocking)

### Configuration
- COM1 (`0x3F8`), 115200 baud, 8N1, FIFO enabled

## Filesystem Service (`src/fs.go`)

### Storage
- `maxFiles = 32`, `maxFileData = 40960` (40 KiB per file)
- `FileEntry` struct: `{name string, data [40960]byte, size int, used bool}`
- Static `FileSystem` struct with `[32]FileEntry` array in `.bss`

### Direct API (pre-scheduler)
- `fsCreate(name)`, `fsWrite(name, data)`, `fsRead(name)`, `fsList()`, `fsDelete(name)`

### Channel-Based API (post-scheduler)
- `fsRequestChannel`: capacity 8
- `fsTaskEntry()`: kernel task, dispatches FSRequest ops (create/write/read/list/delete)
- Request/Response use static pools (`fsReqPool[8]`, `fsRespPool[8]`) with ring indices
- `fsSendCreate/Write/Read/List/Delete`: each creates a reply channel (`chanCreate(1)`), sends request, blocks on reply, calls `chanFree(replyCh)` after

### FS Operation Codes
| Code | Name | Description |
|---|---|---|
| 0 | fsOpCreate | Create empty file |
| 1 | fsOpWrite | Replace file contents |
| 2 | fsOpRead | Read file contents |
| 3 | fsOpList | List all filenames |
| 4 | fsOpDelete | Delete file |
