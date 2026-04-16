# TODO — Userspace `gc=conservative` Migration

Design sources:
- `impldoc/userspace_conservative_gc_overview.md`
- `impldoc/userspace_conservative_gc_linker.md`
- `impldoc/userspace_conservative_gc_runtime.md`
- `impldoc/userspace_conservative_gc_verification.md`

One git commit per top-level item.

## Items

- [x] **1. Linker foundation (atomic commit)**
  - [x] `user/linker_user.ld`: add `_globals_start` after
        `.rodata`; `_globals_end` + `_globals_size` AFTER `.bss`
        and BEFORE heap; heap moved to dedicated `.heap : ALIGN
        (4096) { *(.heap) }` output section; 1-page guard gap.
  - [x] `user/rt0.S`: add `.heap @nobits` input section with
        `.skip 0x100000` (1 MiB); add synthetic `__ehdr_start`
        Elf64 header (mirror of `src/stubs.S:341-375`).
  - [x] Atomic commit — no intermediate globals/heap overlap.
  - [x] Verify: `make build` clean; `nm user/build/hello.elf`
        shows `_globals_start=0x4010071C`,
        `_globals_end=0x40101059`, `_globals_size=0x93D`,
        `__ehdr_start=0x40100600`,
        `_heap_start=0x40102000`, `_heap_end=0x40202000`
        (1 MiB heap, strictly after globals range);
        `readelf -l` shows RW PT_LOAD filesz=0x28 memsz=0x101000;
        `tmp/test_sendkey.sh 1` PASS (`pf=0 exit=3 cat=1`).

- [x] **2. `tinygo_scanCurrentStack` in `user/runtime_asm_amd64.S`**
  - [x] Port `src/stubs.S:248-269` (trampoline + weak dummy).
  - [x] Verify: `make build` clean;
        `tinygo_scanCurrentStack=0x401000F2 T` +
        `tinygo_scanstack=0x4010010D W` in `hello.elf`;
        `test_sendkey.sh 1` PASS.

- [x] **3. `Process.HeapLimit` + `sysSbrkHandler` enforcement**
  - [x] Added `HeapLimit uintptr` to `Process` struct
        (`src/process.go:33`).
  - [x] `userHeapLimit = 2*1024*1024` constant
        (`src/process.go:22`).
  - [x] Init `HeapLimit = HeapBreak + userHeapLimit` at both
        `src/elf.go:229` (elfLoad) and
        `src/process.go:289` (elfSpawn).
  - [x] `src/userspace.go:433-439`: refuse sbrk past the
        ceiling with -1 return.
  - [x] Verify: `make build` clean;
        `test_sendkey.sh 1 → pf=0 exit=3 cat=1`.

- [x] **4. `scripts/verify_globals_user.sh`**
  - [x] New script — generalization of `scripts/verify_globals.sh`
        taking an ELF path arg; `chmod +x`.
  - [x] Tolerates missing runtime queues (DCE case: prints
        "no runtime queues (OK, DCE)").
  - [x] Wired into `user/Makefile` ELF link rule via
        `bash ../scripts/verify_globals_user.sh $@`.
  - [x] Verify: every user ELF either green or DCE-tolerated.
        `goprobe.elf` + `gochan.elf` show 1 queue symbol each
        inside their globals range; the other 8 ELFs DCE'd
        the queues entirely.

- [x] **5. `maxFileData` bump 131072 → 262144 (256 KiB)**
  - [x] `src/fs.go:12` change + comment update.
  - [x] `scripts/embed_elfs.sh` pre-flight size check
        (`MAX_ELF_SIZE = 262144`).
  - [x] Verify: `make build` clean;
        `verify-globals: OK (1 symbols inside [0x10b286, 0x9d9118))`
        — globals range grew by ~4 MiB matching
        32 × (256 − 128) KiB FS footprint growth.
        `test_sendkey.sh 1` PASS.

- [ ] **6. FLIP `user/target.json` gc=leaking → gc=conservative**
  - [ ] Single JSON edit.
  - [ ] Verify: `make build` clean; no undefined-symbol
        errors; `verify_globals_user.sh` green per ELF.

- [ ] **7. Regression matrix**
  - [ ] `tmp/test_sendkey.sh` × 10 all PASS.
  - [ ] `test_fd_probe.sh` PASS.
  - [ ] `test_redirect.sh` PASS.
  - [ ] `test_pipe.sh` PASS.
  - [ ] `test_wc_pipe.sh` PASS.
  - [ ] `test_pipe_matrix.sh` PASS.
  - [ ] `test_goprobe.sh` PASS.
  - [ ] `test_gochan.sh` PASS.
  - [ ] `test_tinyc.sh` PASS.
  - [ ] `test_edit.sh` PASS.
  - [ ] Optional post-flip polish: restore `fib(10)` in
        `fib.tc` fixture.

- [ ] **8. README.md + `current_impl_doc/` update**
  - [ ] README.md progress-table row for user-side GC.
  - [ ] Remove any stale `gc=leaking` claims in README.
  - [ ] Note lifted constraint (fib(10), long-running
        programs).
  - [ ] `current_impl_doc/userland.md` — update TinyGo target
        table + heap size.
  - [ ] `current_impl_doc/memory.md` — update user heap
        description.

- [ ] **9. Reviewer pass + completeness**
  - [ ] Reviewer subagent: CRITICAL=0, MAJOR=0 (fix inline).
  - [ ] MINOR recorded in this doc's tail.
  - [ ] `grep -rn 'TODO\|FIXME\|XXX'` diff range — no new
        markers.
  - [ ] Every checked item has a matching commit.

## Deferred items

(None yet — append here if anything slips out of scope.)

## Reviewer MINOR notes

(None yet — reviewer pass will populate.)
