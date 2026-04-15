# Memory Management

## Page Allocator (`src/vm.go`)

**Bump + LIFO free stack.** Freed pages are pushed onto an external stack in `.bss` and reused by the next `allocPage()`; when the stack is empty, `allocPage()` falls back to bumping `nextFreePage`.

```go
const freeStackCap = 4096 // 32 KiB metadata in .bss

var (
    nextFreePage uintptr
    freeStack    [freeStackCap]uintptr
    freeStackLen int
)

func allocPage() uintptr {
    // cli/sti guarded via readFlags/restoreFlags
    if freeStackLen > 0 {
        freeStackLen--
        page := freeStack[freeStackLen]
        // zero, then return
    } else {
        page := nextFreePage
        nextFreePage += pageSize
        // zero, then return
    }
}

func freePage(paddr uintptr) {
    // zero the page, then push onto freeStack
    // if stack full, leak (bump has ~950 MiB headroom)
}
```

**Safety notes**:

- Metadata lives OUTSIDE freed pages — unlike the previous free-list-in-freed-pages design, there is no stale next-pointer to be misread as PTE bits when a freed page is later reused as an intermediate page table.
- `freePage` zeros the page before pushing; `allocPage` zeros again on pop. A reused page reads as all-zeros from the moment it is handed back.
- `cli`/`readFlags`/`restoreFlags` bracket the stack push/pop so a timer ISR cannot observe a half-updated `freeStackLen`.

### Contiguous multi-page allocation

`allocPage` does **not** guarantee physical contiguity across successive calls (the free stack is LIFO, not linear). For multi-page structures accessed as a single flat region via the identity map — primarily **per-task kernel stacks** in `scheduler.go` — use `allocPagesContig(n)`, which always bumps and never pops from the free stack.

```go
// scheduler.go — 2-page (8 KiB) kernel stack per task, must be contiguous
kernelStackBase := allocPagesContig(2)
kernelStackTop := kernelStackBase + 2*pageSize
```

User-space multi-page regions (ELF segments, user stack, sbrk heap) are mapped one page at a time via `mapPage(vaddr+i*pageSize, allocPage(), ...)`, so each individual page is free to come from the LIFO stack.

**Address space**: `nextFreePage` starts at `_alloc_start` (after `.pagetables` section, typically `0x6DD000`+). The 1 GiB identity map provides ~950 MiB of bump-allocatable space; plus the LIFO stack reuses pages freed by `processExit`, so a shell running repeated commands no longer exhausts memory.

## Virtual Memory (4-Level Page Tables)

### Page Flags
- `pagePresent = 1 << 0` — entry is valid
- `pageWrite = 1 << 1` — writable
- `pageUser = 1 << 2` — accessible from Ring 3

### Index Calculation
```
PML4 index = (vaddr >> 39) & 0x1FF
PDP  index = (vaddr >> 30) & 0x1FF
PD   index = (vaddr >> 21) & 0x1FF
PT   index = (vaddr >> 12) & 0x1FF
```

### Key Functions

- **`mapPage(vaddr, paddr, flags)`**: walks PML4->PDP->PD->PT via `walkOrCreate`, sets leaf PTE
- **`unmapPage(vaddr)`**: walks via `walkExisting`, clears leaf PTE, calls `invlpg(vaddr)` for TLB flush
- **`walkOrCreate(table, index, flags)`**: returns next-level table address; allocates new page via `allocPage()` if entry not present; propagates `pageUser` flag to intermediate entries
- **`walkAndGetPaddr(vaddr)`**: read-only walk, returns physical page address or 0 if not mapped
- **`walkExisting(table, index)`**: returns next-level table or 0 if not present

### Boot-Time Identity Map (`src/boot.S`)

Boot.S creates a 1 GiB identity map using 2 MiB huge pages:
- PML4[0] -> PDP (at `pml4` symbol)
- PDP[0] -> PD (at `pdp` symbol)
- PD[0..511] = 2 MiB pages with Present+Write+PageSize flags

`mapPage()` does NOT split existing 2 MiB huge pages. User virtual addresses must be >= `0x40000000` (outside the first 1 GiB) to avoid conflicts.

## Linker Script Memory Layout (`src/linker.ld`)

```
0x100000          .multiboot (Multiboot 1 header)
                  .text
                  .rodata
_globals_start    .data
                  .bss (includes kernel stack)
_globals_end
                  .heap (4 MiB, _heap_start to _heap_end)
                  Guard gap (1 page = 4 KiB)
                  .pagetables (PML4, PDP, PD — 3 pages)
_alloc_start      (bump allocator starts here)
```

### GC and Page Table Separation

The `.pagetables` section is placed **after** the heap with a 1-page guard gap. This is critical because:

1. TinyGo's conservative GC (when enabled) calls `memset(metadataStart, 0, _heap_end - metadataStart)` during mark phase
2. `metadataStart` is a runtime variable inside `.bss`
3. If page tables were within this range, the memset would zero PML4/PDP/PD entries, causing page faults

Currently `gc=leaking` is used as a workaround (no GC mark phase runs), but the `.pagetables` placement is retained for when conservative GC is restored.

### `_alloc_start`

Defined after `.pagetables` in the linker script. The `allocStartAddr()` assembly stub returns this address. `vmInit()` reads it to initialize `nextFreePage`. This ensures `allocPage()` never returns an address that overlaps with boot page tables.

## User Address Space

Per-process virtual memory, managed by `elfExec`/`processExit` in `src/process.go`:

| Region | Address Range | Size | Description |
|---|---|---|---|
| Code/Data | `0x40100000`+ | varies | ELF PT_LOAD segments |
| Arg page | `0x40300000` | 4 KiB | Command-line arguments |
| Heap | `0x40102000`+ | ~1 MiB | Grown by `sys_sbrk` |
| Stack | `0x7FFF0000` | 8 KiB | 2 pages, grows downward |

All user pages are mapped with `pagePresent | pageWrite | pageUser`.

During `sys_exec`, parent pages are saved (vaddr+paddr pairs in `savedParent`), unmapped (PTEs cleared but physical pages preserved), child pages are mapped, and on child exit, parent pages are restored via `mapPage` with the original physical addresses.
