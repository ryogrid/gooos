# Lessons learned

- Validate shell liveness with prompt/input readiness (`$` + keyboard path), not only by reaching `ring3Wrapper: jumping to Ring 3` or printing the shell banner.
