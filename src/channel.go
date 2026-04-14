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
