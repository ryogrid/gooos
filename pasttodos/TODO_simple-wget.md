# TODO: Simple userspace `wget` command

Design doc: [design_docs/01_simple-wget_overview.md](design_docs/01_simple-wget_overview.md)

## Steps

- [x] 1. Create wget skeleton + Makefile wiring (usage stub + CMDS append)
- [x] 2. URL parser (parseURL, parseIPOK, parseInt, splitSpace; print parsed components)
- [x] 3. HTTP transaction (TCP connect + send GET + read raw response into 4 KiB buf, print)
- [x] 4. Header parser (readHeaders, parseStatus, indexOfSeq; print status + body-prefix length)
- [x] 5. Body streaming + status gating (Open/Write/Close output file; non-200 → no file)
- [x] 6. Embed + kernel rebuild (regenerate src/user_binaries.go via embed_elfs.sh)
- [x] 7. Documentation updates (README.md L42, docs/user_programs.md, docs/repo_layout.md)

## Deferred

- **Verification §4 / §5 / §9 require manual QEMU testing.**
  These need a host-side `python3 -m http.server 8000` bound
  to a TCP port, which the build sandbox refuses to start.
  Build/lint/ISO/regression checks (§1–§3, §10–§11) cover
  the gates the agent can run unattended; deterministic
  parseURL rejections (§6–§8) are now automated via
  `scripts/test_wget_parse_rejections.sh`. The user should
  run §4 / §5 / §9 interactively when convenient — recipes
  preserved verbatim in `design_docs/01_simple-wget_overview.md`
  "Verification" §4 (happy path), §5 (HTTP 404), §9 (FS-limit).

## Verification

- [x] 1. `make -C user` succeeds; `user/build/wget.elf` exists (121,312 bytes)
- [x] 2. `make build` succeeds end-to-end (lint, embed, kernel link, verify-globals all green)
- [x] 3. `make iso` succeeds; `tmp/kernel.iso` exists (3779 sectors, ~7.4 MiB)
- [ ] 4. Happy path (manual, QEMU) — **deferred to user; needs host http.server**
- [ ] 5. Error path — HTTP 404 — **deferred to user; needs host http.server**
- [x] 6. Error path — HTTPS rejection — `scripts/test_wget_parse_rejections.sh` PASS (https=1)
- [x] 7. Error path — hostname rejection — `scripts/test_wget_parse_rejections.sh` PASS (host=1)
- [x] 8. Error path — empty basename — `scripts/test_wget_parse_rejections.sh` PASS (basename=1)
- [ ] 9. FS-limit path (manual, QEMU) — **deferred to user; needs host http.server**
- [x] 10. Regression — non-net build path: 12s QEMU smoke boot reached `gooos shell v0.1` + `$ ` prompt with no `panic`/`PF`/`FATAL` markers
- [x] 11. Regression — net build path: `scripts/test_net.sh` PASS (UDP echo + ARP + ICMP + netbuf + netDiag); `scripts/test_tcp_phase5.sh` exit 0
