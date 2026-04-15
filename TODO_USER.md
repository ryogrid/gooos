# TODO — Userspace Goroutines & Channels

Plan source: `/home/ryo/.claude/plans/lazy-beaming-donut.md`.
Design source: `impldoc/userspace_*.md` (five files).
One git commit per top-level item. Check off when that commit lands.

## Items

- [x] **1. TinyGo patch extension (build-tag split)**
  - [x] Add `"kernelspace"` to `src/target.json` `build-tags`.
  - [x] Extend `scripts/tinygo_runtime.patch`:
    - tighten `runtime_gooos.go` tag → `&& kernelspace`
    - tighten `interrupt_gooos.go` tag → `&& kernelspace`
    - new-file hunk: `runtime/runtime_gooos_user.go`
    - new-file hunk: `runtime/interrupt/interrupt_gooos_user.go`
  - [x] Update `scripts/patch_tinygo_runtime.sh` sentinel/apply logic.
  - [x] Re-apply patch; `make build` clean (kernel).
  - [x] `bash tmp/test_sendkey.sh 1` PASS (`trial=1 pf=0 exit=3 cat=1`).

- [x] **2. `user/gooos/runtime_hooks.go`**
  - [x] Add Go file with `gooosOnResume` (no-op) +
        `gooosStackOverflow` (sys_write + sys_exit).
        Dead code until TODO 3 flips scheduler=tasks, but
        `make build` stays clean.

- [x] **3. Flip `user/target.json` to tasks scheduler**
  - [x] `scheduler=tasks`, `default-stack-size=8192`,
        `automatic-stack-size=true`,
        `build-tags=["gooos","baremetal"]`.
  - [x] Add `user/task_stack_amd64.S` (copy of TinyGo stub) +
        wire into `user/Makefile`. TinyGo's `-o *.o` flow does
        not assemble embedded .S, same issue the kernel works
        around in `src/task_stack_amd64.S`.
  - [x] Extend `user/linker_user.ld` with `_heap_end` — add a
        1 MiB fixed heap region (baremetal.go's `growHeap`
        returns false; matches `rt0.S:mmap` 1 MiB cap).
  - [x] Patch script bugfix: also `rm -f` the `_user` runtime
        files on re-apply (new-file hunks append when target
        exists, duplicating bodies); clean up `.rej` residuals
        left by `--forward` on already-applied modify hunks.
  - [x] `make build` clean; every user ELF links.
  - [x] `nm user/build/hello.elf | grep '\bmain\b'` shows `T main`
        at `0x40100496`.
  - [x] Baseline harnesses (`test_sendkey.sh 1`) — PASS after
        TODO 4 lands the FS cap bump + heap-region fix
        (`trial=1 pf=0 exit=3 cat=1`).

- [x] **4. Bump `maxFileData` to 96 KiB**
  - [x] `src/fs.go:12` 40960 → 98304; update comment.
  - [x] Follow-up to TODO 3's linker_user.ld: nest the
        256 KiB heap reservation INSIDE .bss so ld.lld
        extends PT_LOAD memsz and the kernel elfLoader maps
        the pages. Without this, `_heap_start` pointed at
        unmapped virtual space and every user process
        page-faulted on first heap touch.
  - [x] `make build` clean; `test_sendkey.sh 1` PASS.

- [x] **5. `user/cmd/goprobe/main.go`**
  - [x] New probe with 4 sub-tests (go+chan, select,
        time.Sleep, yield-cycle). Standalone tinygo compile
        clean via the patched `/home/ryo/.local/tinygo/bin/tinygo`.

- [x] **6. Wire goprobe into build + preload**
  - [x] `user/Makefile` CMDS adds `goprobe`.
  - [x] `src/main.go` preloads `goprobe.elf`.
  - [x] `src/user_binaries.go` regenerated.
  - [x] Add `user/runtime_asm_amd64.S` (tinygo_longjmp) — uncovered
        by goprobe's `time` import pulling the panic path.
        Mirrors kernel `src/runtime_asm_amd64.S`.
  - [x] Every user ELF under 96 KiB (goprobe.elf 89.3 KiB — 7 KiB
        headroom; watch if goprobe gains more tests).
  - [x] Baseline `test_sendkey.sh 1` still PASS after wiring.

- [ ] **7. `tmp/test_goprobe.sh` harness + PASS**
  - [ ] Script added (mirrors `test_fd_probe.sh` pattern),
        `chmod +x`.
  - [ ] `bash tmp/test_goprobe.sh` → PASS.

- [ ] **8. Regression matrix green**
  - [ ] `tmp/test_sendkey.sh` × 10 — all PASS.
  - [ ] `tmp/test_fd_probe.sh` PASS.
  - [ ] `tmp/test_redirect.sh` PASS.
  - [ ] `tmp/test_pipe.sh` PASS.
  - [ ] `tmp/test_wc_pipe.sh` PASS.
  - [ ] `tmp/test_pipe_matrix.sh` PASS.
  - [ ] `tmp/test_goprobe.sh` PASS (re-run).
  - [ ] `make build` clean + `verify-globals: OK`.
  - [ ] `make run-smp` smoke PASS (optional).

- [ ] **9. README userspace-goroutine section**
  - [ ] Short paragraph + link to
        `impldoc/userspace_goroutines_overview.md`.

## Reviewer & completeness (after item 9)

- [ ] Reviewer subagent pass — no CRITICAL/MAJOR findings.
- [ ] `grep -rn 'TODO\|FIXME\|XXX'` diff range — no new markers.
- [ ] Every checked item has a matching commit
      (`git log --oneline`).

## Deferred items

(None yet — append here if scope changes.)

## Reviewer MINOR notes

(None yet — append here as the reviewer pass flags them.)
