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

- [x] **6. FLIP `user/target.json` gc=leaking → gc=conservative**
  - [x] JSON edit.
  - [x] Required follow-up: add `_stack_top = 0x7FFF2000;` to
        `user/linker_user.ld` — gc_stack_raw.go needs the
        symbol to bound system-stack scanning. Mirrors
        `src/linker.ld:86`.
  - [x] Verify: `make build` clean; every user ELF linked
        without `undefined: _stack_top` or `tinygo_scanstack`
        errors; `verify_globals_user.sh` green per ELF.
  - [x] ELF sizes post-flip (all under 256 KiB cap):
        hello 63 KiB · ls 66 · cat 66 · wc 74 · fdprobe 65 ·
        sh 88 · edit 103 · goprobe 103 · gochan 113 ·
        **tinyc 138.5 KiB** (peak, +15 KiB from gc=leaking).
  - [x] `test_sendkey.sh 1 → pf=0 exit=3 cat=1` — shell boots
        and works under `gc=conservative`.

- [x] **7. Regression matrix — all green under `gc=conservative`**
  - [x] `tmp/test_sendkey.sh` trials 1–10 all
        `pf=0 exit=3 cat=1`.
  - [x] `test_fd_probe.sh` → `contents=1 read_write=1 err=1 pf=0`.
  - [x] `test_redirect.sh` → `hello_lines=1 pf=0`.
  - [x] `test_pipe.sh` → `pf=0 exit=3 hello_lines=1 world_lines=1`.
  - [x] `test_wc_pipe.sh` → `echo_counts=1 file_counts=1 pf=0`.
  - [x] `test_pipe_matrix.sh` → all 4 cases `pf=0`.
  - [x] `test_goprobe.sh` → `pf=0 begin=1 go_chan=1 select=1 time_sleep=1 yield=1 all=1`.
  - [x] `test_gochan.sh` → `pf=0 sq=1/1/1/1/1 alpha=1 beta=1 fin=1`.
  - [x] `test_tinyc.sh` → **`fib(10) = 55`** works under
        `gc=conservative` (177 calls would OOM under leaking;
        conservative reclaims). Result: `pf=0 s45=2 fib55=1 forsum=1`.
  - [x] `test_edit.sh` → `pf=0 hello=1`.
  - [x] Optional polish applied: `fib.tc` fixture upgraded
        from `fib(7)` → `fib(10)`; harness assertion updated.

- [x] **8. README.md + `current_impl_doc/` update**
  - [x] README.md progress-table row added:
        "Userspace conservative GC".
  - [x] Fixed stale `gc=leaking` annotation in README's
        directory-layout listing (line 230).
  - [x] Noted lifted constraint: `fib(10)` works, long-running
        programs no longer leak.
  - [x] `current_impl_doc/userland.md` — TinyGo target table
        updated (`"gc": "conservative"`); added paragraph on
        conservative-GC root scanning + HeapLimit.
  - [x] `current_impl_doc/memory.md` — user heap section
        rewritten: 1 MiB dedicated `.heap @nobits` section,
        globals brackets, mark/sweep, HeapLimit enforcement.

- [x] **9. Reviewer pass + completeness**
  - [x] Reviewer subagent (`general-purpose`, fresh run after
        first attempt 500'd) reports **CRITICAL=0, MAJOR=0,
        MINOR=0**. Every design-doc requirement verified via
        `file:line` spot-check. Every harness re-run green
        including `test_tinyc.sh → fib55=1` (the reclamation
        demonstration).
  - [x] `grep -rn TODO/FIXME/XXX` over
        `git diff 81576b1..HEAD` in `src/*.go`, `user/*.S`,
        `user/*.ld`, `user/*.json`, `scripts/*.sh` — zero new
        markers.
  - [x] Commit sequence matches 1:1 with TODO items 1–8:
        `9aeca38 7b65605 22ce849 886d9e8 a61c3e5 86ad09a
        0306abb a7aca0d`.

## Deferred items

(None yet — append here if anything slips out of scope.)

## Reviewer MINOR notes

(None yet — reviewer pass will populate.)
