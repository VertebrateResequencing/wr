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

package fs

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/VertebrateResequencing/wr/backoff"
	bm "github.com/VertebrateResequencing/wr/backoff/mock"
	"github.com/VertebrateResequencing/wr/fs/mock"
)

func TestVolume(t *testing.T) {
	ctx := context.Background()
	path := os.TempDir()

	Convey("Calling Size() multiple times calculates the size multiple times", t, func() {
		expectedSize := 1
		m := &mock.VolumeUsageCalculator{
			SizeFn: func(volumePath string) uint64 {
				return gb
			},
		}
		volume := &Volume{Dir: path, UsageCalculator: m}
		So(volume.Size(ctx), ShouldEqual, expectedSize)
		So(volume.Size(ctx), ShouldEqual, expectedSize)
		So(m.SizeInvoked, ShouldEqual, 2)

		Convey("Unless a CachedVolumeUsageCalculator is used; then it is only calculated once", func() {
			m.SizeInvoked = 0
			cached := &CachedVolumeUsageCalculator{UsageCalculator: m}
			volume.UsageCalculator = cached
			So(volume.Size(ctx), ShouldEqual, expectedSize)
			So(volume.Size(ctx), ShouldEqual, expectedSize)
			So(m.SizeInvoked, ShouldEqual, 1)
		})
	})

	Convey("NoSpaceLeft() returns true when there's less than 100MB", t, func() {
		frees := []uint64{5, 0}
		m := &mock.VolumeUsageCalculator{}
		m.FreeFn = func(volumePath string) uint64 {
			return frees[m.FreeInvoked-1]
		}
		volume := &Volume{Dir: path, UsageCalculator: m}
		So(volume.NoSpaceLeft(ctx), ShouldBeTrue)

		Convey("This result is not cached by a CachedVolumeUsageCalculator", func() {
			m.FreeInvoked = 0
			cached := &CachedVolumeUsageCalculator{UsageCalculator: m}
			volume.UsageCalculator = cached
			So(volume.NoSpaceLeft(ctx), ShouldBeTrue)
			So(volume.NoSpaceLeft(ctx), ShouldBeTrue)
			So(m.FreeInvoked, ShouldEqual, 2)
		})
	})

	makeCheckedMockVolumeAndCalculator := func(attempts int,
		wait time.Duration,
		max time.Duration) (*Volume, *mock.VolumeUsageCalculator, *bm.Sleeper) {
		m := &mock.VolumeUsageCalculator{
			FreeFn: func(volumePath string) uint64 {
				return 0
			},
			SizeFn: func(volumePath string) uint64 {
				return 0
			},
		}
		bm := &bm.Sleeper{}
		checked := &CheckedVolumeUsageCalculator{
			UsageCalculator: m,
			Retries:         attempts - 1,
			Backoff: &backoff.Backoff{
				Min:     wait,
				Max:     max,
				Factor:  2,
				Sleeper: bm,
			},
		}
		volume := &Volume{Dir: path, UsageCalculator: checked}

		return volume, m, bm
	}

	Convey("When using a CheckedVolumeUsageCalculator", t, func() {
		attempts := 3
		volume, m, _ := makeCheckedMockVolumeAndCalculator(attempts, 0*time.Millisecond, 0*time.Millisecond)

		Convey("Free space is checked multiple times if 0", func() {
			So(volume.NoSpaceLeft(ctx), ShouldBeTrue)
			So(m.FreeInvoked, ShouldEqual, attempts)
		})

		Convey("You can choose the number of attempts when checking", func() {
			attempts = 4
			volume, m, _ = makeCheckedMockVolumeAndCalculator(attempts, 0*time.Millisecond, 0*time.Millisecond)
			So(volume.NoSpaceLeft(ctx), ShouldBeTrue)
			So(m.FreeInvoked, ShouldEqual, attempts)
		})

		Convey("You can choose how long to wait in between checks", func() {
			var bm *bm.Sleeper
			volume, m, bm = makeCheckedMockVolumeAndCalculator(attempts, 2*time.Millisecond, 2*time.Millisecond)
			So(volume.NoSpaceLeft(ctx), ShouldBeTrue)
			So(m.FreeInvoked, ShouldEqual, attempts)
			So(bm.Invoked(), ShouldEqual, attempts-1)
			So(bm.Elapsed(), ShouldEqual, time.Duration((attempts-1)*2)*time.Millisecond)
		})

		Convey("Free space is only checked once if more than 0", func() {
			m.FreeFn = func(volumePath string) uint64 {
				return 1
			}
			So(volume.NoSpaceLeft(ctx), ShouldBeTrue)
			So(m.FreeInvoked, ShouldEqual, 1)
		})
	})

	Convey("When using a CheckedVolumeUsageCalculator, size is checked multiple times if 0", t, func() {
		attempts := 3
		volume, m, bm := makeCheckedMockVolumeAndCalculator(attempts, 2*time.Millisecond, 1*time.Hour)
		So(volume.Size(ctx), ShouldEqual, 0)
		So(m.SizeInvoked, ShouldEqual, attempts)
		So(bm.Invoked(), ShouldEqual, attempts-1)
		elapsed := bm.Elapsed()
		So(elapsed, ShouldBeGreaterThan, time.Duration((attempts-1)*2)*time.Millisecond)

		Convey("The backoff is reset when size is greater than 0", func() {
			m.SizeFn = func(volumePath string) uint64 {
				return gb
			}
			So(volume.Size(ctx), ShouldEqual, 1)
			So(m.SizeInvoked, ShouldEqual, attempts+1)
			So(bm.Invoked(), ShouldEqual, attempts-1)
			So(bm.Elapsed(), ShouldEqual, elapsed)

			called := false
			m.SizeFn = func(volumePath string) uint64 {
				if called {
					return gb
				}
				called = true

				return 0
			}
			So(volume.Size(ctx), ShouldEqual, 1)
			So(m.SizeInvoked, ShouldEqual, attempts+3)
			So(bm.Invoked(), ShouldEqual, attempts)
			So(bm.Elapsed(), ShouldEqual, elapsed+2*time.Millisecond)
		})
	})
}
