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

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/internal/publishexit"
	"github.com/VertebrateResequencing/wr/internal/testcerts"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const (
	testDBPerm           = 0o600
	testServerWait       = 60 * time.Second
	testServerAttempts   = 3
	testReserveTimeout   = 10 * time.Second
	testRunStateStartMs  = 1700000000123
	testColState         = 4
	testColHost          = 7
	testLiveDBName       = "live.db"
	testCertDomain       = "localhost"
	testServerInterrupts = 10 * time.Millisecond
	testLiveLine         = "jobslive\tk1\tportal\t12\tready\t0\t0\t\t0\t0\t0"
)

func TestUsage(t *testing.T) {
	Convey("dbstart prints usage and exits 2", t, func() {
		var out, errOut bytes.Buffer

		for _, args := range [][]string{{}, {"-schema"}, {"a", "b"}, {"-schema", "a", "b"}} {
			So(runArgs(args, &out, &errOut), ShouldEqual, exitUsage)
		}

		So(out.String(), ShouldBeEmpty)
		So(errOut.String(), ShouldEqual, strings.Repeat("usage: dbstart [-schema] <db file>\n", 4))
	})
}

// testRunState holds the jobRunState fields dbstart prints, named as in
// jobqueue's jobRunState, which binc encodes as a map by field name.
type testRunState struct {
	StartTime time.Time
	EndTime   time.Time
	State     jobqueue.JobState
	Host      string
	Exitcode  int
	Pid       int
	Attempts  uint32
}

func TestRunOverlaysRunState(t *testing.T) {
	Convey("Given a live job in ready state with Cmd /x/psimjob.sh portal 12", t, func() {
		ch := new(codec.BincHandle)
		live := encodeTest(ch, &jobqueue.Job{Cmd: "/x/psimjob.sh portal 12", State: jobqueue.JobStateReady})
		runState := encodeTest(ch, &testRunState{
			State: jobqueue.JobStateRunning, Host: "h1", Pid: 7, Attempts: 1,
			StartTime: time.UnixMilli(testRunStateStartMs),
		})
		path := filepath.Join(t.TempDir(), testLiveDBName)

		Convey("and a jobRunState record written over that live record, run prints the run state", func() {
			writeTestDB(path, live, runStateRecordFor(live, runState))

			So(runLines(path), ShouldResemble,
				[]string{"jobslive\tk1\tportal\t12\trunning\t0\t1\th1\t7\t" +
					strconv.Itoa(testRunStateStartMs) + "\t0"})
		})

		Convey("and a jobRunState record whose CRC does not match, run prints the live record", func() {
			record := runStateRecordFor(live, runState)
			record[0] ^= 0xff

			writeTestDB(path, live, record)

			So(runLines(path), ShouldResemble, []string{testLiveLine})
		})

		Convey("and a CRC-matching jobRunState record whose body does not decode, run prints the live record", func() {
			writeTestDB(path, live, runStateRecordFor(live, runState[:len(runState)/2]))

			So(runLines(path), ShouldResemble, []string{testLiveLine})
		})

		Convey("and no jobRunState bucket, run prints the live record", func() {
			writeTestDB(path, live, nil)

			So(runLines(path), ShouldResemble, []string{testLiveLine})
		})
	})
}

// encodeTest binc encodes v, as jobqueue's database does.
func encodeTest(ch *codec.BincHandle, v any) []byte {
	var encoded []byte

	So(codec.NewEncoderBytes(&encoded, ch).Encode(v), ShouldBeNil)

	return encoded
}

// writeTestDB creates a bolt database at path whose jobslive bucket holds live
// under key k1 and, if record is not nil, whose jobRunState bucket holds record
// under k1.
func writeTestDB(path string, live, record []byte) {
	So(updateTestDB(path, func(tx *bolt.Tx) error {
		if err := putTestValue(tx, "jobslive", "k1", live); err != nil {
			return err
		}

		if record == nil {
			return nil
		}

		return putTestValue(tx, "jobRunState", "k1", record)
	}), ShouldBeNil)
}

func updateTestDB(path string, fn func(tx *bolt.Tx) error) error {
	bdb, err := bolt.Open(path, testDBPerm, nil)
	if err != nil {
		return err
	}

	defer bdb.Close()

	return bdb.Update(fn)
}

func putTestValue(tx *bolt.Tx, bucket, key string, value []byte) error {
	b, err := tx.CreateBucketIfNotExists([]byte(bucket))
	if err != nil {
		return err
	}

	return b.Put([]byte(key), value)
}

// runStateRecordFor returns a jobRunState bucket value for encodedRunState over
// live: the big-endian CRC-32C of live, then encodedRunState.
func runStateRecordFor(live, encodedRunState []byte) []byte {
	record := binary.BigEndian.AppendUint32(nil, crc32.Checksum(live, crc32.MakeTable(crc32.Castagnoli)))

	return append(record, encodedRunState...)
}

// runLines returns the lines run writes for the database at path.
func runLines(path string) []string {
	var out bytes.Buffer

	So(run(path, &out), ShouldBeNil)

	return strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
}

func TestRunReadsRealReservation(t *testing.T) {
	Convey("Given a server with a job reserved but not started, and a crash image of its database", t, func() {
		ctx := context.Background()
		server, addr, caFile, token := startTestServer(t)

		jq, err := jobqueue.Connect(addr, caFile, testCertDomain, token, testServerWait)
		So(err, ShouldBeNil)

		job := &jobqueue.Job{
			Cmd:          "/x/psimjob.sh portal 13",
			Cwd:          t.TempDir(),
			ReqGroup:     "dbstart",
			RepGroup:     "dbstart",
			Requirements: &jqs.Requirements{RAM: 10, Time: time.Second, Cores: 1, Other: map[string]string{}},
		}

		added, _, err := jq.Add([]*jobqueue.Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		reserved, err := jq.Reserve(testReserveTimeout)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		image := filepath.Join(t.TempDir(), "crash.db")
		f, err := os.Create(image)
		So(err, ShouldBeNil)
		So(server.BackupDB(f), ShouldBeNil)
		So(f.Close(), ShouldBeNil)

		So(jq.Disconnect(), ShouldBeNil)
		server.Stop(ctx, true)

		Convey("run prints the job as reserved on this host", func() {
			var line []string

			for _, l := range runLines(image) {
				if strings.Contains(l, "\tportal\t13\t") {
					line = strings.Split(l, "\t")
				}
			}

			hostname, err := os.Hostname()
			So(err, ShouldBeNil)
			So(line, ShouldNotBeEmpty)
			So(line[0], ShouldEqual, "jobslive")
			So(line[testColState], ShouldEqual, string(jobqueue.JobStateReserved))
			So(line[testColHost], ShouldEqual, hostname)
		})
	})
}

// startTestServer starts a jobqueue server on a temp database and free ports,
// returning it, its address, its CA file and its client token. It retries with
// new ports if the ones picked were taken before the server bound them.
func startTestServer(t *testing.T) (*jobqueue.Server, string, string, []byte) {
	t.Helper()

	for attempt := range testServerAttempts {
		dir := t.TempDir()
		port, webPort := freeTestPorts()
		config := jobqueue.ServerConfig{
			Port:            port,
			WebPort:         webPort,
			SchedulerName:   "local",
			SchedulerConfig: &jqs.ConfigLocal{Shell: "bash"},
			DBFile:          filepath.Join(dir, "db"),
			DBFileBackup:    filepath.Join(dir, "db_bk"),
			TokenFile:       filepath.Join(dir, "client.token"),
			CAFile:          filepath.Join(dir, "ca.pem"),
			CertFile:        filepath.Join(dir, "cert.pem"),
			KeyFile:         filepath.Join(dir, "key.pem"),
			CertDomain:      testCertDomain,
			Deployment:      internal.Development,
			Timings:         jobqueue.ServerTimings{InterruptTime: testServerInterrupts},
		}

		// Serve would otherwise make new RSA keys.
		So(testcerts.Write(config.CAFile, config.CertFile, config.KeyFile, config.CertDomain), ShouldBeNil)

		server, token, ok := tryServe(t, config, attempt == testServerAttempts-1)
		if ok {
			return server, testCertDomain + ":" + port, config.CAFile, token
		}
	}

	t.Fatal("test server did not start")

	return nil, "", "", nil
}

// freeTestPorts returns two distinct free ports, holding both listeners open
// at once so they cannot be the same.
func freeTestPorts() (string, string) {
	var lc net.ListenConfig

	l1, err := lc.Listen(context.Background(), "tcp", "0.0.0.0:0")
	So(err, ShouldBeNil)

	defer l1.Close()

	l2, err := lc.Listen(context.Background(), "tcp", "0.0.0.0:0")
	So(err, ShouldBeNil)

	defer l2.Close()

	return portOf(l1), portOf(l2)
}

func portOf(l net.Listener) string {
	addr, ok := l.Addr().(*net.TCPAddr)
	So(ok, ShouldBeTrue)

	return strconv.Itoa(addr.Port)
}

// tryServe starts a server with config and waits for it to serve, reporting
// false if it could not bind its ports and last is false.
func tryServe(t *testing.T, config jobqueue.ServerConfig, last bool) (*jobqueue.Server, []byte, bool) {
	t.Helper()

	exits, restore := publishexit.Notify()
	defer restore()

	server, _, token, err := jobqueue.Serve(context.Background(), config)
	if err != nil {
		if last || !strings.Contains(err.Error(), "address already in use") {
			t.Fatalf("test server did not start: %s", err)
		}

		return nil, nil, false
	}

	select {
	case <-server.Serving():
		return server, token, true
	case <-exits:
	case <-time.After(testServerWait):
		t.Fatal("timed out waiting for the test server to serve")
	}

	server.Stop(context.Background(), true)

	if last {
		t.Fatal("test server could not bind its manager port")
	}

	return nil, nil, false
}

func TestSchema(t *testing.T) {
	Convey("Given a database", t, func() {
		path := filepath.Join(t.TempDir(), testLiveDBName)

		var out, errOut bytes.Buffer

		Convey("stamped with schema version 2, -schema prints it and exits 0", func() {
			writeTestStamp(path, binary.BigEndian.AppendUint64(nil, 2))

			So(runSchema(path, &out, &errOut), ShouldEqual, 0)
			So(out.String(), ShouldEqual, "schemaVersion=2\n")
			So(errOut.String(), ShouldBeEmpty)
		})

		Convey("with no stamp, -schema prints version 0 and exits 0", func() {
			writeTestDB(path, []byte("live"), nil)

			So(runSchema(path, &out, &errOut), ShouldEqual, 0)
			So(out.String(), ShouldEqual, "schemaVersion=0\n")
			So(errOut.String(), ShouldBeEmpty)
		})

		Convey("with a meta bucket but no stamp, -schema prints version 0 and exits 0", func() {
			So(updateTestDB(path, func(tx *bolt.Tx) error {
				_, err := tx.CreateBucket([]byte("meta"))

				return err
			}), ShouldBeNil)

			So(runSchema(path, &out, &errOut), ShouldEqual, 0)
			So(out.String(), ShouldEqual, "schemaVersion=0\n")
		})

		Convey("with a 3-byte stamp, -schema prints the malformed error and exits 1", func() {
			writeTestStamp(path, []byte{0, 0, 2})

			So(runSchema(path, &out, &errOut), ShouldEqual, 1)
			So(out.String(), ShouldBeEmpty)
			So(errOut.String(), ShouldEqual, "dbstart: malformed schema version (3 bytes)\n")
		})

		Convey("that does not exist, -schema reports the error and exits 1", func() {
			So(runSchema(filepath.Join(t.TempDir(), "absent", "db"), &out, &errOut), ShouldEqual, 1)
			So(out.String(), ShouldBeEmpty)
			So(errOut.String(), ShouldStartWith, "dbstart: ")
		})
	})
}

// writeTestStamp creates a bolt database at path whose meta bucket holds stamp
// as its schemaVersion.
func writeTestStamp(path string, stamp []byte) {
	So(updateTestDB(path, func(tx *bolt.Tx) error {
		return putTestValue(tx, "meta", "schemaVersion", stamp)
	}), ShouldBeNil)
}
