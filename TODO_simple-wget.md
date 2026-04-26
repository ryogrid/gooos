# TODO: Simple userspace `wget` command

Design doc: [design_docs/01_simple-wget_overview.md](design_docs/01_simple-wget_overview.md)

## Steps

- [x] 1. Create wget skeleton + Makefile wiring (usage stub + CMDS append)
- [x] 2. URL parser (parseURL, parseIPOK, parseInt, splitSpace; print parsed components)
- [x] 3. HTTP transaction (TCP connect + send GET + read raw response into 4 KiB buf, print)
- [x] 4. Header parser (readHeaders, parseStatus, indexOfSeq; print status + body-prefix length)
- [ ] 5. Body streaming + status gating (Open/Write/Close output file; non-200 → no file)
- [ ] 6. Embed + kernel rebuild (regenerate src/user_binaries.go via embed_elfs.sh)
- [ ] 7. Documentation updates (README.md L42, docs/user_programs.md, docs/repo_layout.md)

## Deferred

(none yet)

## Verification

- [ ] 1. `make -C user` succeeds; `user/build/wget.elf` exists with non-zero size
- [ ] 2. `make build` succeeds end-to-end (lint, embed, kernel link, verify-globals)
- [ ] 3. `make iso` succeeds; `tmp/kernel.iso` exists
- [ ] 4. Happy path (manual, QEMU): wget downloads test.txt, ls + cat verify content
- [ ] 5. Error path — HTTP 404: prints `wget: HTTP 404`, no file created
- [ ] 6. Error path — HTTPS rejection: prints HTTPS-not-supported parse error
- [ ] 7. Error path — hostname rejection: prints IP-literal-required parse error
- [ ] 8. Error path — empty basename: prints no-basename parse error
- [ ] 9. FS-limit path (manual, QEMU): >256 KiB body → `wget: short write` message
- [ ] 10. Regression — non-net build path: ls/cat/tcpcli/tcpecho still build; `make run` reaches shell
- [ ] 11. Regression — net build path: dhcp/udpecho/tcpecho still work under `make run-net`
