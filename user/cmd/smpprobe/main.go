// smpprobe — demonstrates SMP multi-core scheduling on gooos.
//
// When invoked without arguments (parent mode), spawns 4 worker
// child processes via sys_spawn. Each worker loops, printing its
// worker ID and current CPU core (via sys_getcpuid). Under
// `make run-smp` (-smp 4), workers are distributed across
// multiple cores by the scheduler's work-stealing mechanism.
//
// Usage from the gooos shell:
//
//	$ smpprobe
//
// Expected output (SMP):
//
//	smpprobe: spawning 4 workers...
//	worker-0: cpuID=0
//	worker-1: cpuID=2
//	worker-2: cpuID=1
//	worker-3: cpuID=3
//	...
//	smpprobe: done

package main

import (
	"strconv"

	"github.com/ryogrid/gooos/user/gooos"
)

const numWorkers = 4
const iterationsPerWorker = 3

func main() {
	args := gooos.Args()

	// Worker mode: args starts with "worker-N"
	if len(args) >= 8 && args[:7] == "worker-" {
		workerID := args[7:]
		for i := 0; i < iterationsPerWorker; i++ {
			cpu := gooos.GetCpuID()
			gooos.Println("worker-" + workerID + ": cpuID=" + strconv.Itoa(cpu))
			// Sleep to allow scheduler work-stealing to migrate this worker
			// to other CPU cores. 10ms is enough to trigger a reschedule
			// while keeping output compact.
			gooos.Sleep(10)
		}
		gooos.Exit(0)
		return
	}

	// Parent mode: spawn workers and wait
	gooos.Println("smpprobe: spawning " + strconv.Itoa(numWorkers) + " workers...")
	gooos.Println("smpprobe: parent cpuID=" + strconv.Itoa(gooos.GetCpuID()))

	pids := make([]int, numWorkers)
	for i := 0; i < numWorkers; i++ {
		arg := "worker-" + strconv.Itoa(i)
		pid, errno := gooos.Spawn("smpprobe.elf", arg)
		if pid < 0 {
			gooos.Println("smpprobe: spawn failed, errno=" + strconv.Itoa(errno))
			gooos.Exit(1)
			return
		}
		pids[i] = pid
	}

	// Wait for all workers to finish
	for i := 0; i < numWorkers; i++ {
		gooos.Wait(pids[i])
	}

	gooos.Println("smpprobe: done")
}
