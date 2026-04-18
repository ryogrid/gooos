// src/netstats.go -- Network stack statistics counters.
//
// One struct holds every counter the net stack maintains. Writers
// typically come from the RX dispatch goroutine (single producer on
// BSP-only scheduling today) and from arbitrary goroutines doing TX.
// statsInc / statsAdd take the spinlock for SMP safety. Snapshot
// reads copy the whole struct under the same lock.

package main

// NetStats aggregates the counters exposed by netDiag.
type NetStats struct {
	// Link layer
	TxPackets, TxBytes            uint64
	RxPackets, RxBytes            uint64
	RxDropped, RxUnknownEtherType uint64

	// ARP
	ArpHits, ArpMisses            uint64
	ArpRepliesSent, ArpRequestsSent uint64

	// IPv4
	ChecksumErr      uint64
	FragmentsDropped uint64

	// ICMP
	IcmpEcho uint64

	// UDP
	UdpRecv, UdpSend uint64
	UdpPortUnreach   uint64

	// Buffer pool
	BufAllocFail uint64
}

var (
	netStats  NetStats
	statsLock Spinlock // lock-ordering rank 8 (innermost)
)

// statsInc atomically bumps `counter` by 1 under statsLock.
//
//go:nosplit
func statsInc(counter *uint64) {
	flags := statsLock.Acquire()
	*counter++
	statsLock.Release(flags)
}

// statsAdd atomically adds `n` to `counter`.
//
//go:nosplit
func statsAdd(counter *uint64, n uint64) {
	flags := statsLock.Acquire()
	*counter += n
	statsLock.Release(flags)
}

// netStatsSnapshot returns a copy of the whole counter struct taken
// under lock.
func netStatsSnapshot() NetStats {
	flags := statsLock.Acquire()
	s := netStats
	statsLock.Release(flags)
	return s
}
