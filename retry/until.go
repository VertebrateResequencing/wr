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

package retry

import "context"

// Reason is the type of our Because* constants.
type Reason string

// Because* constants are returned by Until.ShouldStop().
const (
	BecauseLimitReached  Reason = "limit reached"
	BecauseErrorNil      Reason = "there was no error"
	BecauseContextClosed Reason = "context closed"
	doNotStop            Reason = ""
)

// Until is used by Retry to determine when to stop retrying.
type Until interface {
	// ShouldStop takes the number of retries so far along with the error from
	// the last attempt, and returns a non-blank Reason if no more attempts
	// should be made.
	ShouldStop(retries int, err error) Reason
}

// Untils is a slice of Until which itself implements Until, letting you combine
// multiple Untils.
type Untils []Until

// ShouldStop returns a non-blank Reason when any of the elements of this
// slice return one.
func (u Untils) ShouldStop(retries int, err error) Reason {
	for _, until := range u {
		if reason := until.ShouldStop(retries, err); reason != doNotStop {
			return reason
		}
	}

	return doNotStop
}

// UntilLimit implements Until, stopping retries after Max retries. A Max
// of 0 means "don't retry". A Max of 1 means up to 1 retry will be attempted,
// and so on.
type UntilLimit struct {
	Max int
}

// ShouldStop returns BecauseLimitReached when retries is greater than or equal
// to Max. err is not considered.
func (u *UntilLimit) ShouldStop(retries int, err error) Reason {
	if retries >= u.Max {
		return BecauseLimitReached
	}

	return doNotStop
}

// UntilNoError implements Until, stopping retries when the error passed to
// ShouldStop is nil.
type UntilNoError struct{}

// ShouldStop returns BecauseErrorNil when err is nil. retries is not
// considered.
func (u *UntilNoError) ShouldStop(retries int, err error) Reason {
	if err == nil {
		return BecauseErrorNil
	}

	return doNotStop
}

// untilContext implements Until, stopping retries after the context has been
// closed.
type untilContext struct {
	Context context.Context //nolint:containedctx
}

// ShouldStop returns BecauseContextClosed after the context has been
// closed. retries and err are not considered.
func (u *untilContext) ShouldStop(retries int, err error) Reason {
	if u.Context.Err() != nil {
		return BecauseContextClosed
	}

	return doNotStop
}
