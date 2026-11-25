//go:build reliability_repro

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
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// copyBigDBSynced copies a big DB fixture to dst for a scenario to mutate, then
// writes dst and everything else dirty on dst's filesystem to disk, so the
// scenario's measured window starts with no writeback of setup data pending.
//
// Without this, the kernel's writeback of the unsynced multi-GB copy, and of
// the test binary `go test` had just linked into the same /tmp, stalled one
// bolt commit's fdatasync for tens to hundreds of ms some seconds into the
// window, so a round's max latency measured host writeback rather than the
// product (bugfix 261119 item M).
//
// The speed gate's base trees get this file too (DEVELOPERS.md), so both trees
// start their windows alike; it depends on nothing else in the package, so it
// builds in older trees and survives Phase 2's removal of the bbolt-only tests.
func copyBigDBSynced(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}

	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	buf := make([]byte, 8<<20)
	if _, err = io.CopyBuffer(out, in, buf); err != nil {
		_ = out.Close()

		return err
	}

	if err = out.Sync(); err != nil {
		_ = out.Close()

		return err
	}

	if unix.Syncfs(int(out.Fd())) != nil {
		unix.Sync()
	}

	return out.Close()
}
