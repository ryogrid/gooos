# Memory Model, VM Mapping, Allocators, and GC-Related Constraints

## Address-Space Baseline

Boot paging (`src/boot.S`) establishes identity mapping `[0, 1 GiB)` via 2 MiB huge pages.

Dynamic mappings in kernel/user paths are added through 4-level page-table walkers in `src/vm.go`.

## Page Table and Mapping API (`src/vm.go`)

Core operations:

- `mapPage(vaddr, paddr, flags)` (current CR3)
- `unmapPage(vaddr)`
- `mapPageInto(pml4, vaddr, paddr, flags)` (explicit target pml4)
- `unmapPageFrom(pml4, vaddr)`
- `walkAndGetPaddr(vaddr)` / `walkAndGetPaddrIn(pml4, vaddr)`

Intermediate table population uses `walkOrCreate` and propagates user bit (`pageUser`) across levels.

## Physical Page Allocator

Allocator mode:

- bump allocator from `allocStartAddr()` (`nextFreePage`)
- reclaim path via explicit free stack (`freeStack`) with capacity `4096`
- lock: `pageAllocLock`

Functions:

- `allocPage()` => 4 KiB zeroed page
- `allocPagesContig(n)` => contiguous run for stack/pool use
- `freePage(paddr)` => zero + push to LIFO free stack (or leak if free stack full)

## Per-Process Address Spaces (`src/proc_pml4.go`)

- `newProcPML4()` allocates process PML4 and private PDP.
- Process PML4[0] points to per-process PDP with:
  - PDP[0] shared kernel identity mapping entry
  - PDP[3] copied LAPIC mapping if present
- `captureBootPML4()` records baseline kernel CR3 for safe restoration.
- `freeProcPML4(pml4)` recursively frees user page-table layers.

## Ring 3 Stack/Heap Layout (effective)

From process/ELF setup:

- argument page: `0x40300000`
- user stack base: `0x7FFF0000`
- stack mapping: 4 pages
- initial stack top used by loader/spawn: `userStackBase + 3*pageSize - 8`

Heap controls:

- per-process `HeapBreak` initialized near end of loaded segments
- `HeapLimit = HeapBreak + userHeapLimit`
- `userHeapLimit` constant currently 2 MiB (`src/process.go`)

## Runtime/GC Constraints

Kernel target (`src/target.json`):

- `gc: conservative`

User target (`user/target.json`):

- `gc: conservative`
- `scheduler: tasks`

Global/static memory constraints:

- in-memory FS (`src/fs.go`) reserves large static region: `maxFiles * maxFileData` (`32 * 262144`)
- network buffer pool allocates contiguous 128 * 2048 bytes at runtime (`src/netbuf.go`)

## Memory Invariants

1. All page-table writes must preserve flag semantics across levels for Ring 3 mappings.
2. Any teardown of per-process PML4 must execute only after CR3 has been switched away from that PML4.
3. User-memory copy helpers in syscall/signal paths must be page-boundary safe.
4. Stack top placement must avoid immediate boundary faults during early user runtime/setup frames.

## Active Risk Surfaces

- AP LAPIC timer enable is deferred partly due to unresolved interactions in AP interrupt/timer path.
- Fixed-capacity allocator metadata (`freeStackCap`) can force page leaks on sustained churn once free stack saturates.
- Large static memory consumers (FS arrays) reduce dynamic headroom and should be considered in capacity planning.
