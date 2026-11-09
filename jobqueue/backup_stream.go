/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package jobqueue

// This file streams a manager's self-contained backup (spec E5) to a client
// in chunks, one request per chunk, so that the manager holds at most a chunk
// of each backup in memory whatever the size of its store.

import (
	"cmp"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofrs/uuid/v5"
)

// maxBackupStreams is how many backups clients may be fetching at once. Each
// holds a goroutine, and its store's backup open (for the log store, the
// checkpoint lock, so a second waits for the first), until it is fetched,
// abandoned or the manager stops.
const maxBackupStreams = 4

//nolint:gochecknoglobals // tunables that tests shorten
var (
	// backupChunkSize is the most bytes of a backup one reply carries.
	backupChunkSize = 4 << 20

	// backupStreamIdleTimeout is how long a backup may go unfetched before
	// the manager abandons it, ending its store's backup. It is twice the
	// least time a client waits for a reply, as that time is counted from
	// before a chunk's reply is sent, and so includes its transfer.
	backupStreamIdleTimeout = 2 * ClientMinRequestTimeout

	// backupProduceHook, a test seam nil outside tests, is what a backup
	// stream writes in place of the store's backup when it is set.
	backupProduceHook atomic.Pointer[func(io.Writer) error]
)

var (
	errBackupStreamsFull = errors.New("too many backups are being fetched from the manager; " +
		"try again once one has finished")
	errBackupStreamUnknown = errors.New("the manager is not making that backup: it was abandoned " +
		"after not being fetched for too long, or has ended; start the backup again")
	errBackupStreamOutOfStep = errors.New("a chunk of a backup was asked for out of order, so the " +
		"backup was abandoned; start it again")
	errBackupStreamAbandoned = errors.New("backup abandoned after not being fetched for too long")
	errBackupStreamCancelled = errors.New("backup cancelled by the client fetching it")
	errBackupStreamsClosed   = errors.New("the manager is stopping")
	errBackupNotStarted      = errors.New("the manager did not start a backup")
)

// backupProduceError is the error a store's backup failed with, as a backup
// stream's reader sees it. It hides that error from errors.Is, so that one
// wrapping io.EOF, as a file copied short gives, is never taken for the
// backup's clean end, which would publish it truncated.
type backupProduceError struct {
	err error
}

func (e backupProduceError) Error() string {
	return e.err.Error()
}

// backupStreamErr returns the error strings to reply to a backup request
// that failed with err: the client is told why its backup could not be
// fetched, a manager that is stopping answers as for any other request, and
// the store's own backup errors are logged in detail.
func backupStreamErr(err error) (string, string) {
	switch {
	case errors.Is(err, errBackupStreamsClosed):
		return ErrClosedStop, err.Error()
	case errors.Is(err, errBackupStreamsFull), errors.Is(err, errBackupStreamUnknown),
		errors.Is(err, errBackupStreamOutOfStep):
		return err.Error(), ""
	default:
		return ErrInternalError, err.Error()
	}
}

// backupStream is one backup a client is fetching: the read end of a pipe its
// store's backup writes to, and how much of it has been read.
type backupStream struct {
	r           *io.PipeReader
	idle        *time.Timer
	idleSince   time.Time
	idleTimeout time.Duration
	offset      int64
	busy        bool
	endedBy     error
}

// backupStreams are the backups clients are fetching, by id. The zero value is
// ready to use.
type backupStreams struct {
	mu      sync.Mutex
	streams map[string]*backupStream
	closed  bool
}

// start starts a backup that produce writes, which a client then fetches by
// the returned id with chunk. produce runs in its own goroutine, blocked on
// each write until a chunk request reads it.
func (b *backupStreams) start(produce func(io.Writer) error) (string, error) {
	u, err := uuid.NewV4()
	if err != nil {
		return "", err
	}

	id := u.String()

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return "", errBackupStreamsClosed
	}

	if len(b.streams) >= maxBackupStreams {
		return "", errBackupStreamsFull
	}

	if b.streams == nil {
		b.streams = make(map[string]*backupStream)
	}

	b.streams[id] = b.produce(id, produce)

	return id, nil
}

// produce runs produce in its own goroutine, writing to the pipe the returned
// stream id reads, whose idle timer it starts.
func (b *backupStreams) produce(id string, produce func(io.Writer) error) *backupStream {
	r, w := io.Pipe()
	st := &backupStream{r: r, idleSince: time.Now(), idleTimeout: backupStreamIdleTimeout}
	st.idle = time.AfterFunc(st.idleTimeout, func() { b.abandon(id, st) })

	go func() {
		if err := produce(w); err != nil {
			w.CloseWithError(backupProduceError{err})

			return
		}

		w.Close()
	}()

	return st
}

// abandon ends stream id, st, if it has gone unfetched for its idle timeout:
// its idle timer may fire just as a chunk request takes it, and run after that
// request has finished.
func (b *backupStreams) abandon(id string, st *backupStream) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if st.busy || time.Since(st.idleSince) < st.idleTimeout {
		return
	}

	b.removeLocked(id, st, errBackupStreamAbandoned)
}

// removeLocked forgets stream id, st, if it is still known, and closes it with
// err, so that its store's backup ends with err as its next write's error,
// and a request reading it with err, unless it had already ended. The caller
// holds mu.
func (b *backupStreams) removeLocked(id string, st *backupStream, err error) {
	if b.streams[id] == st {
		delete(b.streams, id)
	}

	if st.endedBy == nil {
		st.endedBy = err
	}

	st.idle.Stop()
	st.r.CloseWithError(err)
}

// chunk returns the next up to backupChunkSize bytes of backup id, which must
// start at offset, and whether they are its last. A chunk asked for at any
// other offset, or while another request is reading it, as a resent request
// would be, ends the backup, so a client never gets a chunk twice or misses
// one.
func (b *backupStreams) chunk(id string, offset int64) ([]byte, bool, error) {
	st, err := b.checkout(id, offset)
	if err != nil {
		return nil, false, err
	}

	buf := make([]byte, backupChunkSize)
	n, err := io.ReadFull(st.r, buf)
	done := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)

	endedBy := b.checkin(id, st, n, done || err != nil)

	if err != nil && !done {
		// a read the stream was closed under fails with io.ErrClosedPipe,
		// which does not say why
		return nil, false, cmp.Or(endedBy, err)
	}

	return buf[:n], done, nil
}

// checkout returns stream id, marked busy with its idle timer stopped, if it
// is at offset and not already busy; otherwise it ends it.
func (b *backupStreams) checkout(id string, offset int64) (*backupStream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	st := b.streams[id]
	if st == nil {
		return nil, errBackupStreamUnknown
	}

	if st.busy || st.offset != offset {
		b.removeLocked(id, st, errBackupStreamOutOfStep)

		return nil, errBackupStreamOutOfStep
	}

	st.idle.Stop()
	st.busy = true

	return st, nil
}

// checkin records that n more bytes of stream id, st, were read, forgetting
// it if it ended, or else restarting its idle timer. It returns why the
// stream was ended while it was read, if it was.
func (b *backupStreams) checkin(id string, st *backupStream, n int, ended bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	st.busy = false
	st.offset += int64(n)
	endedBy := st.endedBy

	if ended {
		b.removeLocked(id, st, errBackupStreamUnknown)

		return endedBy
	}

	if b.streams[id] == st {
		st.idleSince = time.Now()
		st.idle.Reset(st.idleTimeout)
	}

	return endedBy
}

// cancel ends backup id, which its client has given up fetching, if it is
// still known.
func (b *backupStreams) cancel(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if st := b.streams[id]; st != nil {
		b.removeLocked(id, st, errBackupStreamCancelled)
	}
}

// close ends every backup being fetched, so that their stores' backups end
// and a store can close, and refuses to start any more.
func (b *backupStreams) close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true

	for id, st := range b.streams {
		b.removeLocked(id, st, errBackupStreamsClosed)
	}
}
