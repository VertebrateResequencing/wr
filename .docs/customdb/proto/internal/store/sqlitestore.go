package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/flat"
	"github.com/VertebrateResequencing/wr/jobqueue"
	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

type sqlOp struct {
	kind Kind
	key  []byte
	st   []byte
	done chan struct{}
	err  *error
}

// sqliteStore is D4: SQLite in WAL mode with an exclusive lock (no -shm file,
// so it works on NFS from one process), specs and the 192-byte state in one
// row per job, and one group-commit writer folding queued transitions into a
// transaction.
type sqliteStore struct {
	db      *sql.DB
	mu      sync.Mutex
	pending []*sqlOp
	kick    chan struct{}
	stop    chan struct{}
	wg      sync.WaitGroup

	txs, ops, txNanos atomic.Int64
}

func sqliteDSN(dir string, noSync bool) string {
	sync := "FULL"
	if noSync {
		sync = "OFF"
	}

	return fmt.Sprintf("file:%s?_pragma=locking_mode(EXCLUSIVE)&_pragma=journal_mode(WAL)&"+
		"_pragma=synchronous(%s)&_pragma=wal_autocheckpoint(10000)", filepath.Join(dir, "jobs.sqlite"), sync)
}

func openSQLite(dir string, noSync bool) (*sqliteStore, error) {
	db, err := sql.Open("sqlite", sqliteDSN(dir, noSync))
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS jobs (key BLOB PRIMARY KEY, rg TEXT, spec BLOB,
		state BLOB, done INTEGER, endtime INTEGER) WITHOUT ROWID;
		CREATE INDEX IF NOT EXISTS jobs_rg ON jobs(rg, key);
		CREATE INDEX IF NOT EXISTS jobs_live ON jobs(done, key);`)
	if err != nil {
		return nil, err
	}

	s := &sqliteStore{db: db, kick: make(chan struct{}, 1), stop: make(chan struct{})}
	s.wg.Add(1)

	go s.loop()

	return s, nil
}

func (s *sqliteStore) Add(jobs []*jobqueue.Job, keys []flat.Key) error {
	// queued as one op so it shares the writer, like every other write.
	s.mu.Lock()
	done := make(chan struct{})
	errp := new(error)

	for i, j := range jobs {
		spec := flat.AppendSpec(nil, j, nil)
		s.pending = append(s.pending, &sqlOp{kind: 99, key: keys[i][:], st: spec, err: errp})
		_ = j
	}

	s.pending = append(s.pending, &sqlOp{kind: 100, done: done, err: errp})
	s.mu.Unlock()
	s.signal()
	<-done

	return *errp
}

func (s *sqliteStore) signal() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *sqliteStore) Put(kind Kind, st *flat.State, _ *jobqueue.Job) (<-chan struct{}, func() error) {
	op := &sqlOp{kind: kind, key: append([]byte(nil), st.Key[:]...), st: make([]byte, flat.StateSize),
		done: make(chan struct{}), err: new(error)}
	flat.PutState(op.st, st)

	s.mu.Lock()
	s.pending = append(s.pending, op)
	s.mu.Unlock()
	s.signal()

	return op.done, func() error { return *op.err }
}

func (s *sqliteStore) loop() {
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

func (s *sqliteStore) drain() {
	s.mu.Lock()
	ops := s.pending
	s.pending = nil
	s.mu.Unlock()

	if len(ops) == 0 {
		return
	}

	start := time.Now()
	err := s.apply(ops)
	s.txs.Add(1)
	s.ops.Add(int64(len(ops)))
	s.txNanos.Add(int64(time.Since(start)))

	for _, op := range ops {
		*op.err = err

		if op.done != nil {
			close(op.done)
		}
	}
}

func (s *sqliteStore) apply(ops []*sqlOp) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	ins, _ := tx.Prepare(`INSERT OR REPLACE INTO jobs(key, rg, spec, state, done) VALUES (?, 'rg', ?, NULL, 0)`)
	upd, _ := tx.Prepare(`UPDATE jobs SET state = ? WHERE key = ?`)
	arc, _ := tx.Prepare(`UPDATE jobs SET state = ?, done = 1, endtime = ? WHERE key = ?`)

	for _, op := range ops {
		switch op.kind {
		case 99:
			_, err = ins.Exec(op.key, op.st)
		case 100:
		case Archive:
			_, err = arc.Exec(op.st, time.Now().UnixNano(), op.key)
		default:
			_, err = upd.Exec(op.st, op.key)
		}

		if err != nil {
			_ = tx.Rollback()

			return err
		}
	}

	return tx.Commit()
}

func (s *sqliteStore) Stats() string {
	t := s.txs.Load()

	return fmt.Sprintf("txs=%d ops=%d meanTx=%s", t, s.ops.Load(),
		time.Duration(s.txNanos.Load()/max(t, 1)).Round(time.Microsecond))
}

func (s *sqliteStore) Close() error {
	close(s.stop)
	s.wg.Wait()

	return s.db.Close()
}

func recoverSQLite(dir string, decode bool) (Recovered, error) {
	var r Recovered

	db, err := sql.Open("sqlite", sqliteDSN(dir, false))
	if err != nil {
		return r, err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT spec, state FROM jobs WHERE done = 0`)
	if err != nil {
		return r, err
	}
	defer rows.Close()

	var spec, state []byte

	for rows.Next() {
		if err := rows.Scan(&spec, &state); err != nil {
			return r, err
		}

		r.Live++
		r.Bytes += int64(len(spec) + len(state))

		if decode {
			j := &jobqueue.Job{}
			if _, err := flat.DecodeSpec(append([]byte(nil), spec...), j); err != nil {
				return r, err
			}

			var st flat.State
			if len(state) > 0 && flat.GetState(state, &st) == nil {
				st.ApplyTo(j)
			}
		}
	}

	return r, rows.Err()
}
