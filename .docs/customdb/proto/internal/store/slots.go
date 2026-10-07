package store

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/flat"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/gc"
	"github.com/VertebrateResequencing/wr/jobqueue"
)

// File indexes of the slot design.
const (
	slotSpecs   = 0 // append-only spec segment, written once per job
	slotTable   = 1 // fixed 192-byte state slots, overwritten in place
	slotHistory = 2 // append-only completion records (history tier)
)

// slotStore is D3: immutable specs appended once, plus one fixed-size state
// slot per live job that every transition overwrites in place. A finished
// job's slot gets its final state, its completion goes to the history log in
// the same batch, and the slot is reused.
// slotRef is where a live job's slot and spec are.
type slotRef struct {
	slot, specOff int64
}

type slotStore struct {
	w     *gc.Writer
	mu    sync.Mutex
	slot  map[flat.Key]slotRef
	free  []int64
	next  int64
	bufMu sync.Mutex
	buf   []byte
}

func openSlots(dir string, noSync bool) (*slotStore, error) {
	files, err := openFiles(dir, "specs.log", "slots.dat", "history.log")
	if err != nil {
		return nil, err
	}

	w := gc.New(files)
	w.NoSync = noSync

	st, err := files[slotTable].Stat()
	if err != nil {
		return nil, err
	}

	return &slotStore{w: w, slot: make(map[flat.Key]slotRef), next: st.Size() / flat.StateSize}, nil
}

// slotFor returns k's slot and spec offset, allocating a slot (recording
// specOff) if k has none.
func (s *slotStore) slotFor(k flat.Key, specOff int64) slotRef {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.slot[k]; ok {
		return r
	}

	var n int64
	if l := len(s.free); l > 0 {
		n, s.free = s.free[l-1], s.free[:l-1]
	} else {
		n = s.next
		s.next++
	}

	r := slotRef{slot: n, specOff: specOff}
	s.slot[k] = r

	return r
}

func (s *slotStore) Add(jobs []*jobqueue.Job, keys []flat.Key) error {
	var (
		done <-chan struct{}
		errf func() error
	)

	state := make([]byte, flat.StateSize)

	for i, j := range jobs {
		off := s.w.Size(slotSpecs) + gc.FrameHeader
		rec := append(make([]byte, 0, len(j.Cmd)+300), keys[i][:]...)
		rec = flat.AppendSpec(rec, j, nil)
		s.w.Append(slotSpecs, rec)

		st := flat.State{Key: keys[i], State: flat.SReady, SpecOff: uint64(off)}
		flat.PutState(state, &st)
		done, errf = s.w.WriteAt(slotTable, s.slotFor(keys[i], off).slot*flat.StateSize, state)
	}

	return gc.Wait(done, errf)
}

func (s *slotStore) Put(kind Kind, st *flat.State, _ *jobqueue.Job) (<-chan struct{}, func() error) {
	s.bufMu.Lock()
	if s.buf == nil {
		s.buf = make([]byte, flat.StateSize)
	}

	ref := s.slotFor(st.Key, 0)
	n := ref.slot
	st.SpecOff = uint64(ref.specOff)
	flat.PutState(s.buf, st)

	if kind == Archive {
		s.w.Append(slotHistory, s.buf)
	}

	done, errf := s.w.WriteAt(slotTable, n*flat.StateSize, s.buf)
	s.bufMu.Unlock()

	if kind == Archive {
		// the slot can be reused once this batch is durable; a reuse queued in
		// a later batch is ordered after it by the writer.
		go func() {
			<-done
			s.mu.Lock()
			delete(s.slot, st.Key)
			s.free = append(s.free, n)
			s.mu.Unlock()
		}()
	}

	return done, errf
}

func (s *slotStore) Stats() string { return writerStats(s.w) }

func (s *slotStore) Close() error {
	s.w.Close()

	return nil
}

// recoverSlots reads the slot table sequentially; with decode it also preads
// every live job's spec, as today's manager (which keeps whole Jobs in memory)
// would need. Without it, it stops at the slots: what a manager that loads
// specs lazily would read.
func recoverSlots(dir string, decode bool) (Recovered, error) {
	var r Recovered

	f, err := os.Open(filepath.Join(dir, "slots.dat"))
	if err != nil {
		return r, err
	}
	defer f.Close()

	specs, err := os.Open(filepath.Join(dir, "specs.log"))
	if err != nil {
		return r, err
	}
	defer specs.Close()

	br := bufio.NewReaderSize(f, 4<<20)
	buf := make([]byte, flat.StateSize)

	var (
		st   flat.State
		offs []uint64
	)

	for {
		if _, err := io.ReadFull(br, buf); err != nil {
			break
		}

		r.Records++
		r.Bytes += flat.StateSize

		if flat.GetState(buf, &st) != nil {
			continue // torn slot: never-written or mid-overwrite; see design doc
		}

		if st.State == flat.SComplete || st.State == flat.SDeleted {
			r.Complete++

			continue
		}

		r.Live++
		offs = append(offs, st.SpecOff)
	}

	if !decode {
		return r, nil
	}

	n, err := readSpecs(specs, offs)
	r.Bytes += n

	return r, err
}

// readSpecs preads each spec with 8 readers, decoding each into a new Job.
func readSpecs(f *os.File, offs []uint64) (int64, error) {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int64
		first error
	)

	chunk := (len(offs) + 7) / 8

	for c := 0; c < len(offs); c += max(chunk, 1) {
		part := offs[c:min(c+chunk, len(offs))]

		wg.Add(1)

		go func() {
			defer wg.Done()

			hdr := make([]byte, gc.FrameHeader)

			var n int64

			for _, off := range part {
				if _, err := f.ReadAt(hdr, int64(off)-gc.FrameHeader); err != nil {
					mu.Lock()
					first = err
					mu.Unlock()

					return
				}

				rec := make([]byte, binary.LittleEndian.Uint32(hdr))
				if _, err := f.ReadAt(rec, int64(off)); err != nil {
					return
				}

				n += int64(len(rec))

				j := &jobqueue.Job{}
				if _, err := flat.DecodeSpec(rec[16:], j); err != nil {
					mu.Lock()
					first = err
					mu.Unlock()

					return
				}
			}

			mu.Lock()
			total += n
			mu.Unlock()
		}()
	}

	wg.Wait()

	return total, first
}
