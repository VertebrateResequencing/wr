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
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"unsafe"

	. "github.com/smartystreets/goconvey/convey"
	"golang.org/x/sys/unix"
)

func TestLogStoreCheckpointPacesItsWrite(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store holding 3,000 live jobs with 4KiB random commands, and the default pace", t, func() {
		var paces atomic.Int64

		origHook := backupPaceHook
		backupPaceHook = func() { paces.Add(1) }

		Reset(func() { backupPaceHook = origHook })

		s, _ := openTestLogStore(t, ctx)

		r := rand.New(rand.NewSource(1)) //nolint:gosec // reproducible commands

		jobs := make([]*Job, 0, 150*20)
		for tick := range 150 {
			jobs = append(jobs, deferTestJobs(r, tick)...)
		}

		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		Convey("a checkpoint forces the writeback of its file at least once a MiB, and leaves the file in the "+
			"page cache for the reads of its records", func() {
			So(s.checkpoint(), ShouldBeNil)

			f, err := os.Open(checkpointPath(s.dir, s.ckptFrom))
			So(err, ShouldBeNil)

			defer f.Close()

			info, err := f.Stat()
			So(err, ShouldBeNil)
			So(info.Size(), ShouldBeGreaterThan, 3<<20)
			So(paces.Load(), ShouldBeGreaterThanOrEqualTo, info.Size()>>20-1)
			soResidency(t, f, info.Size(), ShouldBeGreaterThan, 0.5)
		})
	})
}

// soResidency asserts that the fraction of f's pages, f being size bytes, in
// the page cache is as assertion and expected say, if the filesystem holding f
// can show that; else it logs why it does not check.
func soResidency(t *testing.T, f *os.File, size int64, assertion func(any, ...any) string, expected float64) {
	t.Helper()

	if why := residencyUnobservable(filepath.Dir(f.Name())); why != "" {
		t.Logf("not checking the page-cache residency of %s: %s", f.Name(), why)
		SkipSo(residentFraction(f, size), assertion, expected)

		return
	}

	So(residentFraction(f, size), assertion, expected)
}

// residencyUnobservable returns why files in dir cannot show whether pages are
// kept in the page cache or dropped from it, or "" if they can: a probe file
// there, written, synced, renamed and opened again, as the tests' files are,
// must have its pages resident, as on ext4 (NFS's mincore then shows none),
// and must lose them on FADV_DONTNEED (tmpfs keeps them).
func residencyUnobservable(dir string) string {
	if runtime.GOOS != osLinux {
		return "mincore and FADV_DONTNEED are checked on linux only"
	}

	w, err := os.CreateTemp(dir, "residency-probe")
	So(err, ShouldBeNil)

	const size = 16 << 12

	_, err = w.Write(make([]byte, size))
	So(err, ShouldBeNil)
	So(w.Sync(), ShouldBeNil)
	So(w.Close(), ShouldBeNil)

	path := w.Name() + ".renamed"
	So(os.Rename(w.Name(), path), ShouldBeNil)

	defer func() { So(os.Remove(path), ShouldBeNil) }()

	f, err := os.Open(path)
	So(err, ShouldBeNil)

	defer f.Close()

	if residentFraction(f, size) == 0 {
		return "a file written, synced, renamed and opened again there shows no page resident (as on NFS)"
	}

	So(unix.Fadvise(int(f.Fd()), 0, size, unix.FADV_DONTNEED), ShouldBeNil)

	if residentFraction(f, size) == 1 {
		return "FADV_DONTNEED leaves its pages resident there (as on tmpfs)"
	}

	return ""
}

// residentFraction returns the fraction of f's pages, f being size bytes, that
// are in the page cache.
func residentFraction(f *os.File, size int64) float64 {
	data, err := unix.Mmap(int(f.Fd()), 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	So(err, ShouldBeNil)

	defer func() { So(unix.Munmap(data), ShouldBeNil) }()

	pages := (int(size) + os.Getpagesize() - 1) / os.Getpagesize()
	vec := make([]byte, pages)
	_, _, errno := unix.Syscall(unix.SYS_MINCORE, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
		uintptr(unsafe.Pointer(&vec[0])))
	So(errno, ShouldEqual, unix.Errno(0))

	resident := 0

	for _, v := range vec {
		resident += int(v & 1)
	}

	return float64(resident) / float64(pages)
}
