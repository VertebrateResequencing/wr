package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/flat"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/gc"
	"github.com/VertebrateResequencing/wr/jobqueue"
)

// keyFileBuckets is how many files the owner's key-laid-out design spreads
// jobs over (bucket = first key byte mod this). Fixed, so the file count does
// not grow with the number of jobs.
const keyFileBuckets = 64

// Record types in the log designs.
const (
	recSpec  = 1
	recState = 2
)

// logStore is both the single write-ahead log (D2, buckets=1) and the owner's
// key-bucketed files (D1, buckets=64): every record is appended to the file
// its key hashes to, and a batch fsyncs every file it touched.
type logStore struct {
	w       *gc.Writer
	buckets int
	bufPool sync.Pool
}

func bucketNames(n int) []string {
	names := make([]string, n)
	for i := range n {
		names[i] = fmt.Sprintf("b%03d.log", i)
	}

	return names
}

func openLogStore(dir string, buckets int, noSync bool) (*logStore, error) {
	files, err := openFiles(dir, bucketNames(buckets)...)
	if err != nil {
		return nil, err
	}

	w := gc.New(files)
	w.NoSync = noSync

	return &logStore{w: w, buckets: buckets, bufPool: sync.Pool{New: func() any { return new([]byte) }}}, nil
}

func (s *logStore) bucket(k flat.Key) int { return int(k[0]) % s.buckets }

func (s *logStore) Add(jobs []*jobqueue.Job, keys []flat.Key) error {
	idx := make([]int, len(jobs))
	recs := make([][]byte, len(jobs))

	for i, j := range jobs {
		rec := append(make([]byte, 0, len(j.Cmd)+300), recSpec)
		rec = append(rec, keys[i][:]...)
		recs[i] = flat.AppendSpec(rec, j, nil)
		idx[i] = s.bucket(keys[i])
	}

	return gc.Wait(s.w.AppendMulti(idx, recs))
}

func (s *logStore) Put(_ Kind, st *flat.State, _ *jobqueue.Job) (<-chan struct{}, func() error) {
	bp := s.bufPool.Get().(*[]byte) //nolint:forcetypeassert
	if cap(*bp) < 17+flat.StateSize {
		*bp = make([]byte, 0, 17+flat.StateSize)
	}

	rec := append((*bp)[:0], recState)
	rec = append(rec, st.Key[:]...)
	rec = rec[:17+flat.StateSize]
	flat.PutState(rec[17:], st)

	done, errf := s.w.Append(s.bucket(st.Key), rec)
	*bp = rec
	s.bufPool.Put(bp)

	return done, errf
}

func (s *logStore) Stats() string { return writerStats(s.w) }

func (s *logStore) Close() error {
	s.w.Close()

	return nil
}

// jobEntry is recovery's view of one key.
type jobEntry struct {
	spec  []byte
	state flat.State
	has   bool
}

func recoverLogStore(dir string, buckets int, decode bool) (Recovered, error) {
	var (
		r  Recovered
		mu sync.Mutex
		wg sync.WaitGroup
	)

	maps := make([]map[flat.Key]*jobEntry, buckets)
	errs := make([]error, buckets)
	sem := make(chan struct{}, 8)

	for b, name := range bucketNames(buckets) {
		wg.Add(1)

		go func() {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			m, rr, err := scanBucket(filepath.Join(dir, name), decode)
			maps[b], errs[b] = m, err

			mu.Lock()
			r.Records += rr.Records
			r.Bytes += rr.Bytes
			r.Live += rr.Live
			r.Complete += rr.Complete
			r.TornAt = max(r.TornAt, rr.TornAt)
			mu.Unlock()
		}()
	}

	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return r, err
		}
	}

	return r, nil
}

func scanBucket(path string, decode bool) (map[flat.Key]*jobEntry, Recovered, error) {
	var r Recovered

	f, err := os.Open(path)
	if err != nil {
		return nil, r, err
	}
	defer f.Close()

	st, _ := f.Stat()
	m := make(map[flat.Key]*jobEntry)

	end, err := gc.Scan(f, func(_ int64, rec []byte) error {
		r.Records++

		var k flat.Key
		copy(k[:], rec[1:17])

		e := m[k]
		if e == nil {
			e = &jobEntry{}
			m[k] = e
		}

		switch rec[0] {
		case recSpec:
			e.spec = append([]byte(nil), rec[17:]...)
		case recState:
			if err := flat.GetState(rec[17:], &e.state); err != nil {
				return err
			}

			e.has = true
		}

		return nil
	})
	if err != nil {
		return nil, r, err
	}

	r.Bytes = st.Size()
	if end < st.Size() {
		r.TornAt = end
	}

	for _, e := range m {
		if e.has && (e.state.State == flat.SComplete || e.state.State == flat.SDeleted) {
			r.Complete++

			continue
		}

		if e.spec == nil {
			continue
		}

		r.Live++

		if decode {
			j := &jobqueue.Job{}
			if _, err := flat.DecodeSpec(e.spec, j); err != nil {
				return nil, r, err
			}

			if e.has {
				e.state.ApplyTo(j)
			}
		}
	}

	return m, r, nil
}
