## Workflow Orchestration

### 1. Plan Mode Default
- Enter plan mode for ANY non-trivial task (3+ steps or architectural decisions)
- If something goes sideways, STOP and re-plan immediately - don't keep pushing
- Use plan mode for verification steps, not just building
- Write detailed specs upfront to reduce ambiguity

### 2. Subagent Strategy
- Use subagents liberally to keep main context window clean
- Offload research, exploration, and parallel analysis to subagents
- For complex problems, throw more compute at it via subagents
- One tack per subagent for focused execution

### 3. Self-Improvement Loop
- After ANY correction from the user: update `tasks/lessons.md` with the pattern
- Write rules for yourself that prevent the same mistake
- Ruthlessly iterate on these lessons until mistake rate drops
- Review lessons at session start for relevant project

### 4. Verification Before Done
- Never mark a task complete without proving it works
- Diff behavior between main and your changes when relevant
- Ask yourself: "Would a staff engineer approve this?"
- Run tests, check logs, demonstrate correctness

### 5. Demand Elegance (Balanced)
- For non-trivial changes: pause and ask "is there a more elegant way?"
- If a fix feels hacky: "Knowing everything I know now, implement the elegant solution"
- Skip this for simple, obvious fixes - don't over-engineer
- Challenge your own work before presenting it

### 6. Autonomous Bug Fixing
- When given a bug report: just fix it. Don't ask for hand-holding
- Point at logs, errors, failing tests - then resolve them
- Zero context switching required from the user
- Go fix failing CI tests without being told how

## Task Management

1. **Plan First**: Write plan to `tasks/TODO.md` with checkable items
2. **Verify Plan**: Check in before starting implementation
3. **Track Progress**: Mark items complete as you go
4. **Explain Changes**: High-level summary at each step
5. **Document Results**: Add review section to `tasks/TODO.md`
6. **Capture Lessons**: Update `tasks/lessons.md` after corrections

## Core Principles

- **Simplicity First**: Make every change as simple as possible. Impact minimal code.
- **No Laziness**: Find root causes. No temporary fixes. Senior developer standards.
- **Minimal Impact**: Changes should only touch what's necessary. Avoid introducing bugs.
- **No Band-Aid Fixes**: Before applying a local fix, verify it won't break other parts of the system. Check how TiKV handles the same situation (search `tikv/` source). If a fix feels like a workaround, find the architecturally correct solution first. A fix that solves one problem but makes another design unsound is worse than no fix.

## Shell
- **Commands**: Do not use compound commands (e.g., pipes, `&&`, `;`, subshells). Run each command as a separate Bash invocation.

## Temporary Files
- Use `tmp` directory instead of `/tmp` for any temporary files during development or testing

## Git
- ** Don't merge to master/main without order from the user. **

## Background Task Monitoring Reliability

You often fail to detect when background tasks have terminated, even when you believe you are actively monitoring them. Implement explicit countermeasures:

- **Verify status definitively.** Confirm task state via an explicit check (e.g., `BashOutput` with exit code confirmation) rather than relying on assumed state.
- **Re-poll at regular intervals.** Set checkpoints to re-check task status periodically instead of assuming a single observation is sufficient.
- **Confirm termination before proceeding.** Do not advance to dependent steps until you have positively verified that the prerequisite task has exited.

## Periodic Cleanup of Stale Background Processes

At natural breakpoints during your work (e.g., after completing a subtask, before starting a new phase), audit all background tasks and monitors currently managed by Claude Code:

- Identify any that are no longer needed.
- Terminate them explicitly.
- Do not let orphaned processes accumulate across the session.
