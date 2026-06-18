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

package mock

import (
	"context"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/VertebrateResequencing/wr/fs"
)

func TestVolumeUsageCalculator(t *testing.T) {
	ctx := context.Background()

	Convey("VolumeUsageCalculator implements fs.VolumeUsageCalculator", t, func() {
		var _ fs.VolumeUsageCalculator = (*VolumeUsageCalculator)(nil)
	})

	Convey("Size() and Free() methods are just mocks", t, func() {
		path := "/foo"
		answer := uint64(1)

		calc := &VolumeUsageCalculator{
			FreeFn: func(volumePath string) uint64 {
				return answer
			},
			SizeFn: func(volumePath string) uint64 {
				return answer
			},
		}

		So(calc.Free(ctx, path), ShouldEqual, answer)
		So(calc.FreeInvoked, ShouldEqual, 1)
		So(calc.Size(ctx, path), ShouldEqual, answer)
		So(calc.SizeInvoked, ShouldEqual, 1)
	})
}
