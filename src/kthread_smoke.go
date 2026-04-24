// src/kthread_smoke.go -- M0 kernel-thread smoke test.
//
// Spawned from main() when `runKthreadSmoke` is true. Creates two
// trivial kernel threads that round-robin print 'A' and 'B' via
// kschedYield, then kschedExit; the smoke harness observes the
// interleaved output and the trailing "SMOKE: OK" banner.
//
// Deleted in M1 once the migrated probes exercise the scheduler
// through real services.

package main

// kschedSmokeThreadsActive tracks how many smoke threads remain
// running; the last one to exit flips kschedSmokeAllDone so
// kschedLoop returns back to its caller.
var kschedSmokeThreadsActive uint32

// kschedSmokeBodyA + kschedSmokeBodyB are the thread bodies. Each
// emits a distinctive character 5 times, yielding in between.
//
//go:nosplit
func kschedSmokeBodyA() {
	for i := 0; i < 5; i++ {
		serialPutChar('A')
		kschedYield()
	}
	kschedSmokeThreadDone()
}

//go:nosplit
func kschedSmokeBodyB() {
	for i := 0; i < 5; i++ {
		serialPutChar('B')
		kschedYield()
	}
	kschedSmokeThreadDone()
}

// kschedSmokeThreadDone decrements the active counter under the
// pool lock (convenient mutex; not a lock-order violation because
// kthreadPoolLock is the highest unranked lock in the Route C
// design and nothing nested inside takes another lock).
//
//go:nosplit
func kschedSmokeThreadDone() {
	flags := kthreadPoolLock.Acquire()
	kschedSmokeThreadsActive--
	if kschedSmokeThreadsActive == 0 {
		kschedSmokeAllDone = 1
	}
	kthreadPoolLock.Release(flags)
}

// kschedSmokeRun is the entry point called from main(). Initialises
// the scheduler, spawns the two bodies, enters kschedLoop briefly,
// and prints the success banner.
func kschedSmokeRun() {
	serialPrintln("SMOKE: kthread smoke test starting")
	kschedInit()
	kschedSmokeThreadsActive = 2
	kschedSmokeAllDone = 0
	kschedSpawn("smokeA", kschedSmokeBodyA)
	kschedSpawn("smokeB", kschedSmokeBodyB)
	// Drive the scheduler from this context. Returns when both
	// smoke threads have called kschedExit.
	kschedLoop()
	serialPrintln("")
	serialPrintln("SMOKE: OK")
}
