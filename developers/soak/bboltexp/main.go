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

// Command bboltexp is soak tooling, not part of wr: it opens a COPY of a
// manager database the way the manager does (map freelist, NoFreelistSync, a
// large InitialMmapSize) and either checks it or churns it, so a freelist
// panic seen through the fusestall mount can be compared with the same file
// opened directly.
//
// usage:
//
//	bboltexp check <db>              open, print free pages and tx.Check() errors, close
//	bboltexp churn <db> <txs> [exit] open, commit <txs> write txs to a scratch bucket;
//	                                 "exit" os.Exits without Close (a kill -9)
package main

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	exitUsage      = 2
	dbPerm         = 0o600
	mmapFactor     = 2 // map twice the file, as the manager's headroom does
	openTimeout    = 10 * time.Second
	maxCheckErrs   = 20
	churnValueSize = 3000
	churnKeySize   = 16
	churnDeletes   = 150
	churnPuts      = 300
	minArgs        = 3
	exitArg        = 4
)

const (
	cmdCheck = "check"
	cmdChurn = "churn"
)

var errUsage = errors.New("usage: bboltexp check <db> | bboltexp churn <db> <txs> [exit]")

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is main, returning the exit code, so that the database is closed first.
func run(args []string) int {
	if !validArgs(args) {
		fmt.Fprintln(os.Stderr, errUsage)

		return exitUsage
	}

	db, err := open(args[1], args[0] == cmdCheck)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bboltexp:", err)

		return 1
	}

	if args[0] == cmdCheck {
		check(db)
	} else {
		err = churnArgs(db, args)
	}

	err = errors.Join(err, db.Close())
	if err != nil {
		fmt.Fprintln(os.Stderr, "bboltexp:", err)

		return 1
	}

	return 0
}

func validArgs(args []string) bool {
	switch {
	case len(args) == minArgs-1 && args[0] == cmdCheck:
		return true
	case len(args) >= minArgs && args[0] == cmdChurn:
		return true
	default:
		return false
	}
}

// open opens p as the manager opens its database, and prints its free pages.
func open(p string, ro bool) (*bolt.DB, error) {
	st, err := os.Stat(p) //nolint:gosec // G703: the path is the user's own argument
	if err != nil {
		return nil, err
	}

	t0 := time.Now()

	db, err := bolt.Open(p, dbPerm, &bolt.Options{FreelistType: bolt.FreelistMapType, NoFreelistSync: true,
		InitialMmapSize: int(st.Size()) * mmapFactor, Timeout: openTimeout, ReadOnly: ro})
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	s := db.Stats()
	fmt.Fprintf(os.Stdout, "opened %s in %s: size=%d freePages=%d pending=%d\n", p,
		time.Since(t0).Round(time.Millisecond), st.Size(), s.FreePageN, s.PendingPageN)

	return db, nil
}

// check prints the first maxCheckErrs of tx.Check()'s errors and their count.
func check(db *bolt.DB) {
	n := 0

	err := db.View(func(tx *bolt.Tx) error {
		for e := range tx.Check() {
			n++

			if n <= maxCheckErrs {
				fmt.Fprintln(os.Stdout, "check:", e)
			}
		}

		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stdout, "check: view:", err)
	}

	fmt.Fprintf(os.Stdout, "check errors: %d\n", n)
}

// churnArgs runs churn with the txs and optional exit of args, and on "exit"
// leaves the process without closing the database, as a kill -9 would.
func churnArgs(db *bolt.DB, args []string) error {
	txs, err := strconv.Atoi(args[2])
	if err != nil {
		return fmt.Errorf("txs: %w", err)
	}

	if err = churn(db, txs); err != nil {
		return err
	}

	if len(args) >= exitArg && args[3] == "exit" {
		os.Exit(0)
	}

	return nil
}

// churn commits txs write transactions, each deleting up to churnDeletes keys
// of a scratch bucket and putting churnPuts new ones, so pages are freed and
// reused as the manager's commits do.
func churn(db *bolt.DB, txs int) error {
	val := make([]byte, churnValueSize)

	for i := range txs {
		if err := db.Update(func(tx *bolt.Tx) error { return churnTx(tx, val) }); err != nil {
			return fmt.Errorf("tx %d: %w", i, err)
		}
	}

	s := db.Stats()
	fmt.Fprintf(os.Stdout, "churned %d txs: freePages=%d pending=%d\n", txs, s.FreePageN, s.PendingPageN)

	return nil
}

func churnTx(tx *bolt.Tx, val []byte) error {
	b, err := tx.CreateBucketIfNotExists([]byte("soakchurn"))
	if err != nil {
		return err
	}

	c := b.Cursor()
	d := 0

	for k, _ := c.First(); k != nil && d < churnDeletes; k, _ = c.Next() {
		if err = c.Delete(); err != nil {
			return err
		}

		d++
	}

	for range churnPuts {
		// crypto/rand.Read never returns an error
		k := make([]byte, churnKeySize)
		rand.Read(k)
		rand.Read(val)
		binary.BigEndian.PutUint64(k, uint64(time.Now().UnixNano()))

		if err = b.Put(k, val); err != nil {
			return err
		}
	}

	return nil
}
