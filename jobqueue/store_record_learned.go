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

// This file is how a replay (spec A5) checks the store's learned-requirements
// cache reads. What such a read returned while recording depended on when the
// cache's loads ran: a load starts at most once per refresh interval of wall
// clock, and refreshReqGroupRecsLater's runs in the background. A recording
// holds neither clock nor load timing, so a replay cannot give the same
// answers. Instead it loads every value the replayed store's learned
// requirements take, and checks that each recorded answer is one of them.

import (
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

// unloadedDigests are the digests of the answers from a cache that has not
// loaded a ReqGroup: refreshReqGroupRecs's nil, and cachedReqGroupRecs's nil,
// not current.
//
//nolint:gochecknoglobals // a constant set
var unloadedDigests = map[[32]byte]bool{
	storeDigest((*scheduler.Requirements)(nil)):        true,
	storeDigest((*scheduler.Requirements)(nil), false): true,
}

// learnedReplay is what a replay has seen of the replayed store's learned
// requirements: by ReqGroup, the digest of each value they have had (alone,
// and with each current flag, as cachedReqGroupRecs answers) and the latest;
// and the digest of each change stamp up to the highest the store has given.
type learnedReplay struct {
	values   map[string]map[[32]byte]bool
	latest   map[string][32]byte
	stamps   map[[32]byte]bool
	stampsTo uint32
}

// newLearnedReplay returns a learnedReplay that has seen nothing.
func newLearnedReplay() *learnedReplay {
	return &learnedReplay{
		values: make(map[string]map[[32]byte]bool),
		latest: make(map[string][32]byte),
		stamps: map[[32]byte]bool{storeDigest(uint32(0)): true},
	}
}

// note notes rec as a value of reqGroup's learned requirements.
func (l *learnedReplay) note(reqGroup string, rec *scheduler.Requirements) {
	values := l.values[reqGroup]
	if values == nil {
		values = make(map[[32]byte]bool)
		l.values[reqGroup] = values
	}

	for _, digest := range learnedDigests(rec) {
		values[digest] = true
	}

	l.latest[reqGroup] = storeDigest(rec)
}

// learnedDigests returns the digests a recorded refreshReqGroupRecs or
// cachedReqGroupRecs answer of rec would have.
func learnedDigests(rec *scheduler.Requirements) [3][32]byte {
	return [3][32]byte{storeDigest(rec), storeDigest(rec, true), storeDigest(rec, false)}
}

// seen reports whether a recorded answer with the given digest is one a cache
// gives before its first load of a ReqGroup, or a value noted for reqGroup.
func (l *learnedReplay) seen(reqGroup string, recorded [32]byte) bool {
	return unloadedDigests[recorded] || l.values[reqGroup][recorded]
}

// isLatest reports whether rec is the latest value noted for reqGroup.
func (l *learnedReplay) isLatest(reqGroup string, rec *scheduler.Requirements) bool {
	latest, ok := l.latest[reqGroup]

	return ok && latest == storeDigest(rec)
}

// stampAtMost reports whether a recorded change stamp with the given digest is
// at most stamp. It notes the digests of the stamps up to stamp.
func (l *learnedReplay) stampAtMost(recorded [32]byte, stamp uint32) bool {
	for ; l.stampsTo < stamp; l.stampsTo++ {
		l.stamps[storeDigest(l.stampsTo+1)] = true
	}

	return l.stamps[recorded]
}

// replayCachedReqGroupRecs replays a cachedReqGroupRecs call, accepting the
// recorded answer if it is a value the store's learned requirements have had,
// and the store answered with their latest value, as current.
func replayCachedReqGroupRecs(rp *replayer, a *recKey) [32]byte {
	rp.probeLearned(a.Key)

	rec, current := rp.s.cachedReqGroupRecs(a.Key)
	answered := current && rp.learned.isLatest(a.Key, rec)
	rp.accept = func(recorded [32]byte) bool { return answered && rp.learned.seen(a.Key, recorded) }

	return storeDigest(rec, current)
}

// replayRefreshReqGroupRecs is replayCachedReqGroupRecs for a
// refreshReqGroupRecs call.
func replayRefreshReqGroupRecs(rp *replayer, a *recKey) [32]byte {
	rp.probeLearned(a.Key)

	rec := rp.s.refreshReqGroupRecs(a.Key)
	answered := rp.learned.isLatest(a.Key, rec)
	rp.accept = func(recorded [32]byte) bool { return answered && rp.learned.seen(a.Key, recorded) }

	return storeDigest(rec)
}

// replayReqStatChanges replays a reqStatChanges call, accepting the recorded
// stamp if it is at most the store's. The recording's loads saw some of the
// values the replay's probes load, so its stamp, which a load raises when a
// ReqGroup's values change, cannot be higher.
func replayReqStatChanges(rp *replayer, _ *recNone) [32]byte {
	stamp := rp.s.reqStatChanges()
	rp.accept = func(recorded [32]byte) bool { return rp.learned.stampAtMost(recorded, stamp) }

	return storeDigest(stamp)
}

// probeLearned loads reqGroup's learned requirements in the replayed store, if
// they are not current, and notes them. The replay runs with no refresh
// interval, so the load is never held back.
func (rp *replayer) probeLearned(reqGroup string) {
	rp.learned.note(reqGroup, rp.s.refreshReqGroupRecs(reqGroup))
}

// probeAllLearned is probeLearned of every ReqGroup the replay has seen, which
// it does after each write, since a write may add a sample that changes them.
func (rp *replayer) probeAllLearned() {
	for reqGroup := range rp.learned.latest {
		rp.probeLearned(reqGroup)
	}
}
