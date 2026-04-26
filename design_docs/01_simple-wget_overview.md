# US-001: Simple userspace `wget` command for gooos

## Context

gooos already exposes a complete userspace TCP client SDK
(`TCPSocket` / `TCPConnect` / `TCPSendAll` / `TCPRecv` /
`TCPShutdown` in `user/gooos/net.go:165–257`) and a flat
in-memory FS reachable from Ring-3 via `Open` / `Write` /
`Close` (`user/gooos/io.go:22–59`). The existing
`user/cmd/tcpcli/main.go` proves the active-open path
end-to-end but only roundtrips raw bytes. There is no
userspace HTTP client today, so a user wanting to fetch a
remote file has to type the HTTP request out by hand into
`tcpcli` (and then has no path to persist the response).
This change adds a minimal `wget`-style command that
performs a single HTTP/1.0 GET and persists the response
body to a same-named file in the gooos FS, sourcing its
data from any HTTP server reachable from QEMU's slirp
(typically `10.0.2.2:<port>` for the host).

## Architecture

The program is a single `package main` under
`user/cmd/wget/`, following the structure and
per-program-helper convention of `user/cmd/tcpcli/main.go`.
No kernel changes; only userspace + build wiring + small
doc updates.

**Wire choice:** HTTP/1.0 with `Connection: close`. Reasons:
- Avoids HTTP/1.1's chunked transfer encoding, which the
  parser would otherwise need to decode.
- Server signals end-of-body by closing the TCP connection
  (RFC 1945 §7.2.2), so the body length is implicit — no
  `Content-Length` dependency.
- Maximum interoperability against `python3 -m http.server`,
  busybox httpd, nginx defaults, etc.

**CRLF tolerance:** the parser assumes strict `\r\n` line
terminators (i.e. `\r\n\r\n` for the header/body boundary).
Servers that emit bare-LF separators are non-conformant
and unsupported; documented as a Known Limitation.

**URL parsing:** in-place, no regex (the gooos user runtime
intentionally avoids stdlib pulls). The grammar accepted is:

```
http://<IPv4-literal>[:port]/<path>
```

Anything else (HTTPS, hostname, missing scheme, missing path,
empty basename) is rejected with an error message. Default
port is 80 if omitted. Default path is rejected (no basename
to write to).

**Filename derivation:** the last `/`-delimited segment of
the URL path. `http://10.0.2.2/foo/bar.txt` → `bar.txt`.
`http://10.0.2.2/` → error (no basename).

**Call chain:**

```
main()
  ├─ gooos.Args()
  │   └─ splitSpace → tokens[0] = URL
  ├─ parseURL(url) → (ip, port, path, filename) | error
  ├─ gooos.TCPSocket()                         → fd
  ├─ gooos.TCPConnect(fd, ip, port, 0)         → 0 on success
  ├─ gooos.TCPSendAll(fd, requestBytes)        → len(req)
  ├─ readHeaders(fd, buf)
  │   ├─ loops TCPRecv into the same 4 KiB buf, accumulating
  │   │   into buf[0:totalRead], rescanning ALL of
  │   │   buf[:totalRead] for "\r\n\r\n" after each recv
  │   │   (the sentinel may straddle two TCPRecv returns).
  │   ├─ TCPRecv n < 0 → error path: "wget: recv error <n>"
  │   ├─ TCPRecv n == 0 (clean EOF before "\r\n\r\n") → error
  │   │   path: "wget: server closed before headers"
  │   ├─ buffer fills (totalRead == cap) without sentinel →
  │   │   error path: "wget: header too large"
  │   ├─ parseStatus(buf) → status code (0 on parse failure)
  │   └─ returns (status, bodyOff, totalRead, errMsg)
  ├─ if status != 200: print "wget: HTTP <n>", exit (no file
  │   created)
  ├─ gooos.Open(filename, OpenWrite)           → outfd (>= 0)
  ├─ gooos.Write(outfd, buf[bodyOff:totalRead]) — body prefix
  │   that arrived in the same recv as the header tail
  ├─ loop:
  │     n := gooos.TCPRecv(fd, buf, 0)
  │     if n == 0: break (clean EOF — success)
  │     if n  < 0: print "wget: recv error <n>", close outfd,
  │                continue to TCP shutdown (partial file
  │                left behind for inspection); exit non-zero
  │     w := gooos.Write(outfd, buf[:n])
  │     if w != n: print "wget: short write (FS limit ~256 KiB)",
  │                close outfd, continue to TCP shutdown; exit
  │                non-zero
  │     totalBody += n
  ├─ gooos.Close(outfd)
  ├─ gooos.TCPShutdown(fd, SHUT_WR)
  ├─ gooos.Close(fd)                           — explicit, mirrors tcpcli pattern
  └─ print "wget: saved <filename> (<totalBody>)"
```

**HTTP request format (sent verbatim):**

```
GET <path> HTTP/1.0\r\n
Host: <host[:port]>\r\n
User-Agent: gooos-wget/0.1\r\n
Connection: close\r\n
\r\n
```

**Buffer sizing:**
- Request: built into a small (`<256` byte) byte slice.
- Response read buffer: 4 KiB (`var buf [4096]byte`). Header
  scan caps at the buffer size — pathologically large
  header blocks (>4 KiB) are rejected with an error.
- Body chunk reuse: same 4 KiB buffer is reused per
  `TCPRecv` → `Write` iteration (no per-chunk allocation).

## Files to Create

### 1. `user/cmd/wget/main.go` (~280 LOC)

- `func main()` — argv parsing, URL parsing, socket open,
  request send, response read, file write, status messages.
- `func parseURL(s string) (ip uint32, port uint16, path string, filename string, errMsg string)` —
  splits an HTTP URL into components. Returns
  `errMsg = ""` on success; non-empty `errMsg` on
  rejection. Reject cases:
  - HTTPS / no scheme → `"wget: only http:// supported"`
  - Host that fails the IPv4-literal check →
    `"wget: hostname not supported (no DNS); use IP literal"`
  - Empty basename (URL ends in `/`, or path is empty/`.`)
    → `"wget: URL has no basename"`
  - Bad port → `"wget: bad port"`
  IPv4 parse uses a `parseIPOK(s string) (uint32, bool)`
  helper (see below) so that the literal `0.0.0.0` is
  distinguishable from "malformed input"; both are still
  rejected by parseURL (`0.0.0.0` isn't a useful target),
  but with different error messages.
- `func splitSpace(s string) []string` — copy of tcpcli's
  helper at `user/cmd/tcpcli/main.go:58–82`. The
  2-tokens-then-tail behavior is irrelevant for wget's
  single-arg input; we use only `tokens[0]` (the URL).
  Imported as-is for symmetry with tcpcli.
- `func parseIPOK(s string) (uint32, bool)` — derived from
  tcpcli's `parseIP` (`user/cmd/tcpcli/main.go:84–116`)
  but returns an explicit `ok bool` so the caller can
  distinguish the literal `0.0.0.0` from "malformed input".
  The original tcpcli `parseIP` returns 0 on both, an
  ambiguity that bites here (a URL like
  `http://0.0.0.0:8000/x` would otherwise be silently
  rejected as malformed). The signature change is a local
  refactor; tcpcli is not touched.
- `func parseInt(s string) int` — copy of tcpcli's helper
  at `user/cmd/tcpcli/main.go:120–133`. Decimal string →
  int (returns 0 on malformed input — port=0 is its own
  rejection in parseURL).
- `func readHeaders(fd int, buf []byte) (status int, bodyOff int, totalRead int, errMsg string)` —
  loops `gooos.TCPRecv` into `buf`, accumulating bytes by
  appending to `buf[totalRead:]`. After each successful
  recv, rescans **all** of `buf[:totalRead]` (not just
  the latest chunk) for `\r\n\r\n` so a sentinel that
  straddles two reads is still found. Returns:
  - on TCPRecv n < 0: `errMsg = "wget: recv error <n>"`.
  - on TCPRecv n == 0 before sentinel:
    `errMsg = "wget: server closed before headers"`.
  - on buffer-full without sentinel:
    `errMsg = "wget: header too large (>4 KiB)"`.
  - on sentinel found: `bodyOff = idx + 4` (past the
    `\r\n\r\n`), `status = parseStatus(buf, totalRead)`,
    `errMsg = ""`.
- `func indexOfSeq(buf []byte, n int, seq []byte) int` —
  naïve sub-slice search over `buf[:n]`. Returns the start
  index of `seq` or -1 if not present. Used to find
  `\r\n\r\n`. Always called with the full accumulated
  length `n = totalRead` so straddled sentinels are caught.
- `func parseStatus(buf []byte, n int) int` — extracts the
  numeric status code from the status line ("HTTP/1.x XXX
  REASON\r\n"). Returns 0 on parse failure.
- `func hasPrefix(s, prefix string) bool` — local
  stand-in for `strings.HasPrefix`. The gooos user runtime
  intentionally avoids stdlib pulls, so a four-line scan
  is cheaper than dragging in `strings`.
- `func formatIP(ip uint32) string` — local stand-in for
  net-package address formatting. Used to build the
  `Host: <a.b.c.d[:port]>` header for the request.
  Mirrors the inverse of `parseIPOK`.

## Files to Modify

### 1. `user/Makefile` (line 21)

Append `wget` to the `CMDS` variable so the pattern rule
at line 50 picks up `cmd/wget/main.go` and produces
`build/wget.elf`. Single-token addition, no other changes.

### 2. `src/user_binaries.go` (auto-regenerated)

**Do not hand-edit.** `scripts/embed_elfs.sh` regenerates
this file from `user/build/*.elf`; the wget ELF lands
automatically once it appears in the build directory. The
kernel rebuild re-embeds it.

### 3. `README.md` (line 42 — BusyBox-style shell row)

Append `wget` to the comma-separated external command
roster ("…, `cpuhog`, `markerprint`, `wget`, plus net-stack
demos…") so the table row reflects reality. One-token
addition.

### 4. `docs/user_programs.md`

Add a 3–5 line paragraph describing `wget`: HTTP-only,
IP-literal-only, single-shot download, basename → flat FS.
Cross-link to `docs/networking_demos.md` for the host-side
test-server recipe.

### 5. `docs/repo_layout.md`

Add `user/cmd/wget/` to the per-program listing in the
same style as the existing `tcpcli/` / `tcpecho/` entries.

## Implementation Steps

1. **Create wget skeleton + Makefile wiring.** Add
   `user/cmd/wget/main.go` with `package main`, gooos
   import, and a `main()` that uses `gooos.Args()` + a
   stub `splitSpace` to extract the first token; if
   missing, prints `usage: wget <url>` and returns. Append
   `wget` to `user/Makefile:21` `CMDS`. Verify
   `make -C user` produces `user/build/wget.elf`. **Commit
   + push.**

2. **URL parser.** Implement `parseURL`, `parseIPOK`,
   `parseInt`, `splitSpace`. `main()` calls `parseURL` and
   prints the parsed `(ip, port, path, filename)` for now
   — verifies parsing works without yet hitting the
   network. Re-build user side; add wget to the kernel
   ISO via `make iso`; smoke test by typing
   `wget http://10.0.2.2:8000/foo/bar.txt` at the gooos
   shell and observing the parsed-components print line.
   **Commit + push.**

3. **HTTP transaction (in-memory).** `main()` now opens a
   TCP socket, connects, sends the HTTP/1.0 GET request,
   reads up to one 4 KiB buffer of response, prints the
   raw response. Verifies the wire format is well-formed
   against `python3 -m http.server` on the host. **Commit
   + push.**

4. **Header parser (in-memory only).** Add `readHeaders`,
   `parseStatus`, `indexOfSeq`. After `TCPSendAll`, call
   `readHeaders` and print the parsed status code + the
   number of body-prefix bytes already in the buffer. Do
   not write to the FS yet. Cover the cross-recv straddle
   path by ensuring `indexOfSeq` is called over
   `buf[:totalRead]` after every recv. **Commit + push.**

5. **Body streaming + status gating.** On `status == 200`,
   open the output file via `gooos.Open(filename,
   OpenWrite)`, write the body-prefix bytes that arrived
   in the same recv as the header tail, then loop
   `TCPRecv` → `Write` until `TCPRecv` returns 0 (clean
   EOF — success) or negative (recv error). On any
   non-200 status, print `wget: HTTP <code>` and exit
   without creating the file. On success, print `wget:
   saved <filename> (<bytes>)`. Per the architecture
   call-chain, treat `Write` returning `< n` as a
   `wget: short write (FS limit ~256 KiB)` error and
   exit non-zero. **Commit + push.**

6. **Embed + kernel rebuild.** Run
   `make -C user && bash scripts/embed_elfs.sh && make`
   to produce `tmp/kernel.iso` with `wget.elf` embedded
   in `src/user_binaries.go`. (No source changes; this
   commit captures the regenerated `src/user_binaries.go`
   and any incidental ISO regeneration that might result
   in an embedded-byte diff.) **Commit + push.**

7. **Documentation updates.** Edit `README.md` line 42,
   `docs/user_programs.md`, and `docs/repo_layout.md`
   with one-liner entries describing `wget` and its
   IP-only / HTTP-only constraints. **Commit + push.**

## Verification

1. **User-side build:** `make -C user` succeeds;
   `user/build/wget.elf` exists with non-zero size.
2. **Lint + kernel build:** `make build` succeeds
   end-to-end (lint, embed, kernel link, verify-globals
   all green).
3. **ISO produces:** `make iso` succeeds;
   `tmp/kernel.iso` exists.
4. **Happy path (manual, in QEMU):**
   - Host: `python3 -m http.server 8000` in a temporary
     directory containing `test.txt` (~100 bytes, known
     content).
   - `make run-net`.
   - In gooos shell: `wget http://10.0.2.2:8000/test.txt`.
   - Observe `wget: saved test.txt (NN bytes)`.
   - `ls` lists `test.txt`; `cat test.txt` shows expected
     content byte-for-byte.
5. **Error path — HTTP 404:**
   - `wget http://10.0.2.2:8000/nonexistent.txt`.
   - Observe `wget: HTTP 404`.
   - `ls` does **not** list `nonexistent.txt`.
6. **Error path — HTTPS rejection:**
   - `wget https://example.com/foo` → parse error message
     mentioning HTTPS not supported.
7. **Error path — hostname rejection:**
   - `wget http://example.com/foo` → parse error message
     mentioning IP-literal required (no DNS).
8. **Error path — empty basename:**
   - `wget http://10.0.2.2:8000/` → parse error message
     mentioning no basename.
9. **FS-limit path (manual, in QEMU):**
   - Host: serve a file slightly larger than 256 KiB
     (`dd if=/dev/urandom of=big.bin bs=1024 count=300`).
   - In gooos shell: `wget http://10.0.2.2:8000/big.bin`.
   - Observe `wget: short write (FS limit ~256 KiB)`
     after the FS fills; `ls` shows `big.bin` exists at
     truncated length (verifies the write-rejection path
     of `src/fs.go:fsWrite`).
10. **Regression — non-net build path:** existing
    programs `ls`, `cat`, `tcpcli`, `tcpecho` still
    build; `make run` (single core, no NIC) reaches the
    shell prompt and `help` works.
11. **Regression — net build path:** under `make
    run-net`, `dhcp` still completes the DORA exchange
    and `udpecho`/`tcpecho` still work (smoke-checked
    from host, not just from gooos shell).

## Reuse References

- `gooos.TCPSocket() / TCPConnect(fd, ip, port, timeout) /
  TCPSendAll(fd, buf) / TCPRecv(fd, buf, timeout) /
  TCPShutdown(fd, how)` — `user/gooos/net.go:165–257`.
- `gooos.Open(name, mode) / Write(fd, buf) / Close(fd)` —
  `user/gooos/io.go:22–59`. Use `gooos.OpenWrite`
  (truncate) for the output file.
- `gooos.Args()` — `user/gooos/proc.go:36–46` (returns
  the argv string verbatim, max 256 bytes).
- `gooos.Println(s string)` — `user/gooos/io.go`
  (single-arg output convention used across all user
  programs).
- Helper patterns to **copy verbatim** into wget/main.go
  (gooos convention: each cmd carries its own helpers; no
  shared utility module): `splitSpace`, `parseIP` (→
  `parseIPOK` rename for ok-flag), `parseInt` from
  `user/cmd/tcpcli/main.go:58–133`.
- Build extension: `user/Makefile:21` `CMDS` variable;
  the pattern rule at line 50 auto-discovers any new
  entry.
- Embed flow: `scripts/embed_elfs.sh` (no edits needed —
  scans `user/build/*.elf` automatically).
- Shell dispatch: `user/cmd/sh/main.go:345`
  (`gooos.Exec(cmd + ".elf", args)`); any ELF in the FS
  is runnable from the shell prompt. Confirmed `wget`
  does not collide with the sh built-ins (`help`,
  `echo`, `clear`, `exit`).

## Known Limitations

- **IP literals only.** No DNS resolver in gooos; the
  kernel stores DHCP-supplied DNS at `src/net.go:25` but
  exposes no resolver. Hostnames in URLs are rejected
  with a parse error.
- **HTTP/1.0 only.** Sidesteps chunked transfer
  encoding. Server is expected to honor close-delimited
  body semantics.
- **No HTTPS.** TLS stack is not implemented in gooos.
- **No redirects.** 3xx responses are surfaced as
  `wget: HTTP <code>` errors.
- **256 KiB max file size.** Inherited from gooos FS
  (`maxFileData` at `src/fs.go:12`). Larger downloads
  short-write at the FS limit; a `wget: short write
  (FS limit ~256 KiB)` error message is emitted and the
  process exits non-zero.
- **Header block ≤ 4 KiB.** Servers returning >4 KiB of
  response headers will see `wget: header too large`.
- **No User-Agent / cookie / auth-header customization.**
  Fixed `User-Agent: gooos-wget/0.1`.
- **No partial-download resume / Range header support.**
- **Strict CRLF only.** The header parser requires
  `\r\n` line terminators (per RFC 1945 §3.1). Servers
  emitting bare-LF separators are non-conformant and
  not supported.
- **Args bounded at 256 bytes.** `gooos.Args()` returns
  a string capped at 256 bytes
  (`user/gooos/proc.go:36–46`). After the `wget ` prefix
  is consumed, URLs longer than ~250 bytes are silently
  truncated by the kernel argv delivery. URLs are
  typically well under this limit.
- **Overwrites silently.** `gooos.Open(name, OpenWrite)`
  truncates an existing file with the same name (no
  prompt), matching wget(1) semantics.
- **`0.0.0.0` rejected as a target.** `parseURL` rejects
  the literal `0.0.0.0` with an explicit error message
  (it's syntactically valid as an IPv4 literal but not a
  useful destination). Distinguished from malformed-IP
  rejection via the `parseIPOK` helper.

## Open Questions

- The decision to reject `0.0.0.0` (rather than allow
  it) is per the design above; record this so a future
  reviewer doesn't relitigate it without checking the
  rationale here. (Surfaced by the Phase-2 review.)
