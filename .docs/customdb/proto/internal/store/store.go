// Package store holds the prototype storage designs behind one interface, so
// one driver can measure their hot write paths and recovery the same way.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/flat"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/gc"
	"github.com/VertebrateResequencing/wr/jobqueue"
)

// Kind of transition.
type Kind uint8

// Transition kinds.
const (
	Reserve Kind = iota
	Start
	Archive
	Release
)

// Store persists job specs and run-state transitions.
type Store interface {
	// Add durably stores new jobs (returns once on disk).
	Add(jobs []*jobqueue.Job, keys []flat.Key) error
	// Put queues a transition and returns its durability wait. j is the full
	// in-memory job (only the bbolt full-rewrite variant reads it).
	Put(kind Kind, st *flat.State, j *jobqueue.Job) (<-chan struct{}, func() error)
	// Stats describes the write path so far.
	Stats() string
	Close() error
}

// Recovered summarises a recovery.
type Recovered struct {
	Live, Complete, Records int
	Bytes                   int64
	TornAt                  int64
	Took                    time.Duration
	Phases                  string
}

func (r Recovered) String() string {
	return fmt.Sprintf("live=%d complete=%d records=%d readMB=%.0f took=%s %s",
		r.Live, r.Complete, r.Records, float64(r.Bytes)/1e6, r.Took.Round(time.Millisecond), r.Phases)
}

// Open opens design name in dir.
func Open(name, dir string, noSync bool) (Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	switch name {
	case "keyfiles":
		return openLogStore(dir, keyFileBuckets, noSync)
	case "wal":
		return openLogStore(dir, 1, noSync)
	case "slots":
		return openSlots(dir, noSync)
	case "boltfull", "boltsmall":
		return openBolt(dir, name == "boltsmall", noSync)
	case "sqlite":
		return openSQLite(dir, noSync)
	}

	return nil, errors.New("unknown design " + name)
}

// Recover reads design name's files in dir back into memory.
func Recover(name, dir string, decode bool) (Recovered, error) {
	start := time.Now()

	var (
		r   Recovered
		err error
	)

	switch name {
	case "keyfiles":
		r, err = recoverLogStore(dir, keyFileBuckets, decode)
	case "wal":
		r, err = recoverLogStore(dir, 1, decode)
	case "slots", "slotslazy":
		r, err = recoverSlots(dir, decode && name == "slots")
	case "boltfull", "boltsmall":
		r, err = recoverBolt(dir, decode)
	case "sqlite":
		r, err = recoverSQLite(dir, decode)
	default:
		err = errors.New("unknown design " + name)
	}

	r.Took = time.Since(start)

	return r, err
}

func openFiles(dir string, names ...string) ([]*os.File, error) {
	files := make([]*os.File, 0, len(names))

	for _, n := range names {
		f, err := os.OpenFile(filepath.Join(dir, n), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}

		if _, err = f.Seek(0, 2); err != nil {
			return nil, err
		}

		files = append(files, f)
	}

	return files, nil
}

func writerStats(w *gc.Writer) string {
	b := w.Batches.Load()

	return fmt.Sprintf("batches=%d syncs=%d MB=%.1f meanBatchFlush=%s", b, w.Syncs.Load(),
		float64(w.Bytes.Load())/1e6, time.Duration(w.SyncNanos.Load()/max(b, 1)).Round(time.Microsecond))
}
