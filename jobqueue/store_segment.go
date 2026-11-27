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

// This file holds the log store's sealed history segments (spec "Folder and
// history", Phase 2, and D2): immutable NNNNNNNN.hist files of zstd blocks of
// at most checkpointBlockBytes (64KiB) uncompressed, each holding completed-job
// and rglookup records, then a footer frame of the segment's block offsets,
// the folded position it covers, its highest complete seq and sorted sections
// equivalent to today's history buckets for the jobs in it, then a fixed-size
// trailer: the footer's offset, a CRC of it and the magic WRHIST01. A segment
// is written as NNNNNNNN.hist.tmp and sealed (footer, trailer, fsync, rename),
// so a whole .hist file is never torn: damage anywhere in one is corruption.

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unsafe"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/ugorji/go/codec"
)

const (
	// histFileExt is the extension of a sealed history segment's name,
	// NNNNNNNN.hist.
	histFileExt = ".hist"

	// histTrailerLen is the length of a segment's trailer: the footer's
	// offset, its CRC and the magic.
	histTrailerLen = 8 + 4 + 8

	// histFooterVersion is the first byte of every footer this wr writes; a
	// reader refuses a higher one.
	histFooterVersion byte = 1

	// histRecJob and histRecLookup are the first byte of a segment's
	// completed-job and rglookup records.
	histRecJob    byte = 1
	histRecLookup byte = 2

	// histRecHeader is the length of a segment record's type, seq and key.
	histRecHeader = 1 + 8 + logKeyLen

	// histMinRefBytes, histMinGroupBytes and histMinSampleBytes are the
	// fewest bytes a footer's key or end time entry (a key and three varints),
	// group entry (a name's length and a count or varint) and sample (kind,
	// req group length, value) take, so that a count the footer cannot hold
	// is refused before anything is allocated for it.
	histMinRefBytes    = logKeyLen + 3
	histMinGroupBytes  = 2
	histMinSampleBytes = 3
)

var (
	// histTrailerMagic ends every sealed segment.
	histTrailerMagic = []byte("WRHIST01") //nolint:gochecknoglobals // a constant

	// histCRCTable is the CRC-32C table of a segment trailer's CRC.
	histCRCTable = crc32.MakeTable(crc32.Castagnoli) //nolint:gochecknoglobals // a constant table

	// errHistFooterMalformed is the error of parsing bytes that are not a
	// footer a segment writer writes.
	errHistFooterMalformed = errors.New("malformed history segment footer")
)

// histSampleKind is which learned requirement a sample is of, as history.bolt's
// jobRAM, jobDisk and jobSecs buckets.
type histSampleKind uint8

const (
	histSampleRAM histSampleKind = iota
	histSampleDisk
	histSampleSecs

	// histSampleKinds is how many kinds there are.
	histSampleKinds
)

// histSegmentNumbers returns the numbers of the sealed segments in dir,
// ascending.
func histSegmentNumbers(dir string) ([]uint32, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var numbers []uint32

	for _, entry := range entries {
		hex, ok := strings.CutSuffix(entry.Name(), histFileExt)
		if !ok || len(hex) != walFileDigits {
			continue
		}

		if n, errp := strconv.ParseUint(hex, 16, 32); errp == nil {
			numbers = append(numbers, uint32(n))
		}
	}

	slices.Sort(numbers)

	return numbers, nil
}

// parseHistSamples reads the samples section, marking r bad for a sample of a
// kind there is not.
func parseHistSamples(r *flatReader) []histSample {
	samples := make([]histSample, histCount(r, histMinSampleBytes))

	for i := range samples {
		kind := histSampleKind(r.u8())
		if kind >= histSampleKinds {
			r.bad = true
		}

		samples[i] = histSample{kind: kind, reqGroup: r.str(), value: int(r.varint())}
	}

	return samples
}

// histCount reads a section's count, marking r bad for one the footer cannot
// hold at minBytes an item.
func histCount(r *flatReader, minBytes uint64) int {
	n := r.uvarint()
	if n > r.left()/minBytes {
		r.bad = true

		return 0
	}

	return int(n) //nolint:gosec // at most the footer's length
}

// histKeyEntry is a key's entry in a segment's key index: the seq of its
// complete record and the Ref of its completed-job record, as the index of its
// block among the segment's blocks and its index in that block. It is 32 bytes,
// so a million keys take 32MB (spec D2, test 5). The key is the 16 bytes of the
// job's 32-hex-digit md5 key, so it is exact: no two jobs share an entry.
type histKeyEntry struct {
	key   logKey
	seq   uint64
	block uint32
	idx   uint32
}

// parseHistKeys reads the key section.
func parseHistKeys(r *flatReader) []histKeyEntry {
	keys := make([]histKeyEntry, histCount(r, histMinRefBytes))

	for i := range keys {
		keys[i] = histKeyEntry{key: histKey(r), seq: r.uvarint(), block: r.u32(), idx: r.u32()}
	}

	return keys
}

// histKey reads a key.
func histKey(r *flatReader) logKey {
	var k logKey

	copy(k[:], r.span(logKeyLen))

	return k
}

// histEndTime is a completion's end time, as UnixNano, and its key and record.
type histEndTime struct {
	end   int64
	key   logKey
	block uint32
	idx   uint32
}

// parseHistEndTimes reads the end time section.
func parseHistEndTimes(r *flatReader) []histEndTime {
	ends := make([]histEndTime, histCount(r, histMinRefBytes))

	for i := range ends {
		ends[i] = histEndTime{end: r.varint(), key: histKey(r), block: r.u32(), idx: r.u32()}
	}

	return ends
}

// compareHistEndTimes orders end times by time, then key.
func compareHistEndTimes(a, b histEndTime) int {
	return cmp.Or(cmp.Compare(a.end, b.end), compareLogKeys(a.key, b.key))
}

// compareLogKeys orders keys by their bytes.
func compareLogKeys(a, b logKey) int {
	return bytes.Compare(a[:], b[:])
}

// histPosting is a group's name and, sorted, the keys of the segment under it.
type histPosting struct {
	name string
	keys []logKey
}

// parseHistPostings reads a postings section.
func parseHistPostings(r *flatReader) []histPosting {
	postings := make([]histPosting, histCount(r, histMinGroupBytes))

	for i := range postings {
		postings[i].name = r.str()
		postings[i].keys = parseHistKeyList(r)
	}

	return postings
}

// parseHistKeyList reads a count of keys and the keys.
func parseHistKeyList(r *flatReader) []logKey {
	keys := make([]logKey, histCount(r, logKeyLen))

	for i := range keys {
		keys[i] = histKey(r)
	}

	return keys
}

// histGroupEnd is a rep group's latest end time among the segment's
// completions, as a unix time in seconds, as history.bolt's repgroupEndTime.
type histGroupEnd struct {
	name string
	end  int64
}

// parseHistGroupEnds reads the rep group end time section.
func parseHistGroupEnds(r *flatReader) []histGroupEnd {
	ends := make([]histGroupEnd, histCount(r, histMinGroupBytes))

	for i := range ends {
		ends[i] = histGroupEnd{name: r.str(), end: r.varint()}
	}

	return ends
}

// histSample is a learned-requirement sample: a kind's value for a req group.
type histSample struct {
	kind     histSampleKind
	reqGroup string
	value    int
}

// compareHistSamples orders samples by kind, req group, then value.
func compareHistSamples(a, b histSample) int {
	return cmp.Or(cmp.Compare(a.kind, b.kind), strings.Compare(a.reqGroup, b.reqGroup), cmp.Compare(a.value, b.value))
}

// histFooter is a segment's footer. Each section is sorted, by key, by end time
// then key, or by name; samples by kind, req group and value. Strings parsed
// from a footer alias the bytes it was parsed from.
type histFooter struct {
	blocks        []uint64
	folded        historyPos
	maxSeq        uint64
	keys          []histKeyEntry
	endTimes      []histEndTime
	repGroups     []histPosting // rglookups, as history.bolt's repgroupToKey
	depGroups     []histPosting // completions' DepGroups
	revDeps       []histPosting // completions' Dependencies.DepGroups()
	depGroupsSeen []string
	repGroupEnds  []histGroupEnd
	samples       []histSample
	drops         []logKey // keys a modify dropped the rglookups and reverse dep group postings of
}

// parseHistFooter returns the footer b encodes, its strings aliasing b. It
// refuses as errHistFooterMalformed bytes that are not a whole footer, a
// section out of order, or a Ref of a block the segment does not have, and
// wraps errStoreVersionTooNew for a footer of a newer wr.
func parseHistFooter(b []byte) (*histFooter, error) {
	r := flatReader{b: b}

	if _, err := r.version(histFooterVersion); err != nil {
		if errors.Is(err, errStoreVersionTooNew) {
			return nil, err
		}

		return nil, errHistFooterMalformed
	}

	f := &histFooter{blocks: make([]uint64, histCount(&r, 1))}
	for i := range f.blocks {
		f.blocks[i] = r.uvarint()
	}

	f.folded = historyPos{file: r.u32(), off: r.uvarint()}
	f.maxSeq = r.uvarint()
	f.keys = parseHistKeys(&r)
	f.endTimes = parseHistEndTimes(&r)
	f.repGroups, f.depGroups, f.revDeps = parseHistPostings(&r), parseHistPostings(&r), parseHistPostings(&r)
	f.depGroupsSeen = parseHistNames(&r)
	f.repGroupEnds = parseHistGroupEnds(&r)
	f.samples = parseHistSamples(&r)
	f.drops = parseHistKeyList(&r)

	if r.end() != nil || !f.wellFormed() {
		return nil, errHistFooterMalformed
	}

	return f, nil
}

// readHistFooter returns the footer of sealed segment n in dir, with the
// offset it starts at and the capacity of the buffer it was parsed from, which
// its strings keep alive: reading a big frame grows that buffer past the
// frame's length. A segment whose trailer or footer is not
// whole and valid is errStoreCorrupt; a footer of a newer wr wraps
// errStoreVersionTooNew; an error reading the file is returned as it is.
func readHistFooter(dir string, n uint32) (*histFooter, uint64, int, error) {
	f, err := os.Open(histSegmentPath(dir, n))
	if err != nil {
		return nil, 0, 0, err
	}

	footer, off, length, err := readHistFooterOf(f)
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, 0, 0, fmt.Errorf("history segment %08x: %w", n, err)
	}

	return footer, off, length, nil
}

// readHistFooterOf is readHistFooter of the open segment f.
func readHistFooterOf(f *os.File) (*histFooter, uint64, int, error) {
	off, err := histFooterOffset(f)
	if err != nil {
		return nil, 0, 0, err
	}

	raw, err := wal.ReadAt(f, wal.Ref{Off: off})
	if errors.Is(err, wal.ErrBadRef) {
		return nil, 0, 0, fmt.Errorf("%w: its footer is damaged: %w", errStoreCorrupt, err)
	}

	if err != nil {
		return nil, 0, 0, err
	}

	if err = histFooterFills(f, off, raw); err != nil {
		return nil, 0, 0, err
	}

	footer, err := parseHistFooter(raw)
	if errors.Is(err, errHistFooterMalformed) {
		return nil, 0, 0, fmt.Errorf("%w: %w", errStoreCorrupt, err)
	}

	return footer, off, cap(raw), err
}

// encode returns f's encoding. Counts are uvarints; keys their 16 bytes;
// strings length-prefixed.
func (f *histFooter) encode() []byte {
	dst := []byte{histFooterVersion}
	dst = binary.AppendUvarint(dst, uint64(len(f.blocks)))

	for _, off := range f.blocks {
		dst = binary.AppendUvarint(dst, off)
	}

	dst = binary.AppendUvarint(dst, uint64(f.folded.file))
	dst = binary.AppendUvarint(dst, f.folded.off)
	dst = binary.AppendUvarint(dst, f.maxSeq)
	dst = f.appendRefs(dst)

	return f.appendGroups(dst)
}

// appendRefs appends f's key and end time sections to dst.
func (f *histFooter) appendRefs(dst []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(f.keys)))

	for _, e := range f.keys {
		dst = append(dst, e.key[:]...)
		dst = binary.AppendUvarint(dst, e.seq)
		dst = binary.AppendUvarint(dst, uint64(e.block))
		dst = binary.AppendUvarint(dst, uint64(e.idx))
	}

	dst = binary.AppendUvarint(dst, uint64(len(f.endTimes)))

	for _, e := range f.endTimes {
		dst = binary.AppendVarint(dst, e.end)
		dst = append(dst, e.key[:]...)
		dst = binary.AppendUvarint(dst, uint64(e.block))
		dst = binary.AppendUvarint(dst, uint64(e.idx))
	}

	return dst
}

// appendGroups appends f's postings, dep groups seen, rep group end time,
// samples and drops sections to dst.
func (f *histFooter) appendGroups(dst []byte) []byte {
	for _, postings := range [][]histPosting{f.repGroups, f.depGroups, f.revDeps} {
		dst = appendHistPostings(dst, postings)
	}

	dst = binary.AppendUvarint(dst, uint64(len(f.depGroupsSeen)))
	dst = appendFlatStrings(dst, f.depGroupsSeen...)
	dst = binary.AppendUvarint(dst, uint64(len(f.repGroupEnds)))

	for _, g := range f.repGroupEnds {
		dst = appendFlatStrings(dst, g.name)
		dst = binary.AppendVarint(dst, g.end)
	}

	dst = binary.AppendUvarint(dst, uint64(len(f.samples)))

	for _, s := range f.samples {
		dst = append(dst, byte(s.kind))
		dst = appendFlatStrings(dst, s.reqGroup)
		dst = binary.AppendVarint(dst, int64(s.value))
	}

	return appendHistKeyList(dst, f.drops)
}

// appendHistPostings appends postings to dst: their count, then each one's
// name and keys.
func appendHistPostings(dst []byte, postings []histPosting) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(postings)))

	for _, p := range postings {
		dst = appendHistKeyList(appendFlatStrings(dst, p.name), p.keys)
	}

	return dst
}

// appendHistKeyList appends to dst the count of keys and the keys.
func appendHistKeyList(dst []byte, keys []logKey) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(keys)))

	for _, k := range keys {
		dst = append(dst, k[:]...)
	}

	return dst
}

// wellFormed reports whether every section of f is in order, with no name or
// key twice, and every Ref names one of its blocks: lookups binary search
// them, and read the block a Ref names.
func (f *histFooter) wellFormed() bool {
	return f.refsInBlocks() && f.sorted()
}

// refsInBlocks reports whether every Ref of f names one of its blocks.
func (f *histFooter) refsInBlocks() bool {
	nblocks := uint32(len(f.blocks)) //nolint:gosec // at most the footer's length

	return !slices.ContainsFunc(f.keys, func(e histKeyEntry) bool { return e.block >= nblocks }) &&
		!slices.ContainsFunc(f.endTimes, func(e histEndTime) bool { return e.block >= nblocks })
}

// sorted reports whether every section of f is in strictly ascending order.
func (f *histFooter) sorted() bool {
	return strictlySorted(f.blocks, cmp.Compare) &&
		strictlySorted(f.keys, func(a, b histKeyEntry) int { return compareLogKeys(a.key, b.key) }) &&
		strictlySorted(f.endTimes, compareHistEndTimes) && f.groupsSorted()
}

// strictlySorted reports whether s is in strictly ascending order by compare.
func strictlySorted[T any](s []T, compare func(a, b T) int) bool {
	for i := range len(s) - 1 {
		if compare(s[i], s[i+1]) >= 0 {
			return false
		}
	}

	return true
}

// groupsSorted reports whether f's postings, dep groups seen, rep group end
// times, samples and drops are each in strictly ascending order.
func (f *histFooter) groupsSorted() bool {
	return postingsSorted(f.repGroups) && postingsSorted(f.depGroups) && postingsSorted(f.revDeps) &&
		strictlySorted(f.depGroupsSeen, strings.Compare) &&
		strictlySorted(f.repGroupEnds, func(a, b histGroupEnd) int { return strings.Compare(a.name, b.name) }) &&
		strictlySorted(f.samples, compareHistSamples) && strictlySorted(f.drops, compareLogKeys)
}

// postingsSorted reports whether postings and each one's keys are in strictly
// ascending order.
func postingsSorted(postings []histPosting) bool {
	byName := func(a, b histPosting) int { return strings.Compare(a.name, b.name) }

	return strictlySorted(postings, byName) && !slices.ContainsFunc(postings, func(p histPosting) bool {
		return !strictlySorted(p.keys, compareLogKeys)
	})
}

// memSize returns about how many bytes of memory f holds: raw, the capacity of
// the buffer it was parsed from, which its strings alias, and its sections.
func (f *histFooter) memSize(raw int) int64 {
	size := raw + len(f.blocks)*int(unsafe.Sizeof(uint64(0))) +
		len(f.keys)*int(unsafe.Sizeof(histKeyEntry{})) +
		len(f.endTimes)*int(unsafe.Sizeof(histEndTime{})) +
		len(f.depGroupsSeen)*int(unsafe.Sizeof("")) +
		len(f.repGroupEnds)*int(unsafe.Sizeof(histGroupEnd{})) +
		len(f.samples)*int(unsafe.Sizeof(histSample{})) + len(f.drops)*logKeyLen

	for _, postings := range [][]histPosting{f.repGroups, f.depGroups, f.revDeps} {
		size += len(postings) * int(unsafe.Sizeof(histPosting{}))

		for _, p := range postings {
			size += len(p.keys) * logKeyLen
		}
	}

	return int64(size)
}

// histBuiltJob is what the footer indexes of a key's completed-job record of
// the highest seq.
type histBuiltJob struct {
	entry     histKeyEntry
	depGroups []string
	end       int64
}

// revDepPostings returns the footer's reverse dep group postings: each dep
// group a completion depends on, of a seq no drop of its key in the segment
// is above.
func (b *histFooterBuilder) revDepPostings() []histPosting {
	postings := make(map[string]map[logKey]struct{})

	for k, deps := range b.revDeps {
		for dg, seq := range deps {
			if dropped, ok := b.drops[k]; !ok || dropped <= seq {
				addHistPosting(postings, dg, k)
			}
		}
	}

	return sortedHistPostings(postings)
}

// noteRevDeps notes each dep group c's job depends on, with c's seq, unless
// it noted the dep group of c's key with a higher seq already.
func (b *histFooterBuilder) noteRevDeps(c *historyFolded) {
	for _, dg := range c.job.Dependencies.DepGroups() {
		if b.revDeps[c.key] == nil {
			b.revDeps[c.key] = make(map[string]uint64)
		}

		b.revDeps[c.key][dg] = max(b.revDeps[c.key][dg], c.seq)
	}
}

// addHistPosting adds k to name's posting in postings.
func addHistPosting(postings map[string]map[logKey]struct{}, name string, k logKey) {
	if postings[name] == nil {
		postings[name] = make(map[logKey]struct{})
	}

	postings[name][k] = struct{}{}
}

// sortedHistPostings returns postings as a footer section.
func sortedHistPostings(postings map[string]map[logKey]struct{}) []histPosting {
	sorted := make([]histPosting, 0, len(postings))

	for _, name := range slices.Sorted(maps.Keys(postings)) {
		sorted = append(sorted, histPosting{name: name,
			keys: slices.SortedFunc(maps.Keys(postings[name]), compareLogKeys)})
	}

	return sorted
}

// histFooterBuilder gathers a segment's footer as its records are added. Its
// rep group postings are its rglookups, but those a drop after them dropped,
// as history.bolt's repgroupToKey holds rglookups alone: a job can be looked
// up under one rep group and complete under another, in another segment. Its
// dep group postings are of each key's record of the highest seq, as such a
// posting is valid only while the key index maps its key to that record, but
// its rep group end times and dep groups seen are of every record not of a
// seq at or below one before it of its key, as a key can complete under one
// rep group, be moved to another and complete again. history.bolt's rep group
// end times grow the same way with every completion it folds; its dep groups
// seen are instead noted when a job is added. Its reverse dep group postings
// are of every completion, but those of a lower seq than a drop of their key
// in the segment, as history.bolt keeps a key's postings across its
// completions and a modify drops them all: so a completion before a modify of
// its key in the segment puts none, and one after it still does (see
// historyLookupChanges.dropSeqs).
type histFooterBuilder struct {
	jobs      map[logKey]*histBuiltJob
	lookups   map[string]map[logKey]struct{}
	drops     map[logKey]uint64
	revDeps   map[logKey]map[string]uint64
	samples   map[histSample]struct{}
	groupEnds map[string]int64
	depSeen   map[string]struct{}
	maxSeq    uint64
}

func newHistFooterBuilder() histFooterBuilder {
	return histFooterBuilder{
		jobs:      make(map[logKey]*histBuiltJob),
		lookups:   make(map[string]map[logKey]struct{}),
		drops:     make(map[logKey]uint64),
		revDeps:   make(map[logKey]map[string]uint64),
		samples:   make(map[histSample]struct{}),
		groupEnds: make(map[string]int64),
		depSeen:   make(map[string]struct{}),
	}
}

// complete notes c's record, at index idx of the block numbered block: the
// dep groups its job depends on, with its seq, and, unless the segment holds a
// record of its key of the same or a higher seq, which history.bolt's
// putComplete would skip as already folded, its rep group end time and dep
// groups, and its key's entry and postings.
func (b *histFooterBuilder) complete(c *historyFolded, block, idx uint32) {
	b.maxSeq = max(b.maxSeq, c.seq)
	b.noteRevDeps(c)

	if prev := b.jobs[c.key]; prev != nil && prev.entry.seq >= c.seq {
		return
	}

	if end, ok := b.groupEnds[c.job.RepGroup]; !ok || c.job.EndTime.Unix() > end {
		b.groupEnds[c.job.RepGroup] = c.job.EndTime.Unix()
	}

	for _, dg := range c.job.DepGroups {
		b.depSeen[dg] = struct{}{}
	}

	b.jobs[c.key] = &histBuiltJob{
		entry:     histKeyEntry{key: c.key, seq: c.seq, block: block, idx: idx},
		depGroups: slices.Clone(c.job.DepGroups),
		end:       c.job.EndTime.UnixNano(),
	}
}

// lookup notes an rglookup of k under repGroup.
func (b *histFooterBuilder) lookup(repGroup string, k logKey) {
	addHistPosting(b.lookups, repGroup, k)
}

// drop notes a modify, of seq, dropping every rglookup and reverse dep group
// posting of k: the rglookups noted before it go, as do the postings of
// completions of k of a lower seq, and its key is in the footer's drops, so
// that queries skip those of earlier segments and history.bolt too. A rep
// group keeps its posting though it is left empty, as history.bolt's repgroups
// keeps every rep group ever added. It visits every rep group the segment has
// rglookups of, a cost per modify a reverse map of each rglookup's key would
// remove at the price of memory per rglookup; modifies are far fewer than
// rglookups.
func (b *histFooterBuilder) drop(k logKey, seq uint64) {
	for _, keys := range b.lookups {
		delete(keys, k)
	}

	b.drops[k] = max(b.drops[k], seq)
}

// footer returns the footer of what b has noted, with the segment's block
// offsets and the folded position it covers.
func (b *histFooterBuilder) footer(blocks []uint64, folded historyPos) *histFooter {
	f := &histFooter{blocks: blocks, folded: folded, maxSeq: b.maxSeq,
		repGroups:     sortedHistPostings(b.lookups),
		revDeps:       b.revDepPostings(),
		depGroupsSeen: slices.Sorted(maps.Keys(b.depSeen)),
		samples:       slices.SortedFunc(maps.Keys(b.samples), compareHistSamples),
		drops:         slices.SortedFunc(maps.Keys(b.drops), compareLogKeys)}
	depGroups := make(map[string]map[logKey]struct{})

	for _, name := range slices.Sorted(maps.Keys(b.groupEnds)) {
		f.repGroupEnds = append(f.repGroupEnds, histGroupEnd{name: name, end: b.groupEnds[name]})
	}

	for k, j := range b.jobs {
		f.keys = append(f.keys, j.entry)
		f.endTimes = append(f.endTimes, histEndTime{end: j.end, key: k, block: j.entry.block, idx: j.entry.idx})

		for _, dg := range j.depGroups {
			addHistPosting(depGroups, dg, k)
		}
	}

	slices.SortFunc(f.keys, func(a, b histKeyEntry) int { return compareLogKeys(a.key, b.key) })
	slices.SortFunc(f.endTimes, compareHistEndTimes)
	f.depGroups = sortedHistPostings(depGroups)

	return f
}

// histSegmentWriter writes one history segment, NNNNNNNN.hist.tmp, until seal
// makes it NNNNNNNN.hist. Its records go into zstd blocks of at most
// checkpointBlockBytes (64KiB) uncompressed, as a checkpoint's do (a record
// bigger than that alone in its block), written through a backupCopyWriter so
// that seal's fsync never sends the whole segment at once ahead of the log's
// syncs, as a checkpoint's is paced.
type histSegmentWriter struct {
	dir  string
	n    uint32
	ch   codec.Handle
	f    *os.File
	cw   *checkpointWriter
	foot histFooterBuilder
	rec  []byte
	spec []byte
}

// createHistSegment starts writing segment n in dir, encoding specs with ch.
func createHistSegment(dir string, n uint32, ch codec.Handle) (*histSegmentWriter, error) {
	f, err := os.OpenFile(histSegmentPath(dir, n)+storeTmpExt, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, dbFilePermission)
	if err != nil {
		return nil, err
	}

	paced := &backupCopyWriter{f: f, syncEvery: backupCopySyncBytes, keepCached: true}

	return &histSegmentWriter{
		dir: dir, n: n, ch: ch, f: f,
		cw:   &checkpointWriter{w: bufio.NewWriter(paced), refFile: n},
		foot: newHistFooterBuilder(),
	}, nil
}

// addCompletion adds c, a complete record with the job of its history entry,
// as a completed-job record: [type][seq][key][uvarint spec length][flat
// spec][flat run state]. Of a key added more than once, the footer indexes
// the record of the highest seq.
func (w *histSegmentWriter) addCompletion(c *historyFolded) error {
	spec, err := appendFlatSpec(w.spec[:0], c.job, w.ch)
	if err != nil {
		return err
	}

	w.spec = spec
	rs := newLogRunState(c.job)

	rec := appendHistRecHeader(w.rec[:0], histRecJob, c.seq, c.key)
	rec = binary.AppendUvarint(rec, uint64(len(spec)))
	rec = append(rec, spec...)
	w.rec = appendFlatRunState(rec, &rs)

	ref, err := w.cw.add(w.rec)
	if err != nil {
		return err
	}

	w.foot.complete(c, uint32(len(w.cw.offsets)), ref.Idx) //nolint:gosec // block counts fit

	return nil
}

// appendHistRecHeader appends a segment record's type, seq and key to dst.
func appendHistRecHeader(dst []byte, typ byte, seq uint64, k logKey) []byte {
	dst = append(dst, typ)
	dst = binary.LittleEndian.AppendUint64(dst, seq)

	return append(dst, k[:]...)
}

// addLookup adds an rglookup of key under repGroup, of seq, as an rglookup
// record: [type][seq][key][rep group].
func (w *histSegmentWriter) addLookup(repGroup string, k logKey, seq uint64) error {
	w.rec = append(appendHistRecHeader(w.rec[:0], histRecLookup, seq, k), repGroup...)

	if _, err := w.cw.add(w.rec); err != nil {
		return err
	}

	w.foot.lookup(repGroup, k)

	return nil
}

// addDrop notes a modify, of seq, dropping every rglookup and reverse dep
// group posting of k (see histFooterBuilder.drop).
func (w *histSegmentWriter) addDrop(k logKey, seq uint64) {
	w.foot.drop(k, seq)
}

// addSamples adds learned-requirement samples to the footer, once each.
func (w *histSegmentWriter) addSamples(samples ...histSample) {
	for _, s := range samples {
		w.foot.samples[s] = struct{}{}
	}
}

// seal writes the last block, the footer, covering the log up to folded, and
// the trailer, makes the segment durable and renames it to NNNNNNNN.hist,
// durably. On error it removes the .tmp file.
func (w *histSegmentWriter) seal(folded historyPos) error {
	err := w.writeTail(folded)
	if err = errors.Join(err, w.f.Close()); err != nil {
		return errors.Join(err, os.Remove(w.f.Name()))
	}

	if err = os.Rename(w.f.Name(), histSegmentPath(w.dir, w.n)); err != nil {
		return errors.Join(err, os.Remove(w.f.Name()))
	}

	return syncDir(w.dir)
}

// histSegmentPath returns the path of sealed segment n in dir.
func histSegmentPath(dir string, n uint32) string {
	return filepath.Join(dir, fmt.Sprintf("%0*x%s", walFileDigits, n, histFileExt))
}

// writeTail writes the last block, footer and trailer, and syncs the file.
func (w *histSegmentWriter) writeTail(folded historyPos) error {
	if err := w.cw.flush(); err != nil {
		return err
	}

	footer := w.foot.footer(w.cw.offsets, folded)
	footerOff := w.cw.off

	if err := w.cw.frame(footer.encode()); err != nil {
		return err
	}

	if _, err := w.cw.w.Write(histTrailer(footerOff)); err != nil {
		return err
	}

	if err := w.cw.w.Flush(); err != nil {
		return err
	}

	return w.f.Sync()
}

// histTrailer returns a segment's trailer, for a footer at footerOff.
func histTrailer(footerOff uint64) []byte {
	b := binary.LittleEndian.AppendUint64(make([]byte, 0, histTrailerLen), footerOff)
	b = binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, histCRCTable))

	return append(b, histTrailerMagic...)
}

// histJobRecord is a parsed completed-job record. spec and state alias the
// record.
type histJobRecord struct {
	seq         uint64
	key         logKey
	spec, state []byte
}

// parseHistJobRecord returns the completed-job record rec, or
// errStoreCorrupt if it is not one.
func parseHistJobRecord(rec []byte) (histJobRecord, error) {
	if len(rec) < histRecHeader || rec[0] != histRecJob {
		return histJobRecord{}, fmt.Errorf("%w: not a completed-job record", errStoreCorrupt)
	}

	r := flatReader{b: rec, off: histRecHeader}
	spec := r.span(r.uvarint())

	if r.bad {
		return histJobRecord{}, fmt.Errorf("%w: a malformed completed-job record", errStoreCorrupt)
	}

	j := histJobRecord{seq: binary.LittleEndian.Uint64(rec[1:]), spec: spec, state: rec[r.off:]}
	copy(j.key[:], rec[9:histRecHeader])

	return j, nil
}

// parseHistNames reads a section of names.
func parseHistNames(r *flatReader) []string {
	names := make([]string, histCount(r, 1))

	for i := range names {
		names[i] = r.str()
	}

	return names
}

// histFooterOffset returns the footer offset segment f's trailer names, or
// errStoreCorrupt if f does not end in a valid trailer.
func histFooterOffset(f *os.File) (uint64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}

	if info.Size() < histTrailerLen {
		return 0, fmt.Errorf("%w: it is too short to hold a trailer", errStoreCorrupt)
	}

	var trailer [histTrailerLen]byte
	if _, err = f.ReadAt(trailer[:], info.Size()-histTrailerLen); err != nil {
		return 0, err
	}

	valid := bytes.Equal(trailer[12:], histTrailerMagic) &&
		binary.LittleEndian.Uint32(trailer[8:]) == crc32.Checksum(trailer[:8], histCRCTable)
	if !valid {
		return 0, fmt.Errorf("%w: its trailer is damaged", errStoreCorrupt)
	}

	return binary.LittleEndian.Uint64(trailer[:]), nil
}

// histFooterFills returns errStoreCorrupt unless raw, the footer frame's record
// at off in segment f, is a plain frame ending where f's trailer starts.
func histFooterFills(f *os.File, off uint64, raw []byte) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}

	if off+wal.FrameHeader+uint64(len(raw)) != uint64(info.Size()-histTrailerLen) { //nolint:gosec // not negative
		return fmt.Errorf("%w: its footer does not end at its trailer", errStoreCorrupt)
	}

	return nil
}
