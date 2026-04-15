// src/pipe.go — kernel pipe object.
//
// Phase 3 (this commit) ships the sequential variant: a
// fixed-size buffer the writer fills, then the reader drains.
// Both ends are FileDesc impls so they slot into the existing
// fd table (src/fd.go) and get inherited / dup2'd /
// processExit-closed like any other fd.
//
// In single-process v1 the shell exec's cmd1 to completion (it
// fills the buffer through fd 1), then exec's cmd2 (it drains
// the buffer through fd 0). The seqPipe's writer-close +
// reader-EOF discipline is what tells cmd2 the upstream is
// done.
//
// Phase 5 will replace seqPipe* with a chan-byte pipe for true
// concurrency. The sys_pipe syscall and the FileDesc shape
// stay the same; only the impl struct swaps.
//
// See impldoc/shell_io_pipes.md §2.

package main

const seqPipeMaxBytes = 64 * 1024 // 64 KiB per pipe

type seqPipeBuf struct {
	data       []byte // grows up to seqPipeMaxBytes
	offset     int    // reader cursor
	writerDone bool   // writer Close()d
	readerGone bool   // reader Close()d
}

type seqPipeWriter struct{ buf *seqPipeBuf }
type seqPipeReader struct{ buf *seqPipeBuf }

// newSeqPipe returns the (reader, writer) pair backed by a
// fresh seqPipeBuf. Both ends share the *seqPipeBuf; close
// semantics use the writerDone / readerGone flags to deliver
// EOF / EPIPE.
func newSeqPipe() (*seqPipeReader, *seqPipeWriter) {
	b := &seqPipeBuf{}
	return &seqPipeReader{buf: b}, &seqPipeWriter{buf: b}
}

// --- writer ---

func (w *seqPipeWriter) Read([]byte) (int, fdErr) { return 0, fdErrBad }

func (w *seqPipeWriter) Write(p []byte) (int, fdErr) {
	if w.buf.readerGone {
		return 0, fdErrPipe
	}
	room := seqPipeMaxBytes - len(w.buf.data)
	n := len(p)
	if n > room {
		n = room
	}
	w.buf.data = append(w.buf.data, p[:n]...)
	if n < len(p) {
		// Buffer full — surface short write so caller doesn't
		// silently lose bytes. Sequential pipe is a stop-gap;
		// phase 5's concurrent variant has back-pressure.
		return n, fdErrOK
	}
	return n, fdErrOK
}

// Close is idempotent: fd inheritance can leave the same
// *seqPipeWriter in both parent and child fd tables, so
// processExit may run Close more than once.
func (w *seqPipeWriter) Close() fdErr {
	w.buf.writerDone = true
	return fdErrOK
}

// --- reader ---

func (r *seqPipeReader) Write([]byte) (int, fdErr) { return 0, fdErrBad }

func (r *seqPipeReader) Read(p []byte) (int, fdErr) {
	if r.buf.offset >= len(r.buf.data) {
		// No data buffered. In sequential mode the writer has
		// already finished by the time the reader starts, so
		// this is always real EOF.
		return 0, fdErrEOF
	}
	n := copy(p, r.buf.data[r.buf.offset:])
	r.buf.offset += n
	if r.buf.offset >= len(r.buf.data) && r.buf.writerDone {
		// Drain hit the end after the writer closed; subsequent
		// reads see EOF without further data.
	}
	return n, fdErrOK
}

func (r *seqPipeReader) Close() fdErr {
	r.buf.readerGone = true
	return fdErrOK
}
