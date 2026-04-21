# Design: `smpprobe` correctness and shell continuity

## Goals

1. Validate the real shell command path for `smpprobe` deterministically under SMP.
2. Prove shell remains interactive after `smpprobe` exits.
3. Avoid relying on SMP-sendkey as the primary gate.

## Command-path model (current)

`sh` external command execution path:

1. `user/cmd/sh/main.go`: `parsePipeline(line)` -> `executePipeline(p, background)`.
2. For a single-stage external command: `executeCmdLine` -> `runCommand` -> `gooos.Exec(filename, args)`.
3. `src/userspace.go`: `sysExecHandler` -> `elfExec(filename, args, parent)`.
4. `src/process.go`: `elfExec` -> `elfSpawn` + `processWait`.
5. `processWait` transfers foreground to child while waiting, then restores parent foreground.

`smpprobe` path:

1. `user/cmd/smpprobe/main.go` spawns N workers via `Spawn("smpprobe.elf", arg)` and waits each via `Wait(pid)`.
2. Completion (`smpprobe: done`) should return control to shell prompt.

## Deterministic shell execution strategy

Add a **shell autorun file path** for tests:

1. Kernel test gate writes an autorun file in in-memory FS before shell launch (for probe builds only).
2. Shell startup checks for this file once, executes each line using existing parser/executor, then continues normal interactive prompt loop.
3. Autorun file is deleted/ignored after one-time execution.

Why this design:

1. Exercises real shell parser + exec + wait path.
2. Removes keyboard/sendkey timing confounder from the primary SMP gate.
3. Keeps normal user behavior unchanged when no autorun file is present.

Suggested touch points:

1. `src/main.go`
   1. probe-gated creation of autorun script (for example: `smpprobe` then `echo POST_SMPPROBE_OK`),
   2. no change in normal boot when gate is false.
2. `user/cmd/sh/main.go`
   1. add `runAutorunIfPresent()` called once before prompt loop,
   2. execute lines via existing `parsePipeline` + `executePipeline`,
   3. delete autorun file after execution finishes (success or error),
   4. print explicit marker on autorun completion.
3. `src/preempt_config.go`
   1. add dedicated test gate constant for this flow (for example `runSMPProbeShellTest`; avoid reusing unrelated probes).
4. safety behavior
   1. if autorun file exists while gate is off, shell startup deletes it and logs a warning marker.

## Shell-liveness invariants to enforce

1. After synchronous external command (`Exec`) returns, shell regains foreground ownership.
2. `consoleStdin.Read` for shell returns actual keyboard data (not EOF) post-command.
3. No stranded foreground owner after child exit.

Potential hardening (if needed):

1. In `processWait`, explicitly restore foreground to `parent` (wait caller) rather than generic previous snapshot semantics.
2. Add one-shot diagnostic marker for foreground owner before/after wait in probe mode.

## `smpprobe` pass semantics

Minimum expected signals for success:

1. `smpprobe: spawning ...` observed.
2. At least one worker `worker-N: cpuID=M` observed.
3. `smpprobe: done` observed.
4. Post-command shell marker observed (for example `POST_SMPPROBE_OK`) and prompt continues.
