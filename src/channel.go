// src/channel.go — Bounded channel with ring buffer for kernel IPC.
//
// Provides a Channel struct with a fixed-capacity ring buffer of uintptr slots,
// sender and receiver WaitQueues for blocking operations, and a static pool
// to avoid heap allocation. chanCreate returns a pointer into the pool.

package main

// Maximum capacity for a single channel's ring buffer.
const chanMaxSlots = 256

// Maximum number of channels that can exist simultaneously.
const chanPoolSize = 32

// Channel is a bounded ring buffer with associated wait queues for
// blocking send and receive operations.
type Channel struct {
	buf      [chanMaxSlots]uintptr // ring buffer slots
	readIdx  int                   // next slot to read from
	writeIdx int                   // next slot to write to
	count    int                   // number of items currently in the buffer
	capacity int                   // maximum items (0 = unbuffered)
	senderWQ   WaitQueue           // tasks blocked on send (buffer full)
	receiverWQ WaitQueue           // tasks blocked on recv (buffer empty)
	used     bool                  // true if this pool slot is allocated
}

// chanPool is a statically allocated pool of channels.
var (
	chanPool      [chanPoolSize]Channel
	chanPoolCount int
)

// chanCreate allocates a channel from the static pool with the given capacity.
// capacity=0 means unbuffered (rendezvous). Returns nil if pool is exhausted.
func chanCreate(capacity int) *Channel {
	if chanPoolCount >= chanPoolSize {
		return nil
	}
	if capacity > chanMaxSlots {
		capacity = chanMaxSlots
	}
	idx := chanPoolCount
	chanPoolCount++
	ch := &chanPool[idx]
	ch.readIdx = 0
	ch.writeIdx = 0
	ch.count = 0
	ch.capacity = capacity
	ch.senderWQ.count = 0
	ch.receiverWQ.count = 0
	ch.used = true
	return ch
}

// chanSend sends val on ch, blocking until space is available.
// For unbuffered channels (capacity=0), blocks until a receiver is waiting.
func chanSend(ch *Channel, val uintptr) {
	if ch.capacity == 0 {
		// Unbuffered (rendezvous): store value, then wait for a receiver
		// to pick it up. If a receiver is already waiting, wake it and
		// hand off the value directly.
		ch.buf[0] = val
		ch.count = 1
		if ch.receiverWQ.count > 0 {
			// Receiver already waiting — wake it to grab the value.
			waitQueueWakeOne(&ch.receiverWQ)
		} else {
			// No receiver yet — block sender until one arrives.
			waitQueueSleep(&ch.senderWQ)
		}
		return
	}

	// Buffered: block while buffer is full.
	for ch.count >= ch.capacity {
		waitQueueSleep(&ch.senderWQ)
	}

	// Write value into ring buffer.
	ch.buf[ch.writeIdx] = val
	ch.writeIdx = (ch.writeIdx + 1) % ch.capacity
	ch.count++

	// Wake one blocked receiver, if any.
	waitQueueWakeOne(&ch.receiverWQ)
}

// chanRecv receives a value from ch, blocking until data is available.
// For unbuffered channels (capacity=0), blocks until a sender is waiting.
func chanRecv(ch *Channel) uintptr {
	if ch.capacity == 0 {
		// Unbuffered (rendezvous): if a sender already deposited a value,
		// grab it. Otherwise block until a sender arrives.
		if ch.count == 0 {
			// No sender yet — wake a blocked sender if one is waiting
			// (it will deposit the value), or block until one arrives.
			if ch.senderWQ.count > 0 {
				waitQueueWakeOne(&ch.senderWQ)
			}
			// Block until sender deposits a value.
			for ch.count == 0 {
				waitQueueSleep(&ch.receiverWQ)
			}
		}
		val := ch.buf[0]
		ch.count = 0
		// Wake sender if it's still blocked (sender blocks if no receiver was waiting).
		if ch.senderWQ.count > 0 {
			waitQueueWakeOne(&ch.senderWQ)
		}
		return val
	}

	// Buffered: block while buffer is empty.
	for ch.count == 0 {
		waitQueueSleep(&ch.receiverWQ)
	}

	// Read value from ring buffer.
	val := ch.buf[ch.readIdx]
	ch.readIdx = (ch.readIdx + 1) % ch.capacity
	ch.count--

	// Wake one blocked sender, if any.
	waitQueueWakeOne(&ch.senderWQ)

	return val
}

// chanTrySend attempts a non-blocking send on ch. Returns false if the
// buffer is full. Safe to call from interrupt context.
func chanTrySend(ch *Channel, val uintptr) bool {
	if ch.capacity == 0 {
		// Unbuffered: only succeeds if a receiver is already waiting.
		if ch.receiverWQ.count == 0 {
			return false
		}
		ch.buf[0] = val
		ch.count = 1
		waitQueueWakeOne(&ch.receiverWQ)
		return true
	}

	if ch.count >= ch.capacity {
		return false
	}

	ch.buf[ch.writeIdx] = val
	ch.writeIdx = (ch.writeIdx + 1) % ch.capacity
	ch.count++

	waitQueueWakeOne(&ch.receiverWQ)
	return true
}

// ---------- Select multiplexer ----------

// Direction constants for SelectCase.
const (
	selectSend = 0
	selectRecv = 1
)

// Maximum number of cases in a selectWait call.
const selectMaxCases = 8

// SelectCase describes one case in a selectWait call.
type SelectCase struct {
	ch  *Channel
	dir int     // selectSend or selectRecv
	val uintptr // value to send (only for selectSend cases)
}

// chanRecvReady returns true if a receive on ch would succeed without blocking.
func chanRecvReady(ch *Channel) bool {
	return ch.count > 0
}

// chanSendReady returns true if a send on ch would succeed without blocking.
func chanSendReady(ch *Channel) bool {
	if ch.capacity == 0 {
		return ch.receiverWQ.count > 0
	}
	return ch.count < ch.capacity
}

// chanRecvDirect performs a non-blocking receive, assuming the channel is ready.
// Returns the received value. Wakes one blocked sender if any.
func chanRecvDirect(ch *Channel) uintptr {
	if ch.capacity == 0 {
		val := ch.buf[0]
		ch.count = 0
		if ch.senderWQ.count > 0 {
			waitQueueWakeOne(&ch.senderWQ)
		}
		return val
	}
	val := ch.buf[ch.readIdx]
	ch.readIdx = (ch.readIdx + 1) % ch.capacity
	ch.count--
	waitQueueWakeOne(&ch.senderWQ)
	return val
}

// chanSendDirect performs a non-blocking send, assuming the channel is ready.
// Wakes one blocked receiver if any.
func chanSendDirect(ch *Channel, val uintptr) {
	if ch.capacity == 0 {
		ch.buf[0] = val
		ch.count = 1
		if ch.receiverWQ.count > 0 {
			waitQueueWakeOne(&ch.receiverWQ)
		}
		return
	}
	ch.buf[ch.writeIdx] = val
	ch.writeIdx = (ch.writeIdx + 1) % ch.capacity
	ch.count++
	waitQueueWakeOne(&ch.receiverWQ)
}

// selectWait blocks until one of the given cases is ready, executes it,
// and returns the case index and received value (for recv cases; 0 for send).
// If multiple cases are ready, the first in array order is chosen.
// The task is removed from all wait queues upon wakeup to prevent double-wake.
func selectWait(cases *[selectMaxCases]SelectCase, n int) (int, uintptr) {
	for {
		// Phase 1: Check for immediate readiness (first-ready wins).
		for i := 0; i < n; i++ {
			if cases[i].dir == selectRecv {
				if chanRecvReady(cases[i].ch) {
					return i, chanRecvDirect(cases[i].ch)
				}
			} else {
				if chanSendReady(cases[i].ch) {
					chanSendDirect(cases[i].ch, cases[i].val)
					return i, 0
				}
			}
		}

		// Phase 2: No case ready — register on all relevant wait queues.
		tid := currentTask
		for i := 0; i < n; i++ {
			if cases[i].dir == selectRecv {
				waitQueueAppend(&cases[i].ch.receiverWQ, tid)
			} else {
				waitQueueAppend(&cases[i].ch.senderWQ, tid)
			}
		}
		tasks[tid].State = taskBlocked
		schedule()

		// Phase 3: Woke up — remove from ALL wait queues atomically.
		for i := 0; i < n; i++ {
			if cases[i].dir == selectRecv {
				waitQueueRemove(&cases[i].ch.receiverWQ, tid)
			} else {
				waitQueueRemove(&cases[i].ch.senderWQ, tid)
			}
		}
		// Loop back to Phase 1 to find and execute the ready case.
		// This also handles spurious wakeups gracefully.
	}
}

// ---------- Channel test tasks ----------

// Global channels for test tasks, initialized in main before spawning.
var (
	testBufCh   *Channel // buffered channel (capacity 4) for producer/consumer test
	testRendCh  *Channel // unbuffered channel for rendezvous test
)

// chanProducerTaskAddr returns the address of chanProducerTask. Implemented in switch.S.
//
//go:linkname chanProducerTaskAddr chanProducerTaskAddr
func chanProducerTaskAddr() uintptr

// chanConsumerTaskAddr returns the address of chanConsumerTask. Implemented in switch.S.
//
//go:linkname chanConsumerTaskAddr chanConsumerTaskAddr
func chanConsumerTaskAddr() uintptr

// chanRendezvousAAddr returns the address of chanRendezvousA. Implemented in switch.S.
//
//go:linkname chanRendezvousAAddr chanRendezvousAAddr
func chanRendezvousAAddr() uintptr

// chanRendezvousBAddr returns the address of chanRendezvousB. Implemented in switch.S.
//
//go:linkname chanRendezvousBAddr chanRendezvousBAddr
func chanRendezvousBAddr() uintptr

// chanProducerTask sends 10 values (0-9) on testBufCh.
//
//export chanProducerTask
func chanProducerTask() {
	sti()
	serialPrintln("Chan: producer started")
	for i := uintptr(0); i < 10; i++ {
		chanSend(testBufCh, i)
		serialPrintln("Chan: sent " + utoa(uint64(i)))
	}
	serialPrintln("Chan: producer done")
	vgaWriteLine(20, "Chan: producer sent 0-9")
	// Block forever after finishing.
	for {
		taskSleep(10000)
	}
}

// chanConsumerTask receives 10 values from testBufCh and verifies order.
//
//export chanConsumerTask
func chanConsumerTask() {
	sti()
	serialPrintln("Chan: consumer started")
	ok := true
	for i := uintptr(0); i < 10; i++ {
		val := chanRecv(testBufCh)
		serialPrintln("Chan: recv " + utoa(uint64(val)))
		if val != i {
			ok = false
		}
	}
	if ok {
		serialPrintln("Chan: buffered test PASS — all 10 values correct")
		vgaWriteLine(21, "Chan: buffered PASS")
	} else {
		serialPrintln("Chan: buffered test FAIL — value mismatch")
		vgaWriteLine(21, "Chan: buffered FAIL")
	}
	for {
		taskSleep(10000)
	}
}

// chanRendezvousA sends a value on the unbuffered channel.
//
//export chanRendezvousA
func chanRendezvousA() {
	sti()
	serialPrintln("Chan: rendezvous sender started")
	// Small delay so the receiver has time to block first.
	taskSleep(10)
	chanSend(testRendCh, 0xBEEF)
	serialPrintln("Chan: rendezvous sender done (sent 0xBEEF)")
	vgaWriteLine(22, "Chan: rendezvous sent")
	for {
		taskSleep(10000)
	}
}

// chanRendezvousB receives a value from the unbuffered channel.
//
//export chanRendezvousB
func chanRendezvousB() {
	sti()
	serialPrintln("Chan: rendezvous receiver started")
	val := chanRecv(testRendCh)
	if val == 0xBEEF {
		serialPrintln("Chan: rendezvous PASS — received 0xBEEF")
		vgaWriteLine(23, "Chan: rendezvous PASS")
	} else {
		serialPrintln("Chan: rendezvous FAIL — got " + utoa(uint64(val)))
		vgaWriteLine(23, "Chan: rendezvous FAIL")
	}
	for {
		taskSleep(10000)
	}
}

// ---------- Select test tasks ----------

// Global channels for the select test, initialized in main before spawning.
var (
	selectCh1 *Channel // buffered channel for select case 0
	selectCh2 *Channel // buffered channel for select case 1
)

// selectTestTaskAddr returns the address of selectTestTask. Implemented in switch.S.
//
//go:linkname selectTestTaskAddr selectTestTaskAddr
func selectTestTaskAddr() uintptr

// selectProducerAAddr returns the address of selectProducerA. Implemented in switch.S.
//
//go:linkname selectProducerAAddr selectProducerAAddr
func selectProducerAAddr() uintptr

// selectProducerBAddr returns the address of selectProducerB. Implemented in switch.S.
//
//go:linkname selectProducerBAddr selectProducerBAddr
func selectProducerBAddr() uintptr

// selectTestTask selects on two channels and verifies correct dispatch.
//
//export selectTestTask
func selectTestTask() {
	sti()
	serialPrintln("Select: test task started")

	var cases [selectMaxCases]SelectCase
	cases[0] = SelectCase{ch: selectCh1, dir: selectRecv}
	cases[1] = SelectCase{ch: selectCh2, dir: selectRecv}

	ok := true

	// First select: producer A sends to ch1 after 50 ticks — expect case 0.
	idx, val := selectWait(&cases, 2)
	serialPrintln("Select: case " + utoa(uint64(idx)) + " val=" + utoa(uint64(val)))
	if idx != 0 || val != 0xAAAA {
		ok = false
	}

	// Second select: producer B sends to ch2 after 100 ticks — expect case 1.
	idx, val = selectWait(&cases, 2)
	serialPrintln("Select: case " + utoa(uint64(idx)) + " val=" + utoa(uint64(val)))
	if idx != 1 || val != 0xBBBB {
		ok = false
	}

	if ok {
		serialPrintln("Select: PASS — correct dispatch to right case index")
		vgaWriteLine(24, "Select: PASS")
	} else {
		serialPrintln("Select: FAIL — wrong dispatch")
		vgaWriteLine(24, "Select: FAIL")
	}
	for {
		taskSleep(10000)
	}
}

// selectProducerA sends 0xAAAA to selectCh1 after a delay.
//
//export selectProducerA
func selectProducerA() {
	sti()
	taskSleep(50)
	chanSend(selectCh1, 0xAAAA)
	serialPrintln("Select: producer A sent 0xAAAA to ch1")
	for {
		taskSleep(10000)
	}
}

// selectProducerB sends 0xBBBB to selectCh2 after a longer delay.
//
//export selectProducerB
func selectProducerB() {
	sti()
	taskSleep(100)
	chanSend(selectCh2, 0xBBBB)
	serialPrintln("Select: producer B sent 0xBBBB to ch2")
	for {
		taskSleep(10000)
	}
}
