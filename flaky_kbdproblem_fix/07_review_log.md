# Review log

## Pass 1 (code-review subagent)

Status: completed

Findings:

1. Medium: command-path section for shell execution was overly simplified and internally inconsistent with parser/executor reuse.
2. Medium: startup phase transition criteria did not define exact `phaseOperational` predicate.
3. Medium: rollback section for autorun flow did not include cleanup verification details.
4. Medium: verification matrix did not explicitly validate foreground ownership restoration.
5. Low: naming consistency issues for dedicated probe gate and new test script.

Applied fixes:

1. Rewrote command-path section to include `parsePipeline -> executePipeline -> executeCmdLine -> runCommand -> Exec` and aligned it with autorun strategy.
2. Added explicit transition condition using `apSchedEnteredCount` with `bspBootDone`.
3. Added autorun cleanup semantics in shell design and rollback checklist.
4. Expanded Tier 0 checks to require foreground ownership verification and no shell-stdin EOF symptom.
5. Renamed examples to consistent `runSMPProbeShellTest` and `test_smp_shell_smpprobe.sh`.

## Pass 2 (post-fix re-review)

Status: completed

Findings:

1. No High/Medium issues remaining.
2. Optional Low: naming choice for the new probe gate can be adjusted to match existing `runSMPShellPreemptProbe` style if desired.

Resolution:

1. Plan accepted as implementation-ready with no blocking review findings.
