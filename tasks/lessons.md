# Lessons learned

- Validate shell liveness with prompt/input readiness (`$` + keyboard path), not only by reaching `ring3Wrapper: jumping to Ring 3` or printing the shell banner.
- Treat `current_impl_0421_night/` as the authoritative implementation reference; do not use `current_impl_doc/` for current-state decisions.
- For timing-sensitive SMP/input bugs, avoid embedding repro probes directly in kernel/userspace code when the timing itself is under suspicion; prefer external harnesses with human-scale delays.
