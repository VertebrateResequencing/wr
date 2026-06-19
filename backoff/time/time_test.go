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

package time

import (
	"context"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/backoff"
	. "github.com/smartystreets/goconvey/convey"
)

func TestSleeper(t *testing.T) {
	Convey("Sleeper implements backoff.Sleeper", t, func() {
		var _ backoff.Sleeper = (*Sleeper)(nil)
	})

	Convey("Sleeper.Sleep() really sleeps", t, func() {
		sleeper := &Sleeper{}
		tn := time.Now()
		delay := 1 * time.Millisecond

		sleeper.Sleep(context.Background(), delay)
		So(time.Now(), ShouldHappenOnOrAfter, tn.Add(delay))
	})

	Convey("Sleep() can be cancelled via the context", t, func() {
		sleeper := &Sleeper{}
		tn := time.Now()
		delay := 1 * time.Second
		cancelAfter := 1 * time.Millisecond

		ctx, cancel := context.WithTimeout(context.Background(), cancelAfter)
		defer cancel()

		sleeper.Sleep(ctx, delay)
		So(time.Now(), ShouldHappenBetween, tn.Add(cancelAfter), tn.Add(500*time.Millisecond))
	})

	Convey("SecondsRangeBackoff returns a generally useful Backoff in the seconds range", t, func() {
		b := SecondsRangeBackoff()
		So(b.Min, ShouldEqual, secondsRangeMin)
		So(b.Max, ShouldEqual, secondsRangeMax)
		So(b.Factor, ShouldEqual, secondsRangeFactor)
		So(b.Sleeper, ShouldHaveSameTypeAs, &Sleeper{})
	})
}
