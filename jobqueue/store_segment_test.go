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

// Spec D2's tests: sealed history segments, their footers and trailers, and
// the in-memory index the store loads from them when it opens.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
)

const (
	// segTestRepGroup is the rep group of the jobs segment tests complete,
	// and segTestLookupGroup one only rglookups name.
	segTestRepGroup    = "segrg"
	segTestLookupGroup = "lookedup"

	// segTestKeys is how many keys test 5 indexes, and segTestKeySegments
	// over how many segments.
	segTestKeys        = 1_000_000
	segTestKeySegments = 4

	// segTestKeyIndexMax is the most heap test 5's key index may take: 100MB
	// at 2.15M keys, scaled to 1M.
	segTestKeyIndexMax = 47 << 20

	// segTestFooterKeys is how many keys the footer memSize test's segment
	// holds: enough that each key's few dozen bytes far outweigh heap noise.
	segTestFooterKeys = 200_000

	// segTestFooterSlack is the divisor of the footer's heap growth that
	// memSize may differ from it by: 1%.
	segTestFooterSlack = 100

	// segTestBigFooterKeys is how many keys the footer cache test's oversized
	// segment holds: enough that its footer outweighs two of one key.
	segTestBigFooterKeys = 20

	// segTestBarrierWait is how long a footer read waits for the other
	// concurrent read to start: far longer than starting a goroutine takes.
	segTestBarrierWait = 10 * time.Second
)

// errSegTestBarrier is a footer read's error when the other concurrent read
// never started.
var errSegTestBarrier = errors.New("the other read never started")

// segTestEnd is the end time of the first job segment tests complete; each
// later one ends a second later.
var segTestEnd = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) //nolint:gochecknoglobals // a test constant

const (
	// segRevA, segRevB and segRevC are the dep groups
	// TestHistSegmentReverseDeps's job depends on.
	segRevA = "segRevA"
	segRevB = "segRevB"
	segRevC = "segRevC"

	// segRevSeqOrder and segRevLaterFirst are the orders
	// TestHistSegmentReverseDeps writes two completions of its job in.
	segRevSeqOrder = "seq order"

	// histSiblingSegments is the last segment TestHistIndex adds to copies of
	// its index, one after another, so the copies' slices pass several
	// capacities.
	histSiblingSegments = 12
	segRevLaterFirst    = "later completion first"
)

// segDamage is a way of damaging a sealed segment.
type segDamage struct {
	name   string
	damage func(data []byte) []byte
}

// segDamages are the ways a sealed segment can be damaged that make the store
// holding it corrupt.
var segDamages = []segDamage{ //nolint:gochecknoglobals // a test table
	{"its trailer CRC damaged", func(data []byte) []byte {
		data[len(data)-12] ^= 0xff

		return data
	}},
	{"its trailer magic damaged", func(data []byte) []byte {
		data[len(data)-1] ^= 0xff

		return data
	}},
	{"its footer offset damaged", func(data []byte) []byte {
		data[len(data)-histTrailerLen] ^= 0x01

		return data
	}},
	{"a byte of its footer damaged", func(data []byte) []byte {
		data[len(data)-histTrailerLen-3] ^= 0xff

		return data
	}},
	{"its last byte cut off", func(data []byte) []byte { return data[:len(data)-1] }},
	{"less than a trailer left", func(data []byte) []byte { return data[:histTrailerLen-1] }},
	{"a valid trailer naming its first block", func(data []byte) []byte { return withTrailerOffset(data, 0) }},
	{"a valid trailer naming the middle of its footer", func(data []byte) []byte {
		before, _ := segTrailer(data)

		return withTrailerOffset(data, uint64(len(before))+3)
	}},
	{"bytes between its footer and its trailer", func(data []byte) []byte {
		before, footer := segTrailer(data)
		out := append(wal.AppendFrame(slices.Clone(before), footer), 0)

		return append(out, histTrailer(uint64(len(before)))...)
	}},
	{"a whole footer frame that is not a footer", func(data []byte) []byte {
		_, footer := segTrailer(data)

		return withFooter(data, append(slices.Clone(footer), 0))
	}},
}

// withTrailerOffset returns a segment's bytes with a valid trailer naming off.
func withTrailerOffset(data []byte, off uint64) []byte {
	return append(slices.Clone(data[:len(data)-histTrailerLen]), histTrailer(off)...)
}

func TestHistSegmentWriter(t *testing.T) {
	Convey("Given 1000 completions, 2 of one key, rglookups and samples written to a sealed segment", t, func() {
		dir := t.TempDir()

		complete := make([]historyFolded, 0, 1002)
		for i := range 1000 {
			complete = append(complete, segCompletion(i, segTestRepGroup, uint64(i+10)))
		}

		again := segCompletion(7, segTestRepGroup, 5000)
		again.job.EndTime = segTestEnd.Add(time.Hour)
		again.job.PeakRAM = 77
		older := segCompletion(8, segTestRepGroup, 1)
		older.job.PeakRAM = 88
		complete = append(complete, again, older)

		lookups := []logKey{complete[0].key, {0xff, 1}}
		samples := []histSample{
			{kind: histSampleRAM, reqGroup: "q", value: 100},
			{kind: histSampleSecs, reqGroup: "q", value: 3},
			{kind: histSampleRAM, reqGroup: "q", value: 100},
			{kind: histSampleRAM, reqGroup: "a", value: 7},
		}
		folded := historyPos{file: 3, off: 1234}

		writeSegment(dir, 2, complete, lookups, samples, folded)

		Convey("it is NNNNNNNN.hist, with no .tmp left, in more than one block, none over 64KiB", func() {
			So(histSegmentPath(dir, 2), ShouldEqual, filepath.Join(dir, "00000002.hist"))
			So(histSegmentPath(dir, 2)+storeTmpExt, shouldNotExist)

			sizes := segBlockSizes(dir, 2)
			So(len(sizes), ShouldBeGreaterThan, 1)
			So(slices.Max(sizes), ShouldBeLessThanOrEqualTo, 65536)
		})

		Convey("its footer holds what was written, each section sorted", func() {
			footer, _, _, err := readHistFooter(dir, 2)
			So(err, ShouldBeNil)

			So(footer.folded, ShouldResemble, folded)
			So(footer.maxSeq, ShouldEqual, 5000)
			So(footer.keys, ShouldHaveLength, 1000)

			names := func(postings []histPosting) []string {
				out := make([]string, 0, len(postings))
				for _, p := range postings {
					out = append(out, fmt.Sprintf("%s:%d", p.name, len(p.keys)))
				}

				return out
			}

			So(names(footer.repGroups), ShouldResemble, []string{"lookedup:2"})
			So(names(footer.depGroups), ShouldResemble, []string{"segdg0:500", "segdg1:500"})
			So(names(footer.revDeps), ShouldResemble, []string{"segdep0:334", "segdep1:333", "segdep2:333"})
			So(footer.depGroupsSeen, ShouldResemble, []string{"segdg0", "segdg1"})
			So(footer.repGroupEnds, ShouldResemble, []histGroupEnd{
				{name: segTestRepGroup, end: segTestEnd.Add(time.Hour).Unix()},
			})
			So(footer.samples, ShouldResemble, []histSample{
				{kind: histSampleRAM, reqGroup: "a", value: 7},
				{kind: histSampleRAM, reqGroup: "q", value: 100},
				{kind: histSampleSecs, reqGroup: "q", value: 3},
			})
			So(footer.endTimes, ShouldHaveLength, 1000)
			So(footer.endTimes[len(footer.endTimes)-1].end, ShouldEqual, segTestEnd.Add(time.Hour).UnixNano())
			So(footer.endTimes[len(footer.endTimes)-1].key, ShouldEqual, complete[7].key)
			So(footer.wellFormed(), ShouldBeTrue)
		})

		Convey("loading it indexes every key, each to its record of the highest seq", func() {
			x, err := loadHistIndex(dir)
			So(err, ShouldBeNil)

			mismatched := 0

			for i := range 1000 {
				want := complete[i]
				if i == 7 {
					want = again
				}

				if !segJobMatches(x, want.key, want.job, want.seq) {
					mismatched++
				}
			}

			So(mismatched, ShouldEqual, 0)

			ch := new(codec.BincHandle)

			job, seq, found, err := x.job(complete[8].key, ch)
			So(err, ShouldBeNil)
			So(found, ShouldBeTrue)
			So(seq, ShouldEqual, complete[8].seq)
			So(job.PeakRAM, ShouldNotEqual, 88)

			_, _, found, err = x.job(logKey{0xff, 1}, ch)
			So(err, ShouldBeNil)
			So(found, ShouldBeFalse)
		})

		Convey("reading a key's record is errStoreCorrupt if", func() {
			x, err := loadHistIndex(dir)
			So(err, ShouldBeNil)

			seg := x.segs[0]

			Convey("its entry names another key's record", func() {
				seg.keys[0].block, seg.keys[0].idx = seg.keys[1].block, seg.keys[1].idx

				_, _, _, err = x.job(seg.keys[0].key, new(codec.BincHandle))
				So(err, ShouldWrap, errStoreCorrupt)
			})

			Convey("its block is damaged", func() {
				damageSegment(dir, 2, func(data []byte) []byte {
					data[seg.blocks[seg.keys[0].block]+wal.FrameHeader+1] ^= 0xff

					return data
				})

				_, _, _, err = x.job(seg.keys[0].key, new(codec.BincHandle))
				So(err, ShouldWrap, errStoreCorrupt)
			})

			Convey("its entry names a record past the end of its block, read with others of the block", func() {
				past := seg.keys[0]
				past.idx = math.MaxUint32
				calls := 0

				err = x.eachRecord([]histRef{{seg: seg, entry: seg.keys[0]}, {seg: seg, entry: past}},
					func(histJobRecord) error {
						calls++

						return nil
					})
				So(err, ShouldWrap, errStoreCorrupt)
				So(calls, ShouldEqual, 1)
			})
		})
	})
}

func TestHistIndex(t *testing.T) {
	Convey("Given sealed segments 1 and 3, a key in both, when loaded", t, func() {
		dir := t.TempDir()

		first := []historyFolded{segCompletion(0, segTestRepGroup, 10), segCompletion(1, "segother", 11)}
		later := segCompletion(0, segTestRepGroup, 20)
		later.job.EndTime = segTestEnd.Add(time.Hour)
		third := []historyFolded{later, segCompletion(2, "third", 21)}

		writeSegment(dir, 1, first, nil, []histSample{
			{kind: histSampleRAM, reqGroup: "q", value: 300},
			{kind: histSampleRAM, reqGroup: "q", value: 100},
		}, historyPos{file: 2, off: 9})
		writeSegment(dir, 3, third, []logKey{{0xaa}}, []histSample{
			{kind: histSampleRAM, reqGroup: "q", value: 200},
			{kind: histSampleRAM, reqGroup: "q", value: 100},
			{kind: histSampleDisk, reqGroup: "q", value: 5},
		}, historyPos{file: 4, off: 1})

		So(os.WriteFile(filepath.Join(dir, "00000005.hist.tmp"), []byte("being written"), dbFilePermission),
			ShouldBeNil)

		x, err := loadHistIndex(dir)
		So(err, ShouldBeNil)

		Convey("each group maps to the segments holding it, and each segment to its end-time range", func() {
			So(x.segs, ShouldHaveLength, 2)
			So(x.repGroups, ShouldResemble, map[string][]uint32{segTestLookupGroup: {3}})
			So(x.depGroups, ShouldResemble, map[string][]uint32{"segdg0": {1, 3}, "segdg1": {1}})
			So(x.segs[0].endMin, ShouldEqual, segTestEnd.UnixNano())
			So(x.segs[0].endMax, ShouldEqual, segTestEnd.Add(time.Second).UnixNano())
			So(x.segs[1].endMin, ShouldEqual, segTestEnd.Add(2*time.Second).UnixNano())
			So(x.segs[1].endMax, ShouldEqual, segTestEnd.Add(time.Hour).UnixNano())
		})

		Convey("the samples are each segment's, sorted and once each", func() {
			So(x.sampleValues("q", histSampleRAM), ShouldResemble, []int{100, 200, 300})
			So(x.sampleValues("q", histSampleDisk), ShouldResemble, []int{5})
			So(x.sampleValues("q", histSampleSecs), ShouldBeEmpty)
			So(x.sampleValues("none", histSampleRAM), ShouldBeEmpty)
		})

		Convey("the highest seq is the segments', and the folded position the last one's", func() {
			So(x.maxSeq, ShouldEqual, 21)
			So(x.folded, ShouldResemble, historyPos{file: 4, off: 1})
		})

		Convey("copies each adding a different later segment to one index leave each other as they were", func() {
			for n := uint32(4); n <= histSiblingSegments+1; n++ {
				writeSegment(dir, n, nil, []logKey{{byte(n)}}, nil, historyPos{})
			}

			cur := x
			mixed := 0

			for n := uint32(4); n <= histSiblingSegments; n++ {
				a, _, erra := cur.withSegment(n)
				So(erra, ShouldBeNil)

				_, _, errb := cur.withSegment(n + 1)
				So(errb, ShouldBeNil)

				lookedUp := a.repGroups[segTestLookupGroup][1:]
				if a.segs[len(a.segs)-1].n != n || !slices.Equal(lookedUp, segNumbersFrom(4, n)) {
					mixed++
				}

				cur = a
			}

			So(mixed, ShouldEqual, 0)
		})

		Convey("the key in both is found in the newer segment", func() {
			ref, found := x.lookup(first[0].key)
			So(found, ShouldBeTrue)
			So(ref.seg.n, ShouldEqual, 3)

			job, seq, found, errj := x.job(first[0].key, new(codec.BincHandle))
			So(errj, ShouldBeNil)
			So(found, ShouldBeTrue)
			So(seq, ShouldEqual, 20)
			So(job.EndTime.Equal(later.job.EndTime), ShouldBeTrue)

			ref, found = x.lookup(first[1].key)
			So(found, ShouldBeTrue)
			So(ref.seg.n, ShouldEqual, 1)
		})
	})

	Convey("Given a directory with no segments, the index is empty", t, func() {
		x, err := loadHistIndex(t.TempDir())
		So(err, ShouldBeNil)
		So(x.segs, ShouldBeEmpty)
		So(x.maxSeq, ShouldEqual, 0)

		_, found := x.lookup(logKey{1})
		So(found, ShouldBeFalse)
	})
}

func TestHistSegmentGroupSections(t *testing.T) {
	Convey("Given a key completed under rep group A and dep group X, then under rep group B and dep group Y", t,
		func() {
			first, second := segRegroupedCompletions()
			wantSeen := []string{"segX", "segY"}
			wantEnds := segRegroupedEnds(first, second)

			Convey("one segment holding both has bbolt's rep group end times and both dep groups seen", func() {
				dir := t.TempDir()
				writeSegment(dir, 1, []historyFolded{first, second}, nil, nil, historyPos{})

				footer, _, _, err := readHistFooter(dir, 1)
				So(err, ShouldBeNil)
				So(footer.repGroupEnds, ShouldResemble, wantEnds)
				So(footer.depGroupsSeen, ShouldResemble, wantSeen)
				So(footer.wellFormed(), ShouldBeTrue)
			})

			Convey("two segments, one each, hold the same between them", func() {
				dir := t.TempDir()
				writeSegment(dir, 1, []historyFolded{first}, nil, nil, historyPos{})
				writeSegment(dir, 2, []historyFolded{second}, nil, nil, historyPos{})

				var (
					ends []histGroupEnd
					seen []string
				)

				for n := range uint32(2) {
					footer, _, _, err := readHistFooter(dir, n+1)
					So(err, ShouldBeNil)

					ends = append(ends, footer.repGroupEnds...)
					seen = append(seen, footer.depGroupsSeen...)
				}

				So(ends, ShouldResemble, wantEnds)
				So(seen, ShouldResemble, wantSeen)
			})

			Convey("one segment holding the second before the first has only the second's, as bbolt skips "+
				"a completion of a lower seq than its key's", func() {
				dir := t.TempDir()
				writeSegment(dir, 1, []historyFolded{second, first}, nil, nil, historyPos{})

				footer, _, _, err := readHistFooter(dir, 1)
				So(err, ShouldBeNil)
				So(footer.repGroupEnds, ShouldResemble, wantEnds[1:])
				So(footer.depGroupsSeen, ShouldResemble, wantSeen[1:])
				So(footer.keys, ShouldHaveLength, 1)
				So(footer.keys[0].seq, ShouldEqual, second.seq)
			})
		})
}

// segRegroupedCompletions returns two completions of one key: the first under
// rep group segA and dep group segX, the second, of a higher seq and ending an
// hour later, under rep group segB and dep group segY.
func segRegroupedCompletions() (first, second historyFolded) {
	first = segCompletion(0, "segA", 10)
	first.job.DepGroups = []string{"segX"}
	second = segCompletion(0, "segB", 20)
	second.job.DepGroups = []string{"segY"}
	second.job.EndTime = segTestEnd.Add(time.Hour)
	So(second.key, ShouldEqual, first.key)

	return first, second
}

// segRegroupedEnds returns the rep group end times history.bolt holds once
// segRegroupedCompletions' first and second are folded: each rep group's, as
// repgroupEndTime grows with every completion (TestHistoryBoltRegroupedEnds).
func segRegroupedEnds(first, second historyFolded) []histGroupEnd {
	return []histGroupEnd{
		{name: "segA", end: first.job.EndTime.Unix()},
		{name: "segB", end: second.job.EndTime.Unix()},
	}
}

func TestHistFooterCache(t *testing.T) {
	Convey("Given 3 sealed segments and a footer cache that holds 2 of their end time sections", t, func() {
		dir := t.TempDir()

		var largest int64

		for n := range uint32(3) {
			writeSegment(dir, n+1, []historyFolded{segCompletion(int(n), segTestRepGroup, uint64(n+1))}, nil,
				nil, historyPos{})

			largest = max(largest, segSectionSize(dir, n+1, histSectionEndTimes))
		}

		old := histFooterCacheBytes
		histFooterCacheBytes = 2*largest + 1

		Reset(func() { histFooterCacheBytes = old })

		x, err := loadHistIndex(dir)
		So(err, ShouldBeNil)

		reads := func(ns ...uint32) int64 {
			before := x.footers.reads.Load()

			for _, n := range ns {
				footer, errf := x.section(n, histSectionEndTimes)
				So(errf, ShouldBeNil)
				So(footer.endTimes, ShouldHaveLength, 1)
			}

			return x.footers.reads.Load() - before
		}

		Convey("loading the index caches no section", func() {
			So(reads(1), ShouldEqual, 1)
		})

		Convey("a section read twice is read from disk once", func() {
			So(reads(1, 1), ShouldEqual, 1)
		})

		Convey("another section of a cached footer's segment is read from disk, and cached apart", func() {
			So(reads(1), ShouldEqual, 1)

			before := x.footers.reads.Load()
			footer, errf := x.section(1, histSectionRepGroups)
			So(errf, ShouldBeNil)
			So(footer.repGroups, ShouldBeEmpty)
			So(footer.endTimes, ShouldBeEmpty)
			So(x.footers.reads.Load()-before, ShouldEqual, 1)
			So(reads(1), ShouldEqual, 0)
		})

		Convey("a cached section holds only its own section, aliasing nothing of the footer's bytes", func() {
			footer, _, _, errr := readHistFooter(dir, 1)
			So(errr, ShouldBeNil)

			only, size := footer.only(histSectionEndTimes)
			So(only.endTimes, ShouldResemble, footer.endTimes)
			So(only.keys, ShouldBeEmpty)
			So(only.repGroups, ShouldBeEmpty)
			So(size, ShouldEqual, only.memSize(0))

			raw := testFooter().encode()
			parsed, errp := parseHistFooter(raw)
			So(errp, ShouldBeNil)

			postings, _ := parsed.only(histSectionRepGroups)

			clear(raw)
			So(postings.repGroups, ShouldResemble, testFooter().repGroups)
		})

		Convey("a third section drops the least recently used one", func() {
			So(reads(1, 2, 1, 3), ShouldEqual, 3)
			So(reads(1), ShouldEqual, 0)
			So(reads(2), ShouldEqual, 1)
		})

		Convey("a section bigger than the cache is never cached", func() {
			histFooterCacheBytes = largest - 1
			x, err = loadHistIndex(dir)
			So(err, ShouldBeNil)
			So(reads(1, 1), ShouldEqual, 2)
		})

		Convey("a section exactly the cache's size is cached", func() {
			histFooterCacheBytes = segSectionSize(dir, 1, histSectionEndTimes)
			x, err = loadHistIndex(dir)
			So(err, ShouldBeNil)
			So(reads(1, 1), ShouldEqual, 1)
		})

		Convey("a section bigger than the cache drops no cached section", func() {
			big := make([]historyFolded, segTestBigFooterKeys)
			for i := range big {
				big[i] = segCompletion(i+3, segTestRepGroup, uint64(i+4))
			}

			writeSegment(dir, 4, big, nil, nil, historyPos{})
			So(segSectionSize(dir, 4, histSectionEndTimes), ShouldBeGreaterThan, histFooterCacheBytes)

			x, err = loadHistIndex(dir)
			So(err, ShouldBeNil)
			So(reads(1), ShouldEqual, 1)

			_, err = x.section(4, histSectionEndTimes)
			So(err, ShouldBeNil)
			So(reads(1), ShouldEqual, 0)
		})

		Convey("a section read by two callers at once is cached once", func() {
			footer, _, _, errr := readHistFooter(dir, 1)
			So(errr, ShouldBeNil)

			only, size := footer.only(histSectionEndTimes)
			both := segReadBarrier(2)
			read := func() (*histFooter, int64, error) {
				if !both() {
					return nil, 0, errSegTestBarrier
				}

				return only, size, nil
			}

			errs := make(chan error, 2)
			key := histSectionKey{n: 1, section: histSectionEndTimes}

			for range 2 {
				go func() {
					_, errg := x.footers.get(key, read)
					errs <- errg
				}()
			}

			So(<-errs, ShouldBeNil)
			So(<-errs, ShouldBeNil)
			So(x.footers.reads.Load(), ShouldEqual, 2)
			So(x.footers.order.Len(), ShouldEqual, 1)
			So(x.footers.size, ShouldEqual, size)
		})

		Convey("a section of a segment since damaged is an error, and is not cached", func() {
			damageSegment(dir, 2, func(data []byte) []byte { return data[:len(data)-1] })

			_, err = x.section(2, histSectionEndTimes)
			So(err, ShouldWrap, errStoreCorrupt)
			So(x.footers.byN, ShouldBeEmpty)
		})
	})
}

func TestHistSegmentOpen(t *testing.T) {
	ctx := context.Background()

	Convey("Given a closed log store holding a sealed segment written by the segment writer", t, func() {
		cfg, _ := logStoreOpenConfig(t)

		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		complete := segCompletion(1, segTestRepGroup, 1000)
		writeSegment(cfg.dbFile, 1, []historyFolded{complete}, nil, nil, historyPos{file: 1})

		Convey("an open loads it into the store's history index and resumes seqs above its own", func() {
			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)

			_, found := result.s.segs().lookup(complete.key)
			So(found, ShouldBeTrue)
			So(result.s.nextSeq(), ShouldBeGreaterThan, 1000)
		})

		Convey("and a later segment holding only rglookups, an open keeps the highest seq the earlier one's", func() {
			writeSegment(cfg.dbFile, 2, nil, []logKey{complete.key}, nil, historyPos{file: 1})

			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)
			So(result.s.segs().maxSeq, ShouldEqual, 1000)
			So(result.s.nextSeq(), ShouldBeGreaterThan, 1000)
		})

		Convey("with its trailer CRC damaged, openStore treats the store as corrupt", func() {
			So(os.CopyFS(cfg.dbBkFile, os.DirFS(cfg.dbFile)), ShouldBeNil)
			damageSegment(cfg.dbFile, 1, segDamages[0].damage)

			Convey("failing with no backup, leaving the damaged segment as it was", func() {
				So(os.RemoveAll(cfg.dbBkFile), ShouldBeNil)

				before := sha256Of(histSegmentPath(cfg.dbFile, 1))

				result := openRestoring(ctx, cfg)
				So(result.s, ShouldBeNil)
				So(result.err, ShouldWrap, errStoreCorrupt)
				So(result.err.Error(), ShouldContainSubstring, "history segment 00000001")
				So(sha256Of(histSegmentPath(cfg.dbFile, 1)), ShouldEqual, before)
			})

			Convey("restoring it from a backup directory, whose segment it then loads", func() {
				result := openRestoring(ctx, cfg)
				So(result.err, ShouldBeNil)
				So(result.msg, ShouldStartWith, "recreated corrupt (?) db file "+cfg.dbFile)
				So(result.msg, ShouldContainSubstring, errStoreCorrupt.Error())

				ls, ok := result.s.(*logStore)
				So(ok, ShouldBeTrue)

				_, found := ls.segs().lookup(complete.key)
				So(found, ShouldBeTrue)
			})
		})

		for _, d := range segDamages[1:] {
			Convey("with "+d.name+", an open fails as corrupt", func() {
				damageSegment(cfg.dbFile, 1, d.damage)

				result := openLogStoreBounded(ctx, cfg)
				So(result.s, ShouldBeNil)
				So(result.err, ShouldWrap, errStoreCorrupt)
			})
		}

		Convey("with a whole footer of a newer wr, an open is refused as too new, not corrupt", func() {
			damageSegment(cfg.dbFile, 1, func(data []byte) []byte {
				_, footer := segTrailer(data)
				newer := slices.Clone(footer)
				newer[0] = histFooterVersion + 1

				return withFooter(data, newer)
			})

			result := openLogStoreBounded(ctx, cfg)
			So(result.s, ShouldBeNil)
			So(result.err, ShouldWrap, errStoreVersionTooNew)
			So(errors.Is(result.err, errStoreCorrupt), ShouldBeFalse)
		})

		Convey("with an unreadable segment, an open fails with ErrDBUnreadable, not as corrupt", func() {
			So(os.Chmod(histSegmentPath(cfg.dbFile, 1), 0), ShouldBeNil)

			result := openLogStoreBounded(ctx, cfg)
			So(result.s, ShouldBeNil)
			So(result.err, ShouldWrap, ErrDBUnreadable)
			So(errors.Is(result.err, errStoreCorrupt), ShouldBeFalse)
		})
	})
}

func TestHistSegmentRecordType(t *testing.T) {
	Convey("Given a segment holding a completion and, after it, an rglookup of its key", t, func() {
		dir := t.TempDir()
		c := segCompletion(1, segTestRepGroup, 1)

		w, err := createHistSegment(dir, 1, new(codec.BincHandle))
		So(err, ShouldBeNil)
		So(w.addCompletion(&c), ShouldBeNil)
		So(w.addLookup("\x00x", c.key, 2), ShouldBeNil)
		So(w.seal(historyPos{}), ShouldBeNil)

		x, err := loadHistIndex(dir)
		So(err, ShouldBeNil)
		So(x.segs[0].keys, ShouldHaveLength, 1)
		So(x.segs[0].keys[0].idx, ShouldEqual, 0)

		Convey("reading the key through an entry naming the rglookup is errStoreCorrupt", func() {
			x.segs[0].keys[0].idx = 1

			_, _, _, err = x.job(c.key, new(codec.BincHandle))
			So(err, ShouldWrap, errStoreCorrupt)
		})
	})
}

func TestHistSegmentReverseDeps(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a completed job depending on dep groups segRevA and segRevB", t, func() {
		dir := t.TempDir()

		completion := func(seq uint64, depGroups ...string) historyFolded {
			c := segCompletion(1, "segRev", seq)
			c.job.Dependencies = nil

			for _, dg := range depGroups {
				c.job.Dependencies = append(c.job.Dependencies, NewDepGroupDependency(dg))
			}

			return c
		}

		first := completion(5, segRevA, segRevB)
		k := first.key

		revDeps := func() map[string][]string {
			x, err := loadHistIndex(dir)
			So(err, ShouldBeNil)

			found := make(map[string][]string)

			for _, dg := range []string{segRevA, segRevB, segRevC} {
				So(x.reverseDeps(dg, func(key logKey) { found[dg] = append(found[dg], key.String()) }), ShouldBeNil)
			}

			return found
		}

		Convey("a segment holding it has a posting of its key under each, found by the index", func() {
			writeSegmentOps(dir, 1, func(w *histSegmentWriter) { So(w.addCompletion(&first), ShouldBeNil) })

			So(revDeps(), ShouldResemble, map[string][]string{segRevA: {k.String()}, segRevB: {k.String()}})
		})

		Convey("a later completion of it of other dep groups adds their postings, keeping the first's, as "+
			"history.bolt keeps a key's postings across its completions, wherever in the segment each is", func() {
			for _, order := range []string{segRevSeqOrder, segRevLaterFirst} {
				Convey(order, func() {
					second := completion(7, segRevC)
					completions := []*historyFolded{&first, &second}

					if order == segRevLaterFirst {
						slices.Reverse(completions)
					}

					writeSegmentOps(dir, 1, func(w *histSegmentWriter) {
						for _, c := range completions {
							So(w.addCompletion(c), ShouldBeNil)
						}
					})

					So(revDeps(), ShouldResemble, map[string][]string{segRevA: {k.String()}, segRevB: {k.String()},
						segRevC: {k.String()}})
				})
			}
		})

		Convey("a modify of it in the same segment, of a higher seq, drops its postings, but not those of a "+
			"completion of a higher seq still, wherever in the segment each is", func() {
			for _, order := range []string{"completions first", "modify first"} {
				Convey(order, func() {
					second := completion(7, segRevC)

					writeSegmentOps(dir, 1, func(w *histSegmentWriter) {
						if order == "modify first" {
							w.addDrop(k, 6)
						}

						So(w.addCompletion(&first), ShouldBeNil)
						So(w.addCompletion(&second), ShouldBeNil)

						if order == "completions first" {
							w.addDrop(k, 6)
						}
					})

					So(revDeps(), ShouldResemble, map[string][]string{segRevC: {k.String()}})
				})
			}
		})

		Convey("a modify of it between two completions of the same dep group drops only the first's posting, "+
			"so the dep group keeps the second's, as history.bolt puts the second's back, wherever in the "+
			"segment each is", func() {
			for _, order := range []string{segRevSeqOrder, segRevLaterFirst} {
				Convey(order, func() {
					second := completion(7, segRevA)

					writeSegmentOps(dir, 1, func(w *histSegmentWriter) {
						if order == segRevLaterFirst {
							So(w.addCompletion(&second), ShouldBeNil)
						}

						So(w.addCompletion(&first), ShouldBeNil)
						w.addDrop(k, 6)

						if order == segRevSeqOrder {
							So(w.addCompletion(&second), ShouldBeNil)
						}
					})

					So(revDeps(), ShouldResemble, map[string][]string{segRevA: {k.String()}})
				})
			}
		})

		Convey("two modifies of it, the later written first, drop the postings of every completion below the "+
			"later, as history.bolt drops them at each", func() {
			second := completion(7, segRevC)

			writeSegmentOps(dir, 1, func(w *histSegmentWriter) {
				So(w.addCompletion(&first), ShouldBeNil)
				So(w.addCompletion(&second), ShouldBeNil)
				w.addDrop(k, 8)
				w.addDrop(k, 6)
			})

			So(revDeps(), ShouldBeEmpty)
		})

		Convey("a modify of it before every completion of it in the segment drops none of their postings", func() {
			second := completion(7, segRevC)

			writeSegmentOps(dir, 1, func(w *histSegmentWriter) {
				w.addDrop(k, 3)
				So(w.addCompletion(&first), ShouldBeNil)
				So(w.addCompletion(&second), ShouldBeNil)
			})

			So(revDeps(), ShouldResemble, map[string][]string{segRevA: {k.String()}, segRevB: {k.String()},
				segRevC: {k.String()}})
		})

		Convey("a modify of it in a later segment drops the earlier segment's postings, but not those of a "+
			"completion after it in its own segment", func() {
			writeSegmentOps(dir, 1, func(w *histSegmentWriter) { So(w.addCompletion(&first), ShouldBeNil) })
			writeSegmentOps(dir, 2, func(w *histSegmentWriter) { w.addDrop(k, 6) })

			So(revDeps(), ShouldBeEmpty)

			second := completion(7, segRevC)

			writeSegmentOps(dir, 3, func(w *histSegmentWriter) {
				w.addDrop(k, 6)
				So(w.addCompletion(&second), ShouldBeNil)
			})

			So(revDeps(), ShouldResemble, map[string][]string{segRevC: {k.String()}})
		})
	})
}

// writeSegment writes and seals segment n in dir holding complete, rglookups
// of lookups' keys under segTestLookupGroup, and samples, covering the log up
// to folded.
func writeSegment(dir string, n uint32, complete []historyFolded, lookups []logKey, samples []histSample,
	folded historyPos) {
	w, err := createHistSegment(dir, n, new(codec.BincHandle))
	So(err, ShouldBeNil)

	for i := range complete {
		So(w.addCompletion(&complete[i]), ShouldBeNil)
	}

	for i, k := range lookups {
		So(w.addLookup(segTestLookupGroup, k, uint64(i+1)), ShouldBeNil)
	}

	w.addSamples(samples...)

	So(w.seal(folded), ShouldBeNil)
}

// segCompletion returns the i'th completed job of the segment tests, of rep
// group rg, depending on dep group "segdep<i%3>" and in dep group "segdg<i%2>", as a
// fold would hand it to a segment, with seq.
func segCompletion(i int, rg string, seq uint64) historyFolded {
	job := testDBArchivedJob(fmt.Sprintf("echo seg %d", i), rg, segTestEnd)
	job.EndTime = segTestEnd.Add(time.Duration(i) * time.Second)
	job.StartTime = job.EndTime.Add(-time.Second)
	job.DepGroups = []string{fmt.Sprintf("segdg%d", i%2)}
	job.Dependencies = Dependencies{NewDepGroupDependency(fmt.Sprintf("segdep%d", i%3))}

	k, err := parseLogKey(job.Key())
	So(err, ShouldBeNil)

	return historyFolded{key: k, seq: seq, job: job}
}

// segSectionSize returns the memory size the footer cache gives section of
// segment n in dir.
func segSectionSize(dir string, n uint32, section histSection) int64 {
	footer, _, _, err := readHistFooter(dir, n)
	So(err, ShouldBeNil)

	_, size := footer.only(section)

	return size
}

// segReadBarrier returns a func that n callers call at once: each returns
// true once all n have called it, or false if they have not within
// segTestBarrierWait.
func segReadBarrier(n int) func() bool {
	var arrived sync.WaitGroup

	arrived.Add(n)

	all := make(chan struct{})

	go func() {
		arrived.Wait()
		close(all)
	}()

	return func() bool {
		arrived.Done()

		select {
		case <-all:
			return true
		case <-time.After(segTestBarrierWait):
			return false
		}
	}
}

// segNumbersFrom returns the segment numbers from to through, ascending.
func segNumbersFrom(from, through uint32) []uint32 {
	numbers := make([]uint32, 0, through-from+1)
	for n := from; n <= through; n++ {
		numbers = append(numbers, n)
	}

	return numbers
}

// segBlockSizes returns the decompressed size of each block of segment n in
// dir, read through the footer's block offsets.
func segBlockSizes(dir string, n uint32) []int {
	footer, _, _, err := readHistFooter(dir, n)
	So(err, ShouldBeNil)

	f, err := os.Open(histSegmentPath(dir, n))
	So(err, ShouldBeNil)

	defer func() { So(f.Close(), ShouldBeNil) }()

	sizes := make([]int, len(footer.blocks))

	for i, off := range footer.blocks {
		So(wal.ReadFrame(f, off, func(_ wal.Ref, rec []byte) error {
			sizes[i] += wal.FrameHeader + len(rec)

			return nil
		}), ShouldBeNil)
	}

	return sizes
}

// segJobMatches reports whether x gives back want, with seq, as k's history
// entry.
func segJobMatches(x *histIndex, k logKey, want *Job, seq uint64) bool {
	job, gotSeq, found, err := x.job(k, new(codec.BincHandle))
	if err != nil || !found || gotSeq != seq || job.Key() != k.String() {
		return false
	}

	flat, err := flatCopy(want)

	return err == nil && persistedEqual(persistedCopy(job), flat)
}

// flatCopy returns the persisted fields of job as a segment's flat records give
// it back.
func flatCopy(job *Job) (*Job, error) {
	ch := new(codec.BincHandle)

	spec, err := appendFlatSpec(nil, job, ch)
	if err != nil {
		return nil, err
	}

	rs := newLogRunState(job)

	got, err := jobOfFlat(spec, appendFlatRunState(nil, &rs), ch)
	if err != nil {
		return nil, err
	}

	return persistedCopy(got), nil
}

// persistedEqual reports whether a and b, persisted copies, are equal.
func persistedEqual(a, b *Job) bool {
	return string(canonicalBinc(a)) == string(canonicalBinc(b))
}

// damageSegment replaces segment n in dir with damage applied to its bytes.
func damageSegment(dir string, n uint32, damage func(data []byte) []byte) {
	path := histSegmentPath(dir, n)

	data, err := os.ReadFile(path)
	So(err, ShouldBeNil)
	So(os.WriteFile(path, damage(data), dbFilePermission), ShouldBeNil)
}

// withFooter returns a segment's bytes with its footer payload replaced by
// footer, in a whole frame, and its trailer naming it.
func withFooter(data, footer []byte) []byte {
	before, _ := segTrailer(data)
	out := wal.AppendFrame(slices.Clone(before), footer)

	return append(out, histTrailer(uint64(len(before)))...)
}

// segTrailer splits a segment's bytes into everything before its footer and
// its footer's payload.
func segTrailer(data []byte) (before, footer []byte) {
	off := binary.LittleEndian.Uint64(data[len(data)-histTrailerLen:])
	frame := data[off : len(data)-histTrailerLen]

	return data[:off], frame[wal.FrameHeader:]
}

func TestHistFooterMemSize(t *testing.T) {
	Convey("Given a sealed segment of 200,000 completions, its footer's memSize is within 1% of the heap it takes",
		t, func() {
			dir := t.TempDir()
			job := testDBArchivedJob("echo footer size", segTestRepGroup, segTestEnd)
			job.DepGroups = []string{"segdg"}
			job.Dependencies = Dependencies{NewDepGroupDependency("segdep")}

			w, err := createHistSegment(dir, 1, new(codec.BincHandle))
			So(err, ShouldBeNil)

			failed := 0

			for i := range segTestFooterKeys {
				c := historyFolded{key: segTestKey(i), seq: uint64(i + 1), job: job}
				if w.addCompletion(&c) != nil {
					failed++
				}
			}

			So(failed, ShouldEqual, 0)
			So(w.seal(historyPos{}), ShouldBeNil)

			runtime.GC()

			var before runtime.MemStats

			runtime.ReadMemStats(&before)

			footer, _, length, err := readHistFooter(dir, 1)
			So(err, ShouldBeNil)

			runtime.GC()

			var after runtime.MemStats

			runtime.ReadMemStats(&after)

			var growth int64
			if after.HeapAlloc > before.HeapAlloc {
				growth = int64(after.HeapAlloc - before.HeapAlloc) //nolint:gosec // a heap size
			}

			size := footer.memSize(length)
			t.Logf("footer of %d bytes: memSize %d, heap growth %d", length, size, growth)

			// The heap rounds each big allocation up to whole pages, which
			// memSize leaves out: some KB, where leaving out the raw bytes or
			// the key section would leave out MBs.
			So(footer.keys, ShouldHaveLength, segTestFooterKeys)
			So(size, ShouldBeGreaterThanOrEqualTo, growth-growth/segTestFooterSlack)
			So(size, ShouldBeLessThanOrEqualTo, growth+growth/segTestFooterSlack)

			runtime.KeepAlive(footer)
		})
}

func TestHistKeyIndexMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("indexes a million keys")
	}

	Convey("Given 1,000,000 completed keys in sealed segments, each with a req group sample, when indexed", t, func() {
		dir := t.TempDir()
		job := testDBArchivedJob("echo key index", segTestRepGroup, segTestEnd)
		perSegment := segTestKeys / segTestKeySegments

		for n := range segTestKeySegments {
			w, err := createHistSegment(dir, uint32(n+1), new(codec.BincHandle))
			So(err, ShouldBeNil)

			failed := 0

			for i := range perSegment {
				c := historyFolded{key: segTestKey(n*perSegment + i), seq: uint64(n*perSegment + i + 1), job: job}
				if w.addCompletion(&c) != nil {
					failed++
				}
			}

			So(failed, ShouldEqual, 0)

			w.addSamples(histSample{kind: histSampleRAM, reqGroup: segTestKeyReqGroup(n), value: n + 1})

			So(w.seal(historyPos{}), ShouldBeNil)
		}

		runtime.GC()

		var before runtime.MemStats

		runtime.ReadMemStats(&before)

		x, err := loadHistIndex(dir)
		So(err, ShouldBeNil)

		runtime.GC()

		var after runtime.MemStats

		runtime.ReadMemStats(&after)

		var growth uint64
		if after.HeapAlloc > before.HeapAlloc {
			growth = after.HeapAlloc - before.HeapAlloc
		}

		t.Logf("key index heap growth for %d keys: %d bytes", segTestKeys, growth)

		Convey("the key index's heap growth is under 47MB, and it finds every key and sample", func() {
			So(growth, ShouldBeLessThan, segTestKeyIndexMax)

			missing := 0

			for i := range segTestKeys {
				if _, found := x.lookup(segTestKey(i)); !found {
					missing++
				}
			}

			So(missing, ShouldEqual, 0)

			_, found := x.lookup(segTestKey(segTestKeys))
			So(found, ShouldBeFalse)

			for n := range segTestKeySegments {
				So(x.sampleValues(segTestKeyReqGroup(n), histSampleRAM), ShouldResemble, []int{n + 1})
			}
		})

		runtime.KeepAlive(x)
	})
}

// segTestKey returns the i'th of test 5's keys.
func segTestKey(i int) logKey {
	var k logKey

	binary.BigEndian.PutUint64(k[8:], uint64(i)) //nolint:gosec // not negative
	copy(k[:8], strings.Repeat("k", 8))

	return k
}

// segTestKeyReqGroup returns the req group of test 5's n'th segment's sample:
// one per segment, so the index's samples hold a name from every footer.
func segTestKeyReqGroup(n int) string {
	return fmt.Sprintf("keyidx-rg%d", n)
}

func TestHistFooterParse(t *testing.T) {
	Convey("A footer parses back to what was encoded", t, func() {
		f := testFooter()

		got, err := parseHistFooter(f.encode())
		So(err, ShouldBeNil)
		So(got, ShouldResemble, f)

		got, err = parseHistFooter((&histFooter{}).encode())
		So(err, ShouldBeNil)
		So(got.keys, ShouldBeEmpty)
	})

	Convey("A footer is refused as malformed", t, func() {
		malformed := map[string]func(f *histFooter){
			"with blocks out of order":        func(f *histFooter) { f.blocks[0], f.blocks[1] = 100, 0 },
			"with keys out of order":          func(f *histFooter) { f.keys[0], f.keys[1] = f.keys[1], f.keys[0] },
			"with a key twice":                func(f *histFooter) { f.keys[1].key = f.keys[0].key },
			"with a key's block not in it":    func(f *histFooter) { f.keys[1].block = 2 },
			"with end times out of order":     func(f *histFooter) { f.endTimes[0].end = 8 },
			"with an end time's block absent": func(f *histFooter) { f.endTimes[0].block = 2 },
			"with rep groups out of order":    func(f *histFooter) { f.repGroups[0].name = "c" },
			"with a posting's keys unsorted":  func(f *histFooter) { f.repGroups[0].keys[0] = logKey{3} },
			"with dep groups out of order": func(f *histFooter) {
				f.depGroups = append(f.depGroups, histPosting{name: "c"})
			},
			"with reverse dep groups out of order": func(f *histFooter) {
				f.revDeps = append(f.revDeps, histPosting{name: "a"})
			},
			"with dep groups seen out of order": func(f *histFooter) { f.depGroupsSeen = []string{"e", "d"} },
			"with rep group ends out of order":  func(f *histFooter) { f.repGroupEnds[1].name = "a" },
			"with samples out of order":         func(f *histFooter) { f.samples[0].value = 2 },
			"with a sample of no kind":          func(f *histFooter) { f.samples[2].kind = histSampleKinds },
			"with drops out of order":           func(f *histFooter) { f.drops[0], f.drops[1] = f.drops[1], f.drops[0] },
			"with a drop twice":                 func(f *histFooter) { f.drops[1] = f.drops[0] },
		}

		for name, change := range malformed {
			f := testFooter()
			change(f)

			_, err := parseHistFooter(f.encode())
			So(fmt.Sprintf("%s: %v", name, err), ShouldEqual, name+": "+errHistFooterMalformed.Error())
		}

		encoded := testFooter().encode()

		for _, bad := range [][]byte{nil, {0}, encoded[:len(encoded)-1], append(slices.Clone(encoded), 0)} {
			_, err := parseHistFooter(bad)
			So(err, ShouldEqual, errHistFooterMalformed)
		}
	})

	Convey("A footer claiming more entries than it could hold is refused without allocating them", t, func() {
		huge := binary.AppendUvarint([]byte{histFooterVersion}, 1<<40)

		_, err := parseHistFooter(huge)
		So(err, ShouldEqual, errHistFooterMalformed)
	})
}

// FuzzHistFooter: parsing any bytes never panics, and a footer that parses
// encodes back to bytes that parse to it.
func FuzzHistFooter(f *testing.F) {
	encoded := testFooter().encode()

	f.Add(encoded)
	f.Add((&histFooter{}).encode())
	f.Add(encoded[:len(encoded)/2])
	f.Add([]byte{})
	f.Add([]byte{histFooterVersion + 1})
	f.Add(binary.AppendUvarint([]byte{histFooterVersion}, 1<<40))

	f.Fuzz(func(t *testing.T, b []byte) {
		footer, err := parseHistFooter(b)
		if err != nil {
			return
		}

		again, err := parseHistFooter(footer.encode())
		if err != nil || fmt.Sprint(again) != fmt.Sprint(footer) {
			t.Fatalf("footer %v did not round-trip: %v, %v", footer, again, err)
		}
	})
}

// testFooter returns a footer with an entry in every section.
func testFooter() *histFooter {
	return &histFooter{
		blocks: []uint64{0, 100},
		folded: historyPos{file: 2, off: 77},
		maxSeq: 9,
		keys: []histKeyEntry{
			{key: logKey{1}, seq: 3, block: 0, idx: 1},
			{key: logKey{2}, seq: 9, block: 1, idx: 0},
		},
		endTimes:      []histEndTime{{end: -5, key: logKey{2}, block: 1}, {end: 7, key: logKey{1}, idx: 1}},
		repGroups:     []histPosting{{name: "a", keys: []logKey{{1}, {2}}}, {name: "b", keys: []logKey{{2}}}},
		depGroups:     []histPosting{{name: "d", keys: []logKey{{1}}}},
		revDeps:       []histPosting{{name: "r", keys: []logKey{{2}}}},
		depGroupsSeen: []string{"d"},
		repGroupEnds:  []histGroupEnd{{name: "a", end: 7}, {name: "b", end: -1}},
		samples: []histSample{
			{kind: histSampleRAM, reqGroup: "q", value: 1},
			{kind: histSampleRAM, reqGroup: "q", value: 2},
			{kind: histSampleSecs, reqGroup: "a", value: 0},
		},
		drops: []logKey{{1}, {3}},
	}
}

func TestHistTrailer(t *testing.T) {
	Convey("A trailer is the footer offset, the CRC-32C of it and WRHIST01", t, func() {
		trailer := histTrailer(0x0102030405060708)

		So(trailer, ShouldHaveLength, histTrailerLen)
		So(binary.LittleEndian.Uint64(trailer), ShouldEqual, uint64(0x0102030405060708))
		So(binary.LittleEndian.Uint32(trailer[8:]), ShouldEqual, segTrailerCRC(0x0102030405060708))
		So(string(trailer[12:]), ShouldEqual, "WRHIST01")
	})
}

// segTrailerCRC returns the CRC a trailer holds of footer offset off, as a
// check that histTrailer computes it over the offset alone.
func segTrailerCRC(off uint64) uint32 {
	return crc32.Checksum(binary.LittleEndian.AppendUint64(nil, off), crc32.MakeTable(crc32.Castagnoli))
}
