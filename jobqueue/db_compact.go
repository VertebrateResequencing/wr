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

// This file holds the copy `wr manager compact` makes of a database older than
// dbSchemaVersionNoCompleteStd, which removes the output older wr versions kept
// with completed jobs.

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

// maxUnreadableKeysReported caps how many unreadable complete records' keys a
// compaction reports, since a badly damaged database could have many.
const maxUnreadableKeysReported = 10

// compactStdDecodeObserver, if set, is called each time compaction decodes a
// complete record to strip its output. It is prod-inert and exists so tests can
// prove a compaction did not run a strip pass.
//
//nolint:gochecknoglobals // prod-inert test seam, like archiveTxObserver.
var compactStdDecodeObserver func()

// stdStripResult is what compactStrippingStd did to the complete records.
type stdStripResult struct {
	// stripped is how many records had output removed.
	stripped int

	// unreadable is how many records could not be decoded, and so were copied
	// unchanged; unreadableKeys holds the first maxUnreadableKeysReported of
	// their keys.
	unreadable     int
	unreadableKeys []string
}

// stdStrippingCopier copies one BoltDB into another the way bolt.Compact does,
// except that it removes the StdOutC and StdErrC of every bucketJobsComplete
// record. bolt.Compact has no hook to change a value, so this mirrors its walk:
// buckets are created in the same order with the same sequences, pages are
// filled completely, and the destination transaction is committed whenever it
// would exceed txMaxSize bytes of keys and values.
type stdStrippingCopier struct {
	dst       *bolt.DB
	tx        *bolt.Tx
	txMaxSize int64
	size      int64
	ch        codec.Handle
	result    stdStripResult
}

// copyBucket creates the bucket name, beneath the buckets named by path, and
// copies src's contents into it.
func (c *stdStrippingCopier) copyBucket(path [][]byte, name []byte, src *bolt.Bucket) error {
	if err := c.reserve(len(name)); err != nil {
		return err
	}

	b, err := c.createBucket(path, name)
	if err != nil {
		return err
	}

	if err = b.SetSequence(src.Sequence()); err != nil {
		return err
	}

	strip := len(path) == 0 && bytes.Equal(name, bucketJobsComplete)
	path = append(path, name)

	return src.ForEach(func(k, v []byte) error {
		if v == nil {
			return c.copyBucket(path, k, src.Bucket(k))
		}

		return c.copyValue(path, k, v, strip)
	})
}

// copyValue stores k and v in the bucket named by path, first stripping v's
// output if strip is set.
func (c *stdStrippingCopier) copyValue(path [][]byte, k, v []byte, strip bool) error {
	if strip {
		var err error
		if v, err = c.stripStd(k, v); err != nil {
			return err
		}
	}

	return c.put(path, k, v)
}

// reserve accounts for n more bytes in the current destination transaction,
// first committing it and starting another if n would take it over txMaxSize.
func (c *stdStrippingCopier) reserve(n int) error {
	sz := int64(n)
	if c.txMaxSize == 0 || c.size+sz <= c.txMaxSize {
		c.size += sz

		return nil
	}

	if err := c.tx.Commit(); err != nil {
		return err
	}

	tx, err := c.dst.Begin(true)
	if err != nil {
		return err
	}

	c.tx = tx
	c.size = sz

	return nil
}

// createBucket creates the bucket name beneath the buckets named by path, or at
// the top level if path is empty.
func (c *stdStrippingCopier) createBucket(path [][]byte, name []byte) (*bolt.Bucket, error) {
	if len(path) == 0 {
		return c.tx.CreateBucket(name)
	}

	return c.bucket(path).CreateBucket(name)
}

// put stores k and v in the bucket named by path.
func (c *stdStrippingCopier) put(path [][]byte, k, v []byte) error {
	if err := c.reserve(len(k) + len(v)); err != nil {
		return err
	}

	return c.bucket(path).Put(k, v)
}

// bucket returns the destination bucket named by path, set to fill its pages
// completely. It is looked up afresh because reserve may have started a new
// transaction.
func (c *stdStrippingCopier) bucket(path [][]byte) *bolt.Bucket {
	b := c.tx.Bucket(path[0])
	for _, name := range path[1:] {
		b = b.Bucket(name)
	}

	b.FillPercent = 1.0

	return b
}

// stripStd returns the complete record encoded, stored under key, without its
// StdOutC and StdErrC. It decodes and re-encodes with the db's codec as
// archiveJob does. It returns encoded itself if the record holds no output, or
// if it cannot be decoded: nothing can serve an unreadable record's output, and
// refusing to copy it would leave the database uncompactable for ever.
func (c *stdStrippingCopier) stripStd(key, encoded []byte) ([]byte, error) {
	if compactStdDecodeObserver != nil {
		compactStdDecodeObserver()
	}

	job := &Job{}
	if err := codec.NewDecoderBytes(encoded, c.ch).Decode(job); err != nil {
		c.result.unreadable++
		if len(c.result.unreadableKeys) < maxUnreadableKeysReported {
			c.result.unreadableKeys = append(c.result.unreadableKeys, string(key))
		}

		return encoded, nil
	}

	if len(job.StdOutC) == 0 && len(job.StdErrC) == 0 {
		return encoded, nil
	}

	job.StdOutC = nil
	job.StdErrC = nil

	var stripped []byte
	if err := codec.NewEncoderBytes(&stripped, c.ch).Encode(job); err != nil {
		return nil, fmt.Errorf("encode completed job %q: %w", key, err)
	}

	c.result.stripped++

	return stripped, nil
}

// compactStrippingStd copies src into the empty dst, removing the output of
// every completed job, then stamps dst with dbSchemaVersionNoCompleteStd. Every
// other value, nested bucket and bucket sequence is copied unchanged, as is a
// complete record that holds no output or cannot be decoded. It returns what it
// did to the complete records.
//
// Only successful jobs are ever archived into bucketJobsComplete (every wr
// version has refused to archive a non-zero exit), so no record there has
// output that should be kept.
func compactStrippingStd(dst, src *bolt.DB, txMaxSize int64) (result stdStripResult, err error) {
	tx, err := dst.Begin(true)
	if err != nil {
		return stdStripResult{}, err
	}

	c := &stdStrippingCopier{dst: dst, tx: tx, txMaxSize: txMaxSize, ch: new(codec.BincHandle)}

	// after a successful Commit this is a no-op returning ErrTxClosed.
	defer func() {
		if errr := c.tx.Rollback(); errr != nil && !errors.Is(errr, berrors.ErrTxClosed) {
			err = errors.Join(err, errr)
		}
	}()

	if err = c.copyAll(src); err != nil {
		return stdStripResult{}, err
	}

	return c.result, nil
}

// copyAll copies every bucket of src, then stamps the destination with
// dbSchemaVersionNoCompleteStd and commits.
func (c *stdStrippingCopier) copyAll(src *bolt.DB) error {
	err := src.View(func(stx *bolt.Tx) error {
		return stx.ForEach(func(name []byte, b *bolt.Bucket) error {
			return c.copyBucket(nil, name, b)
		})
	})
	if err != nil {
		return err
	}

	if err = putDBSchemaVersion(c.tx, dbSchemaVersionNoCompleteStd); err != nil {
		return err
	}

	return c.tx.Commit()
}
