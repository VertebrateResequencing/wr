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

// This file holds history.bolt's pending reverse rglookups: the folder puts
// the keyRepgroups entry of each rglookup it folds into keyRepgroupsPending,
// keyed by its record's seq first, so near its end, and merges them into
// keyRepgroups in key order, a transaction at a time, once
// keyRepgroupsPendingMerge have gathered. keyRepgroups is keyed by job key,
// which is random, so each entry put into it as its rglookup folded changed a
// page of its own, and a page above it: in a 12GB history.bolt on NFS, 3 of
// the 4 pages an rglookup's fold wrote, as many writes to the file server as
// the rest of the fold's. Merged a million at a time in key order, its
// entries share pages instead. A merge takes the entries a sixteenth of the
// key space at a time, by their job key's first hex digit, so that it holds
// about 1/16 of their keys in memory, about 5MB, and still moves them in key
// order. Only a fold of a modify reads either bucket,
// to find the rglookups of its old key (see dropHistoryLookups and
// dropPendingLookups).

import (
	"bytes"
	"encoding/binary"
	"slices"
	"sync/atomic"

	bolt "go.etcd.io/bbolt"
)

const (
	// keyRepgroupsPendingMerge is how many entries keyRepgroupsPending gathers
	// before they are merged into keyRepgroups: about 50 minutes of the 1g.3
	// soak's rglookups, so that each of keyRepgroups' pages (about 105,000
	// for its 3.6M entries) takes about ten new entries a merge, while a
	// modify's fold scans at most about 60MB of pending entries.
	keyRepgroupsPendingMerge = 1 << 20

	// pendingSeqLen is the length of the seq a pending entry's key starts
	// with.
	pendingSeqLen = 8

	// reverseMergeParts is how many parts of the key space, by a job key's
	// first hex digit, a merge of keyRepgroupsPending takes one at a time.
	reverseMergeParts = len(hexDigits)

	// hexDigits are the digits a job key's hex starts with, in order, one per
	// part of the key space.
	hexDigits = "0123456789abcdef"
)

// bucketKeyRGsPending holds the keyRepgroups entries not merged into it yet,
// each keyed by the seq of its rglookup record and then its keyRepgroups key.
var bucketKeyRGsPending = []byte("keyRepgroupsPending") //nolint:gochecknoglobals

// logReverseMergeEntries, if above 0, replaces keyRepgroupsPendingMerge. A
// test seam, 0 outside tests.
var logReverseMergeEntries atomic.Int64 //nolint:gochecknoglobals // a test seam

// pendingReverseKey returns the keyRepgroupsPending key of key's rglookup
// under repGroup, whose latest record has seq seq.
func pendingReverseKey(seq uint64, key, repGroup string) []byte {
	k := binary.BigEndian.AppendUint64(make([]byte, 0, pendingSeqLen+len(key)+len(dbDelimiter)+len(repGroup)), seq)
	k = append(k, key...)
	k = append(k, dbDelimiter...)

	return append(k, repGroup...)
}

// pendingReverse is the folder's note of keyRepgroupsPending: about how many
// entries it holds (-1 until counted), never fewer, as a modify's fold drops
// some without counting them, the keys of those it is merging of one part of
// the key space, in the order of their keyRepgroups keys, and the part a
// merge under way takes next, 0 when none is.
type pendingReverse struct {
	pending int
	merging [][]byte
	part    int
}

// underWay reports whether a merge of keyRepgroupsPending is under way.
func (p *pendingReverse) underWay() bool {
	return len(p.merging) > 0 || p.part > 0
}

// takeNextPart takes the keys to merge of the next part of the key space that
// pending, keyRepgroupsPending, has any of, up to its last part.
func (p *pendingReverse) takeNextPart(pending *bolt.Bucket) error {
	for len(p.merging) == 0 {
		keys, err := mergeOrder(pending, p.part, p.pending/reverseMergeParts)
		if err != nil {
			return err
		}

		p.merging = keys
		p.part = (p.part + 1) % reverseMergeParts

		if p.part == 0 {
			return nil
		}
	}

	return nil
}

// mergeOrder returns the keys of pending's entries of part of the key space
// (see reverseMergeParts), about n of them, in the order of their
// keyRepgroups keys.
func mergeOrder(pending *bolt.Bucket, part, n int) ([][]byte, error) {
	keys := make([][]byte, 0, n)
	digit := hexDigits[part]

	err := pending.ForEach(func(k, _ []byte) error {
		if len(k) > pendingSeqLen && k[pendingSeqLen] == digit {
			keys = append(keys, slices.Clone(k))
		}

		return nil
	})

	slices.SortFunc(keys, func(a, b []byte) int { return bytes.Compare(a[pendingSeqLen:], b[pendingSeqLen:]) })

	return keys, err
}

// due reports whether pending, keyRepgroupsPending, holds at least
// keyRepgroupsPendingMerge entries, counting them if p's note of them is not
// below that or not counted yet: the note counts none a modify's fold drops,
// so it can count more than there are, and is counted again rather than
// starting a merge that finds fewer, or none at each fold.
func (p *pendingReverse) due(pending *bolt.Bucket) bool {
	if p.pending >= 0 && p.pending < reverseMergeEntries() {
		return false
	}

	p.pending = pending.Stats().KeyN

	return p.pending >= reverseMergeEntries()
}

// reverseMergeEntries returns keyRepgroupsPendingMerge, or
// logReverseMergeEntries if a test set it.
func reverseMergeEntries() int {
	if n := logReverseMergeEntries.Load(); n > 0 {
		return int(n)
	}

	return keyRepgroupsPendingMerge
}

// dropPendingLookups deletes every rglookup of each of keys that
// keyRepgroupsPending indexes, from repgroupToKey and keyRepgroupsPending,
// scanning keyRepgroupsPending once.
func dropPendingLookups(tx *bolt.Tx, keys []logKey) error {
	if len(keys) == 0 {
		return nil
	}

	pending, lookups := tx.Bucket(bucketKeyRGsPending), tx.Bucket(bucketRTK)

	found, err := pendingOfKeys(pending, keys)
	if err != nil {
		return err
	}

	for _, k := range found {
		key, rest := k[pendingSeqLen:pendingSeqLen+logKeyHexLen], k[pendingSeqLen+logKeyHexLen+len(dbDelimiter):]

		if err = lookups.Delete(slices.Concat(rest, []byte(dbDelimiter), key)); err != nil {
			return err
		}

		if err = pending.Delete(k); err != nil {
			return err
		}
	}

	return nil
}

// pendingOfKeys returns the keys of pending's entries of each of keys.
func pendingOfKeys(pending *bolt.Bucket, keys []logKey) ([][]byte, error) {
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k.String()] = true
	}

	var found [][]byte

	err := pending.ForEach(func(k, _ []byte) error {
		if len(k) >= pendingSeqLen+logKeyHexLen && drop[string(k[pendingSeqLen:pendingSeqLen+logKeyHexLen])] {
			found = append(found, slices.Clone(k))
		}

		return nil
	})

	return found, err
}

// moveReverse moves the entries of keyRepgroupsPending with keys from
// keyRepgroupsPending to keyRepgroups in tx, in order, foldLookupBatch at a
// time until size is full, returning how many of keys it took. A key no
// longer pending, as a modify's fold dropped it, is skipped.
func moveReverse(tx *bolt.Tx, keys [][]byte, size *foldTxSize) (int, error) {
	pending, reverse := tx.Bucket(bucketKeyRGsPending), tx.Bucket(bucketKeyRGs)

	for i, k := range keys {
		if i > 0 && i%foldLookupBatch == 0 && size.full() {
			return i, nil
		}

		written, err := moveOneReverse(pending, reverse, k)
		if err != nil {
			return i, err
		}

		size.written += written
	}

	return len(keys), nil
}

// moveOneReverse moves keyRepgroupsPending's entry k, if it has it, from
// pending to reverse, keyRepgroups, returning the bytes of keys it wrote.
func moveOneReverse(pending, reverse *bolt.Bucket, k []byte) (int, error) {
	if got, _ := pending.Cursor().Seek(k); !bytes.Equal(got, k) {
		return 0, nil
	}

	if err := reverse.Put(k[pendingSeqLen:], nil); err != nil {
		return 0, err
	}

	return len(k) + len(k[pendingSeqLen:]), pending.Delete(k)
}

// mergeSomeReverse makes one transaction of merging keyRepgroupsPending into
// keyRepgroups, if a merge is under way or keyRepgroupsPending holds
// keyRepgroupsPendingMerge entries, which starts one: it moves as many
// entries as fit maxBytes, as a fold's transaction does, in keyRepgroups' key
// order, skipping any a modify's fold has dropped since the merge started.
// The folder calls it before each fold, so a merge goes a transaction a fold.
func (h *historyBolt) mergeSomeReverse(maxBytes int) error {
	if err := h.startReverseMerge(); err != nil || len(h.reverse.merging) == 0 {
		return err
	}

	moved := 0

	err := h.bolt.Update(func(tx *bolt.Tx) error {
		pageSize := tx.DB().Info().PageSize
		size := &foldTxSize{tx: tx, pageSize: pageSize, max: max(maxBytes, h.freelistWrite(pageSize)),
			scale: h.writeScale}

		var err error

		moved, err = moveReverse(tx, h.reverse.merging, size)

		return err
	})
	if err != nil {
		return err
	}

	h.reverse.merging = h.reverse.merging[moved:]
	h.reverse.pending -= moved

	return nil
}

// startReverseMerge, if it has no keys left to merge, takes those of the next
// part of the key space that has any, if a merge is under way, or else if one
// is due (see due), which starts one.
func (h *historyBolt) startReverseMerge() error {
	if len(h.reverse.merging) > 0 {
		return nil
	}

	return h.bolt.View(func(tx *bolt.Tx) error {
		pending := tx.Bucket(bucketKeyRGsPending)

		if h.reverse.part == 0 && !h.reverse.due(pending) {
			return nil
		}

		return h.reverse.takeNextPart(pending)
	})
}
