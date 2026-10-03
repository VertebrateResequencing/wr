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

// Command dbstart is soak tooling, not part of wr: it opens a STOPPED
// manager's bolt DB read-only and prints one line per psimjob.sh job in the
// complete and live buckets (and each relbury.sh job, as kind relb): bucket,
// key, kind, id, state, exit code, attempts, host, pid, and start and end
// times in unix ms.
//
// usage: dbstart <db file>
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const (
	exitUsage   = 2
	dbPerm      = 0o400
	openTimeout = 10 * time.Second
	minFields   = 3
)

// rec decodes only the fields it names, by name, from an encoded Job.
type rec struct {
	StartTime time.Time
	EndTime   time.Time
	Cmd       string
	State     string
	Host      string
	Exitcode  int
	Pid       int
	Attempts  uint32
}

// printJob prints the job encoded in v, if it is a psimjob.sh or relbury.sh
// job.
func printJob(ch *codec.BincHandle, bucket string, k, v []byte) {
	var r rec

	if err := codec.NewDecoderBytes(v, ch).Decode(&r); err != nil {
		fmt.Fprintln(os.Stderr, "dbstart: decode", string(k), err)

		return
	}

	f := strings.Fields(r.Cmd)
	if len(f) < minFields || (!strings.HasSuffix(f[0], "psimjob.sh") && f[0] != ":") {
		return
	}

	// relbury.sh's jobs start ": relb <name>;"
	f[2] = strings.TrimSuffix(f[2], ";")
	fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%d\t%d\t%d\n", bucket, k, f[1], f[2], r.State,
		r.Exitcode, r.Attempts, r.Host, r.Pid, ms(r.StartTime), ms(r.EndTime))
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}

	return t.UnixMilli()
}

func main() {
	if len(os.Args) != 2 { //nolint:mnd // the program name and the db
		fmt.Fprintln(os.Stderr, "usage: dbstart <db file>")
		os.Exit(exitUsage)
	}

	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "dbstart:", err)
		os.Exit(1)
	}
}

func run(path string) error {
	bdb, err := bolt.Open(path, dbPerm, &bolt.Options{ReadOnly: true, Timeout: openTimeout})
	if err != nil {
		return err
	}

	defer bdb.Close()

	ch := new(codec.BincHandle)

	return bdb.View(func(tx *bolt.Tx) error {
		for _, bn := range []string{"jobscomplete", "jobslive"} {
			b := tx.Bucket([]byte(bn))
			if b == nil {
				continue
			}

			if err := b.ForEach(func(k, v []byte) error {
				printJob(ch, bn, k, v)

				return nil
			}); err != nil {
				return err
			}
		}

		return nil
	})
}
