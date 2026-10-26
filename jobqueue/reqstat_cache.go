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

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	bolt "go.etcd.io/bbolt"
)

// reqStatLoadHook, if not nil, is called at the start of each load of a
// ReqGroup's learned requirements; a non-nil error fails the load. It is a test
// seam, nil in production.
//
//nolint:gochecknoglobals // prod-inert test seam, like jobExitSnapshotHook.
var reqStatLoadHook func(reqGroup string) error

// reqStatEntry is one ReqGroup's cached learned requirements and the state of
// their loading.
type reqStatEntry struct {
	rec       *scheduler.Requirements // last loaded; nil if never loaded
	gen       uint64                  // incremented by invalidate
	loadedGen uint64                  // gen when rec's load started
	loaded    bool
	loadStart time.Time // when the last load started; zero if none has
	loading   bool
}

// current reports whether e was loaded and not invalidated since that load
// started.
func (e *reqStatEntry) current() bool {
	return e.loaded && e.loadedGen == e.gen
}

// loadReqGroupRecsInto does the load startLoadLocked marked e as doing, for
// gen, with c.mu not held, then stores the result in e and returns e's value.
func (db *db) loadReqGroupRecsInto(reqGroup string, e *reqStatEntry, gen uint64) *scheduler.Requirements {
	c := db.reqStats

	rec, err := db.loadReqGroupRecs(reqGroup)

	c.mu.Lock()
	defer c.mu.Unlock()

	e.loading = false

	if err != nil {
		clog.Debug(context.Background(), "loading learned requirements failed", "reqgroup", reqGroup, "err", err)

		return e.rec
	}

	if learnedReqsOf(rec) != learnedReqsOf(e.rec) {
		c.changes.Add(1)
	}

	e.rec, e.loadedGen, e.loaded = rec, gen, true

	return rec
}

// reqStatCache holds each ReqGroup's learned requirements, as
// recommendedReqForGroup computed them from the jobRAM, jobDisk and jobSecs
// buckets. Entries are never evicted (production has about 130 ReqGroups).
type reqStatCache struct {
	mu       sync.Mutex
	entries  map[string]*reqStatEntry
	interval time.Duration  // least time between load starts for one ReqGroup
	wg       sync.WaitGroup // loads started by refreshReqGroupRecsLater
	closed   bool

	// changes is a stamp that a load increments whenever it changes some
	// ReqGroup's learnedReqs. It starts at 1, so it never equals a Job's
	// preparedStamp of 0.
	changes atomic.Uint32
}

// newReqStatCache returns an empty reqStatCache with its changes stamp at 1.
func newReqStatCache() *reqStatCache {
	c := &reqStatCache{entries: make(map[string]*reqStatEntry)}
	c.changes.Store(1)

	return c
}

// startLoadLocked reports whether a load of e is due: e is not current, no
// load of it is in flight, and the last one started at least c.interval ago.
// If so it marks e loading and returns the gen the load is for. c.mu must be
// held.
func (c *reqStatCache) startLoadLocked(e *reqStatEntry) (uint64, bool) {
	if e.current() || e.loading || (!e.loadStart.IsZero() && time.Since(e.loadStart) < c.interval) {
		return 0, false
	}

	e.loading = true
	e.loadStart = time.Now()

	return e.gen, true
}

// close stops refreshReqGroupRecsLater starting loads and waits for those it
// started.
func (c *reqStatCache) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()

	c.wg.Wait()
}

// entryLocked returns reqGroup's entry, creating it if needed. c.mu must be
// held.
func (c *reqStatCache) entryLocked(reqGroup string) *reqStatEntry {
	e, ok := c.entries[reqGroup]
	if !ok {
		e = &reqStatEntry{}
		c.entries[reqGroup] = e
	}

	return e
}

// invalidate increments reqGroup's entry's gen, so its loaded value is no
// longer current.
func (c *reqStatCache) invalidate(reqGroup string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entryLocked(reqGroup).gen++
}

// putStat puts val for reqGroup in tx's bucket, keyed so it sorts within the
// ReqGroup. If the Put created a new key, it registers tx.OnCommit to
// invalidate reqGroup, so the invalidation happens only once the write is
// committed. An overwrite of an existing key invalidates nothing: stats are
// stored per distinct value, so only a new key can change a recommendation.
// Repeated invalidations of one ReqGroup in a transaction are harmless.
func (c *reqStatCache) putStat(tx *bolt.Tx, bucket []byte, reqGroup string, val int) error {
	b := tx.Bucket(bucket)
	key := fmt.Appendf(nil, "%s%s%20d", reqGroup, dbDelimiter, val)
	isNew := b.Get(key) == nil

	if err := b.Put(key, []byte(strconv.Itoa(val))); err != nil {
		return err
	}

	if isNew {
		tx.OnCommit(func() { c.invalidate(reqGroup) })
	}

	return nil
}

// mbToRoundedUpGB returns mb in GB, rounded up; 0 if mb is not positive.
func mbToRoundedUpGB(mb int) int {
	if mb <= 0 {
		return 0
	}

	return int(math.Ceil(float64(mb) / float64(mbPerGB)))
}

// cachedReqGroupRecs returns reqGroup's last loaded learned requirements (nil
// if never loaded) and whether they are current: loaded, and not invalidated
// since the load started. It does no database I/O and is safe under the
// queue lock.
func (db *db) cachedReqGroupRecs(reqGroup string) (*scheduler.Requirements, bool) {
	c := db.reqStats

	c.mu.Lock()
	defer c.mu.Unlock()

	e := c.entryLocked(reqGroup)

	return e.rec, e.current()
}

// refreshReqGroupRecs loads reqGroup's learned requirements if they are not
// current, the last load started at least interval ago, and no load is in
// flight; otherwise it loads nothing. It returns the cached value afterwards,
// which after a failed load is the previous value.
func (db *db) refreshReqGroupRecs(reqGroup string) *scheduler.Requirements {
	c := db.reqStats

	c.mu.Lock()
	e := c.entryLocked(reqGroup)
	gen, due := c.startLoadLocked(e)
	rec := e.rec
	c.mu.Unlock()

	if !due {
		return rec
	}

	return db.loadReqGroupRecsInto(reqGroup, e, gen)
}

// refreshReqGroupRecsLater is refreshReqGroupRecs in a goroutine tracked by
// the cache's wg, started only when a load is due. It does nothing once the
// database has started closing.
//
// The load is detached from whichever request found the entry stale, so it
// takes no context; contextcheck reports that at every caller that has one,
// and nolintlint cannot see the reports this directive suppresses there.
//
//nolint:contextcheck,nolintlint // see above
func (db *db) refreshReqGroupRecsLater(reqGroup string) {
	c := db.reqStats

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	e := c.entryLocked(reqGroup)

	gen, due := c.startLoadLocked(e)
	if !due {
		return
	}

	c.wg.Go(func() { db.loadReqGroupRecsInto(reqGroup, e, gen) })
}

// warmReqGroupRecs calls refreshReqGroupRecs once for each distinct ReqGroup
// of jobs.
//
// A load is a short local read whose result is shared by every later caller,
// so it takes no context, as refreshReqGroupRecsLater's does not; contextcheck
// reports that at every caller that has one.
//
//nolint:contextcheck,nolintlint // see above
func (db *db) warmReqGroupRecs(jobs []*Job) {
	seen := make(map[string]bool)

	for _, job := range jobs {
		job.RLock()
		reqGroup := job.ReqGroup
		job.RUnlock()

		if seen[reqGroup] {
			continue
		}

		seen[reqGroup] = true

		db.refreshReqGroupRecs(reqGroup)
	}
}

// loadReqGroupRecs reads reqGroup's learned requirements from the database:
// RAM from recommendedReqGroupMemory, disk in GB rounded up, and time in
// seconds. A ReqGroup with no stats gets zero RAM, disk and time.
func (db *db) loadReqGroupRecs(reqGroup string) (*scheduler.Requirements, error) {
	if reqStatLoadHook != nil {
		if err := reqStatLoadHook(reqGroup); err != nil {
			return nil, err
		}
	}

	recm, err := db.recommendedReqGroupMemory(reqGroup)
	if err != nil {
		return nil, err
	}

	recd, err := db.recommendedReqGroupDisk(reqGroup)
	if err != nil {
		return nil, err
	}

	recs, err := db.recommendedReqGroupTime(reqGroup)
	if err != nil {
		return nil, err
	}

	return &scheduler.Requirements{
		RAM:     max(recm, 0),
		Disk:    mbToRoundedUpGB(recd),
		DiskSet: true,
		Time:    time.Duration(max(recs, 0)) * time.Second,
	}, nil
}
