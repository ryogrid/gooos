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

- [x] **7. `tmp/test_goprobe.sh` harness + PASS**
  - [x] Script added at `tmp/test_goprobe.sh` (mirrors
        `test_fd_probe.sh` pattern), `chmod +x`, wait
        extended to 15 s for QEMU's post-yield scheduling
        overhead.
  - [x] **Not tracked in git** — matches the project's
        existing convention where every `tmp/test_*.sh`
        harness (test_sendkey, test_fd_probe, test_redirect,
        test_pipe, test_wc_pipe, test_pipe_matrix) lives
        locally under the `.gitignore`'d `tmp/` directory.
        Recorded here as a deliberate deviation from the
        prompt's "one commit per TODO item" rule; forcing
        the harness into the tree would invert a standing
        project decision. This TODO's commit therefore
        carries only the `TODO_USER.md` update.
  - [x] Prerequisite fix landed in preceding commit
        (`fix(syscall): sys_sleep via afterTicks`) —
        sysSleepHandler hung because the kernel's patched
        sleepTicks is a busy loop rather than a parking
        primitive.
  - [x] `bash tmp/test_goprobe.sh` →
        `pf=0 begin=1 go_chan=1 select=1 time_sleep=1 yield=1 all=1`
        → PASS.

- [x] **8. Regression matrix green (with pre-existing deferrals)**
  - [x] `tmp/test_sendkey.sh` × 10 — 10/10 PASS (every trial
        `pf=0 exit=3 cat=1`).
  - [x] `tmp/test_goprobe.sh` — PASS (re-run,
        `pf=0 begin=1 go_chan=1 select=1 time_sleep=1 yield=1 all=1`).
  - [x] `make build` clean + `verify-globals: OK`
        (`1 symbols inside [0x1089f5, 0x473018)`).
  - [ ] `tmp/test_fd_probe.sh` — **FAIL (pre-existing, not
        caused by this round)**. Needs `fdprobe` user binary
        (never wired into `CMDS` on master, noted at
        `userspace_verification.md §2.2`) AND the shift-key
        handling that lives only on the unmerged
        `pipe-redirect-multiproc` branch (commit 4cd6c39).
  - [ ] `tmp/test_redirect.sh` — **FAIL (pre-existing)**. `>`
        and `<` are sent as `shift-dot` / `shift-comma`; the
        master keyboard driver (`src/keyboard.go` post-Phase-B
        big-bang at 7a5ef02) has no shift state or
        `scancodeToASCIIShifted` table, so both characters
        arrive as `.` / `,`. Same root cause as fd_probe.
  - [ ] `tmp/test_pipe.sh` — **FAIL (pre-existing)**. Needs
        `|` via `shift-backslash`; keyboard driver produces
        `\`. Same root cause.
  - [ ] `tmp/test_wc_pipe.sh` — **FAIL (pre-existing)**. Same
        `|` dependency.
  - [ ] `tmp/test_pipe_matrix.sh` — **FAIL (pre-existing)**.
        Same `|` dependency.
  - [~] `make run-smp` — skipped (target boots interactively
        to stdio, not an automated harness). Baseline
        confirms "SMP: 1 cores online" on the kernel ISO
        used by every passing harness above.

- [ ] **9. README userspace-goroutine section**
  - [ ] Short paragraph + link to
        `impldoc/userspace_goroutines_overview.md`.

## Reviewer & completeness (after item 9)

- [ ] Reviewer subagent pass — no CRITICAL/MAJOR findings.
- [ ] `grep -rn 'TODO\|FIXME\|XXX'` diff range — no new markers.
- [ ] Every checked item has a matching commit
      (`git log --oneline`).

## Deferred items

- **Regression-matrix shift-key dependency** — every harness
  that types `>`, `<`, `|`, or `_` via QEMU monitor sendkey
  fails because `src/keyboard.go`'s current driver (post-Phase-B
  big-bang migration at commit 7a5ef02) does not track shift
  state and has no shifted-ASCII table. The minimal shift
  implementation was added on the `pipe-redirect-multiproc`
  branch (commit 4cd6c39, "feat(sh): 2 — shell redirection")
  and never merged to master. This is the sole reason
  `tmp/test_{fd_probe,redirect,pipe,wc_pipe,pipe_matrix}.sh`
  all fail; the userspace-goroutine changes landed here do not
  touch keyboard handling. Re-running those harnesses will
  require merging the keyboard shift patch from
  `pipe-redirect-multiproc` (or re-implementing it) — out of
  scope for this round, reported for follow-up.
- **`make run-smp` smoke** — marked optional in the plan;
  target `run-smp` in the project Makefile runs QEMU
  interactively (`-serial stdio`) with no quit path, so it
  cannot be driven from an automated harness in this
  repository's current shape. Boot-time "SMP: 1 cores online"
  is observed on every green harness, confirming the kernel's
  SMP bring-up sequence still runs.

## Reviewer MINOR notes

(None yet — append here as the reviewer pass flags them.)
