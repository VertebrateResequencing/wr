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
// per segment rather than a Go map of keys; rep group and dep group to the
// segments holding them; each segment's end-time range; the learned-requirement
// samples; and the footers, read again on demand through an LRU of at most
// histFooterCacheBytes. The store loads it from the sealed segments when it
// opens.

import (
	"bytes"
	"container/list"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/ugorji/go/codec"
)

// histFooterCacheBytes is the most memory the footers the history index caches
// may take (see histFooter.memSize). A var so tests can shrink it.
var histFooterCacheBytes int64 = 64 << 20 //nolint:gochecknoglobals // tuning knob, varied by tests

// histSegment is what the history index holds of one sealed segment: its
// number, block offsets, the offset of its footer, its key index, sorted by
// key, and the range of its completions' end times, as UnixNano.
type histSegment struct {
	n              uint32
	blocks         []uint64
	footerOff      uint64
	keys           []histKeyEntry
	endMin, endMax int64
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

// histCachedFooter is a cached footer, its segment number and memory size.
type histCachedFooter struct {
	n      uint32
	footer *histFooter
	size   int64
}

// histFooterCache is an LRU of parsed footers by segment number, holding at
// most histFooterCacheBytes of them (see histFooter.memSize). A footer bigger
// than that is not cached.
type histFooterCache struct {
	mu    sync.Mutex
	max   int64
	size  int64
	order *list.List // of *histCachedFooter, most recently used first
	byN   map[uint32]*list.Element

	// reads counts the footers read from disk: inert observability, as
	// db.archivedDecodes.
	reads atomic.Int64
}

// newHistFooterCache returns an empty cache of histFooterCacheBytes.
func newHistFooterCache() *histFooterCache {
	return &histFooterCache{max: histFooterCacheBytes, order: list.New(), byN: make(map[uint32]*list.Element)}
}

// get returns segment n's footer from the cache, or from read, which also
// returns its memory size, caching it, and dropping the least recently used
// footers until the cache is back within its size. It does not hold the
// cache's lock while it reads.
func (c *histFooterCache) get(n uint32, read func() (*histFooter, int64, error)) (*histFooter, error) {
	if footer := c.cached(n); footer != nil {
		return footer, nil
	}

	c.reads.Add(1)

	footer, size, err := read()
	if err != nil {
		return nil, err
	}

	c.put(n, footer, size)

	return footer, nil
}

// cached returns segment n's footer, marking it most recently used, or nil.
func (c *histFooterCache) cached(n uint32) *histFooter {
	c.mu.Lock()
	defer c.mu.Unlock()

	e := c.byN[n]
	if e == nil {
		return nil
	}

	c.order.MoveToFront(e)

	return e.Value.(*histCachedFooter).footer //nolint:forcetypeassert,errcheck // only these are put
}

// put caches segment n's footer of size bytes, unless it is cached already or
// bigger than the cache, then drops the least recently used footers until the
// cache is within its size.
func (c *histFooterCache) put(n uint32, footer *histFooter, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.byN[n] != nil || size > c.max {
		return
	}

	c.byN[n] = c.order.PushFront(&histCachedFooter{n: n, footer: footer, size: size})
	c.size += size

	for c.size > c.max {
		oldest := c.order.Remove(c.order.Back()).(*histCachedFooter) //nolint:forcetypeassert,errcheck // only these are put
		delete(c.byN, oldest.n)
		c.size -= oldest.size
	}
}

// histIndex is the in-memory index of a store's sealed history segments.
type histIndex struct {
	dir  string
	segs []*histSegment // ascending by number

	// repGroups and depGroups are each rep group and dep group (from the
	// footers' rep group and dep group sections) to the numbers of the
	// segments holding it, ascending.
	repGroups map[string][]uint32
	depGroups map[string][]uint32

	samples map[string]*histSamples

	// maxSeq is the highest complete seq in any segment, and folded the
	// folded position the last sealed segment covers, which is the folded
	// position durable across a crash (spec "Folder and history").
	maxSeq uint64
	folded historyPos

	footers *histFooterCache
}

// loadHistIndex returns the index of the sealed segments in dir, reading each
// one's footer once. A segment whose trailer or footer is damaged is
// errStoreCorrupt.
func loadHistIndex(dir string) (*histIndex, error) {
	numbers, err := histSegmentNumbers(dir)
	if err != nil {
		return nil, err
	}

	x := &histIndex{
		dir:       dir,
		repGroups: make(map[string][]uint32),
		depGroups: make(map[string][]uint32),
		samples:   make(map[string]*histSamples),
		footers:   newHistFooterCache(),
	}

	for _, n := range numbers {
		footer, off, _, errr := readHistFooter(dir, n)
		if errr != nil {
			return nil, errr
		}

		x.add(n, off, footer)
	}

	return x, nil
}

// add adds segment n, whose footer at off is footer, to x, after every segment
// it holds. x keeps footer's key
// index, but nothing of footer that aliases the bytes it was parsed from.
func (x *histIndex) add(n uint32, off uint64, footer *histFooter) {
	seg := &histSegment{n: n, blocks: footer.blocks, footerOff: off, keys: footer.keys}

	if len(footer.endTimes) > 0 {
		seg.endMin, seg.endMax = footer.endTimes[0].end, footer.endTimes[len(footer.endTimes)-1].end
	}

	x.segs = append(x.segs, seg)
	addHistGroupSegments(x.repGroups, footer.repGroups, n)
	addHistGroupSegments(x.depGroups, footer.depGroups, n)

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
		i, found := slices.BinarySearchFunc(seg.keys, k, func(e histKeyEntry, k logKey) int {
			return bytes.Compare(e.key[:], k[:])
		})

		if found {
			return histRef{seg: seg, entry: seg.keys[i]}, true
		}
	}

	return histRef{}, false
}

// job returns the job of k's history entry and its complete record's seq, or
// false if k has none, decoding specs with ch. A record that is not k's is
// errStoreCorrupt.
func (x *histIndex) job(k logKey, ch codec.Handle) (*Job, uint64, bool, error) {
	ref, ok := x.lookup(k)
	if !ok {
		return nil, 0, false, nil
	}

	rec, err := x.record(ref)
	if err != nil {
		return nil, 0, false, err
	}

	parsed, err := parseHistJobRecord(rec)
	if err == nil && parsed.key != k {
		err = fmt.Errorf("%w: the record of key %s in history segment %08x is of key %s", errStoreCorrupt, k,
			ref.seg.n, parsed.key)
	}

	if err != nil {
		return nil, 0, false, err
	}

	job, err := jobOfFlat(parsed.spec, parsed.state, ch)

	return job, parsed.seq, err == nil, err
}

// record returns the record ref names.
func (x *histIndex) record(ref histRef) ([]byte, error) {
	f, err := os.Open(histSegmentPath(x.dir, ref.seg.n))
	if err != nil {
		return nil, err
	}

	rec, err := wal.ReadAt(f, wal.Ref{Off: ref.seg.blocks[ref.entry.block], Idx: ref.entry.idx})
	if errors.Is(err, wal.ErrBadRef) {
		err = fmt.Errorf("%w: history segment %08x: %w", errStoreCorrupt, ref.seg.n, err)
	}

	return rec, errors.Join(err, f.Close())
}

// footer returns the footer of x's segment n, through x's footer cache.
func (x *histIndex) footer(n uint32) (*histFooter, error) {
	return x.footers.get(n, func() (*histFooter, int64, error) {
		footer, _, length, err := readHistFooter(x.dir, n)
		if err != nil {
			return nil, 0, err
		}

		return footer, footer.memSize(length), nil
	})
}
