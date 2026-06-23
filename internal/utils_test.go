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
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
	. "github.com/smartystreets/goconvey/convey"
)

var errLegacyEOF = errors.New("EOF") // Regression sentinel for legacy close behaviour.

type wrappedEOFCloser struct{}

func (wrappedEOFCloser) Close() error {
	return fmt.Errorf("wrapped close: %w", io.EOF)
}

func TestUtilsErrorWrapping(t *testing.T) {
	Convey("Given utility errors with context", t, func() {
		Convey("PathToContent preserves the read failure cause", func() {
			_, err := PathToContent(filepath.Join(t.TempDir(), "missing"))

			So(err, ShouldNotBeNil)
			So(errors.Is(err, os.ErrNotExist), ShouldBeTrue)
		})

		Convey("LogClose ignores wrapped EOF close errors", func() {
			buff := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			LogClose(context.Background(), wrappedEOFCloser{}, "wrapped eof")

			So(buff.String(), ShouldEqual, "")
		})

		Convey("LogClose still ignores legacy EOF string close errors", func() {
			buff := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			LogClose(context.Background(), legacyEOFCloser{}, "legacy eof")

			So(buff.String(), ShouldEqual, "")
		})
	})
}

type legacyEOFCloser struct{}

func (legacyEOFCloser) Close() error {
	return errLegacyEOF
}
