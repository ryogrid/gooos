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
