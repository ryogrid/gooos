# BusyBox Shell Implementation — TODO

## Phase 0: TinyGo Feasibility Verification
- [x] Compile a minimal TinyGo test program with `gc=leaking`, `scheduler=none` to identify required runtime stubs
- [x] Verify the entry point symbol name (`main` vs `runtime.main` vs other) → **`main`** (C ABI, T symbol)
- [x] Determine minimum set of undefined symbols that need stubs → **`abort`, `mmap`, `raise`, `tinygo_register_fatal_signals`, `write`** (5 stubs only)

## Phase 1: Kernel Infrastructure
- [x] Expand filesystem: `maxFiles=32`, `maxFileData=65536`, add `fsDelete`
- [x] Task recycling: `maxTasks=32`, `taskFree` state, `StackBase` field, `taskReclaim()`, slot reuse in `createTask()`
- [x] Add `chanFree()` to channel.go for channel pool reclamation
- [x] Add `walkAndGetPaddr()` to vm.go for page cleanup
- [x] Create `src/vga.go` — VGA console with cursor, scrolling, `vgaConsolePutChar/Print/Clear`
- [x] Create `src/process.go` — Process struct, SavedMapping, elfExec, elfExecTrampoline, processExit
- [x] Add `elfExecTrampolineAddr` assembly stub to switch.S

## Phase 2: Syscall ABI
- [x] Redesign syscall dispatch in userspace.go — 12 syscalls (exit, write, read, exec, fs_read, fs_write, fs_list, yield, sleep, getargs, sbrk, vga_clear)
- [x] Implement `sys_write` (fd=0 VGA+serial, fd=1 serial only) using VGA console
- [x] Implement `sys_read` — kernel-side line-buffered keyboard input with echo
- [x] Implement `sys_exec` — calls elfExec, blocks parent
- [x] Implement `sys_exit` — calls processExit
- [x] Implement `sys_fs_read`, `sys_fs_write`, `sys_fs_list`
- [x] Implement `sys_getargs`
- [x] Implement `sys_sbrk` — per-process heap growth
- [x] Implement `sys_vga_clear`
- [x] Remove old syscall handlers and hand-crafted user ELF binary

## Phase 3: Userland SDK
- [x] Create `user/target.json` — TinyGo target for gooos userspace
- [x] Create `user/linker_user.ld` — linker script (entry at 0x40100000)
- [x] Create `user/rt0.S` — startup assembly + syscall stubs + runtime stubs (mmap, write, abort, memcpy, memset, raise, tinygo_register_fatal_signals)
- [x] Create `user/gooos/syscall.go` — raw syscall wrappers and constants
- [x] Create `user/gooos/io.go` — Print, Println, ReadLine
- [x] Create `user/gooos/fs.go` — ReadFile, ListDir
- [x] Create `user/gooos/proc.go` — Exec, Exit, Args, Yield, Sleep
- [x] Runtime stubs in rt0.S (assembly, not separate Go file) — mmap, write, abort, raise, tinygo_register_fatal_signals, memcpy, memset
- [x] Create `user/Makefile` — build all user programs (two-step: TinyGo → ld.lld)
- [x] Verify user SDK compiles a minimal test binary successfully

## Phase 4: User Programs
- [x] Create `user/cmd/hello/main.go` — hello world (27 KiB)
- [x] Create `user/cmd/sh/main.go` — interactive shell (37 KiB)
- [x] Create `user/cmd/ls/main.go` — list files (33 KiB)
- [x] Create `user/cmd/cat/main.go` — display file contents (33 KiB)
- [x] Create `user/cmd/wc/main.go` — word/line/byte count (35 KiB)

## Phase 5: Integration
- [ ] Create `scripts/embed_elfs.sh` — convert user ELF binaries to Go byte arrays
- [ ] Update root Makefile — add `user`, `embed-user` targets, integrate into `build`
- [ ] Update `src/main.go` — revised boot sequence: store ELFs in FS, spawn service tasks, exec shell
- [ ] Remove demo/channel/select tasks from boot sequence
- [ ] Build and verify `make clean && make build` succeeds
- [ ] Test in QEMU — shell boots, `help`, `echo hello`, `ls` work

## Phase 6: Review and Verification
- [ ] Submit to reviewer subagent — check consistency with design docs
- [ ] Address all reviewer findings
- [ ] Final TODO.md cross-check — no unchecked items
- [ ] Search for TODO/FIXME/HACK/XXX comments — resolve or defer explicitly

## Deferred Items
(Items deferred during implementation will be listed here)
