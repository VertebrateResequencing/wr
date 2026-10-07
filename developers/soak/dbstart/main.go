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
// times in unix ms. A live job's run-state record, if it was written over that
// live record, is applied first, so the line shows the job's latest durable run
// state.
//
// With -schema, it instead prints the database's schema version, as
// schemaVersion=<n> (0 if the database has none).
//
// usage: dbstart [-schema] <db file>
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
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
	crcBytes    = 4
	stampBytes  = 8
	schemaFlag  = "-schema"
)

//nolint:gochecknoglobals // shared bolt keys and the CRC table.
var (
	bucketComplete       = []byte("jobscomplete")
	bucketLive           = []byte("jobslive")
	bucketRunState       = []byte("jobRunState")
	bucketMeta           = []byte("meta")
	metaKeySchemaVersion = []byte("schemaVersion")
	crcTable             = crc32.MakeTable(crc32.Castagnoli)
)

var errMalformedStamp = errors.New("malformed schema version")

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

// overlayRunState returns live with the encoded run state in runState decoded
// over it, or live unchanged (with a warning on stderr) if runState does not
// decode.
func overlayRunState(ch *codec.BincHandle, k []byte, live rec, runState []byte) rec {
	overlaid := live

	if err := codec.NewDecoderBytes(runState, ch).Decode(&overlaid); err != nil {
		fmt.Fprintln(os.Stderr, "dbstart: decode run state", string(k), err, "(printing the live record)")

		return live
	}

	return overlaid
}

// printJob prints the job encoded in v, with the encoded run state in runState
// (if not nil) decoded over it, if it is a psimjob.sh or relbury.sh job. If
// runState does not decode, it prints the job's values from v alone, as
// jobqueue's recovery falls back to the live record.
func printJob(out io.Writer, ch *codec.BincHandle, bucket string, k, v, runState []byte) {
	var r rec

	if err := codec.NewDecoderBytes(v, ch).Decode(&r); err != nil {
		fmt.Fprintln(os.Stderr, "dbstart: decode", string(k), err)

		return
	}

	if runState != nil {
		r = overlayRunState(ch, k, r, runState)
	}

	f := strings.Fields(r.Cmd)
	if len(f) < minFields || (!strings.HasSuffix(f[0], "psimjob.sh") && f[0] != ":") {
		return
	}

	// relbury.sh's jobs start ": relb <name>;"
	f[2] = strings.TrimSuffix(f[2], ";")
	fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%d\t%d\t%d\n", bucket, k, f[1], f[2], r.State,
		r.Exitcode, r.Attempts, r.Host, r.Pid, ms(r.StartTime), ms(r.EndTime))
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}

	return t.UnixMilli()
}

func main() {
	os.Exit(runArgs(os.Args[1:], os.Stdout, os.Stderr))
}

// runArgs runs dbstart with the command-line arguments args (without the
// program name), writing to out and errOut, and returns the exit code.
func runArgs(args []string, out, errOut io.Writer) int {
	if len(args) == 2 && args[0] == schemaFlag {
		return runSchema(args[1], out, errOut)
	}

	if len(args) != 1 || args[0] == schemaFlag {
		fmt.Fprintln(errOut, "usage: dbstart [-schema] <db file>")

		return exitUsage
	}

	if err := run(args[0], out); err != nil {
		fmt.Fprintln(errOut, "dbstart:", err)

		return 1
	}

	return 0
}

// runSchema prints the schema version of the database at path to out, as
// schemaVersion=<n>, and returns 0, or prints an error to errOut and returns 1.
func runSchema(path string, out, errOut io.Writer) int {
	version, err := schemaVersion(path)
	if err != nil {
		fmt.Fprintln(errOut, "dbstart:", err)

		return 1
	}

	fmt.Fprintf(out, "schemaVersion=%d\n", version)

	return 0
}

// schemaVersion returns the meta bucket's schemaVersion in the database at
// path, or 0 if it has none.
func schemaVersion(path string) (uint64, error) {
	bdb, err := openDB(path)
	if err != nil {
		return 0, err
	}

	defer bdb.Close()

	var version uint64

	err = bdb.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return nil
		}

		stamp := b.Get(metaKeySchemaVersion)
		if stamp == nil {
			return nil
		}

		if len(stamp) != stampBytes {
			return fmt.Errorf("%w (%d bytes)", errMalformedStamp, len(stamp))
		}

		version = binary.BigEndian.Uint64(stamp)

		return nil
	})

	return version, err
}

// run prints a line to out for each psimjob.sh and relbury.sh job in the
// database at path.
func run(path string, out io.Writer) error {
	bdb, err := openDB(path)
	if err != nil {
		return err
	}

	defer bdb.Close()

	ch := new(codec.BincHandle)

	return bdb.View(func(tx *bolt.Tx) error {
		if err := printBucket(out, ch, tx, bucketComplete, nil); err != nil {
			return err
		}

		return printBucket(out, ch, tx, bucketLive, tx.Bucket(bucketRunState))
	})
}

func openDB(path string) (*bolt.DB, error) {
	return bolt.Open(path, dbPerm, &bolt.Options{ReadOnly: true, Timeout: openTimeout})
}

// printBucket prints the jobs in tx's bucket named bn, if it exists, each with
// its matching record in runStates (if not nil) applied.
func printBucket(out io.Writer, ch *codec.BincHandle, tx *bolt.Tx, bn []byte, runStates *bolt.Bucket) error {
	b := tx.Bucket(bn)
	if b == nil {
		return nil
	}

	return b.ForEach(func(k, v []byte) error {
		var runState []byte

		if runStates != nil {
			runState = runStateOver(v, runStates.Get(k))
		}

		printJob(out, ch, string(bn), k, v, runState)

		return nil
	})
}

// runStateOver returns the encoded run state in record if record was written
// over exactly the live record live (it starts with live's big-endian CRC-32C);
// otherwise nil.
func runStateOver(live, record []byte) []byte {
	if len(record) < crcBytes || binary.BigEndian.Uint32(record) != crc32.Checksum(live, crcTable) {
		return nil
	}

	return record[crcBytes:]
}
