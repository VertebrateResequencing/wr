package store

import (
	"bufio"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/flat"
	"github.com/VertebrateResequencing/wr/customdbproto/internal/gc"
)

// RecoverStates returns the newest recovered state (by Seq) of every key, for
// crash tests, plus the number of bytes past the last intact record (a torn
// tail) summed over the design's logs.
func RecoverStates(name, dir string) (map[flat.Key]flat.State, int64, error) {
	out := make(map[flat.Key]flat.State)
	keep := func(st flat.State) {
		if old, ok := out[st.Key]; !ok || st.Seq > old.Seq {
			out[st.Key] = st
		}
	}

	switch name {
	case "wal", "keyfiles":
		n := 1
		if name == "keyfiles" {
			n = keyFileBuckets
		}

		var torn int64

		for _, b := range bucketNames(n) {
			t, err := scanStates(filepath.Join(dir, b), 17, recState, keep)
			if err != nil {
				return nil, 0, err
			}

			torn += t
		}

		return out, torn, nil
	case "slots":
		torn, err := scanStates(filepath.Join(dir, "history.log"), 0, 0, keep)
		if err != nil {
			return nil, 0, err
		}

		return out, torn, slotStates(filepath.Join(dir, "slots.dat"), keep)
	case "sqlite":
		return out, 0, sqliteStates(dir, keep)
	}

	return nil, 0, errors.New("no state recovery for " + name)
}

func scanStates(path string, at int, typ byte, keep func(flat.State)) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	st, _ := f.Stat()

	end, err := gc.Scan(f, func(_ int64, rec []byte) error {
		if typ != 0 && rec[0] != typ {
			return nil
		}

		var s flat.State
		if err := flat.GetState(rec[at:], &s); err != nil {
			return err
		}

		keep(s)

		return nil
	})

	return st.Size() - end, err
}

func slotStates(path string, keep func(flat.State)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	buf := make([]byte, flat.StateSize)

	for {
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil //nolint:nilerr // end of table
		}

		var s flat.State
		if flat.GetState(buf, &s) == nil {
			keep(s)
		}
	}
}

func sqliteStates(dir string, keep func(flat.State)) error {
	db, err := sql.Open("sqlite", sqliteDSN(dir, false))
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT state FROM jobs WHERE state IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return err
		}

		var s flat.State
		if err := flat.GetState(b, &s); err != nil {
			return err
		}

		keep(s)
	}

	return rows.Err()
}
