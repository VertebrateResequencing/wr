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

package backoff

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/backoff/mock"
	"github.com/VertebrateResequencing/wr/clog"
	. "github.com/smartystreets/goconvey/convey"
)

func TestBackoff(t *testing.T) {
	ctx := context.Background()
	base := time.Now()

	Convey("Given a Backoff", t, func() {
		sleeper := &mock.Sleeper{}
		b := &Backoff{
			Min:     1 * time.Millisecond,
			Max:     10 * time.Millisecond,
			Factor:  2,
			Sleeper: sleeper,
		}

		Convey("It Sleep()s for Min", func() {
			b.Sleep(ctx)
			So(sleeper.Invoked(), ShouldEqual, 1)
			So(sleeper.Elapsed(), ShouldEqual, 1*time.Millisecond)

			Convey("The next call Sleep()s for Min*Factor, with jitter", func() {
				b.Sleep(ctx)
				So(sleeper.Invoked(), ShouldEqual, 2)
				So(base.Add(sleeper.Elapsed()), ShouldHappenOnOrBetween, base.Add(1*time.Millisecond), base.Add(3*time.Millisecond))

				Convey("Subsequent calls keep increasing sleep by Factor, with jitter", func() {
					b.Sleep(ctx)
					So(sleeper.Invoked(), ShouldEqual, 3)
					So(base.Add(sleeper.Elapsed()),
						ShouldHappenOnOrBetween,
						base.Add(3*time.Millisecond),
						base.Add(7*time.Millisecond),
					)

					b.Sleep(ctx)
					So(sleeper.Invoked(), ShouldEqual, 4)
					So(base.Add(sleeper.Elapsed()),
						ShouldHappenOnOrBetween,
						base.Add(7*time.Millisecond),
						base.Add(15*time.Millisecond),
					)

					Convey("But not above Max", func() {
						b.Sleep(ctx)
						So(sleeper.Invoked(), ShouldEqual, 5)
						elapsed := sleeper.Elapsed()
						So(base.Add(elapsed), ShouldHappenOnOrBetween, base.Add(15*time.Millisecond), base.Add(25*time.Millisecond))

						Convey("And it can be reset back to Min", func() {
							b.Reset()
							b.Sleep(ctx)
							So(sleeper.Invoked(), ShouldEqual, 6)
							So(sleeper.Elapsed(), ShouldEqual, elapsed+1*time.Millisecond)
						})
					})
				})
			})
		})

		Convey("Sleep()s are logged", func() {
			buff := clog.ToBufferAtLevel("debug")

			defer clog.ToDefault()

			b.Sleep(ctx)
			So(buff.String(), ShouldContainSubstring, "lvl=dbug")
			So(buff.String(), ShouldContainSubstring, "msg=backoff")
			So(buff.String(), ShouldContainSubstring, "sleep=1ms")
		})
	})

	testBackoffProperties := func(
		minSleep, maxSleep time.Duration,
		factor float64,
		elapsedAfter1Sleep, elapsedAfter2Sleeps time.Duration) {
		sleeper := &mock.Sleeper{}
		b := &Backoff{
			Min:     minSleep,
			Max:     maxSleep,
			Factor:  factor,
			Sleeper: sleeper,
		}

		b.Sleep(ctx)
		So(sleeper.Invoked(), ShouldEqual, 1)
		So(sleeper.Elapsed(), ShouldEqual, elapsedAfter1Sleep)

		b.Sleep(ctx)
		So(sleeper.Invoked(), ShouldEqual, 2)
		So(sleeper.Elapsed(), ShouldEqual, elapsedAfter2Sleeps)
	}

	Convey("If you create a Backoff with a Factor less than 1, it always Sleep()s for Min", t, func() {
		testBackoffProperties(
			1*time.Millisecond,
			10*time.Millisecond,
			0.5,
			1*time.Millisecond,
			2*time.Millisecond,
		)
	})

	Convey("A Backoff treats Max less than Min as Min", t, func() {
		testBackoffProperties(
			10*time.Millisecond,
			0*time.Millisecond,
			1,
			10*time.Millisecond,
			20*time.Millisecond,
		)
	})

	Convey("A 0ms Min Backoff sleeps for 0ms every time", t, func() {
		testBackoffProperties(
			0*time.Millisecond,
			10*time.Millisecond,
			2,
			0*time.Millisecond,
			0*time.Millisecond,
		)
	})

	Convey("A Backoff can be used concurrently", t, func() {
		sleeper := &mock.Sleeper{}
		b := &Backoff{
			Min:     1 * time.Millisecond,
			Max:     10 * time.Millisecond,
			Factor:  1,
			Sleeper: sleeper,
		}

		wg := &sync.WaitGroup{}

		sleep := func() {
			b.Sleep(ctx)
			wg.Done()
		}

		c := 4
		wg.Add(c)

		for i := 1; i <= c; i++ {
			go sleep()
		}

		wg.Add(1)

		go func() {
			b.Reset()
			b.Sleep(ctx)
			wg.Done()
		}()

		wg.Wait()

		So(sleeper.Invoked(), ShouldEqual, 5)
		So(base.Add(sleeper.Elapsed()), ShouldHappenOnOrBetween, base.Add(4*time.Millisecond), base.Add(5*time.Millisecond))
	})
}
