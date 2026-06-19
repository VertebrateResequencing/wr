/*******************************************************************************
 * Copyright (c) 2020 Genome Research Ltd.
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

package local

import (
	"context"
	"os"
	"syscall"
	"testing"

	backoff "github.com/VertebrateResequencing/wr/backoff/time"
	"github.com/VertebrateResequencing/wr/fs"
	. "github.com/smartystreets/goconvey/convey"
)

func TestVolumeUsageCalculator(t *testing.T) {
	ctx := context.Background()

	Convey("VolumeUsageCalculator implements fs.VolumeUsageCalculator", t, func() {
		var _ fs.VolumeUsageCalculator = (*VolumeUsageCalculator)(nil)
	})

	Convey("Size() and Free() methods return real values", t, func() {
		path := os.TempDir()

		var stat syscall.Statfs_t

		err := syscall.Statfs(path, &stat)
		if err != nil {
			t.Fatalf("Statfs failed: %s", err)
		}

		So(stat.Bsize, ShouldBeGreaterThan, 0)
		expectedSize := stat.Blocks * uint64(stat.Bsize) //nolint:gosec // Statfs block size is positive above.

		calc := &VolumeUsageCalculator{}
		So(calc.Size(ctx, path), ShouldEqual, expectedSize)
		So(calc.Free(ctx, path), ShouldBeGreaterThan, 0)
	})

	Convey("NewVolume returns a useful Volume", t, func() {
		path := os.TempDir()
		volume := NewVolume(path)
		So(volume, ShouldNotBeNil)
		So(volume.Dir, ShouldEqual, path)
		So(volume.UsageCalculator, ShouldNotBeNil)
		cvc, ok := volume.UsageCalculator.(*fs.CachedVolumeUsageCalculator)
		So(ok, ShouldBeTrue)
		checkedvc, ok := cvc.UsageCalculator.(*fs.CheckedVolumeUsageCalculator)
		So(ok, ShouldBeTrue)
		So(checkedvc.UsageCalculator, ShouldHaveSameTypeAs, &VolumeUsageCalculator{})
		So(checkedvc.Retries, ShouldEqual, usefulNumOfRetryChecks)
		So(checkedvc.Backoff, ShouldResemble, backoff.SecondsRangeBackoff())

		Convey("With which you can get the size of a Volume", func() {
			So(volume.Size(ctx), ShouldBeGreaterThanOrEqualTo, 0)

			Convey("And ask if there's no space left", func() {
				So(volume.NoSpaceLeft(ctx), ShouldBeFalse)
			})
		})
	})
}
