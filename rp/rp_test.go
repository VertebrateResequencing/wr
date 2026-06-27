/*******************************************************************************
 * Copyright (c) 2017-2019, 2026 Genome Research Ltd.
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

package rp

import (
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func BenchmarkRP(b *testing.B) {
	delayBetween := 0 * time.Millisecond
	releaseTimeout := 200 * time.Millisecond

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		rp1 := New("l1", delayBetween, 5, releaseTimeout)
		rp2 := New("l2", delayBetween, 6, releaseTimeout)

		r11 := getRequest(rp1)
		r12 := getRequest(rp1)
		r13 := getRequest(rp1)
		r14 := getRequest(rp1)
		r15 := getRequest(rp1)
		r16 := getRequest(rp1)
		r21 := getRequest(rp2)
		r22 := getRequest(rp2)
		r23 := getRequest(rp2)
		r24 := getRequest(rp2)
		r25 := getRequest(rp2)
		r26 := getRequest(rp2)
		r27 := getRequest(rp2)

		rp1.WaitUntilGranted(r11)
		rp1.WaitUntilGranted(r12)
		rp1.WaitUntilGranted(r13)
		rp1.WaitUntilGranted(r14)
		rp1.WaitUntilGranted(r15)
		rp1.Granted(r16)
		rp2.WaitUntilGranted(r21)
		rp2.WaitUntilGranted(r22)
		rp2.WaitUntilGranted(r23)
		rp2.WaitUntilGranted(r24)
		rp2.WaitUntilGranted(r25)
		rp2.WaitUntilGranted(r26)
		rp2.Granted(r27)

		rp1.Release(r11)
		rp1.Release(r12)
		rp1.Release(r13)
		rp1.Release(r14)
		rp1.Release(r15)
		rp2.Release(r21)
		rp2.Release(r22)
		rp2.Release(r23)
		rp2.Release(r24)
		rp2.Release(r25)
		rp2.Release(r26)

		r11 = getRequest(rp1)
		r12 = getRequest(rp1)
		r13 = getRequest(rp1)
		r14 = getRequest(rp1)
		r15 = getRequest(rp1)
		r16 = getRequest(rp1)
		r17 := getRequest(rp1)
		r18 := getRequest(rp1)
		r19 := getRequest(rp1)
		r110 := getRequest(rp1)

		rp1.Granted(r11)
		rp1.Granted(r12)
		rp1.Granted(r13)
		rp1.Granted(r14)
		rp1.Granted(r15)
		rp1.Granted(r16)
		rp1.Granted(r17)
		rp1.Granted(r18)
		rp1.Granted(r19)
		rp1.Granted(r110)
		rp1.Release(r11)
		rp1.Release(r12)
		rp1.Release(r13)
		rp1.Release(r14)
		rp1.Release(r15)
		rp1.Release(r16)
		rp1.Release(r17)
		rp1.Release(r18)
		rp1.Release(r19)
		rp1.Release(r110)
	}
}

func getRequest(rp *Protector) Receipt {
	r, err := rp.Request(1)
	if err != nil {
		fmt.Printf("Request had an error: %s\n", err) //nolint:forbidigo // test diagnostic
	}

	return r
}

func TestRP(t *testing.T) {
	synctest.Test(t, testRPBody)
}

func testRPBody(t *testing.T) {
	Convey("You can make a new Protector", t, func() {
		delayInt := 50
		delayBetween := time.Duration(delayInt) * time.Millisecond
		maxSimultaneous := 3
		releaseTimeout := time.Duration(delayInt*5) * time.Millisecond
		halfDelay := time.Duration(delayInt/2) * time.Millisecond
		oneFiftyPercentDelay := time.Duration(delayInt+(delayInt/2)) * time.Millisecond
		doubleDelay := time.Duration(delayInt*2) * time.Millisecond

		rp := New("irods", delayBetween, maxSimultaneous, releaseTimeout)
		So(rp, ShouldNotBeNil)
		Reset(func() {
			rp.Shutdown()
			synctest.Wait()
		})

		begin := time.Now()

		Convey("Request() returns immediately, but there is a delay between each granting and once all "+
			"tokens have been granted", func() {
			grantedCh := make(chan time.Time, maxSimultaneous)
			for i := 1; i <= maxSimultaneous; i++ {
				r, err := rp.Request(1)
				So(err, ShouldBeNil)

				go func(r Receipt) {
					rp.WaitUntilGranted(r)

					grantedCh <- time.Now()
				}(r)
			}

			So(time.Now(), ShouldHappenBefore, begin.Add(halfDelay))

			r, err := rp.Request(1)
			So(err, ShouldBeNil)
			So(r, ShouldNotBeNil)

			So(rp.WaitUntilGranted(r), ShouldBeTrue)
			So(time.Now(), ShouldHappenOnOrBetween, begin.Add(releaseTimeout), begin.Add(releaseTimeout).Add(halfDelay))
			rp.Release(r)

			for i := range maxSimultaneous {
				expected := begin.Add(time.Duration(delayInt*i) * time.Millisecond)
				So(<-grantedCh, ShouldHappenOnOrBetween, expected, expected.Add(halfDelay))
			}
		})

		Convey("You can't Request more tokens than max", func() {
			r, err := rp.Request(maxSimultaneous + 1)
			So(string(r), ShouldBeBlank)
			So(err, ShouldNotBeNil)
			shouldBeRPError(err, ErrOverMaximumTokens)
		})

		Convey("You can't do anything with an invalid receipt", func() {
			r, err := rp.Request(1)
			So(err, ShouldBeNil)

			badR := Receipt("invalid")
			So(rp.WaitUntilGranted(badR), ShouldBeFalse)
			So(rp.WaitUntilGranted(r), ShouldBeTrue)

			// Touch() and Release() don't return anything; the most we can do
			// is confirm we don't crash
			rp.Touch(badR)
			rp.Release(badR)
			rp.Touch(r)
			rp.Release(r)
		})

		Convey("You can't do anything with a Shutdown() Protector", func() {
			r, err := rp.Request(1)
			So(err, ShouldBeNil)
			So(rp.WaitUntilGranted(r), ShouldBeTrue)
			r2, err := rp.Request(1)
			So(err, ShouldBeNil)

			rp.Shutdown()

			So(rp.WaitUntilGranted(r2), ShouldBeFalse)
			r3, err := rp.Request(1)
			So(string(r3), ShouldBeBlank)
			So(err, ShouldNotBeNil)
			shouldBeRPError(err, ErrShutDown)
		})

		Convey("WaitUntilGranted can time out and cancel the request", func() {
			r, err := rp.Request(maxSimultaneous)
			So(err, ShouldBeNil)
			So(rp.WaitUntilGranted(r), ShouldBeTrue)

			r2, err := rp.Request(1)
			So(err, ShouldBeNil)
			So(rp.WaitUntilGranted(r2, oneFiftyPercentDelay), ShouldBeFalse)
			So(time.Now(), ShouldHappenOnOrBetween, begin.Add(oneFiftyPercentDelay), begin.Add(doubleDelay))

			So(rp.WaitUntilGranted(r), ShouldBeTrue)
			rp.Release(r)
			So(rp.WaitUntilGranted(r), ShouldBeFalse)
			So(rp.WaitUntilGranted(r2), ShouldBeFalse)
		})

		Convey("You can request the maximum tokens in a single request", func() {
			r, err := rp.Request(maxSimultaneous)
			So(err, ShouldBeNil)

			So(rp.WaitUntilGranted(r), ShouldBeTrue)
			So(time.Now(), ShouldHappenBefore, begin.Add(halfDelay))

			r2, err := rp.Request(1)
			So(err, ShouldBeNil)
			So(r2, ShouldNotBeNil)
			So(time.Now(), ShouldHappenBefore, begin.Add(halfDelay))

			Convey("Subsequent requests must wait until it is released", func() {
				go func() {
					<-time.After(oneFiftyPercentDelay)
					rp.Release(r)
				}()

				So(rp.WaitUntilGranted(r2), ShouldBeTrue)
				So(time.Now(), ShouldHappenOnOrBetween, begin.Add(oneFiftyPercentDelay), begin.Add(doubleDelay))
				rp.Release(r2)
			})

			Convey("Or until it times out", func() {
				So(rp.WaitUntilGranted(r2), ShouldBeTrue)
				So(time.Now(), ShouldHappenOnOrBetween, begin.Add(releaseTimeout), begin.Add(releaseTimeout).Add(halfDelay))
				rp.Release(r2)
			})

			Convey("Touch() delays the time out", func() {
				go func() {
					<-time.After(oneFiftyPercentDelay)
					rp.Touch(r)
				}()

				So(rp.WaitUntilGranted(r2), ShouldBeTrue)

				released := begin.Add(releaseTimeout)
				So(time.Now(), ShouldHappenOnOrBetween, released.Add(oneFiftyPercentDelay), released.Add(doubleDelay))
				rp.Release(r2)
			})
		})

		Convey("You can Touch multiple requests at once to delay all their timeouts", func() {
			r, err := rp.Request(1)
			So(err, ShouldBeNil)
			So(rp.WaitUntilGranted(r), ShouldBeTrue)

			r2, err := rp.Request(1)
			So(err, ShouldBeNil)
			So(rp.WaitUntilGranted(r2), ShouldBeTrue)

			go func() {
				<-time.After(oneFiftyPercentDelay)
				rp.Touch(r, r2)
			}()

			granted, keepChecking := rp.Granted(r)
			So(granted, ShouldBeTrue)
			So(keepChecking, ShouldBeFalse)
			granted, keepChecking = rp.Granted(r2)
			So(granted, ShouldBeTrue)
			So(keepChecking, ShouldBeFalse)

			<-time.After(releaseTimeout)

			granted, keepChecking = rp.Granted(r)
			So(granted, ShouldBeTrue)
			So(keepChecking, ShouldBeFalse)
			granted, keepChecking = rp.Granted(r2)
			So(granted, ShouldBeTrue)
			So(keepChecking, ShouldBeFalse)

			<-time.After(oneFiftyPercentDelay)
			<-time.After(halfDelay)

			granted, keepChecking = rp.Granted(r)
			So(granted, ShouldBeFalse)
			So(keepChecking, ShouldBeFalse)
			granted, keepChecking = rp.Granted(r2)
			So(granted, ShouldBeFalse)
			So(keepChecking, ShouldBeFalse)
		})

		Convey("You can release after a delay", func() {
			r, err := rp.Request(maxSimultaneous)
			So(err, ShouldBeNil)

			So(rp.WaitUntilGranted(r), ShouldBeTrue)
			So(time.Now(), ShouldHappenBefore, begin.Add(halfDelay))
			rp.ReleaseAfter(r, oneFiftyPercentDelay)

			r2, err := rp.Request(1)
			So(err, ShouldBeNil)
			So(r2, ShouldNotBeNil)
			So(time.Now(), ShouldHappenBefore, begin.Add(halfDelay))

			So(rp.WaitUntilGranted(r2), ShouldBeTrue)

			oneFiftyAfterBegin := begin.Add(oneFiftyPercentDelay)
			So(time.Now(), ShouldHappenOnOrBetween, oneFiftyAfterBegin, oneFiftyAfterBegin.Add(halfDelay))
			rp.Release(r2)

			Convey("Once released, the Request methods do nothing", func() {
				rp.Release(r2)
				rp.Touch(r2)
				So(rp.WaitUntilGranted(r2), ShouldBeFalse)
				So(time.Now(), ShouldHappenOnOrBetween, oneFiftyAfterBegin, oneFiftyAfterBegin.Add(halfDelay))
			})
		})

		Convey("Period use of Granted() is an alternative to WaitUntilGranted()", func() {
			r, err := rp.Request(maxSimultaneous)
			So(err, ShouldBeNil)

			So(rp.WaitUntilGranted(r), ShouldBeTrue)
			So(time.Now(), ShouldHappenBefore, begin.Add(halfDelay))
			rp.ReleaseAfter(r, oneFiftyPercentDelay)

			r2, err := rp.Request(1)
			So(err, ShouldBeNil)
			So(r2, ShouldNotBeNil)
			So(time.Now(), ShouldHappenBefore, begin.Add(halfDelay))

			granted, keepChecking := rp.Granted(r2)
			So(granted, ShouldBeFalse)
			So(keepChecking, ShouldBeTrue)

			<-time.After(halfDelay)

			granted, keepChecking = rp.Granted(r2)
			So(granted, ShouldBeFalse)
			So(keepChecking, ShouldBeTrue)

			<-time.After(oneFiftyPercentDelay)

			granted, keepChecking = rp.Granted(r2)
			So(granted, ShouldBeTrue)
			So(keepChecking, ShouldBeFalse)

			rp.Release(r2)
			granted, keepChecking = rp.Granted(r2)
			So(granted, ShouldBeFalse)
			So(keepChecking, ShouldBeFalse)
		})

		Convey("Releasing Request()s in less than delay time lets you request continuously", func() {
			grantedCh := make(chan time.Time, maxSimultaneous)
			for i := 1; i <= maxSimultaneous*3; i++ {
				r, err := rp.Request(1)
				So(err, ShouldBeNil)

				go func(r Receipt) {
					rp.WaitUntilGranted(r)

					grantedCh <- time.Now()

					<-time.After(halfDelay)
					rp.Release(r)
				}(r)
			}

			So(time.Now(), ShouldHappenBefore, begin.Add(halfDelay))

			for i := 0; i < maxSimultaneous*3; i++ {
				expected := begin.Add(time.Duration(delayInt*i) * time.Millisecond)
				So(<-grantedCh, ShouldHappenOnOrBetween, expected, expected.Add(halfDelay))
			}
		})

		Convey("Releasing Request()s immediately with no delay time lets you request continuously with no delay", func() {
			rp = New("irods", 0*time.Second, maxSimultaneous, releaseTimeout)
			So(rp, ShouldNotBeNil)

			grantedCh := make(chan time.Time, maxSimultaneous)
			for i := 1; i <= maxSimultaneous*3; i++ {
				r, err := rp.Request(1)
				So(err, ShouldBeNil)

				go func(r Receipt) {
					rp.WaitUntilGranted(r)

					grantedCh <- time.Now()

					rp.Release(r)
				}(r)
			}

			So(time.Now(), ShouldHappenBefore, begin.Add(halfDelay))

			for i := 0; i < maxSimultaneous*3; i++ {
				So(<-grantedCh, ShouldHappenBefore, begin.Add(halfDelay))
			}
		})

		Convey("AvailabilityCallbacks are obeyed", func() {
			cbCalls := 0
			tooBusyFor := 2
			cb := func() int {
				cbCalls++
				if cbCalls <= tooBusyFor {
					return maxSimultaneous - 1
				}

				return maxSimultaneous
			}
			rp.SetAvailabilityCallback(cb)

			r, err := rp.Request(maxSimultaneous)
			So(err, ShouldBeNil)

			So(rp.WaitUntilGranted(r), ShouldBeTrue)

			expected := begin.Add(time.Duration(delayInt*tooBusyFor) * time.Millisecond)
			So(time.Now(), ShouldHappenOnOrBetween, expected, expected.Add(halfDelay))
		})
	})
}

func shouldBeRPError(err error, target string) {
	var rperr Error
	So(errors.As(err, &rperr), ShouldBeTrue)
	So(rperr.Err, ShouldEqual, target)
}
