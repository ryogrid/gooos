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

- [ ] **3. Flip `user/target.json` to tasks scheduler**
  - [ ] `scheduler=tasks`, `default-stack-size=8192`,
        `automatic-stack-size=true`,
        `build-tags=["gooos","baremetal"]`.
  - [ ] `make build` clean; every user ELF links.
  - [ ] `nm user/build/hello.elf | grep '\bmain\b'` shows `T main`.
  - [ ] Baseline harnesses (`test_sendkey.sh 1`) still PASS.

- [ ] **4. Bump `maxFileData` to 96 KiB**
  - [ ] `src/fs.go:12` 40960 → 98304; update comment.
  - [ ] `make build` clean.

- [ ] **5. `user/cmd/goprobe/main.go`**
  - [ ] New probe with 4 sub-tests (go+chan, select,
        time.Sleep, yield-cycle).

- [ ] **6. Wire goprobe into build + preload**
  - [ ] `user/Makefile` CMDS adds `goprobe`.
  - [ ] `src/main.go` preloads `goprobe.elf`.
  - [ ] `src/user_binaries.go` regenerated.
  - [ ] Every user ELF under 96 KiB.

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
