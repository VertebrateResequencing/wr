//go:build linux

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
	"os"

	"golang.org/x/sys/unix"
)

// backupPaceRange bounds the backup copy's dirty-page backlog using a pipelined
// sync_file_range: it starts asynchronous writeback of the just-written "current"
// chunk (SYNC_FILE_RANGE_WRITE, no wait), then waits for the "previous" chunk
// (one behind) to finish writing back (WAIT_BEFORE|WRITE|WAIT_AFTER) and drops it
// from the page cache (FADV_DONTNEED). This caps outstanding dirty pages at ~two
// chunks so a concurrent foreground DB commit's fdatasync never queues behind
// gigabytes of backup dirty pages (the cause of the periodic backup stall), while
// overlapping writeback with copying so it avoids the ~1.8x copy-duration cost of
// a blocking fsync-per-chunk. It reports handled=true (this Linux build paced the
// copy); non-Linux builds return handled=false so the caller falls back to fsync.
func backupPaceRange(f *os.File, curOffset, curLength, prevOffset, prevLength int64) (handled bool, err error) {
	fd := int(f.Fd())

	if curLength > 0 {
		if err = unix.SyncFileRange(fd, curOffset, curLength, unix.SYNC_FILE_RANGE_WRITE); err != nil {
			return true, err
		}
	}

	if prevLength > 0 {
		const waitFlags = unix.SYNC_FILE_RANGE_WAIT_BEFORE | unix.SYNC_FILE_RANGE_WRITE | unix.SYNC_FILE_RANGE_WAIT_AFTER
		if err = unix.SyncFileRange(fd, prevOffset, prevLength, waitFlags); err != nil {
			return true, err
		}

		// best-effort: drop the now-durable previous chunk from the page cache so
		// the multi-GB backup copy does not evict useful pages.
		_ = unix.Fadvise(fd, prevOffset, prevLength, unix.FADV_DONTNEED) //nolint:errcheck
	}

	return true, nil
}
