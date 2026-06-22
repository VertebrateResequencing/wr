/*******************************************************************************
 * Copyright (c) 2025-2026 Genome Research Ltd.
 *
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

// package time contains a real time-based implementation of backoff.Sleeper.
package time

import (
	"context"
	"time"

	"github.com/VertebrateResequencing/wr/backoff"
)

const (
	secondsRangeMin    = 250 * time.Millisecond
	secondsRangeMax    = 3 * time.Second
	secondsRangeFactor = 1.5
)

// Sleeper represents an implementation of backoff.Sleeper. It does an actual
// sleep using time.Sleep.
type Sleeper struct{}

// Sleep sleeps until the context is cancelled, or the given duration has
// elapsed.
func (s *Sleeper) Sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
		return
	case <-ctx.Done():
		return
	}
}

// SecondsRangeBackoff returns a ready-to-use, generally useful backoff.Backoff
// that uses our Sleeper to start sleeping in the sub-second range and soon
// backs off to sleeping for a few seconds.
func SecondsRangeBackoff() *backoff.Backoff {
	return &backoff.Backoff{
		Min:     secondsRangeMin,
		Max:     secondsRangeMax,
		Factor:  secondsRangeFactor,
		Sleeper: &Sleeper{},
	}
}
