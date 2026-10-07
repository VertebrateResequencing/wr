package store

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/flat"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

//nolint:gochecknoglobals
var (
	bLive     = []byte("jobslive")
	bComplete = []byte("jobscomplete")
	bRunState = []byte("jobRunState")
	bRTK      = []byte("repgroupToKey")
	bLookup   = []byte("jobLookupEntries")
	bEndTime  = []byte("endTimeToKey")
)

type boltOp struct {
	kind  Kind
	key   []byte
	val   []byte
	extra []byte // complete record for archives
	end   int64
	done  chan struct{}
	err   *error
}

// boltStore is the baseline: wr's bucket layout, Binc-encoded jobs, and ONE
// group-commit writer folding every queued op into one Update (better than
// develop, whose archive, best-effort, add and delete writers contend for the
// lock). small=true is develop since #684 (reserve and start write a small
// run-state record); small=false is before it (they rewrite the whole job).
type boltStore struct {
	db      *bolt.DB
	small   bool
	ch      *codec.BincHandle
	encPool sync.Pool
	mu      sync.Mutex
	pending []*boltOp
	kick    chan struct{}
	stop    chan struct{}
	wg      sync.WaitGroup

	txs, ops  atomic.Int64
	txNanos   atomic.Int64
	maxTx     atomic.Int64
}

func openBolt(dir string, small, noSync bool) (*boltStore, error) {
	db, err := bolt.Open(filepath.Join(dir, "bolt.db"), 0o600, &bolt.Options{
		FreelistType: bolt.FreelistMapType, NoFreelistSync: true, NoSync: noSync,
		InitialMmapSize: 32 << 30, Timeout: 5 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bLive, bComplete, bRunState, bRTK, bLookup, bEndTime} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	s := &boltStore{db: db, small: small, ch: new(codec.BincHandle), kick: make(chan struct{}, 1),
		stop: make(chan struct{})}
	s.encPool.New = func() any { return codec.NewEncoderBytes(nil, s.ch) }
	s.wg.Add(1)

	go s.loop()

	return s, nil
}

func (s *boltStore) encode(j *jobqueue.Job) []byte {
	var out []byte

	enc := s.encPool.Get().(*codec.Encoder) //nolint:forcetypeassert
	enc.ResetBytes(&out)

	if err := enc.Encode(j); err != nil {
		panic(err)
	}

	s.encPool.Put(enc)

	return out
}

func (s *boltStore) Add(jobs []*jobqueue.Job, keys []flat.Key) error {
	// like develop's chunked add: one transaction for the batch.
	return s.db.Update(func(tx *bolt.Tx) error {
		live, rtk, look := tx.Bucket(bLive), tx.Bucket(bRTK), tx.Bucket(bLookup)

		for i, j := range jobs {
			k := hexKey(keys[i])
			if err := live.Put(k, s.encode(j)); err != nil {
				return err
			}

			lk := append([]byte(j.RepGroup+"_::_"), k...)
			if err := rtk.Put(lk, nil); err != nil {
				return err
			}

			if err := look.Put(append(append(k, "repgroupToKey_::_"...), lk...), nil); err != nil {
				return err
			}
		}

		return nil
	})
}

func hexKey(k flat.Key) []byte { return fmt.Appendf(nil, "%x", k[:]) }

func (s *boltStore) Put(kind Kind, st *flat.State, j *jobqueue.Job) (<-chan struct{}, func() error) {
	op := &boltOp{kind: kind, key: hexKey(st.Key), done: make(chan struct{}), err: new(error)}

	switch {
	case kind == Archive:
		op.extra = s.encode(j)
		op.end = st.EndTime
	case s.small:
		op.val = make([]byte, flat.StateSize)
		flat.PutState(op.val, st)
	default:
		op.val = s.encode(j)
	}

	s.mu.Lock()
	s.pending = append(s.pending, op)
	s.mu.Unlock()

	select {
	case s.kick <- struct{}{}:
	default:
	}

	return op.done, func() error { return *op.err }
}

func (s *boltStore) loop() {
	defer s.wg.Done()

	for {
		select {
		case <-s.kick:
		case <-s.stop:
			s.drain()

			return
		}

		s.drain()
	}
}

func (s *boltStore) drain() {
	s.mu.Lock()
	ops := s.pending
	s.pending = nil
	s.mu.Unlock()

	if len(ops) == 0 {
		return
	}

	start := time.Now()
	err := s.db.Update(func(tx *bolt.Tx) error {
		live, comp, rs, et := tx.Bucket(bLive), tx.Bucket(bComplete), tx.Bucket(bRunState), tx.Bucket(bEndTime)

		for _, op := range ops {
			if err := s.apply(op, live, comp, rs, et); err != nil {
				return err
			}
		}

		return nil
	})

	el := time.Since(start)
	s.txs.Add(1)
	s.ops.Add(int64(len(ops)))
	s.txNanos.Add(int64(el))

	if int64(el) > s.maxTx.Load() {
		s.maxTx.Store(int64(el))
	}

	for _, op := range ops {
		*op.err = err
		close(op.done)
	}
}

func (s *boltStore) apply(op *boltOp, live, comp, rs, et *bolt.Bucket) error {
	switch {
	case op.kind == Archive:
		if err := live.Delete(op.key); err != nil {
			return err
		}

		if err := rs.Delete(op.key); err != nil {
			return err
		}

		if err := comp.Put(op.key, op.extra); err != nil {
			return err
		}

		ek := binary.BigEndian.AppendUint64(nil, uint64(op.end))

		return et.Put(append(ek, op.key...), nil)
	case s.small:
		return rs.Put(op.key, op.val)
	default:
		return live.Put(op.key, op.val)
	}
}

func (s *boltStore) Stats() string {
	t := s.txs.Load()

	return fmt.Sprintf("txs=%d ops=%d meanTx=%s maxTx=%s", t, s.ops.Load(),
		time.Duration(s.txNanos.Load()/max(t, 1)).Round(time.Microsecond),
		time.Duration(s.maxTx.Load()).Round(time.Millisecond))
}

func (s *boltStore) Close() error {
	close(s.stop)
	s.wg.Wait()

	return s.db.Close()
}

func recoverBolt(dir string, decode bool) (Recovered, error) {
	var r Recovered

	db, err := bolt.Open(filepath.Join(dir, "bolt.db"), 0o600, &bolt.Options{
		FreelistType: bolt.FreelistMapType, NoFreelistSync: true, Timeout: 5 * time.Second,
	})
	if err != nil {
		return r, err
	}
	defer db.Close()

	// as develop's openBoltPrefetched: on NFS the flock drops the client's
	// cache, so read the whole file with 8 streams before decoding.
	t := time.Now()
	pre, _ := prefetch(filepath.Join(dir, "bolt.db"))
	r.Phases = fmt.Sprintf("prefetch=%s(%dMB)", time.Since(t).Round(time.Millisecond), pre>>20)

	ch := new(codec.BincHandle)

	err = db.View(func(tx *bolt.Tx) error {
		rs := tx.Bucket(bRunState)
		r.Complete = tx.Bucket(bComplete).Stats().KeyN

		return tx.Bucket(bLive).ForEach(func(k, v []byte) error {
			r.Live++
			r.Records++
			r.Bytes += int64(len(v))

			if !decode {
				return nil
			}

			j := &jobqueue.Job{}
			if err := codec.NewDecoderBytes(v, ch).Decode(j); err != nil {
				return err
			}

			if v := rs.Get(k); v != nil {
				var st flat.State
				if flat.GetState(v, &st) == nil {
					st.ApplyTo(j)
				}
			}

			return nil
		})
	})

	return r, err
}

func prefetch(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return 0, err
	}

	var (
		wg    sync.WaitGroup
		total atomic.Int64
	)

	const streams = 8

	part := st.Size()/streams + 1

	for i := range streams {
		wg.Add(1)

		go func() {
			defer wg.Done()

			buf := make([]byte, 4<<20)

			for off := int64(i) * part; off < min(int64(i+1)*part, st.Size()); {
				n, err := f.ReadAt(buf, off)
				total.Add(int64(n))
				off += int64(n)

				if err != nil || n == 0 {
					return
				}
			}
		}()
	}

	wg.Wait()

	return total.Load(), nil
}
