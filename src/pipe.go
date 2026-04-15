// src/pipe.go — kernel pipe object.
//
// Phase 5: replaced the sequential variant with a chan-byte
// backed concurrent pipe. Both stages run as independent
// Ring-3 processes (per-proc PML4 from 4e); the writer parks
// on a full channel, the reader parks on an empty one, both
// via TinyGo's native chan parking. Idempotent Close on both
// ends survives fd inheritance dual-close.
//
// See impldoc/shell_io_pipes.md §3.

package main

const pipeBufBytes = 4096

// pipe holds the shared state between a pipeReader and a
// pipeWriter. Close semantics are refcounted: each process
// that holds a pipeReader / pipeWriter counts as one ref on
// rdRefs / wrRefs. procAllocFD / procDup2 (when installing a
// pipe end) bump the count; Close decrements and only flips
// the rdClosed / wrClosed flag + closes the chan when the
// count hits zero. Without refcounting, shell's Close of its
// own pipe-end reference would kill the chan for the just-
// spawned child that legitimately inherited it.
type pipe struct {
	ch       chan byte // buffered, capacity pipeBufBytes
	rdClosed bool
	wrClosed bool
	rdRefs   int
	wrRefs   int
}

type pipeReader struct{ p *pipe }
type pipeWriter struct{ p *pipe }

// newPipe returns a (reader, writer) pair sharing one chan.
// Each end starts with a refcount of 1 (the caller who will
// stash them into two fd slots via procAllocFD — which calls
// fdAddRef to bump to 2 total per end per Stash, matched by
// procClose decrements).
func newPipe() (*pipeReader, *pipeWriter) {
	p := &pipe{ch: make(chan byte, pipeBufBytes), rdRefs: 1, wrRefs: 1}
	return &pipeReader{p: p}, &pipeWriter{p: p}
}

// --- writer ---

func (w *pipeWriter) Read([]byte) (int, fdErr) { return 0, fdErrBad }

func (w *pipeWriter) Write(buf []byte) (int, fdErr) {
	if w.p == nil || w.p.wrClosed {
		return 0, fdErrBad
	}
	for i := 0; i < len(buf); i++ {
		// Check rdClosed before each send so a writer parked
		// on a full chan can still wake to EPIPE if the
		// reader closes — but the chan send itself doesn't
		// race-handle that. With single-CPU cooperative
		// scheduling the check-then-send is atomic for our
		// purposes.
		if w.p.rdClosed {
			return i, fdErrPipe
		}
		w.p.ch <- buf[i]
	}
	return len(buf), fdErrOK
}

// Close drops one writer reference. When the last writer goes
// away, closes the channel so readers see EOF. Idempotent
// beyond the refcount hitting zero (once wrClosed is true,
// further calls are no-ops even if a stale reference Closes).
func (w *pipeWriter) Close() fdErr {
	if w.p == nil || w.p.wrClosed {
		return fdErrOK
	}
	if w.p.wrRefs > 0 {
		w.p.wrRefs--
	}
	if w.p.wrRefs == 0 {
		w.p.wrClosed = true
		close(w.p.ch)
	}
	return fdErrOK
}

// --- reader ---

func (r *pipeReader) Write([]byte) (int, fdErr) { return 0, fdErrBad }

func (r *pipeReader) Read(buf []byte) (int, fdErr) {
	if r.p == nil {
		return 0, fdErrBad
	}
	for i := 0; i < len(buf); i++ {
		b, ok := <-r.p.ch
		if !ok {
			if i == 0 {
				return 0, fdErrEOF
			}
			return i, fdErrOK
		}
		buf[i] = b
	}
	return len(buf), fdErrOK
}

// Close drops one reader reference. When the last reader goes
// away, sets rdClosed so the next writer Write sees EPIPE.
// Don't close the chan from the reader side (would race with
// writer sends).
func (r *pipeReader) Close() fdErr {
	if r.p == nil || r.p.rdClosed {
		return fdErrOK
	}
	if r.p.rdRefs > 0 {
		r.p.rdRefs--
	}
	if r.p.rdRefs == 0 {
		r.p.rdClosed = true
	}
	return fdErrOK
}

// pipeWriterAddRef / pipeReaderAddRef bump the refcount when a
// FileDesc is copied into another fd slot (dup2) or inherited
// by a child process (shallow-copy of Process.fds in elfSpawn).
// Called from fd-table helpers, not from user syscalls directly.
func pipeWriterAddRef(w *pipeWriter) {
	if w != nil && w.p != nil {
		w.p.wrRefs++
	}
}

func pipeReaderAddRef(r *pipeReader) {
	if r != nil && r.p != nil {
		r.p.rdRefs++
	}
}
