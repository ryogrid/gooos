// src/netbuf.go -- Fixed-size packet buffer pool for the network stack.
//
// 128 × 2048-byte buffers in one physically-contiguous region. A
// 128-bit free bitmap (stored as [2]uint64) tracks availability; a
// cleared bit means "free". A ctz64 helper finds the lowest free slot
// in O(1).
//
// Used by the buffer-pool-aware RX/TX paths introduced in Phase 4+.
// In Phase 4's initial landing the pool is allocated and exposed but
// not yet wired into e1000 descriptor management — that conversion
// happens alongside the interrupt-driven RX switch (see Phase 4c).

package main

import "unsafe"

const (
	netBufCount = 128
	netBufSize  = 2048
	// Total: 128 * 2048 = 262144 B = 64 × 4-KiB pages.
	netBufPoolPages = (netBufCount * netBufSize) / pageSize
)

var (
	// netBufPoolBase is the physical (and, via the identity map,
	// virtual) base address of the contiguous pool. Zero until
	// netBufInit runs.
	netBufPoolBase uintptr

	// netBufFree is the 128-bit availability bitmap. A 0 bit means
	// free; a 1 bit means allocated. Guarded by netBufLock.
	netBufFree [2]uint64

	// netBufLock protects netBufFree. Lock-ordering rank 5.
	netBufLock Spinlock
)

// ctz64 returns the index of the lowest set bit in x, or 64 when
// x == 0. Small bit-twiddle that avoids pulling in math/bits, which
// TinyGo's bare-metal target may not always ship.
//
//go:nosplit
func ctz64(x uint64) int {
	if x == 0 {
		return 64
	}
	n := 0
	if (x & 0x00000000FFFFFFFF) == 0 {
		n += 32
		x >>= 32
	}
	if (x & 0x000000000000FFFF) == 0 {
		n += 16
		x >>= 16
	}
	if (x & 0x00000000000000FF) == 0 {
		n += 8
		x >>= 8
	}
	if (x & 0x000000000000000F) == 0 {
		n += 4
		x >>= 4
	}
	if (x & 0x0000000000000003) == 0 {
		n += 2
		x >>= 2
	}
	if (x & 0x0000000000000001) == 0 {
		n++
	}
	return n
}

// netBufInit reserves the pool from the page allocator. Must be called
// after vmInit; safe to call before interrupts are enabled. No-op on
// second call.
func netBufInit() {
	if netBufPoolBase != 0 {
		return
	}
	netBufPoolBase = allocPagesContig(netBufPoolPages)
	if netBufPoolBase >= 0x40000000 {
		// Outside the boot identity map — fatal for DMA use.
		serialPrintln("netbuf: pool allocated above 1 GiB identity map")
		for {
			hlt()
		}
	}
}

// netBufAlloc returns (address, index) for the lowest-numbered free
// buffer. Returns (0, -1) on exhaustion.
func netBufAlloc() (uintptr, int) {
	flags := netBufLock.Acquire()
	defer netBufLock.Release(flags)

	for word := 0; word < 2; word++ {
		inv := ^netBufFree[word] // 1 = free
		if inv == 0 {
			continue
		}
		bit := ctz64(inv)
		idx := word*64 + bit
		if idx >= netBufCount {
			continue
		}
		netBufFree[word] |= uint64(1) << uint(bit)
		return netBufPoolBase + uintptr(idx)*netBufSize, idx
	}
	statsInc(&netStats.BufAllocFail)
	return 0, -1
}

// netBufFreeIdx releases the slot `idx`. No-op on out-of-range.
func netBufFreeIdx(idx int) {
	if idx < 0 || idx >= netBufCount {
		return
	}
	flags := netBufLock.Acquire()
	defer netBufLock.Release(flags)
	netBufFree[idx/64] &^= uint64(1) << uint(idx%64)
}

// netBufSlice returns a byte slice that aliases the buffer at `idx`
// for `length` bytes. Memory lives outside the GC heap — callers must
// not retain the slice past netBufFreeIdx.
func netBufSlice(idx int, length int) []byte {
	if idx < 0 || idx >= netBufCount || length < 0 || length > netBufSize {
		return nil
	}
	base := netBufPoolBase + uintptr(idx)*netBufSize
	return unsafe.Slice((*byte)(unsafe.Pointer(base)), length)
}

// testNetBuf exercises the lifecycle of the buffer pool: fills it,
// proves exhaustion returns (0,-1), frees one slot, reallocates, then
// frees everything. Prints PASS/FAIL to serial. Safe to call once at
// boot; runs before any real traffic so the pool is empty afterwards.
func testNetBuf() {
	netBufInit()

	indices := make([]int, 0, netBufCount)
	for i := 0; i < netBufCount; i++ {
		_, idx := netBufAlloc()
		if idx < 0 {
			serialPrintln("TEST: netbuf FAIL — exhausted before 128 slots")
			return
		}
		indices = append(indices, idx)
	}

	if _, idx := netBufAlloc(); idx != -1 {
		serialPrintln("TEST: netbuf FAIL — 129th alloc should have failed")
		return
	}

	// Free the first and re-allocate — we should get the same index back.
	freed := indices[0]
	netBufFreeIdx(freed)
	_, reclaim := netBufAlloc()
	if reclaim != freed {
		serialPrintln("TEST: netbuf FAIL — expected to reclaim slot " +
			utoa(uint64(freed)) + ", got " + utoa(uint64(reclaim)))
		return
	}

	// Release everything the test grabbed.
	for _, i := range indices {
		netBufFreeIdx(i)
	}
	serialPrintln("TEST: netbuf lifecycle PASS")
}
