// loadrunner is a TEMP reliability-testing tool. It connects to a wr manager as
// many concurrent "runners" and drives the real reserve->start->touch->archive
// client protocol WITHOUT executing any command, so we can stress the manager's
// hot path at arbitrary scale on one node, and construct DBs with thousands of
// running jobs. It also measures Ping latency under load (responsiveness).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

func main() {
	mode := flag.String("mode", "drive", "drive|hold")
	workers := flag.Int("workers", 100, "concurrent worker connections")
	holdPer := flag.Int("holdper", 1, "hold mode: jobs held per worker")
	group := flag.String("group", "", "scheduler group; if empty, computed from reqs")
	ram := flag.Int("ram", 100, "RAM MB (for group calc)")
	tmin := flag.Float64("time", 1, "time minutes (for group calc)")
	cores := flag.Float64("cores", 1, "cores (for group calc)")
	disk := flag.Int("disk", 0, "disk GB (for group calc)")
	touches := flag.Int("touches", 0, "drive mode: touches per job before archive")
	touchGap := flag.Duration("touchgap", 2*time.Second, "gap between touches")
	dur := flag.Duration("duration", 0, "hold mode: how long to hold before exit (0=until killed)")
	deployment := flag.String("deployment", "development", "wr deployment")
	pingEvery := flag.Duration("pingevery", 250*time.Millisecond, "ping interval for responsiveness measurement")
	reserveTO := flag.Duration("reserveto", 3*time.Second, "reserve timeout")
	flag.Parse()

	if *group == "" {
		req := &scheduler.Requirements{RAM: *ram, Time: time.Duration(*tmin * float64(time.Minute)), Cores: *cores, Disk: *disk}
		*group = req.Stringify()
	}
	fmt.Printf("loadrunner mode=%s workers=%d group=%q\n", *mode, *workers, *group)

	ctx := context.Background()
	pid := os.Getpid()

	// background pinger measures manager responsiveness under load
	var pingLat []time.Duration
	var pingMu sync.Mutex
	pingStop := make(chan struct{})
	go func() {
		pc, err := jobqueue.ConnectUsingConfig(ctx, *deployment, 30*time.Second)
		if err != nil {
			fmt.Println("pinger connect error:", err)
			return
		}
		defer pc.Disconnect()
		t := time.NewTicker(*pingEvery)
		defer t.Stop()
		for {
			select {
			case <-pingStop:
				return
			case <-t.C:
				s := time.Now()
				_, err := pc.Ping(10 * time.Second)
				l := time.Since(s)
				if err != nil {
					l = 10 * time.Second
				}
				pingMu.Lock()
				pingLat = append(pingLat, l)
				pingMu.Unlock()
			}
		}
	}()

	var reserved, archived, started, reserveEmpty int64
	var reserveLat, archiveLat []time.Duration
	var latMu sync.Mutex
	addLat := func(dst *[]time.Duration, d time.Duration) {
		latMu.Lock()
		*dst = append(*dst, d)
		latMu.Unlock()
	}

	t0 := time.Now()
	var wg sync.WaitGroup

	if *mode == "ping" {
		d := *dur
		if d == 0 {
			d = 30 * time.Second
		}
		time.Sleep(d)
		close(pingStop)
		pctileP := func(s []time.Duration, p float64) time.Duration {
			if len(s) == 0 {
				return 0
			}
			cp := make([]time.Duration, len(s))
			copy(cp, s)
			sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
			return cp[int(p*float64(len(cp)-1))]
		}
		pingMu.Lock()
		fmt.Printf("PING samples=%d p50=%v p95=%v p99=%v max=%v\n",
			len(pingLat), pctileP(pingLat, .5), pctileP(pingLat, .95), pctileP(pingLat, .99), pctileP(pingLat, 1.0))
		pingMu.Unlock()
		return
	}

	worker := func(id int) {
		defer wg.Done()

		// churn: repeatedly connect -> ping -> disconnect, simulating unstable
		// runners re-dialing a (re)started manager. Runs for -duration.
		if *mode == "churn" {
			deadline := t0.Add(*dur)
			if *dur == 0 {
				deadline = t0.Add(60 * time.Second)
			}
			for time.Now().Before(deadline) {
				cc, err := jobqueue.ConnectUsingConfig(ctx, *deployment, 10*time.Second)
				if err != nil {
					atomic.AddInt64(&reserveEmpty, 1)
					continue
				}
				cc.Ping(5 * time.Second) //nolint:errcheck
				atomic.AddInt64(&reserved, 1)
				cc.Disconnect() //nolint:errcheck
			}
			return
		}

		c, err := jobqueue.ConnectUsingConfig(ctx, *deployment, 30*time.Second)
		if err != nil {
			fmt.Printf("worker %d connect error: %v\n", id, err)
			return
		}
		defer c.Disconnect()

		reserveOne := func() *jobqueue.Job {
			for attempt := 0; attempt < 3; attempt++ {
				s := time.Now()
				job, err := c.ReserveScheduled(*reserveTO, *group)
				addLat(&reserveLat, time.Since(s))
				if err == nil && job != nil {
					atomic.AddInt64(&reserved, 1)
					return job
				}
				atomic.AddInt64(&reserveEmpty, 1)
			}
			return nil
		}

		if *mode == "hold" {
			held := make([]*jobqueue.Job, 0, *holdPer)
			for len(held) < *holdPer {
				job := reserveOne()
				if job == nil {
					break
				}
				if err := c.Started(job, pid); err != nil {
					continue
				}
				atomic.AddInt64(&started, 1)
				held = append(held, job)
			}
			// keep them alive by touching until told to stop / duration
			deadline := time.Time{}
			if *dur > 0 {
				deadline = t0.Add(*dur)
			}
			tk := time.NewTicker(10 * time.Second)
			defer tk.Stop()
			for {
				<-tk.C
				for _, j := range held {
					c.Touch(j) //nolint:errcheck
				}
				if !deadline.IsZero() && time.Now().After(deadline) {
					return
				}
			}
		}

		// drive mode: reserve->start->touch*->archive until dry
		emptyStreak := 0
		for {
			job := reserveOne()
			if job == nil {
				emptyStreak++
				if emptyStreak >= 2 {
					return
				}
				continue
			}
			emptyStreak = 0
			if err := c.Started(job, pid); err != nil {
				continue
			}
			atomic.AddInt64(&started, 1)
			for i := 0; i < *touches; i++ {
				time.Sleep(*touchGap)
				c.Touch(job) //nolint:errcheck
			}
			jes := &jobqueue.JobEndState{Cwd: job.Cwd, Exitcode: 0, PeakRAM: 1, CPUtime: 10 * time.Millisecond, EndTime: time.Now(), Exited: true}
			s := time.Now()
			if err := c.Archive(job, jes); err != nil {
				fmt.Printf("worker %d archive error: %v\n", id, err)
				continue
			}
			addLat(&archiveLat, time.Since(s))
			atomic.AddInt64(&archived, 1)
		}
	}

	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go worker(i)
	}

	// progress reporter
	doneCh := make(chan struct{})
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-doneCh:
				return
			case <-tk.C:
				el := time.Since(t0).Seconds()
				a := atomic.LoadInt64(&archived)
				fmt.Printf("[%.0fs] reserved=%d started=%d archived=%d (%.0f/s) empty=%d\n",
					el, atomic.LoadInt64(&reserved), atomic.LoadInt64(&started), a, float64(a)/el, atomic.LoadInt64(&reserveEmpty))
			}
		}
	}()

	wg.Wait()
	close(doneCh)
	close(pingStop)
	elapsed := time.Since(t0)

	pctile := func(s []time.Duration, p float64) time.Duration {
		if len(s) == 0 {
			return 0
		}
		cp := make([]time.Duration, len(s))
		copy(cp, s)
		sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
		idx := int(p * float64(len(cp)-1))
		return cp[idx]
	}

	fmt.Printf("\n=== RESULTS (mode=%s) ===\n", *mode)
	fmt.Printf("elapsed=%.2fs reserved=%d started=%d archived=%d\n", elapsed.Seconds(), reserved, started, archived)
	if *mode == "drive" && archived > 0 {
		fmt.Printf("throughput=%.1f archived/s\n", float64(archived)/elapsed.Seconds())
		fmt.Printf("reserve  latency p50=%v p95=%v p99=%v\n", pctile(reserveLat, .5), pctile(reserveLat, .95), pctile(reserveLat, .99))
		fmt.Printf("archive  latency p50=%v p95=%v p99=%v\n", pctile(archiveLat, .5), pctile(archiveLat, .95), pctile(archiveLat, .99))
	}
	pingMu.Lock()
	fmt.Printf("PING latency (under load) samples=%d p50=%v p95=%v p99=%v max=%v\n",
		len(pingLat), pctile(pingLat, .5), pctile(pingLat, .95), pctile(pingLat, .99), pctile(pingLat, 1.0))
	pingMu.Unlock()
}
