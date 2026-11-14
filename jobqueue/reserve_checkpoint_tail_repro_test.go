//go:build reliability_repro

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

package jobqueue

// The in-process reproduction of the 1d.2 checkpoint incidental, which failed
// phase 1 gate item 1g.5's soak G6 by one line
// (.docs/bugfixes/261111-reserve-tail-nfs-4766eb70.md): reservations' waits for
// their record to be durable while a checkpoint of a production-sized live set
// is written. It is tagged, not part of `make test`, as it adds 120,000 live
// jobs first.
//
//	go test -tags reliability_repro ./jobqueue -run TestReserveWaitDuringCheckpoint -v -timeout 30m
//
// WR_REPRO_DIR puts the store under a directory of your choosing (an NFS one,
// say) instead of a temporary one.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// reserveCkptLiveJobs is how many live jobs the store holds, so that its
	// checkpoint is about the 55-76MB production's gate soaks wrote.
	reserveCkptLiveJobs = 120000

	// reserveCkptEvery is the time between reservations.
	reserveCkptEvery = 5 * time.Millisecond
)

func TestReserveWaitDuringCheckpoint(t *testing.T) {
	ctx := context.Background()

	Convey("Given a log store holding 120,000 live jobs with 1KiB random commands, checkpointed once", t, func() {
		dir := t.TempDir()
		if d := os.Getenv("WR_REPRO_DIR"); d != "" {
			dir = filepath.Join(d, fmt.Sprintf("reserve-ckpt-%d", time.Now().UnixNano()))
		}

		s, err := openLogStore(ctx, filepath.Join(dir, "queue.db"))
		So(err, ShouldBeNil)

		// the store closes before its directory goes, as NFS keeps open
		// files' names
		Reset(func() {
			So(s.close(ctx), ShouldBeNil)

			if os.Getenv("WR_REPRO_DIR") != "" {
				So(os.RemoveAll(dir), ShouldBeNil)
			}
		})

		r := rand.New(rand.NewSource(3)) //nolint:gosec // reproducible commands
		live := reserveTailJobs(r, "ckpt", 0, reserveCkptLiveJobs)

		began := time.Now()

		for start := 0; start < len(live); start += 5000 {
			_, err = s.storeNewJobsGuarded(ctx, live[start:start+5000], false, nil)
			So(err, ShouldBeNil)
		}

		added := time.Now()

		So(s.checkpoint(), ShouldBeNil)
		t.Logf("adds took %s, the first checkpoint %s", added.Sub(began), time.Since(added))

		Convey("reservations every 5ms while the next checkpoint is made wait at most 50ms for their record "+
			"at p99", func() {
			waits, took, size := reserveCkptRun(s, live[:2000])
			p99 := waits[len(waits)*99/100]

			t.Logf("checkpoint %dB in %s; reservations %d: p50 %s, p99 %s, max %s", size, took, len(waits),
				waits[len(waits)/2], p99, waits[len(waits)-1])

			So(size, ShouldBeGreaterThan, 40<<20)
			So(p99, ShouldBeLessThanOrEqualTo, reserveTailP99)
		})
	})
}

// reserveCkptRun makes a checkpoint of s while reserving toReserve in turn
// every reserveCkptEvery, from a second before it starts until it ends, as
// reserveTailReserve does. It returns the waits of the reservations made
// while the checkpoint ran, sorted, how long it took and its file's size.
func reserveCkptRun(s *logStore, toReserve []*Job) ([]time.Duration, time.Duration, int64) {
	type wait struct {
		at time.Time
		d  time.Duration
	}

	var (
		mu    sync.Mutex
		waits []wait
		errs  []error
		wg    sync.WaitGroup
	)

	done := make(chan struct{})

	wg.Go(func() {
		ticker := time.NewTicker(reserveCkptEvery)
		defer ticker.Stop()

		var reservers sync.WaitGroup

		for i := 0; ; i++ {
			select {
			case <-done:
				reservers.Wait()

				return
			case <-ticker.C:
			}

			reservers.Go(func() {
				start := time.Now()
				err := reserveTailReserve(s, toReserve[i%len(toReserve)])

				mu.Lock()
				defer mu.Unlock()

				waits = append(waits, wait{start, time.Since(start)})
				errs = append(errs, err)
			})
		}
	})

	time.Sleep(time.Second)

	began := time.Now()
	err := s.checkpoint()
	took := time.Since(began)

	close(done)
	wg.Wait()

	So(err, ShouldBeNil)
	So(joinErrs(errs), ShouldBeNil)

	info, err := os.Stat(checkpointPath(s.dir, s.ckptFrom))
	So(err, ShouldBeNil)

	var during []time.Duration

	for _, w := range waits {
		if !w.at.Before(began) && w.at.Before(began.Add(took)) {
			during = append(during, w.d)
		}
	}

	slices.Sort(during)
	So(during, ShouldNotBeEmpty)

	return during, took, info.Size()
}
