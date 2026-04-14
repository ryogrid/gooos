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
- [ ] Create `src/vga.go` — VGA console with cursor, scrolling, `vgaConsolePutChar/Print/Clear`
- [ ] Create `src/process.go` — Process struct, SavedMapping, elfExec, elfExecTrampoline, processExit
- [ ] Add `elfExecTrampolineAddr` assembly stub to switch.S

## Phase 2: Syscall ABI
- [ ] Redesign syscall dispatch in userspace.go — 12 syscalls (exit, write, read, exec, fs_read, fs_write, fs_list, yield, sleep, getargs, sbrk, vga_clear)
- [ ] Implement `sys_write` (fd=0 VGA+serial, fd=1 serial only) using VGA console
- [ ] Implement `sys_read` — kernel-side line-buffered keyboard input with echo
- [ ] Implement `sys_exec` — calls elfExec, blocks parent
- [ ] Implement `sys_exit` — calls processExit
- [ ] Implement `sys_fs_read`, `sys_fs_write`, `sys_fs_list`
- [ ] Implement `sys_getargs`
- [ ] Implement `sys_sbrk` — per-process heap growth
- [ ] Implement `sys_vga_clear`
- [ ] Remove old syscall handlers and hand-crafted user ELF binary

## Phase 3: Userland SDK
- [ ] Create `user/target.json` — TinyGo target for gooos userspace
- [ ] Create `user/linker_user.ld` — linker script (entry at 0x40100000)
- [ ] Create `user/rt0.S` — startup assembly + syscall stubs (syscall0..syscall4)
- [ ] Create `user/gooos/syscall.go` — raw syscall wrappers and constants
- [ ] Create `user/gooos/io.go` — Print, Println, ReadLine
- [ ] Create `user/gooos/fs.go` — ReadFile, ListDir
- [ ] Create `user/gooos/proc.go` — Exec, Exit, Args, Yield, Sleep
- [ ] Create `user/gooos/runtime_stubs.go` — mmap, write, exit stubs for TinyGo runtime
- [ ] Create `user/Makefile` — build all user programs
- [ ] Verify user SDK compiles a minimal test binary successfully

## Phase 4: User Programs
- [ ] Create `user/cmd/hello/main.go` — hello world
- [ ] Create `user/cmd/sh/main.go` — interactive shell (help, echo, clear, exit + external dispatch)
- [ ] Create `user/cmd/ls/main.go` — list files
- [ ] Create `user/cmd/cat/main.go` — display file contents
- [ ] Create `user/cmd/wc/main.go` — word/line/byte count

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
