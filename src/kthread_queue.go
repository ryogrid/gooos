// src/kthread_queue.go -- bounded MPSC queue for *fsRequest.
//
// Route C M2: ships a type-specific variant to carry fs requests
// from caller kernel contexts into fsTask. M3 generalises to a
// polymorphic KQueue[T] when pipe / UDP / TCP callers need it; for
// M2 scope the type-specific version keeps the code simple and
// avoids generics-in-ISR-adjacent-paths concerns.
//
// Semantics:
//   - Push blocks when full; wakes the parked consumer (fsTask).
//   - Pop blocks when empty; wakes one parked producer on resume.
//   - count / cap are under lock; memory ordering via the spinlock.
//
// Lock-ordering: kqLock (instance field) sits at rank 13 per §03
// — higher than every pre-Route-C rank. Drop before kschedWake.

package main

// fsReqQueueCap is the bounded ring size for fsReqQ. Matches the
// old `make(chan *fsRequest, 8)` capacity.
const fsReqQueueCap = 8

// fsReqQueue is a bounded MPSC queue of *fsRequest. Consumer is
// fsTask (§06 service #9); producers are every fs*Send caller.
type fsReqQueue struct {
	lock      Spinlock
	ring      [fsReqQueueCap]*fsRequest
	head      uint32 // next slot to pop
	tail      uint32 // next slot to push
	count     uint32 // in-flight entries (0..cap)
	// Waiter lists (intrusive via KernelThread.WakeLink). Producers
	// park when full; the lone consumer (fsTask) parks when empty.
	producers *KernelThread
	consumer  *KernelThread
}

// Push enqueues v, blocking if the ring is full. Wakes the
// consumer if one is parked.
func (q *fsReqQueue) Push(v *fsRequest) {
	for {
		flags := q.lock.Acquire()
		if q.count < fsReqQueueCap {
			q.ring[q.tail] = v
			q.tail = (q.tail + 1) % fsReqQueueCap
			q.count++
			cons := q.consumer
			q.consumer = nil
			q.lock.Release(flags)
			if cons != nil {
				cons.WakeLink = nil
				cons.ParkLock = nil
				kschedWake(cons)
			}
			return
		}
		// Full: park as a producer.
		cpu := cpuID()
		me := kschedRunning[cpu]
		if me == nil {
			// Not on a kernel thread — pump the scheduler so the
			// consumer (fsTask) can drain before we retry. Required
			// for -smp 1 boots where there is no peer to steal.
			q.lock.Release(flags)
			for q.count >= fsReqQueueCap {
				kschedLoopOnce()
				gooosPause()
			}
			continue
		}
		me.WakeLink = q.producers
		q.producers = me
		me.State = uint32(KStateParked)
		me.ParkLock = &q.lock
		q.lock.Release(flags)
		kschedSwitch(&kschedBootstrap[cpu], me)
		// Resumed (possibly on a different CPU). Re-install
		// CR3+TSS for Ring-3-hosting kthreads (M4.1.b).
		kthreadResumeRing3Ctx()
		// Loop and re-check on resume.
	}
}

// Pop dequeues the next request, blocking if empty. Wakes one
// producer on resume if any are parked waiting for space.
func (q *fsReqQueue) Pop() *fsRequest {
	for {
		flags := q.lock.Acquire()
		if q.count > 0 {
			v := q.ring[q.head]
			q.ring[q.head] = nil
			q.head = (q.head + 1) % fsReqQueueCap
			q.count--
			prod := q.producers
			// Wake one producer (fair-ish; fsTask is the sole
			// consumer so contention stays bounded).
			if prod != nil {
				q.producers = prod.WakeLink
				prod.WakeLink = nil
				prod.ParkLock = nil
			}
			q.lock.Release(flags)
			if prod != nil {
				kschedWake(prod)
			}
			return v
		}
		// Empty: park as the consumer.
		cpu := cpuID()
		me := kschedRunning[cpu]
		if me == nil {
			// Not on a kernel thread — spin; should never happen
			// for fsTask which is always kschedSpawn'd.
			q.lock.Release(flags)
			for q.count == 0 {
				gooosPause()
			}
			continue
		}
		if q.consumer != nil {
			// MPSC design says one consumer. If a second one
			// appears, serialise: the later one parks behind the
			// first via producers list — cheapest way to reuse the
			// machinery. fsTask is singleton in practice.
			me.WakeLink = q.producers
			q.producers = me
		} else {
			me.WakeLink = nil
			q.consumer = me
		}
		me.State = uint32(KStateParked)
		me.ParkLock = &q.lock
		q.lock.Release(flags)
		kschedSwitch(&kschedBootstrap[cpu], me)
		// Resumed (possibly on a different CPU). Re-install
		// CR3+TSS for Ring-3-hosting kthreads (M4.1.b). For
		// fsTask (the singleton consumer) this is a no-op.
		kthreadResumeRing3Ctx()
	}
}

// Len returns the current count. Racey; for diagnostics only.
//
//go:nosplit
func (q *fsReqQueue) Len() uint32 {
	return q.count
}

// ---- udpDgramQueue ----
//
// Route C M4.2.b: bounded MPSC queue of UDPDatagram values for
// the migrated UDP path. Replaces `chan UDPDatagram` in
// UDPBinding.Q (consumed by the kthread-hosted udpEchoServer)
// AND in socketFd.recvQ (consumed by sys_recvfrom + Read on
// kthread-hosted Ring-3 processes).
//
// Differences from fsReqQueue:
//   - Value type: UDPDatagram (struct) instead of *fsRequest.
//   - Adds non-blocking TryPush + TryPop variants — needed by
//     the RX-path producer (udpHandle, called from netRxLoop;
//     drop-on-full mirrors the existing `select { ch <- dg:
//     default: drop }` semantics) and by sys_recvfrom's timeout
//     bounded-poll path.
//
// Lock ordering: same rank 13 as fsReqQueue. Drop before
// kschedWake.

const udpDgramQueueCap = 16

type udpDgramQueue struct {
	lock      Spinlock
	ring      [udpDgramQueueCap]UDPDatagram
	head      uint32
	tail      uint32
	count     uint32
	producers *KernelThread
	consumer  *KernelThread
}

// newUdpDgramQueue allocates a zero-valued queue (zero is valid;
// no Init needed).
func newUdpDgramQueue() *udpDgramQueue {
	return &udpDgramQueue{}
}

// Push enqueues v, blocking if the ring is full. Wakes one
// parked consumer.
func (q *udpDgramQueue) Push(v UDPDatagram) {
	for {
		flags := q.lock.Acquire()
		if q.count < udpDgramQueueCap {
			q.ring[q.tail] = v
			q.tail = (q.tail + 1) % udpDgramQueueCap
			q.count++
			cons := q.consumer
			q.consumer = nil
			q.lock.Release(flags)
			if cons != nil {
				cons.WakeLink = nil
				cons.ParkLock = nil
				kschedWake(cons)
			}
			return
		}
		// Full: park as a producer.
		cpu := cpuID()
		me := kschedRunning[cpu]
		if me == nil {
			q.lock.Release(flags)
			for q.count >= udpDgramQueueCap {
				kschedLoopOnce()
				gooosPause()
			}
			continue
		}
		me.WakeLink = q.producers
		q.producers = me
		me.State = uint32(KStateParked)
		me.ParkLock = &q.lock
		q.lock.Release(flags)
		kschedSwitch(&kschedBootstrap[cpu], me)
		kthreadResumeRing3Ctx()
	}
}

// TryPush attempts to enqueue v without blocking. Returns false
// if the ring is full (drop-on-full semantics — mirrors the old
// `select { ch <- dg: default: drop }` in udpHandle). Wakes one
// parked consumer on successful push.
//
//go:nosplit
func (q *udpDgramQueue) TryPush(v UDPDatagram) bool {
	flags := q.lock.Acquire()
	if q.count >= udpDgramQueueCap {
		q.lock.Release(flags)
		return false
	}
	q.ring[q.tail] = v
	q.tail = (q.tail + 1) % udpDgramQueueCap
	q.count++
	cons := q.consumer
	q.consumer = nil
	q.lock.Release(flags)
	if cons != nil {
		cons.WakeLink = nil
		cons.ParkLock = nil
		kschedWake(cons)
	}
	return true
}

// Pop dequeues, blocking if empty. Wakes one parked producer
// on resume.
func (q *udpDgramQueue) Pop() UDPDatagram {
	for {
		flags := q.lock.Acquire()
		if q.count > 0 {
			v := q.ring[q.head]
			q.ring[q.head] = UDPDatagram{}
			q.head = (q.head + 1) % udpDgramQueueCap
			q.count--
			prod := q.producers
			if prod != nil {
				q.producers = prod.WakeLink
				prod.WakeLink = nil
				prod.ParkLock = nil
			}
			q.lock.Release(flags)
			if prod != nil {
				kschedWake(prod)
			}
			return v
		}
		// Empty: park as the consumer.
		cpu := cpuID()
		me := kschedRunning[cpu]
		if me == nil {
			q.lock.Release(flags)
			for q.count == 0 {
				gooosPause()
			}
			continue
		}
		if q.consumer != nil {
			me.WakeLink = q.producers
			q.producers = me
		} else {
			me.WakeLink = nil
			q.consumer = me
		}
		me.State = uint32(KStateParked)
		me.ParkLock = &q.lock
		q.lock.Release(flags)
		kschedSwitch(&kschedBootstrap[cpu], me)
		kthreadResumeRing3Ctx()
	}
}

// TryPop attempts to dequeue without blocking. Returns
// (zero, false) on empty. Used by sys_recvfrom timeout
// bounded-poll and by socketFd.Close drain. Wakes one
// parked producer on successful pop.
//
//go:nosplit
func (q *udpDgramQueue) TryPop() (UDPDatagram, bool) {
	var zero UDPDatagram
	flags := q.lock.Acquire()
	if q.count == 0 {
		q.lock.Release(flags)
		return zero, false
	}
	v := q.ring[q.head]
	q.ring[q.head] = UDPDatagram{}
	q.head = (q.head + 1) % udpDgramQueueCap
	q.count--
	prod := q.producers
	if prod != nil {
		q.producers = prod.WakeLink
		prod.WakeLink = nil
		prod.ParkLock = nil
	}
	q.lock.Release(flags)
	if prod != nil {
		kschedWake(prod)
	}
	return v, true
}

// Len returns the current count. Racey; diagnostics only.
//
//go:nosplit
func (q *udpDgramQueue) Len() uint32 {
	return q.count
}
