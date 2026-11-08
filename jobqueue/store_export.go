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
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// errExportNotStore is ExportStore's error for a path that is not a store
// directory.
var errExportNotStore = errors.New("not a store directory")

// ExportStore writes a self-contained file of the stopped store at dbDir to w
// (spec E7): the tar `wr manager backup -p` writes (see backup), which a
// manager started with it as its db file restores (spec E2). It takes the
// store lock (dbDir + ".lock") first, so if a manager holds it, it fails with
// an error wrapping ErrDBLocked after waiting managerDBOpenTimeout for it, and
// recovers the store as a manager start would, making no backups. A dbDir that
// is missing is os.Stat's error, with nothing created; one that is not a
// directory is errExportNotStore, as is one with no VERSION, rather than
// becoming a new, empty store.
func ExportStore(dbDir string, w io.Writer) error {
	info, err := os.Stat(dbDir)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		return fmt.Errorf("%w: %s", errExportNotStore, dbDir)
	}

	if _, err = os.Stat(filepath.Join(dbDir, storeVersionFile)); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s has no %s", errExportNotStore, dbDir, storeVersionFile)
	}

	ctx := context.Background()

	lock, err := lockStore(ctx, dbDir)
	if err != nil {
		return err
	}

	s, err := openLogStore(ctx, dbDir)
	if err != nil {
		return errors.Join(err, lock.release())
	}

	s.lock = lock

	return errors.Join(s.backup(w), s.close(ctx))
}
