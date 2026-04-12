# Conservative GC (Mark/Sweep) — Implementation TODO

- [x] Read impldoc/conservative_gc_design.md in full
- [x] Modify src/target.json: gc "leaking" → "conservative"
- [x] Modify src/linker.ld: add _globals_size, update comments
- [x] Modify src/stubs.S: add memset, tinygo_scanCurrentStack, synthetic ELF header
- [x] Rewrite src/main.go: conservative GC demo with runtime.GC() + ReadMemStats
- [x] Build: make clean && make build — zero errors
- [x] Binary-level verification: all 8 checks pass
- [x] Reviewer subagent: zero blockers, zero fixes needed
- [x] Final sweep: no unchecked items, no TODO/FIXME in code
- [x] Git commit
