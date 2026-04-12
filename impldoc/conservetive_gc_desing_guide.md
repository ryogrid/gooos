# Custom Allocator & GC Implementation Design (TinyGo, x86_64)

> **Audience**: This document is written for a Claude Code session (or a human engineer). It serves as a follow-up to `heap_gc_design.md`.
> 
> **Context & Clarification**: The user requested to implement memory management by providing custom `malloc` and `free` functions in Go, believing this is how TinyGo's `conservative` (Mark/Sweep) GC is enabled. 
> 
> **Technical Reality in TinyGo**:
> 1. **`gc: "extalloc"`**: This mode forces TinyGo to route all `new`, `make`, and slice growths to external `malloc` and `free` symbols. If you want to write your own memory allocator in Go and have complete control over `free`, **this is the mode you must use**. However, it does *not* provide automatic garbage collection; you must manually manage memory (or implement your own GC logic).
> 2. **`gc: "conservative"`**: This mode provides true automatic Mark/Sweep GC. It does **not** call an external `malloc`/`free`. Instead, it *exports* its own `malloc`/`free` and requests large blocks of memory from the OS via `mmap` (which we already stubbed in `stubs.S`).
>
> This document provides the implementation guide for **Path A (Custom malloc/free via `extalloc`)** to fulfill the user's explicit request, and includes **Path B (True Conservative GC)** in case the user actually wants automatic Mark/Sweep GC.

---

## Path A: Custom `malloc` / `free` Allocator (`gc: "extalloc"`)

Use this path if you want to implement your own memory allocator logic (e.g., a free-list allocator) in Go and manually manage memory, or build your own GC on top of it.

### 1. Update `target.json`

Change the garbage collector mode to `extalloc`. This tells the compiler to emit calls to `malloc` and `free` for all heap operations.

```json
{
    "llvm-target": "x86_64-unknown-linux-elf",
    "cpu": "x86-64",
    "features": "-mmx,-sse,-sse2,-sse3,-ssse3,-sse4.1,-sse4.2,-avx,-avx2,-avx512f",
    "build-tags": ["gooos"],
    "goos": "linux",
    "goarch": "amd64",
    "gc": "extalloc",
    "scheduler": "none",
    "panic-strategy": "trap",
    "linker": "ld.lld",
    "rtlib": "compiler-rt"
}
```

### 2. Expose Heap Boundaries in `src/stubs.S`

To manage memory in Go, your allocator needs to know where the heap starts and ends. Add these getter functions to `src/stubs.S` so Go can call them.

```gas
    /* ---- Heap Boundary Getters for Go Allocator --------------------- */
    .global get_heap_start
get_heap_start:
    movq    $_heap_start, %rax
    ret

    .global get_heap_end
get_heap_end:
    movq    $_heap_end, %rax
    ret
```

### 3. Implement the Allocator in Go (`src/allocator.go`)

Create a new file `src/allocator.go`. You must implement and `//export` four functions: `malloc`, `free`, `calloc`, and `realloc`. TinyGo and LLVM rely on all four.

Below is a skeleton for a basic allocator. Claude Code should expand the `malloc` and `free` logic into a proper free-list or block allocator.

```go
// src/allocator.go
package main

import "unsafe"

// External assembly functions defined in stubs.S
//go:wasmimport env get_heap_start
func get_heap_start() uintptr

//go:wasmimport env get_heap_end
func get_heap_end() uintptr

var (
	heapStart uintptr
	heapEnd   uintptr
	heapCurr  uintptr
	isInit    bool
)

func initAlloc() {
	if isInit {
		return
	}
	heapStart = get_heap_start()
	heapEnd = get_heap_end()
	heapCurr = heapStart
	isInit = true
}

//export malloc
func malloc(size uintptr) unsafe.Pointer {
	initAlloc()
	
	// Align size to 8 bytes
	size = (size + 7) &^ 7

	// TODO: Implement a proper free-list search here to reuse freed memory.
	// For now, this is a fallback bump allocation.
	if heapCurr+size > heapEnd {
		return nil // Out of memory
	}

	ptr := heapCurr
	heapCurr += size
	return unsafe.Pointer(ptr)
}

//export free
func free(ptr unsafe.Pointer) {
	if ptr == nil {
		return
	}
	// TODO: Implement proper memory reclamation (e.g., add to a free-list).
	// In a simple bump allocator, free() does nothing.
}

//export calloc
func calloc(nmemb, size uintptr) unsafe.Pointer {
	total := nmemb * size
	ptr := malloc(total)
	if ptr != nil {
		// Zero out the memory
		mem := unsafe.Slice((*byte)(ptr), total)
		for i := range mem {
			mem[i] = 0
		}
	}
	return ptr
}

//export realloc
func realloc(ptr unsafe.Pointer, size uintptr) unsafe.Pointer {
	if ptr == nil {
		return malloc(size)
	}
	if size == 0 {
		free(ptr)
		return nil
	}

	// Allocate new block
	newPtr := malloc(size)
	if newPtr == nil {
		return nil
	}

	// TODO: Copy old data to newPtr. 
	// Since we don't know the old size in a simple bump allocator, 
	// a proper allocator must store block metadata (e.g., size) just before the pointer.
	
	free(ptr)
	return newPtr
}
```

---

## Path B: True Automatic Mark/Sweep GC (`gc: "conservative"`)

If the user's actual goal is to enable **automatic garbage collection** (so they don't have to manually call `free`), do **not** implement custom `malloc`/`free`. Instead, use TinyGo's built-in conservative GC.

To enable it, follow these steps:

### 1. Update `target.json`
Set `"gc": "conservative"`.

### 2. Provide Stack Scanning Assembly (`src/stubs.S`)
The conservative GC needs to scan the CPU registers and the stack for pointers. Add this required trampoline to `src/stubs.S`:

```gas
    /* ---- Stack Scanner for Conservative GC -------------------------- */
    /* Called by TinyGo runtime to scan registers and stack for pointers.*/
    .global tinygo_scanCurrentStack
tinygo_scanCurrentStack:
    /* Push all callee-saved registers onto the stack so the GC can scan them */
    pushq   %rbx
    pushq   %rbp
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15

    /* Pass the current stack pointer (RSP) as the first argument (RDI) */
    movq    %rsp, %rdi
    
    /* Call the actual Go runtime scanner */
    call    tinygo_scanstack

    /* Restore registers */
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbp
    popq    %rbx
    ret
```

### 3. Provide `memset` and `memcpy` (`src/stubs.S`)
The conservative GC relies on these standard C functions to manage its internal memory blocks.

```gas
    .global memset
memset:
    movq    %rdi, %r8       /* Save original pointer to return */
    movb    %sil, %al       /* Value to set */
    movq    %rdx, %rcx      /* Count */
    rep stosb
    movq    %r8, %rax       /* Return original pointer */
    ret

    .global memcpy
memcpy:
    movq    %rdi, %rax      /* Save original dest pointer to return */
    movq    %rdx, %rcx      /* Count */
    rep movsb
    ret
```

### 4. Resolve `__ehdr_start` (Linker Script)
The GC needs to find global variables to scan for root pointers. It does this by parsing the ELF header. Add this to the very top of `src/linker.ld` (before `SECTIONS`):

```ld
/* Provide the ELF header address for the GC's findGlobals() */
PROVIDE_HIDDEN(__ehdr_start = 0x100000);
```
*(Note: Since our kernel is loaded at `0x100000` but the Multiboot header is placed there, `__ehdr_start` might not point to a valid ELF header in memory. If the GC crashes during `findGlobals()`, you will need to override `findGlobals()` in Go to use `_globals_start` and `_globals_end` instead).*

---

## Claude Code Execution Instructions

1. Ask the user which path they prefer:
   - **Path A**: Complete manual control over memory via custom `malloc`/`free` in Go (No automatic GC).
   - **Path B**: Automatic Mark/Sweep GC managed by TinyGo.
2. Apply the corresponding changes to `target.json`, `stubs.S`, and Go source files.
3. Run `make clean && make build` to verify there are no undefined symbols.
4. Run `make run-kernel` to verify the kernel boots and allocations succeed without triple-faulting.
