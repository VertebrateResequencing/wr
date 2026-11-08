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
	"errors"
	"os"
)

// errCompactNotSupported is CompactDBFileStats' error for a store directory,
// which it cannot compact until Phase 2.
var errCompactNotSupported = errors.New("compacting the new database store is not supported yet")

// CompactDBFileStats is the entry point for the offline `wr manager compact`
// subcommand (spec E6). Holding the store lock (dbFile + ".lock"), it compacts
// a bbolt file at dbFile as compactBoltDBFile does, and refuses a store
// directory with errCompactNotSupported, changing nothing. If a manager holds
// the lock it fails with an error wrapping ErrDBLocked, after waiting
// managerDBOpenTimeout for it. If nothing is at dbFile it returns os.Stat's
// error and creates nothing.
func CompactDBFileStats(dbFile string) (stats CompactStats, err error) {
	// a missing dbFile is reported as stat's error before taking the lock, so
	// it names dbFile and leaves no dbFile + ".lock" behind.
	if _, err = os.Stat(dbFile); err != nil {
		return stats, err
	}

	lock, err := lockStore(context.Background(), dbFile)
	if err != nil {
		return stats, err
	}

	defer func() {
		if errr := lock.release(); errr != nil {
			err = errors.Join(err, errr)
		}
	}()

	info, err := os.Stat(dbFile)
	if err != nil {
		return stats, err
	}

	if info.IsDir() {
		return stats, errCompactNotSupported
	}

	return compactBoltDBFile(dbFile)
}
