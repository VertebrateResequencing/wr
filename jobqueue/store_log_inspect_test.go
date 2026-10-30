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

// The log store's storeInspector (spec A2).

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	. "github.com/smartystreets/goconvey/convey"
)

// errLogStoreNoStats is count's error for kindStat: the log store keeps no
// learned-requirement samples of its own yet.
var errLogStoreNoStats = errors.New("the log store keeps no learned-requirement samples yet")

// logStoreFlushPoll is how often flush checks for unfinished writes.
const logStoreFlushPoll = time.Millisecond

var _ storeInspector = (*logStore)(nil)

// openTestLogStore opens a log store in a new directory, closed when the
// Convey block ends, and returns it with its directory.
func openTestLogStore(t *testing.T, ctx context.Context) (*logStore, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "store")
	s := openLogStoreIn(ctx, dir)

	Reset(func() { So(s.close(ctx), ShouldBeNil) })

	return s, dir
}

// reopenLogStore closes s and opens its directory again, as a restart would,
// returning the new store, closed when the Convey block ends.
func reopenLogStore(ctx context.Context, s *logStore) *logStore {
	So(s.close(ctx), ShouldBeNil)

	reopened := openLogStoreIn(ctx, s.dir)

	Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

	return reopened
}

// openLogStoreIn opens the log store in dir, asserting it opened. The caller
// closes it.
func openLogStoreIn(ctx context.Context, dir string) *logStore {
	s, err := openLogStore(ctx, dir)
	So(err, ShouldBeNil)

	return s
}

// liveJob returns the keyed job as its latest durable spec and state, and
// whether it is live.
func (s *logStore) liveJob(key string) (*Job, bool, error) {
	k, err := parseLogKey(key)
	if err != nil {
		return nil, false, err
	}

	s.mu.RLock()
	e := s.state.live[k]

	var spec, state wal.Ref
	if e != nil {
		spec, state = e.spec, e.state
	}

	s.mu.RUnlock()

	if e == nil {
		return nil, false, nil
	}

	job, err := s.decodeJob(spec, state, logRecState)

	return job, err == nil, err
}

// completeJob returns the keyed job as its history entry holds it, and whether
// it has one.
func (s *logStore) completeJob(key string) (*Job, bool, error) {
	k, err := parseLogKey(key)
	if err != nil {
		return nil, false, err
	}

	s.mu.RLock()
	h, found := s.state.history[k]
	s.mu.RUnlock()

	if !found {
		return nil, false, nil
	}

	job, err := s.decodeJob(h.spec, h.complete, logRecComplete)

	return job, err == nil, err
}

// count returns how many records of kind the store holds.
func (s *logStore) count(kind recordKind) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	switch kind {
	case kindLive:
		return len(s.state.live), nil
	case kindComplete:
		return len(s.state.history), nil
	case kindStdOut, kindStdErr:
		stream := stdOut
		if kind == kindStdErr {
			stream = stdErr
		}

		n := 0

		for _, e := range s.state.live {
			if e.hasStd[stream] {
				n++
			}
		}

		return n, nil
	case kindEnv:
		return len(s.state.envs), nil
	case kindLimit:
		return len(s.state.limits), nil
	default:
		return 0, errLogStoreNoStats
	}
}

// forceBackup returns errLogStoreBackup: the log store makes no backups yet.
func (s *logStore) forceBackup() error {
	return errLogStoreBackup
}

// flush waits until every write appended so far has finished.
func (s *logStore) flush() error {
	for s.writing.Load() != 0 {
		time.Sleep(logStoreFlushPoll)
	}

	return nil
}
