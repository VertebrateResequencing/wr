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

package internal

import (
	"errors"
	"math"
	"os"
	"strconv"
	"syscall"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestSignalPid(t *testing.T) {
	Convey("SignalPid refuses pids that kill(2) treats as process groups", t, func() {
		invalid := []int{0, -1, -2}

		if strconv.IntSize == 64 {
			// beyond any kernel pid, but only representable in a 64-bit int
			beyond := int64(math.MaxInt32)
			beyond++

			invalid = append(invalid, int(beyond))
		}

		for _, pid := range invalid {
			So(ValidPid(pid), ShouldBeFalse)
			So(errors.Is(SignalPid(pid, syscall.Signal(0)), ErrInvalidPid), ShouldBeTrue)
		}
	})

	Convey("SignalPid probes a real pid", t, func() {
		So(ValidPid(os.Getpid()), ShouldBeTrue)
		So(SignalPid(os.Getpid(), syscall.Signal(0)), ShouldBeNil)
	})
}
