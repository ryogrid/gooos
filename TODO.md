# Heap Allocator & GC Demo — Implementation TODO

- [x] Read impldoc/heap_gc_design.md in full
- [x] Modify src/target.json: gc "none" → "leaking"
- [x] Modify src/linker.ld: add .heap section and linker symbols
- [x] Modify src/stubs.S: functional mmap stub + .heap @nobits region
- [x] Modify src/boot.S: call main instead of kernel_main, add .global stack_top
- [x] Rewrite src/main.go: heap-exercising demo in func main()
- [x] Build: make clean && make build (added memcpy + memmove stubs for new undefined symbols)
- [x] Binary-level verification: all 7 checks from design §7.1 pass
- [x] Reviewer subagent: review passed — zero blockers, zero recommended fixes
- [x] Final sweep: no unchecked TODO.md items, no TODO/FIXME in code
- [x] Git commit
