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

import "time"

var _ store = (*db)(nil)

// upgradedOnOpen reports whether opening the database upgraded its schema.
func (db *db) upgradedOnOpen() bool {
	return db.upgraded
}

// registerRerunGuard returns a new guard, held until releaseRerunGuard, for an
// add about to read its dependents.
func (db *db) registerRerunGuard() *rerunGuard {
	return db.rerunGuards.register()
}

// releaseRerunGuard stops g seeing archives.
func (db *db) releaseRerunGuard(g *rerunGuard) {
	db.rerunGuards.release(g)
}

// reqStatChanges returns the learned-requirements cache's change stamp, which a
// load increments whenever it changes some ReqGroup's recommendations.
func (db *db) reqStatChanges() uint32 {
	return db.reqStats.changes.Load()
}

// setReqStatRefreshInterval sets the least time between loads of one ReqGroup's
// learned requirements.
func (db *db) setReqStatRefreshInterval(d time.Duration) {
	db.reqStats.interval = d
}

// setRecRounding sets the rounding, in seconds and MBs, of recommended reserve
// times and memory/disk.
func (db *db) setRecRounding(secRound, mbRound int) {
	db.recSecRound = secRound
	db.recMBRound = mbRound
}
