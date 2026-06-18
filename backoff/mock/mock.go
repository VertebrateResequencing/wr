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

// package mock contains a mock implementation of backoff.Sleeper
package mock

import (
	"context"
	"sync/atomic"
	"time"
)

// Sleeper represents a mock implementation of backoff.Sleeper. It is
// concurrent safe.
type Sleeper struct {
	sleepInvoked uint64
	elapsed      int64
}

// Sleep increases Elapsed and increments SleepInvoked, but doesn't actually
// sleep.
func (s *Sleeper) Sleep(ctx context.Context, d time.Duration) {
	atomic.AddUint64(&s.sleepInvoked, 1)
	atomic.AddInt64(&s.elapsed, int64(d))
}

// Invoked returns the number of times Sleep() has been called.
func (s *Sleeper) Invoked() int {
	return int(atomic.LoadUint64(&s.sleepInvoked))
}

// Elapsed returns the total elapsed time we were supposed to have slept for.
func (s *Sleeper) Elapsed() time.Duration {
	return time.Duration(atomic.LoadInt64(&s.elapsed))
}
