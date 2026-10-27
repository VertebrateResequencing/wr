// Command hotpath drives one storage design with N closed-loop "runners"
// (reserve, start, run for a while, archive, each waiting for its write to be
// durable as the manager does) plus periodic 1000-job adds, and reports the
// latencies and throughput. It is the A3 model's shape, against each design.
package main

import (
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/flat"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/jobgen"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/stats"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/store"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/zc"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/gofrs/uuid/v5"
	"golang.org/x/sys/unix"
)

type cfg struct {
	design, dir, ship    string
	runners, preload     int
	cmdSize, addN        int
	secs, ramp           int
	dmin, dmax, addEvery time.Duration
	noSync, lease        bool
	stallCtl, timeline   string
	stallAt, stallFor    time.Duration
}

type driver struct {
	cfg
	s       store.Store
	ready   chan int
	mu      sync.Mutex
	jobs    []*jobqueue.Job
	keys    []flat.Key
	nextID  atomic.Int64
	runs    atomic.Int64
	reserve *stats.Lat
	start   *stats.Lat
	archive *stats.Lat
	add     *stats.Lat
	over10  atomic.Int64
	leases  atomic.Int64

	// per-second timeline counters
	reserves, archives, adds atomic.Int64

	// D6 leases: handed is how many hand-outs were asked for, leased how many
	// a durable lease covers; a hand-out past leased waits for the leaser.
	leaseMu sync.Mutex
	leaseCV *sync.Cond
	handed  int64
	leased  int64
	stopped chan struct{}
}

func main() {
	var c cfg

	flag.StringVar(&c.design, "design", "wal", "keyfiles|wal|slots|boltfull|boltsmall|sqlite")
	flag.StringVar(&c.dir, "dir", "", "data directory (created, must not exist)")
	flag.StringVar(&c.ship, "ship", "", "D5: also ship the log files to this directory")
	flag.IntVar(&c.runners, "runners", 3000, "concurrent runners")
	flag.IntVar(&c.preload, "preload", 20000, "live jobs to add first")
	flag.IntVar(&c.cmdSize, "cmd", 10000, "command size in bytes")
	flag.IntVar(&c.addN, "addn", 1000, "jobs per add")
	flag.DurationVar(&c.addEvery, "addevery", 5*time.Second, "time between adds (0 = none)")
	flag.IntVar(&c.secs, "secs", 120, "measured seconds")
	flag.IntVar(&c.ramp, "ramp", 20, "seconds over which runners start")
	flag.DurationVar(&c.dmin, "dmin", 10*time.Second, "min job run time")
	flag.DurationVar(&c.dmax, "dmax", 60*time.Second, "max job run time")
	flag.BoolVar(&c.noSync, "nosync", false, "write() without fdatasync")
	flag.BoolVar(&c.lease, "lease", false, "D6: reserve/start not written; one lease write per 1000 reserves")
	flag.StringVar(&c.stallCtl, "stallctl", "", "fusestall control file to create for the stall")
	flag.DurationVar(&c.stallAt, "stallat", 60*time.Second, "when the stall starts, after the runners do")
	flag.DurationVar(&c.stallFor, "stallfor", 30*time.Second, "how long the stall lasts")
	flag.StringVar(&c.timeline, "timeline", "", "file for per-second counts")
	codecName := flag.String("codec", "none", "spec compression for the log designs: none|s2|zstd|zstd-dict")
	batchName := flag.String("batchcodec", "none", "batch compression for the log designs: none|s2|zstd")
	dictFile := flag.String("dict", "", "zstd-dict's dictionary")
	corpus := flag.String("corpus", "", "build commands from this corpus of real commands")
	flag.Parse()

	if err := setup(*codecName, *batchName, *dictFile, *corpus); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(c cfg) error {
	if _, err := os.Stat(c.dir); err == nil {
		return fmt.Errorf("%s exists", c.dir)
	}

	s, err := store.Open(c.design, c.dir, c.noSync)
	if err != nil {
		return err
	}

	d := &driver{cfg: c, s: s, ready: make(chan int, 4<<20), reserve: stats.New(), start: stats.New(),
		archive: stats.New(), add: stats.New()}
	d.leaseCV = sync.NewCond(&d.leaseMu)

	t := time.Now()
	for d.nextID.Load() < int64(c.preload) {
		if err := d.addBatch(min(c.addN, c.preload-int(d.nextID.Load()))); err != nil {
			return err
		}
	}

	fmt.Printf("preloaded %d in %s\n", c.preload, time.Since(t).Round(time.Millisecond))
	d.add = stats.New()

	stop := make(chan struct{})
	d.stopped = stop

	var wg sync.WaitGroup

	if c.ship != "" {
		wg.Add(1)

		go func() {
			defer wg.Done()
			ship(c.dir, c.ship, stop)
		}()
	}

	if c.lease {
		wg.Add(1)

		go func() {
			defer wg.Done()
			d.leaser(stop)
		}()
	}

	if c.stallCtl != "" {
		go stall(c.stallCtl, c.stallAt, c.stallFor, stop)
	}

	if c.timeline != "" {
		go d.writeTimeline(c.timeline, stop)
	}

	if c.addEvery > 0 {
		wg.Add(1)

		go func() {
			defer wg.Done()
			d.adder(stop)
		}()
	}

	for i := range c.runners {
		wg.Add(1)

		go func() {
			defer wg.Done()

			select {
			case <-time.After(time.Duration(i) * time.Duration(c.ramp) * time.Second / time.Duration(c.runners)):
			case <-stop:
				return
			}

			d.runner(i, stop)
		}()
	}

	time.Sleep(time.Duration(c.secs) * time.Second)

	runs := d.runs.Load()

	close(stop)
	d.leaseMu.Lock()
	d.leaseCV.Broadcast()
	d.leaseMu.Unlock()

	if c.stallCtl != "" {
		_ = os.Remove(c.stallCtl)
	}

	wg.Wait()

	fmt.Printf("RESULT design=%s dir=%s runners=%d cmd=%d nosync=%v lease=%v runs=%d runs/s=%.1f\n",
		c.design, filepath.Dir(c.dir), c.runners, c.cmdSize, c.noSync, c.lease, runs, float64(runs)/float64(c.secs))
	fmt.Printf("  reserve %s >10s=%.2f%%\n", d.reserve, 100*float64(d.over10.Load())/float64(max(d.reserve.N(), 1)))
	fmt.Printf("  start   %s\n  archive %s\n  add     %s (n=%d)\n", d.start, d.archive, d.add, d.add.N())
	fmt.Printf("  store   %s leases=%d\n", s.Stats(), d.leases.Load())

	return s.Close()
}

func (d *driver) addBatch(n int) error {
	first := int(d.nextID.Add(int64(n))) - n
	jobs := make([]*jobqueue.Job, n)
	keys := make([]flat.Key, n)

	for i := range n {
		jobs[i] = jobgen.Job(first+i, d.cmdSize)
		keys[i] = flat.KeyOf(jobgen.Key(jobs[i]))
	}

	t := time.Now()
	if err := d.s.Add(jobs, keys); err != nil {
		return err
	}

	d.add.Add(time.Since(t))
	d.adds.Add(int64(n))

	d.mu.Lock()
	d.jobs = append(d.jobs, jobs...)
	d.keys = append(d.keys, keys...)
	d.mu.Unlock()

	for i := range n {
		d.ready <- first + i
	}

	return nil
}

func (d *driver) adder(stop chan struct{}) {
	tick := time.NewTicker(d.addEvery)
	defer tick.Stop()

	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			if err := d.addBatch(d.addN); err != nil {
				fmt.Fprintln(os.Stderr, "add:", err)
			}
		}
	}
}

func (d *driver) job(id int) (*jobqueue.Job, flat.Key) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.jobs[id], d.keys[id]
}

func (d *driver) runner(n int, stop chan struct{}) {
	rid := uuid.Must(uuid.NewV4())
	host := fmt.Sprintf("node-%d-%d", n/100, n%100)

	for {
		var id int
		select {
		case <-stop:
			return
		case id = <-d.ready:
		}

		j, k := d.job(id)
		st := flat.State{Key: k, State: flat.SReserved, ReservedBy: rid, Pid: uint32(1000 + n), Attempts: 1, Exitcode: -1}
		st.SetHost(host)

		d.persistReserve(&st, j)

		st.State, st.StartTime = flat.SRunning, time.Now().UnixNano()
		if !d.lease {
			d.timed(d.start, store.Start, &st, j)
		}

		select {
		case <-stop:
			return
		case <-time.After(d.dmin + rand.N(d.dmax-d.dmin+1)):
		}

		st.State, st.EndTime, st.Exitcode, st.Exited = flat.SComplete, time.Now().UnixNano(), 0, true
		st.PeakRAM = 1200
		d.timed(d.archive, store.Archive, &st, j)
		d.runs.Add(1)
		d.archives.Add(1)
	}
}

func (d *driver) persistReserve(st *flat.State, j *jobqueue.Job) {
	defer d.reserves.Add(1)

	if d.lease {
		// D6: the hand-out itself is in memory, but only once a durable lease
		// (written ahead by leaser, 1000 hand-outs at a time) covers it.
		t := time.Now()

		d.leaseMu.Lock()
		d.handed++
		n := d.handed
		d.leaseCV.Broadcast()

		for n > d.leased && !d.closed() {
			d.leaseCV.Wait()
		}
		d.leaseMu.Unlock()

		d.reserve.Add(time.Since(t))

		return
	}

	t := time.Now()
	done, errf := d.s.Put(store.Reserve, st, j)

	if _, err := gcWaitWithin(done, errf, 10*time.Second); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}

	if time.Since(t) >= 10*time.Second {
		d.over10.Add(1)
	}

	<-done
	d.reserve.Add(time.Since(t))
}

// setup sets the spec codec and the command corpus.
func setup(codecName, batchName, dictFile, corpus string) error {
	c, err := zc.New(codecName, dictFile)
	if err != nil {
		return err
	}

	store.SpecCodec = c

	if store.BatchCodec, err = zc.New(batchName, dictFile); err != nil {
		return err
	}

	if corpus != "" {
		jobgen.Corpus, err = zc.ReadCorpus(corpus)
	}

	return err
}

func (d *driver) closed() bool {
	select {
	case <-d.stopped:
		return true
	default:
		return false
	}
}

// leaser keeps a durable lease 1000 hand-outs ahead of those asked for, as D6's
// manager would, writing one lease record per 1000.
func (d *driver) leaser(stop chan struct{}) {
	st := flat.State{State: flat.SReserved}

	for {
		d.leaseMu.Lock()
		for d.leased >= d.handed+1000 && !d.closed() {
			d.leaseCV.Wait()
		}
		d.leaseMu.Unlock()

		select {
		case <-stop:
			return
		default:
		}

		st.Seq++
		d.timed(d.start, store.Reserve, &st, nil)
		d.leases.Add(1000)

		d.leaseMu.Lock()
		d.leased += 1000
		d.leaseCV.Broadcast()
		d.leaseMu.Unlock()
	}
}

// stall creates ctl, which makes fusestall hold every fsync, at after and
// removes it for later.
func stall(ctl string, after, length time.Duration, stop chan struct{}) {
	select {
	case <-stop:
		return
	case <-time.After(after):
	}

	f, err := os.Create(ctl)
	if err != nil {
		panic(err)
	}

	f.Close()
	fmt.Printf("  stall   on at +%s for %s\n", after, length)

	select {
	case <-stop:
	case <-time.After(length):
	}

	_ = os.Remove(ctl)
	fmt.Printf("  stall   off\n")
}

// writeTimeline writes, each second, the reserves, archives and added jobs
// done in that second.
func (d *driver) writeTimeline(path string, stop chan struct{}) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	fmt.Fprintln(f, "sec reserves archives addedjobs")

	var lr, la, ld int64

	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for i := 1; ; i++ {
		select {
		case <-stop:
			return
		case <-tick.C:
		}

		r, a, ad := d.reserves.Load(), d.archives.Load(), d.adds.Load()
		fmt.Fprintf(f, "%d %d %d %d\n", i, r-lr, a-la, ad-ld)
		lr, la, ld = r, a, ad
	}
}

func gcWaitWithin(done <-chan struct{}, errf func() error, w time.Duration) (bool, error) {
	select {
	case <-done:
		return true, errf()
	case <-time.After(w):
		return false, nil
	}
}

func (d *driver) timed(l *stats.Lat, k store.Kind, st *flat.State, j *jobqueue.Job) {
	t := time.Now()
	done, errf := d.s.Put(k, st, j)
	<-done

	if err := errf(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}

	l.Add(time.Since(t))
}

// ship is D5's asynchronous replication: every 200ms it appends whatever is
// new in each local file to its copy in dst, fdatasyncing at most once a
// second, and prints the worst lag seen.
func ship(src, dst string, stop chan struct{}) {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		panic(err)
	}

	offs := map[string]int64{}
	outs := map[string]*os.File{}

	var (
		maxLag     int64
		maxLagTime time.Duration
		caughtUp   bool
	)

	lastSync := time.Now()
	tick := time.NewTicker(200 * time.Millisecond)

	defer tick.Stop()

	for {
		select {
		case <-stop:
			fmt.Printf("  ship    steady-state maxLagBytes=%d maxCopyLag=%s (+ up to 1s until the replica's fdatasync)\n",
				maxLag, maxLagTime.Round(time.Millisecond))

			return
		case <-tick.C:
		}

		tickStart := time.Now()
		ents, _ := os.ReadDir(src)

		for _, e := range ents {
			name := e.Name()

			in, err := os.Open(filepath.Join(src, name))
			if err != nil {
				continue
			}

			st, _ := in.Stat()
			if caughtUp {
				maxLag = max(maxLag, st.Size()-offs[name])
			}

			out := outs[name]
			if out == nil {
				out, _ = os.OpenFile(filepath.Join(dst, name), os.O_CREATE|os.O_WRONLY, 0o600)
				outs[name] = out
			}

			n, _ := io.Copy(io.NewOffsetWriter(out, offs[name]), io.NewSectionReader(in, offs[name], st.Size()-offs[name]))
			offs[name] += n
			in.Close()
		}

		// a byte written just after the previous tick's stat waits the tick
		// interval plus this copy; it is synced within a further second.
		if caughtUp {
			maxLagTime = max(maxLagTime, 200*time.Millisecond+time.Since(tickStart))
		}

		caughtUp = true

		if time.Since(lastSync) > time.Second {
			for _, out := range outs {
				_ = unix.Fdatasync(int(out.Fd()))
			}

			lastSync = time.Now()
		}
	}
}
