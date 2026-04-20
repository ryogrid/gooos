// user/cmd/sh/jobs.go — shell-local background-jobs table and the
// reap poll invoked between prompts (feature 2.4).
//
// Design: impldoc/shell_background_jobs.md §2.3, §2.5.
//
// Fixed 16 slots (half of kernel's maxRing3Procs=32 ring3 pool) so
// the foreground pipe + shell always have room. 17th concurrent &
// falls back to foreground execution.

package main

import (
	"strconv"

	"github.com/ryogrid/gooos/user/gooos"
)

const maxJobs = 16

// jobEntry records one background job. Populated by registerJob from
// the executor's background spawn path; cleared by reapBackgroundJobs
// when gooos.Waitpid reports reaped.
//
// A zero jobEntry is "free slot" — pid == 0 is the sentinel because
// the kernel never hands out PID 0 (nextPID starts at 1).
type jobEntry struct {
	id  int    // stable 1-indexed job id for the life of the job
	pid int    // kernel PID
	cmd string // display name (first stage's argv[0])
}

var (
	jobs      [maxJobs]jobEntry
	nextJobID = 1
)

// registerJob claims a free slot and returns the assigned job id,
// or -1 if the table is full.
func registerJob(pid int, cmd string) int {
	for i := range jobs {
		if jobs[i].pid == 0 {
			id := nextJobID
			nextJobID++
			jobs[i] = jobEntry{id: id, pid: pid, cmd: cmd}
			return id
		}
	}
	return -1
}

// reapBackgroundJobs polls every live job with Waitpid(WNOHANG) and
// prints a completion line for any that have exited. Called from the
// shell REPL before every prompt.
func reapBackgroundJobs() {
	for i := range jobs {
		je := &jobs[i]
		if je.pid == 0 {
			continue
		}
		status, reaped := gooos.Waitpid(je.pid, gooos.WNOHANG)
		if !reaped {
			continue
		}
		gooos.Println("[" + strconv.Itoa(je.id) + "] " +
			strconv.Itoa(je.pid) + " done exit=" +
			strconv.Itoa(status) + " " + je.cmd)
		*je = jobEntry{}
	}
}
