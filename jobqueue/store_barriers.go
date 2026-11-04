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

// This file holds what a checkpoint waits for (spec "Checkpoints", steps 1 and
// 4): the append barrier, which lets it wait until every write that appended
// before its cut has its Refs, and readers' holds, which keep the files of the
// Refs a reader copied until it has read them.

import (
	"sync/atomic"
	"time"
)

// checkpointWaitPoll is how often a checkpoint checks whether the writes and
// reads it waits for have ended.
const checkpointWaitPoll = time.Millisecond

// These test seams, nil outside tests, are called at the points in the barriers
// that a write or read racing a checkpoint can reach.
var (
	// logEnterAppendHook is called by enterAppend between its loading the
	// append generation and its noting the write in it.
	logEnterAppendHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam

	// logAwaitAppendsHook is called by awaitAppends once it has started a new
	// append generation, before it waits for the writes of the previous one.
	logAwaitAppendsHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam

	// logAwaitReadersHook is called by awaitRefReaders once it has started a
	// new generation of reader holds, before it waits for the readers of the
	// previous one.
	logAwaitReadersHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam

	// logHoldRefsHook is called by holdRefs before it takes its hold.
	logHoldRefsHook atomic.Pointer[func()] //nolint:gochecknoglobals // a test seam
)

// holdRefs notes a reader about to read records through Refs it copied from
// the state, returning the hold for releaseRefs. The caller holds s.mu, for
// reading or writing, so that the hold is in the generation of the Refs it
// copied: a checkpoint deletes the files its swap leaves no Ref to only once
// no reader holds an earlier generation.
func (s *logStore) holdRefs() uint64 {
	callHook(&logHoldRefsHook)

	gen := s.refGen
	s.refHeld[gen&1].Add(1)

	return gen
}

// releaseRefs ends a hold holdRefs returned.
func (s *logStore) releaseRefs(gen uint64) {
	s.refHeld[gen&1].Add(-1)
}

// awaitRefReaders starts a new generation of reader holds, then waits until no
// reader holds the previous one. The caller has swapped every Ref below its cut
// and holds checkpoints' lock, so generations change one at a time.
func (s *logStore) awaitRefReaders() {
	s.mu.Lock()
	old := s.refGen
	s.refGen++
	s.mu.Unlock()

	callHook(&logAwaitReadersHook)

	for s.refHeld[old&1].Load() != 0 {
		time.Sleep(checkpointWaitPoll)
	}
}

// enterAppend notes a write about to append, until exitAppend, so that a
// checkpoint can wait for every write that appended before its cut to have its
// Refs (see awaitAppends).
func (s *logStore) enterAppend() uint64 {
	for {
		gen := s.appendGen.Load()

		callHook(&logEnterAppendHook)

		s.appending[gen&1].Add(1)

		if s.appendGen.Load() == gen {
			return gen
		}

		s.appending[gen&1].Add(-1)
	}
}

// exitAppend ends a note enterAppend returned.
func (s *logStore) exitAppend(gen uint64) {
	s.appending[gen&1].Add(-1)
}

// awaitAppends returns once every write that entered before it was called has
// exited: after a rotation, every write with records before the cut has its
// Refs, and, as the rotation's batch was durable after theirs, has applied.
func (s *logStore) awaitAppends() {
	old := s.appendGen.Add(1) - 1

	callHook(&logAwaitAppendsHook)

	for s.appending[old&1].Load() != 0 {
		time.Sleep(checkpointWaitPoll)
	}
}
