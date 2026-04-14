# Memory Management

## Page Allocator (`src/vm.go`)

**Bump-only allocator** — no free list. `freePage()` is intentionally a no-op.

```go
var nextFreePage uintptr  // initialized by vmInit() from _alloc_start

func allocPage() uintptr {
    page := nextFreePage
    nextFreePage += pageSize  // 4096
    // Zero the entire page (required for page table entries)
    return page
}

func freePage(paddr uintptr) {
    // No-op: pages are leaked to avoid free list corruption
}
```

**Why no free list**: The previous free list implementation stored a next-pointer in the first 8 bytes of each freed page. When these pages were reallocated as page table entries or user data, the stale next-pointer data caused page table corruption and crashes on the second external command execution.

**Address space**: `nextFreePage` starts at `_alloc_start` (after `.pagetables` section, typically `0x6D5000`+). The 1 GiB identity map provides ~950 MiB of allocatable space, sufficient for short-lived shell sessions.

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
