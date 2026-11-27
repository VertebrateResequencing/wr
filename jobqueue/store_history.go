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

// This file is what the log store holds in memory of its sealed history
// segments (spec D2): the key index, a packed sorted slice of 32-byte entries
// per segment rather than a Go map of keys; rep group, dep group and reverse
// dep group to the segments holding them; each segment's end-time range; each
// rep group's latest end time; the keys a modify dropped the rglookups and
// reverse dep group postings of; the learned-requirement samples; and the
// footer sections queries read, read again on demand through an LRU of at most
// histFooterCacheBytes. The store loads it from the sealed segments when it
// opens, and answers history queries from it (spec D3). An index is never
// changed once the store publishes it: a segment sealed later is added to a
// copy, which the store publishes in its place (see logStore.addSegment), so a
// query reads the one it took throughout, with no lock.

import (
	"bytes"
	"cmp"
	"container/list"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/ugorji/go/codec"
)

// histFooterCacheBytes is the most memory the footers the history index caches
// may take (see histFooter.memSize). A var so tests can shrink it.
var histFooterCacheBytes int64 = 64 << 20 //nolint:gochecknoglobals // tuning knob, varied by tests

// archivedFlatScale is the per mille spendArchivedBytesByRepGroup scales a
// history entry's flat length by (see archivedFlatPrice), so that its byte
// budget trips at about the number of entries today's Binc records trip it
// at (spec D3, test 4).
const archivedFlatScale = 1564

// histSection is a footer section queries read.
type histSection uint8

const (
	// histSectionRepGroups is the rep group postings section.
	histSectionRepGroups histSection = iota

	// histSectionEndTimes is the end time section.
	histSectionEndTimes

	// histSectionRevDeps is the reverse dep group postings section.
	histSectionRevDeps
)

// only returns a footer holding only f's section, with nothing aliasing the
// bytes f was parsed from, and about how many bytes of memory it holds.
func (f *histFooter) only(section histSection) (*histFooter, int64) {
	only := &histFooter{}
	names := 0

	switch section {
	case histSectionRepGroups:
		only.repGroups, names = clonedHistPostings(f.repGroups)
	case histSectionEndTimes:
		only.endTimes = f.endTimes
	case histSectionRevDeps:
		only.revDeps, names = clonedHistPostings(f.revDeps)
	}

	return only, only.memSize(names)
}

// clonedHistPostings returns postings with their names cloned, so that they
// alias nothing, and the bytes of those names.
func clonedHistPostings(postings []histPosting) ([]histPosting, int) {
	cloned := make([]histPosting, len(postings))
	names := 0

	for i, p := range postings {
		cloned[i] = histPosting{name: strings.Clone(p.name), keys: p.keys}
		names += len(p.name)
	}

	return cloned, names
}

// histSegment is what the history index holds of one sealed segment: its
// number, block offsets, the offset of its footer, its key index, sorted by
// key, its highest complete seq, and the range of its completions' end times,
// as UnixNano.
type histSegment struct {
	n              uint32
	blocks         []uint64
	footerOff      uint64
	keys           []histKeyEntry
	maxSeq         uint64
	endMin, endMax int64
}

// entry returns k's entry in seg's key index, if it has one.
func (seg *histSegment) entry(k logKey) (histKeyEntry, bool) {
	i, found := slices.BinarySearchFunc(seg.keys, k, func(e histKeyEntry, k logKey) int {
		return bytes.Compare(e.key[:], k[:])
	})
	if !found {
		return histKeyEntry{}, false
	}

	return seg.keys[i], true
}

// histSamples are a req group's learned-requirement samples of each kind,
// each sorted and distinct, as history.bolt's buckets hold them.
type histSamples [histSampleKinds][]int

// histRef is where a key's history entry is: its segment and its key index
// entry there.
type histRef struct {
	seg   *histSegment
	entry histKeyEntry
}

// segRefs returns segRef of each of keys that has one, skipping keys that are
// live if notLive.
func (r *historyRead) segRefs(keys []logKey, notLive bool) []histRef {
	var refs []histRef

	for _, k := range keys {
		if ref, ok := r.segRef(k); ok && (!notLive || !r.s.liveKey(k)) {
			refs = append(refs, ref)
		}
	}

	return refs
}

// segJob returns the job of the history entry ref names, counting it in
// s.archivedDecodes.
func (r *historyRead) segJob(ref histRef) (*Job, error) {
	r.s.archivedDecodes.Add(1)

	job, _, err := r.segs.jobAt(ref, r.s.ch)

	return job, err
}

// segRecent returns the archived jobs of r's segments that ended at or after
// cutoff, but those live, those r's overlay copy has an entry of, and those
// whose entry history.bolt or a later segment outranks.
func (r *historyRead) segRecent(cutoff time.Time) ([]historyRecent, error) {
	ends := make(map[logKey]int64)

	err := r.segs.endedSince(cutoff, func(ref histRef, end int64) {
		k := ref.entry.key

		if cur, ok := r.segRef(k); ok && sameHistRecord(cur, ref) && !r.s.liveKey(k) {
			ends[k] = end
		}
	})
	if err != nil {
		return nil, err
	}

	keys := slices.Collect(maps.Keys(ends))

	jobs, err := r.jobs(keys)
	if err != nil {
		return nil, err
	}

	recent := make([]historyRecent, 0, len(jobs))
	for k, job := range jobs {
		recent = append(recent, historyRecent{order: endTimeIndexKey(endTimeToBytes(ends[k]), []byte(k.String())),
			job: job})
	}

	return recent, nil
}

// spendSegments charges budget for the history entry each of refs names, in
// the order they are in r's segments, at archivedFlatPrice, until it trips.
func (r *historyRead) spendSegments(refs []histRef, budget *archivedBytesBudget) error {
	return r.segs.eachRecord(refs, func(rec histJobRecord) error {
		return budget.spendLen(archivedFlatPrice(len(rec.spec), len(rec.state)))
	})
}

// archivedFlatPrice returns how many bytes spendArchivedBytesByRepGroup charges
// for a history entry of a flat spec of length spec and a flat run state of
// length state: the uncompressed length of its spec and complete records, as
// the log holds them, times archivedFlatScale per mille.
func archivedFlatPrice(spec, state int) int {
	return (2*(logRecordHeader+logKeyLen) + spec + state) * archivedFlatScale / 1000 //nolint:mnd // per mille
}

// sameHistRecord reports whether a and b name the same record.
func sameHistRecord(a, b histRef) bool {
	return a.seg == b.seg && a.entry.block == b.entry.block && a.entry.idx == b.entry.idx
}

// jobs returns the history entry's job of each of keys that has one, by key,
// reading each segment block once (see histIndex.eachRecord) and each overlay
// frame once (see overlayRecord), counting each it decodes in
// s.archivedDecodes.
func (r *historyRead) jobs(keys []logKey) (map[logKey]*Job, error) {
	jobs := make(map[logKey]*Job, len(keys))

	var refs []histRef

	for _, k := range keys {
		if ref, ok := r.segRef(k); ok {
			refs = append(refs, ref)

			continue
		}

		job, err := r.job(k)
		if err != nil {
			return nil, err
		}

		if job != nil {
			jobs[k] = job
		}
	}

	return jobs, r.segJobs(refs, jobs)
}

// segRef returns where k's history entry is in r's segments, if r finds it
// there: k is not in r's overlay copy, which is newer, and history.bolt holds
// no complete record of k of a higher seq. Phase 2 folds into segments alone,
// so history.bolt then holds only what a store held before; until it does,
// segments are only written by tests.
func (r *historyRead) segRef(k logKey) (histRef, bool) {
	if _, ok := r.overlay[k]; ok {
		return histRef{}, false
	}

	ref, ok := r.segs.lookup(k)
	if !ok {
		return histRef{}, false
	}

	v := r.tx.Bucket(bucketCompleteSeq).Get([]byte(k.String()))
	if len(v) == historySeqLen && binary.BigEndian.Uint64(v) > ref.entry.seq {
		return histRef{}, false
	}

	return ref, true
}

// segJobs adds to jobs the job of the history entry each of refs names, by
// key, reading each segment block once, counting each in s.archivedDecodes.
func (r *historyRead) segJobs(refs []histRef, jobs map[logKey]*Job) error {
	return r.segs.eachRecord(refs, func(rec histJobRecord) error {
		r.s.archivedDecodes.Add(1)

		job, err := jobOfFlat(slices.Clone(rec.spec), slices.Clone(rec.state), r.s.ch)
		jobs[rec.key] = job

		return err
	})
}

// histSectionKey names one segment's footer section.
type histSectionKey struct {
	n       uint32
	section histSection
}

// histCachedFooter is a cached footer section, as a footer holding only it,
// which segment's it is and its memory size.
type histCachedFooter struct {
	key    histSectionKey
	footer *histFooter
	size   int64
}

// histFooterCache is an LRU of parsed footer sections by segment number and
// section, holding at most histFooterCacheBytes of them (see
// histFooter.only). A section bigger than that is not cached. It holds
// sections rather than whole footers as a whole footer of a production-sized
// segment, 200,000 completions, takes about 46MB, so that the cache would hold
// one, and a query of a rep group in several segments would read every one of
// their footers each time; queries read only the rep group postings and end
// times, the key index already holds the key section, and a footer's other
// sections are read only when the store opens.
type histFooterCache struct {
	mu    sync.Mutex
	max   int64
	size  int64
	order *list.List // of *histCachedFooter, most recently used first
	byN   map[histSectionKey]*list.Element

	// reads counts the footers read from disk: inert observability, as
	// db.archivedDecodes.
	reads atomic.Int64
}

// newHistFooterCache returns an empty cache of histFooterCacheBytes.
func newHistFooterCache() *histFooterCache {
	return &histFooterCache{max: histFooterCacheBytes, order: list.New(), byN: make(map[histSectionKey]*list.Element)}
}

// get returns key's footer section from the cache, or from read, which also
// returns its memory size, caching it, and dropping the least recently used
// sections until the cache is back within its size. It does not hold the
// cache's lock while it reads.
func (c *histFooterCache) get(key histSectionKey, read func() (*histFooter, int64, error)) (*histFooter, error) {
	if footer := c.cached(key); footer != nil {
		return footer, nil
	}

	c.reads.Add(1)

	footer, size, err := read()
	if err != nil {
		return nil, err
	}

	c.put(key, footer, size)

	return footer, nil
}

// cached returns key's footer section, marking it most recently used, or nil.
func (c *histFooterCache) cached(key histSectionKey) *histFooter {
	c.mu.Lock()
	defer c.mu.Unlock()

	e := c.byN[key]
	if e == nil {
		return nil
	}

	c.order.MoveToFront(e)

	return e.Value.(*histCachedFooter).footer //nolint:forcetypeassert,errcheck // only these are put
}

// put caches key's footer section of size bytes, unless it is cached already
// or bigger than the cache, then drops the least recently used sections until
// the cache is within its size.
func (c *histFooterCache) put(key histSectionKey, footer *histFooter, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.byN[key] != nil || size > c.max {
		return
	}

	c.byN[key] = c.order.PushFront(&histCachedFooter{key: key, footer: footer, size: size})
	c.size += size

	for c.size > c.max {
		oldest := c.order.Remove(c.order.Back()).(*histCachedFooter) //nolint:forcetypeassert,errcheck // only these are put
		delete(c.byN, oldest.key)
		c.size -= oldest.size
	}
}

// histIndex is the in-memory index of a store's sealed history segments. Once
// published it is never changed (see withSegment); its footer cache and block
// read count are shared by every index of the store.
type histIndex struct {
	dir  string
	segs []*histSegment // ascending by number

	// repGroups, depGroups and revDeps are each rep group, dep group and
	// reverse dep group (from the footers' rep group, dep group and reverse
	// dep group sections) to the numbers of the segments holding it,
	// ascending.
	repGroups map[string][]uint32
	depGroups map[string][]uint32
	revDeps   map[string][]uint32

	// groupEnds is each rep group's latest completion end time, as a unix
	// time in seconds, as history.bolt's repgroupEndTime.
	groupEnds map[string]int64

	// dropped is each key a modify dropped the rglookups and reverse dep
	// group postings of to the number of the last segment it did so in: such
	// a posting of the key in an earlier segment, or in history.bolt, which is
	// older than every segment, is dropped.
	dropped map[logKey]uint32

	samples map[string]*histSamples

	// maxSeq is the highest complete seq in any segment, and folded the
	// folded position the last sealed segment covers, which is the folded
	// position durable across a crash (spec "Folder and history").
	maxSeq uint64
	folded historyPos

	footers *histFooterCache

	// blockReads counts the record blocks read: inert observability, as
	// footers.reads.
	blockReads *atomic.Int64
}

// loadHistIndex returns the index of the sealed segments in dir, reading each
// one's footer once. A segment whose trailer or footer is damaged is
// errStoreCorrupt.
func loadHistIndex(dir string) (*histIndex, error) {
	numbers, err := histSegmentNumbers(dir)
	if err != nil {
		return nil, err
	}

	x := newHistIndex(dir)

	for _, n := range numbers {
		footer, off, _, errr := readHistFooter(dir, n)
		if errr != nil {
			return nil, errr
		}

		x.add(n, off, footer)
	}

	return x, nil
}

// newHistIndex returns the index of no segments of dir.
func newHistIndex(dir string) *histIndex {
	return &histIndex{
		dir:        dir,
		repGroups:  make(map[string][]uint32),
		depGroups:  make(map[string][]uint32),
		revDeps:    make(map[string][]uint32),
		groupEnds:  make(map[string]int64),
		dropped:    make(map[logKey]uint32),
		samples:    make(map[string]*histSamples),
		footers:    newHistFooterCache(),
		blockReads: new(atomic.Int64),
	}
}

// withSegment returns a copy of x with sealed segment n of x's directory added
// after every segment x holds, leaving x as it was, so that a query reading x
// meanwhile needs no lock, and each of the segment's learned-requirement
// samples that x did not hold. It copies x's maps, but not the key indexes of
// its segments, which no index changes.
func (x *histIndex) withSegment(n uint32) (*histIndex, []histSample, error) {
	footer, off, _, err := readHistFooter(x.dir, n)
	if err != nil {
		return nil, nil, err
	}

	c := *x
	c.segs = slices.Clip(x.segs)
	c.repGroups, c.depGroups, c.revDeps = clippedHistGroups(x.repGroups), clippedHistGroups(x.depGroups),
		clippedHistGroups(x.revDeps)
	c.groupEnds, c.dropped = maps.Clone(x.groupEnds), maps.Clone(x.dropped)
	c.samples = maps.Clone(x.samples)

	var added []histSample

	for _, s := range footer.samples {
		if _, held := slices.BinarySearch(x.sampleValues(s.reqGroup, s.kind), s.value); !held {
			added = append(added, histSample{kind: s.kind, reqGroup: strings.Clone(s.reqGroup), value: s.value})
		}

		c.ownSamples(x, s.reqGroup)
	}

	c.add(n, off, footer)

	return &c, added, nil
}

// clippedHistGroups returns a copy of groups whose slices a later append
// copies, leaving groups' as they are.
func clippedHistGroups(groups map[string][]uint32) map[string][]uint32 {
	c := make(map[string][]uint32, len(groups))
	for name, ns := range groups {
		c[name] = slices.Clip(ns)
	}

	return c
}

// ownSamples gives x, a copy of from, a copy of reqGroup's samples, if it
// shares them with from, whose slices a later insert copies, so that adding to
// x's leaves from's as they are. It clones reqGroup, which may alias a footer's
// bytes, as a map assignment stores its key even when the map already has it.
func (x *histIndex) ownSamples(from *histIndex, reqGroup string) {
	samples := x.samples[reqGroup]
	if samples == nil || samples != from.samples[reqGroup] {
		return
	}

	owned := *samples
	for kind := range owned {
		owned[kind] = slices.Clip(owned[kind])
	}

	x.samples[strings.Clone(reqGroup)] = &owned
}

// add adds segment n, whose footer at off is footer, to x, after every segment
// it holds, changing x in place, so only on an index not yet published. x keeps
// footer's key index, but nothing of footer that aliases the bytes it was
// parsed from.
func (x *histIndex) add(n uint32, off uint64, footer *histFooter) {
	seg := &histSegment{n: n, blocks: footer.blocks, footerOff: off, keys: footer.keys, maxSeq: footer.maxSeq}

	if len(footer.endTimes) > 0 {
		seg.endMin, seg.endMax = footer.endTimes[0].end, footer.endTimes[len(footer.endTimes)-1].end
	}

	x.segs = append(x.segs, seg)
	addHistGroupSegments(x.repGroups, footer.repGroups, n)
	addHistGroupSegments(x.depGroups, footer.depGroups, n)
	addHistGroupSegments(x.revDeps, footer.revDeps, n)

	for _, g := range footer.repGroupEnds {
		if end, ok := x.groupEnds[g.name]; !ok || g.end > end {
			x.groupEnds[strings.Clone(g.name)] = g.end
		}
	}

	for _, k := range footer.drops {
		x.dropped[k] = n
	}

	for _, s := range footer.samples {
		x.addSample(s)
	}

	x.maxSeq = max(x.maxSeq, footer.maxSeq)
	x.folded = footer.folded
}

// addHistGroupSegments notes that segment n holds each of postings' groups. It
// clones each name, as a map assignment stores its key even when the map
// already has it, and postings' names alias their footer's bytes, which the
// index must not keep.
func addHistGroupSegments(groups map[string][]uint32, postings []histPosting, n uint32) {
	for _, p := range postings {
		name := strings.Clone(p.name)
		groups[name] = append(groups[name], n)
	}
}

// addSample adds s to the samples of its req group, once.
func (x *histIndex) addSample(s histSample) {
	samples := x.samples[s.reqGroup]
	if samples == nil {
		samples = new(histSamples)
		x.samples[strings.Clone(s.reqGroup)] = samples
	}

	values := samples[s.kind]
	if i, found := slices.BinarySearch(values, s.value); !found {
		samples[s.kind] = slices.Insert(values, i, s.value)
	}
}

// sampleValues returns reqGroup's samples of kind, sorted and distinct. The
// caller must not change them.
func (x *histIndex) sampleValues(reqGroup string, kind histSampleKind) []int {
	if samples := x.samples[reqGroup]; samples != nil {
		return samples[kind]
	}

	return nil
}

// lookup returns where k's history entry is, from the newest segment holding
// k: segments are folded in log order, and a fold records a complete record
// only above the seq of its key's last, so a newer segment's record of a key
// outranks an older one's.
func (x *histIndex) lookup(k logKey) (histRef, bool) {
	for _, seg := range slices.Backward(x.segs) {
		if entry, found := seg.entry(k); found {
			return histRef{seg: seg, entry: entry}, true
		}
	}

	return histRef{}, false
}

// holdsSince reports whether a segment of x holds a complete record of k of
// seq or higher. It searches only the key indexes of segments whose highest
// seq is that high, so a recent seq, as replay mostly asks about, searches
// few.
func (x *histIndex) holdsSince(k logKey, seq uint64) bool {
	for _, seg := range slices.Backward(x.segs) {
		if seg.maxSeq < seq {
			continue
		}

		if entry, found := seg.entry(k); found && entry.seq >= seq {
			return true
		}
	}

	return false
}

// job returns the job of k's history entry and its complete record's seq, or
// false if k has none, decoding specs with ch. A record that is not k's is
// errStoreCorrupt.
func (x *histIndex) job(k logKey, ch codec.Handle) (*Job, uint64, bool, error) {
	ref, ok := x.lookup(k)
	if !ok {
		return nil, 0, false, nil
	}

	job, seq, err := x.jobAt(ref, ch)

	return job, seq, err == nil, err
}

// jobAt returns the job of the history entry ref names and its complete
// record's seq, decoding its spec with ch.
func (x *histIndex) jobAt(ref histRef, ch codec.Handle) (*Job, uint64, error) {
	rec, err := x.record(ref)
	if err != nil {
		return nil, 0, err
	}

	parsed, err := parseHistJobRecordOf(rec, ref)
	if err != nil {
		return nil, 0, err
	}

	job, err := jobOfFlat(parsed.spec, parsed.state, ch)

	return job, parsed.seq, err
}

// parseHistJobRecordOf is parseHistJobRecord of rec, the record ref names,
// which is errStoreCorrupt if it is not ref's key's.
func parseHistJobRecordOf(rec []byte, ref histRef) (histJobRecord, error) {
	parsed, err := parseHistJobRecord(rec)
	if err == nil && parsed.key != ref.entry.key {
		err = fmt.Errorf("%w: the record of key %s in history segment %08x is of key %s", errStoreCorrupt,
			ref.entry.key, ref.seg.n, parsed.key)
	}

	return parsed, err
}

// record returns the record ref names, counting the block it reads.
func (x *histIndex) record(ref histRef) ([]byte, error) {
	f, err := os.Open(histSegmentPath(x.dir, ref.seg.n))
	if err != nil {
		return nil, err
	}

	x.blockReads.Add(1)

	rec, err := wal.ReadAt(f, wal.Ref{Off: ref.seg.blocks[ref.entry.block], Idx: ref.entry.idx})
	if errors.Is(err, wal.ErrBadRef) {
		err = fmt.Errorf("%w: history segment %08x: %w", errStoreCorrupt, ref.seg.n, err)
	}

	return rec, errors.Join(err, f.Close())
}

// section returns section of x's segment n's footer, as a footer holding only
// it, through x's footer cache.
func (x *histIndex) section(n uint32, section histSection) (*histFooter, error) {
	return x.footers.get(histSectionKey{n: n, section: section}, func() (*histFooter, int64, error) {
		footer, _, _, err := readHistFooter(x.dir, n)
		if err != nil {
			return nil, 0, err
		}

		only, size := footer.only(section)

		return only, size, nil
	})
}

// repGroupPosting returns the keys of the rglookups under repGroup in x's
// segment n, sorted.
func (x *histIndex) repGroupPosting(n uint32, repGroup string) ([]logKey, error) {
	return x.posting(n, histSectionRepGroups, repGroup)
}

// posting returns the keys under name in section, a postings section, of x's
// segment n, sorted.
func (x *histIndex) posting(n uint32, section histSection, name string) ([]logKey, error) {
	footer, err := x.section(n, section)
	if err != nil {
		return nil, err
	}

	postings := footer.repGroups
	if section == histSectionRevDeps {
		postings = footer.revDeps
	}

	i, found := slices.BinarySearchFunc(postings, name, func(p histPosting, name string) int {
		return strings.Compare(p.name, name)
	})
	if !found {
		return nil, nil
	}

	return postings[i].keys, nil
}

// lookedUp calls fn with the key of each rglookup under repGroup in x's
// segments that no modify has dropped since, as history.bolt's repgroupToKey
// would hold it: whichever segment holds its key's record, if any. A key
// looked up in more than one segment is given more than once.
func (x *histIndex) lookedUp(repGroup string, fn func(k logKey)) error {
	return x.validPostings(x.repGroups[repGroup], histSectionRepGroups, repGroup, fn)
}

// reverseDeps calls fn with the key of each reverse dep group posting under
// depGroup in x's segments that no modify has dropped since, as history.bolt's
// reverseDepgroupToKey would hold it: valid as an rglookup is (spec D2),
// whichever segment holds its key's record. A key with postings in more than
// one segment is given more than once.
func (x *histIndex) reverseDeps(depGroup string, fn func(k logKey)) error {
	return x.validPostings(x.revDeps[depGroup], histSectionRevDeps, depGroup, fn)
}

// validPostings calls fn with each key under name in section of each of x's
// segments numbered, that no modify has dropped since.
func (x *histIndex) validPostings(numbered []uint32, section histSection, name string, fn func(k logKey)) error {
	for _, n := range numbered {
		keys, err := x.posting(n, section, name)
		if err != nil {
			return err
		}

		for _, k := range keys {
			if !x.droppedSince(k, n) {
				fn(k)
			}
		}
	}

	return nil
}

// droppedSince reports whether a modify in a segment after n dropped k's
// rglookups and reverse dep group postings. One in segment n itself dropped
// only those before it, which the segment does not hold.
func (x *histIndex) droppedSince(k logKey, n uint32) bool {
	d, ok := x.dropped[k]

	return ok && d > n
}

// droppedAny reports whether a modify in any of x's segments dropped k's
// rglookups and reverse dep group postings, so those history.bolt holds, as
// Phase 2's history.bolt, a conversion's, is older than every segment.
func (x *histIndex) droppedAny(k logKey) bool {
	_, ok := x.dropped[k]

	return ok
}

// lookedUpUnder reports whether an rglookup under repGroup in x's segments,
// not since dropped by a modify, is of k.
func (x *histIndex) lookedUpUnder(k logKey, repGroup string) (bool, error) {
	for _, n := range x.repGroups[repGroup] {
		keys, err := x.repGroupPosting(n, repGroup)
		if err != nil {
			return false, err
		}

		if _, found := slices.BinarySearchFunc(keys, k, compareLogKeys); found && !x.droppedSince(k, n) {
			return true, nil
		}
	}

	return false, nil
}

// endedSince calls fn with the ref of each history entry in x's segments, in
// segment order, whose end time is at or after cutoff as the endTimeToKey
// index orders them (see endTimeSeekKey), and its end time, as UnixNano.
// Segments ending before cutoff are skipped unread.
func (x *histIndex) endedSince(cutoff time.Time, fn func(ref histRef, end int64)) error {
	seek := endTimeSeekKey(cutoff)
	since := func(end int64) bool { return bytes.Compare(endTimeToBytes(end), seek) >= 0 }

	for _, seg := range x.segs {
		if len(seg.keys) == 0 || !since(seg.endMax) {
			continue
		}

		footer, err := x.section(seg.n, histSectionEndTimes)
		if err != nil {
			return err
		}

		ends := footer.endTimes
		for _, e := range ends[sort.Search(len(ends), func(i int) bool { return since(ends[i].end) }):] {
			entry := histKeyEntry{key: e.key, block: e.block, idx: e.idx}
			fn(histRef{seg: seg, entry: entry}, e.end)
		}
	}

	return nil
}

// eachRecord calls fn with the completed-job record each of refs names, once
// for each record, decompressing each block once, in the order the records
// are in x's segments, not refs' order, until fn returns an error. rec aliases
// the block it is in, which fn must copy anything of to keep. It sorts refs.
func (x *histIndex) eachRecord(refs []histRef, fn func(rec histJobRecord) error) error {
	slices.SortFunc(refs, compareHistRefs)
	refs = slices.CompactFunc(refs, func(a, b histRef) bool { return compareHistRefs(a, b) == 0 })

	for len(refs) > 0 {
		n := histRun(refs, func(r histRef) bool { return r.seg == refs[0].seg })
		if err := x.eachRecordOf(refs[:n], fn); err != nil {
			return err
		}

		refs = refs[n:]
	}

	return nil
}

// compareHistRefs orders refs by segment, block and index in the block.
func compareHistRefs(a, b histRef) int {
	return cmp.Or(cmp.Compare(a.seg.n, b.seg.n), cmp.Compare(a.entry.block, b.entry.block),
		cmp.Compare(a.entry.idx, b.entry.idx))
}

// histRun returns how many of refs, from the first, same says are like it.
func histRun(refs []histRef, same func(r histRef) bool) int {
	n := 1
	for n < len(refs) && same(refs[n]) {
		n++
	}

	return n
}

// eachRecordOf is eachRecord of refs, sorted and all of one segment.
func (x *histIndex) eachRecordOf(refs []histRef, fn func(rec histJobRecord) error) error {
	seg := refs[0].seg

	f, err := os.Open(histSegmentPath(x.dir, seg.n))
	if err != nil {
		return err
	}

	var bufs wal.FrameBuffers

	for len(refs) > 0 && err == nil {
		n := histRun(refs, func(r histRef) bool { return r.entry.block == refs[0].entry.block })
		err = x.eachBlockRecord(f, &bufs, refs[:n], fn)
		refs = refs[n:]
	}

	return errors.Join(err, f.Close())
}

// eachBlockRecord calls fn with the record each of refs names, all of one
// block of f, sorted by index, reading the block once.
func (x *histIndex) eachBlockRecord(f *os.File, bufs *wal.FrameBuffers, refs []histRef,
	fn func(rec histJobRecord) error,
) error {
	x.blockReads.Add(1)

	next := 0

	err := wal.ReadFrameInto(f, refs[0].seg.blocks[refs[0].entry.block], bufs, func(ref wal.Ref, rec []byte) error {
		if next == len(refs) || ref.Idx != refs[next].entry.idx {
			return nil
		}

		parsed, err := parseHistJobRecordOf(rec, refs[next])
		if err != nil {
			return err
		}

		next++

		return fn(parsed)
	})
	if err == nil && next < len(refs) {
		err = fmt.Errorf("%w: no record %d in its block", wal.ErrBadRef, refs[next].entry.idx)
	}

	if errors.Is(err, wal.ErrBadRef) {
		err = fmt.Errorf("%w: history segment %08x: %w", errStoreCorrupt, refs[0].seg.n, err)
	}

	return err
}

// segs returns s's published index of its sealed history segments.
func (s *logStore) segs() *histIndex {
	return s.segments.Load()
}

// ownedJobState returns state, as a constant if it is a complete job's, else
// as a copy, so that it does not alias the buffer state was decoded from.
func ownedJobState(state JobState) JobState {
	if state == JobStateComplete {
		return JobStateComplete
	}

	return JobState(strings.Clone(string(state)))
}

// segOffer is what selectOldest offers its scan of the history entry in r's
// segments of one of its keys: the group query put it in, unless query
// discarded it, and the times its selector orders it by. ok is whether the
// key's entry is in r's segments.
type segOffer struct {
	start, end time.Time
	group      string
	keep, ok   bool
}

// segOffers returns the segOffer of each of keys, sorted, by position, reading
// each segment block once and decoding no spec, or nil if r has no segments.
// It puts each entry in its group as it reads it, so that it holds only its
// times and its group, one string of each name, rather than its run state,
// which on a rep group of millions of completions would be gigabytes: query's
// group func must therefore not depend on the order it is asked in. The
// facets it is given own their strings, as the bbolt store's do.
func (r *historyRead) segOffers(keys []logKey, query completeJobsQuery) ([]segOffer, error) {
	if len(r.segs.segs) == 0 {
		return nil, nil
	}

	offers := make([]segOffer, len(keys))
	groups := make(map[string]string)

	err := r.eachSegRunState(keys, func(i int, rs *logRunState) {
		facets := archivedJobFacets{StartTime: rs.StartTime, EndTime: rs.EndTime, State: ownedJobState(rs.State),
			Exitcode: rs.Exitcode, FailReason: strings.Clone(rs.FailReason), Lost: rs.Lost}
		group, keep := query.group(&facets)

		if interned, ok := groups[group]; ok {
			group = interned
		} else {
			groups[group] = group
		}

		offers[i] = segOffer{start: rs.StartTime, end: rs.EndTime, group: group, keep: keep, ok: true}
	})

	return offers, err
}

// segUsage is the usage addStatus adds of the history entry in r's segments of
// one of its keys, if ok, which is whether the key's entry is in r's segments.
type segUsage struct {
	usage completeJobUsage
	ok    bool
}

// segUsages returns the segUsage of each of keys, sorted, by position, reading
// each segment block once and decoding no spec, or nil if r has no segments.
func (r *historyRead) segUsages(keys []logKey) ([]segUsage, error) {
	if len(r.segs.segs) == 0 {
		return nil, nil
	}

	usages := make([]segUsage, len(keys))

	err := r.eachSegRunState(keys, func(i int, rs *logRunState) {
		usages[i] = segUsage{usage: completeJobUsage{PeakRAM: rs.PeakRAM, PeakDisk: rs.PeakDisk,
			StartTime: rs.StartTime, EndTime: rs.EndTime, CPUtime: rs.CPUtime, State: ownedJobState(rs.State)},
			ok: true}
	})

	return usages, err
}

// eachSegRunState calls fn with the position in keys, which must be sorted, of
// the key of each history entry r finds in its segments, but those of live
// keys, and the entry's run state, in the order the entries are in the
// segments, reading each block once and decoding no spec. rs's strings alias
// a block buffer reused for the next block, and rs is reused for the next
// entry, so fn must copy what it keeps of them.
func (r *historyRead) eachSegRunState(keys []logKey, fn func(i int, rs *logRunState)) error {
	var rs logRunState

	return r.segs.eachRecord(r.segRefs(keys, true), func(rec histJobRecord) error {
		if err := decodeFlatRunState(rec.state, &rs); err != nil {
			return err
		}

		i, _ := slices.BinarySearchFunc(keys, rec.key, compareLogKeys)
		fn(i, &rs)

		return nil
	})
}

// hasHistory reports whether k has a history entry in s's in-memory history
// key index or its segments. The caller holds s.mu.
func (s *logStore) hasHistory(k logKey) bool {
	return s.state.hasHistoryEntry(k)
}

// addSegment publishes, in place of s's index of its sealed history segments,
// one that also holds sealed segment n, which must be after every segment it
// holds, and makes the learned requirements of each req group it gives a
// sample neither history nor the index held stale. Queries that took the old
// index go on reading it. One add is made at a time.
func (s *logStore) addSegment(n uint32) error {
	s.segMu.Lock()
	defer s.segMu.Unlock()

	x, added, err := s.segs().withSegment(n)
	if err != nil {
		return err
	}

	stale, err := s.newSampleReqGroups(added)
	if err != nil {
		return err
	}

	// Publish before invalidating: a load of a stale req group's learned
	// requirements between an invalidation and the publish would read the old
	// index and cache the result as current, which nothing would invalidate
	// again.
	s.segments.Store(x)

	for _, reqGroup := range stale {
		s.reqStats.invalidate(reqGroup)
	}

	return nil
}
