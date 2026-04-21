# Userland ABI, SDK Surface, and Embedded ELF Lifecycle

## User Build Target (`user/target.json`)

User binaries are compiled with:

- `goarch: amd64`
- `scheduler: tasks`
- `gc: conservative`
- bare-metal oriented LLVM target (`x86_64-unknown-none-elf`)

## Syscall ABI Contract

Low-level syscall stubs are in user assembly (`user/rt0.S`); Go wrappers are in `user/gooos` package.

Kernel dispatch uses vector `0x80` with register ABI:

- `RAX`: syscall number
- `RDI, RSI, RDX, R10, R8, R9`: arguments
- return value in `RAX`

## SDK Packages (`user/gooos`)

- `syscall.go`: base syscall entry wrappers and core syscall constants
- `io.go`, `fs.go`, `proc.go`: process/FD/filesystem APIs
- `net.go`: UDP + TCP socket wrappers and net-config controls
- `signal.go`: user-facing signal registration/return path wrappers

## Syscall Number Space (Current)

Kernel-side canonical mapping is defined in `src/userspace.go`.

The currently active extended range includes:

- 22..27: UDP/socket + net config
- 28..33: TCP sockets
- 34: waitpid
- 35: sigaction
- 36: sigreturn
- 37: listprocs

## Embedded ELF Build Pipeline

### Generation

`scripts/embed_elfs.sh`:

1. reads all `user/build/*.elf`
2. enforces max size against FS capacity (`MAX_ELF_SIZE=262144`)
3. emits `src/user_binaries.go` as byte array declarations

### Boot materialization

`main()` writes each embedded ELF into the in-memory filesystem by name (for example `sh.elf`, `ls.elf`, `markerprint.elf`, `tcpcli.elf`).

### Runtime execution

- boot shell path: `elfLoad("sh.elf")` style startup
- spawn path: `elfSpawn(filename, args, parent)`

## Ring 3 Runtime Model

- Each user process runs as a Ring 3 execution context managed by kernel wrapper goroutine.
- TSS and CR3 are switched on goroutine resume hooks.
- User signal preemption is injected by kernel by rewriting return frame to registered SIGALRM handler.

## ABI Invariants

1. User wrappers and kernel syscall numbers must remain synchronized.
2. Any change to `SyscallFrame` register layout requires matching updates to wrapper expectations and kernel dispatcher.
3. Embedded ELF size must not exceed filesystem write cap at boot.
4. Signal handler ABI depends on exact sigFrame layout expected by `sys_sigreturn` path.

## Known Compatibility Risks

- Partial wrapper/constant drift between `user/gooos` and kernel syscall table can silently misroute syscalls.
- Signal handler non-compliance (missing sigreturn) leaves process in non-resumable signal-in-progress state.
- Any CR3/TSS hook regression can break user process isolation or syscall stack correctness.
