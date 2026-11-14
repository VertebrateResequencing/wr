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

// The in-process reproduction of phase 1 gate item 1g.5's soak failure
// (.docs/bugfixes/261111-reserve-tail-nfs-4766eb70.md): reservations' waits
// for their record to be durable, while the incremental backup copies a
// production-sized history.bolt; it is tagged, not part of `make test`, as it
// builds a history of 300,000 completed jobs (about 840MB) first.
//
//	go test -tags reliability_repro ./jobqueue -run TestReserveWaitDuringBackup -v -timeout 30m
//
// WR_REPRO_DIR puts the store, and its backup, under a directory of your
// choosing (an NFS one, say) instead of a temporary one.

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

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// reserveTailHistoryJobs is how many completed jobs the store's history
	// holds before the run.
	reserveTailHistoryJobs = 300000

	// reserveTailCmdBytes is the size of each job's command's random part,
	// so that the history is about the size the soak's was after an hour.
	reserveTailCmdBytes = 1024

	// reserveTailRate is the archives and the reservations made a second,
	// each: the soak's rate at its 3,000 runners (about 6,600 a minute).
	reserveTailRate = 110

	// reserveTailRunFor is how long the archives and reservations run.
	reserveTailRunFor = 40 * time.Second

	// reserveTailTick is the time between backup ticks, so that the run sees
	// several (production's is 30s).
	reserveTailTick = 10 * time.Second

	// reserveTailP99 is phase 1's daytime target for a reservation's wait.
	reserveTailP99 = 50 * time.Millisecond
)

func TestReserveWaitDuringBackup(t *testing.T) {
	ctx := context.Background()

	Convey("Given a production log store whose history holds 300,000 completed jobs, backing up every 10s", t,
		func() {
			dir := t.TempDir()
			if d := os.Getenv("WR_REPRO_DIR"); d != "" {
				dir = filepath.Join(d, fmt.Sprintf("reserve-tail-%d", time.Now().UnixNano()))
			}

			dbFile := filepath.Join(dir, "queue.db")
			seedReserveTailHistory(ctx, dbFile)

			old := logBackupEvery.Load()
			logBackupEvery.Store(int64(reserveTailTick))
			Reset(func() { logBackupEvery.Store(old) })

			st, _, err := openStore(ctx, storeConfig{dbFile: dbFile, dbBkFile: filepath.Join(dir, "db_bk"),
				deployment: internal.Production, kind: storeKindLog})
			So(err, ShouldBeNil)

			s, ok := st.(*logStore)
			So(ok, ShouldBeTrue)

			// the store closes before its directory goes, as NFS keeps open
			// files' names
			Reset(func() {
				So(s.close(ctx), ShouldBeNil)

				if os.Getenv("WR_REPRO_DIR") != "" {
					So(os.RemoveAll(dir), ShouldBeNil)
				}
			})

			Convey("110 reservations a second, beside 110 archives, wait at most 50ms for their record at p99",
				func() {
					waits := reserveTailRun(ctx, s)
					p99 := waits[len(waits)*99/100]

					t.Logf("reservations %d: p50 %s, p99 %s, max %s", len(waits), waits[len(waits)/2], p99,
						waits[len(waits)-1])

					So(p99, ShouldBeLessThanOrEqualTo, reserveTailP99)
				})
		})
}

// seedReserveTailHistory makes the log store at dbFile with
// reserveTailHistoryJobs completed jobs, all folded into its history.bolt
// file.
func seedReserveTailHistory(ctx context.Context, dbFile string) {
	s, err := openLogStore(ctx, dbFile)
	So(err, ShouldBeNil)

	r := rand.New(rand.NewSource(1)) //nolint:gosec // reproducible commands

	const batch = 10000

	for start := 0; start < reserveTailHistoryJobs; start += batch {
		jobs := reserveTailJobs(r, "seed", start, batch)

		_, err = s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		So(archiveReserveTailJobs(s, jobs, 64), ShouldBeNil)
	}

	So(s.foldNow(), ShouldBeNil)
	So(s.close(ctx), ShouldBeNil)
}

// reserveTailRun adds live jobs to s, then for reserveTailRunFor archives
// reserveTailRate of them a second and records the run state of
// reserveTailRate others a second, as a reservation does, waiting for each
// to be durable. It returns those waits, sorted.
func reserveTailRun(ctx context.Context, s *logStore) []time.Duration {
	r := rand.New(rand.NewSource(2)) //nolint:gosec // reproducible commands
	reserved := 2000
	live := reserveTailJobs(r, "live", 0, reserveTailRate*int(reserveTailRunFor/time.Second)+reserved)

	for start := 0; start < len(live); start += 5000 {
		_, err := s.storeNewJobsGuarded(ctx, live[start:min(len(live), start+5000)], false, nil)
		So(err, ShouldBeNil)
	}

	So(s.foldNow(), ShouldBeNil)

	toReserve, toArchive := live[len(live)-reserved:], live[:len(live)-reserved]

	var (
		mu    sync.Mutex
		waits []time.Duration
		errs  []error
		wg    sync.WaitGroup
	)

	note := func(d time.Duration, err error) {
		mu.Lock()
		defer mu.Unlock()

		waits = append(waits, d)

		if err != nil {
			errs = append(errs, err)
		}
	}

	stop := time.Now().Add(reserveTailRunFor)
	every := time.Second / reserveTailRate

	wg.Go(func() { paced(stop, every, func(i int) { note(0, archiveReserveTailJob(s, toArchive[i])) }) })
	wg.Go(func() {
		paced(stop, every, func(i int) {
			start := time.Now()
			err := reserveTailReserve(s, toReserve[i%len(toReserve)])
			note(time.Since(start), err)
		})
	})

	wg.Wait()

	So(joinErrs(errs), ShouldBeNil)

	waits = slices.DeleteFunc(waits, func(d time.Duration) bool { return d == 0 })
	slices.Sort(waits)
	So(waits, ShouldNotBeEmpty)

	return waits
}

// reserveTailJobs returns n jobs, numbered from start, whose commands have
// reserveTailCmdBytes of random hex from r.
func reserveTailJobs(r *rand.Rand, prefix string, start, n int) []*Job {
	jobs := make([]*Job, n)
	pad := make([]byte, reserveTailCmdBytes)

	for i := range jobs {
		for j := range pad {
			pad[j] = "0123456789abcdef"[r.Intn(16)]
		}

		jobs[i] = testDBJob(fmt.Sprintf("echo %s %d %s", prefix, start+i, pad), fmt.Sprintf("rtrg%d", (start+i)%500))
	}

	return jobs
}

// paced calls fn(i), on goroutines of its own, every interval until stop,
// with i counting from 0, and returns once each has returned.
func paced(stop time.Time, every time.Duration, fn func(i int)) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	var wg sync.WaitGroup

	for i := 0; time.Now().Before(stop); i++ {
		<-ticker.C

		wg.Go(func() { fn(i) })
	}

	wg.Wait()
}

// archiveReserveTailJobs archives jobs in s with the given number of
// concurrent writers, returning their errors joined.
func archiveReserveTailJobs(s *logStore, jobs []*Job, writers int) error {
	errs := make([]error, len(jobs))

	var wg sync.WaitGroup

	for w := range writers {
		wg.Go(func() {
			for i := w; i < len(jobs); i += writers {
				errs[i] = archiveReserveTailJob(s, jobs[i])
			}
		})
	}

	wg.Wait()

	return joinErrs(errs)
}

// archiveReserveTailJob archives job in s as completed just now.
func archiveReserveTailJob(s *logStore, job *Job) error {
	now := time.Now()

	job.Lock()
	job.State, job.Exited, job.StartTime, job.EndTime, job.PeakRAM = JobStateComplete, true, now, now, 42
	job.Unlock()

	_, err := s.archiveCompletion(job.Key(), job)

	return err
}

// reserveTailReserve records job's run state as a reservation does, and waits
// for it to be durable, as persistReservation does.
func reserveTailReserve(s *logStore, job *Job) error {
	commit, err := s.reserveRunState(job)
	if err != nil {
		return err
	}

	done, err := commit.WaitWithin(reserveDurableWait())
	if err == nil && !done {
		err = errReserveNotDurable
	}

	return err
}

// joinErrs returns the first non-nil of errs.
func joinErrs(errs []error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}
