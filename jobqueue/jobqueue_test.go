/*******************************************************************************
 * Copyright (c) 2016-2022, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

package jobqueue

import (
	"context"
	crand "crypto/rand"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	"github.com/phayes/freeport"
	"github.com/shirou/gopsutil/v4/process"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
	"go.nanomsg.org/mangos/v3"
)

const (
	localSchedulerName  = "local"
	maxSpawnTime        = 240 * time.Second
	serverRC            = `echo %s %s %s %s %d %d`
	testCwd             = "/tmp"
	manuallyAdded       = "manually_added"
	reqGroupFake        = "fake_group"
	reqGroupFallocate   = "fallocate"
	futureDepGroup      = "future"
	testCarrierDepGroup = "carrier"
	testLiveDepGroup    = "live"
	testOtherRepGroup   = "other"
	testRepGroupA       = "rg-a"
	reqGroupPerl        = "perl"
	reqGroupSleep       = "sleep"
)

var (
	runnermode          bool
	runnerfail          bool
	runnerdebug         bool
	schedgrp            string
	runnermodetmpdir    string
	rdeployment         string
	rmanagerdir         string //nolint:gochecknoglobals
	rserver             string
	rdomain             string
	rtimeout            int
	maxmins             int
	envVars             = os.Environ()
	servermode          bool
	serverKeepDB        bool
	serverEnableRunners bool
)

var (
	errMissingLiveJobsBucket = errors.New("missing live jobs bucket")
	errUnexpectedLiveJobs    = errors.New("unexpected live job count")
	errFileStillExists       = errors.New("file still exists")
)

func init() {
	clog.ToDefault()

	flag.BoolVar(&runnermode, "runnermode", false, "enable to disable tests and act as a 'runner' client")
	flag.BoolVar(&runnerfail, "runnerfail", false, "make the runner client fail")
	flag.BoolVar(&runnerdebug, "runnerdebug", false, "make the runner create debug files")
	flag.StringVar(&schedgrp, "schedgrp", "", "schedgrp for runnermode")
	flag.StringVar(&rdeployment, "rdeployment", "", "deployment for runnermode")
	flag.StringVar(&rmanagerdir, "rmanagerdir", "", "manager dir (WR_MANAGERDIR) for runnermode")
	flag.StringVar(&rserver, "rserver", "", "server for runnermode")
	flag.StringVar(&rdomain, "rdomain", "", "domain for runnermode")
	flag.IntVar(&rtimeout, "rtimeout", 1, "reserve timeout for runnermode")
	flag.IntVar(&maxmins, "maxmins", 0, "maximum mins allowed for  runnermode")
	flag.StringVar(&runnermodetmpdir, "tmpdir", "", "tmp dir for runnermode")
	flag.BoolVar(&servermode, "servermode", false, "enable to disable tests and act as a 'server'")
	flag.BoolVar(&serverKeepDB, "keepdb", false, "have the server keep its database when it starts")
	flag.BoolVar(&serverEnableRunners, "enablerunners", false, "have the server spawn runners for jobs")
	ServerLogClientErrors = false
}

func serverShutDownTime(touchInterval time.Duration) time.Duration {
	// golang can't actually do exec.Command.Start() in parallel and has a
	// global lock on them, so we have to allow the 500ms of time to any pending
	// starts to resolve before we can shut down.
	return touchInterval + httpServerShutdownTime + serverShutdownRunnerTickerTime + 500*time.Millisecond
}

func TestServerTimingsWithDefaults(t *testing.T) {
	Convey("Non-positive server timing values use package defaults", t, func() {
		timings := ServerTimings{
			InterruptTime:         -1 * time.Nanosecond,
			ItemTTR:               -1 * time.Nanosecond,
			CheckRunnerTime:       -1 * time.Nanosecond,
			LostJobCheckTimeout:   -1 * time.Nanosecond,
			LostJobCheckRetryTime: -1 * time.Nanosecond,
			ReleaseDelayMin:       -1 * time.Nanosecond,
			TouchInterval:         -1 * time.Nanosecond,
			RetryWait:             -1 * time.Nanosecond,
			RetryTime:             -1 * time.Nanosecond,
			RecSecRound:           -1,
			RecMBRound:            -1,
			ShutdownSocketWait:    -1 * time.Nanosecond,
		}.withDefaults()

		So(timings.InterruptTime, ShouldEqual, ServerInterruptTime)
		So(timings.ItemTTR, ShouldEqual, ServerItemTTR)
		So(timings.CheckRunnerTime, ShouldEqual, ServerCheckRunnerTime)
		So(timings.LostJobCheckTimeout, ShouldEqual, ServerLostJobCheckTimeout)
		So(timings.LostJobCheckRetryTime, ShouldEqual, ServerLostJobCheckRetryTime)
		So(timings.ReleaseDelayMin, ShouldEqual, ClientReleaseDelayMin)
		So(timings.TouchInterval, ShouldEqual, ClientTouchInterval)
		So(timings.RetryWait, ShouldEqual, ClientRetryWait)
		So(timings.RetryTime, ShouldEqual, ClientRetryTime)
		So(timings.RecSecRound, ShouldEqual, RecSecRound)
		So(timings.RecMBRound, ShouldEqual, RecMBRound)
		So(timings.ShutdownSocketWait, ShouldEqual, serverSocketWait)
	})
}

func TestDBRecommendationRoundsWithDefaults(t *testing.T) {
	Convey("Non-positive database recommendation rounds use package defaults", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()

		testDB, _, err := initDB(
			ctx,
			filepath.Join(tmpdir, "queue.db"),
			filepath.Join(tmpdir, "queue.db.bak"),
			internal.Development,
			false,
			false,
		)
		So(err, ShouldBeNil)

		if err != nil {
			return
		}

		defer func() {
			So(testDB.close(ctx), ShouldBeNil)
		}()

		testDB.recMBRound = -1
		testDB.recSecRound = -1

		reqGroup := "negative_rounds"
		storeStat := func(bucket []byte, value int) {
			err = testDB.store(
				bucket,
				fmt.Sprintf("%s%s%20d", reqGroup, dbDelimiter, value),
				[]byte(strconv.Itoa(value)),
			)
			So(err, ShouldBeNil)
		}

		storeStat(bucketJobRAM, 42)
		storeStat(bucketJobDisk, 101)
		storeStat(bucketJobSecs, 7)

		rmem, err := testDB.recommendedReqGroupMemory(reqGroup)
		So(err, ShouldBeNil)
		So(rmem, ShouldEqual, RecMBRound)

		rdisk, err := testDB.recommendedReqGroupDisk(reqGroup)
		So(err, ShouldBeNil)

		expectedDisk := int(math.Ceil(float64(101)/float64(RecMBRound))) * RecMBRound
		So(rdisk, ShouldEqual, expectedDisk)

		rtime, err := testDB.recommendedReqGroupTime(reqGroup)
		So(err, ShouldBeNil)

		expectedTime := int(math.Ceil(float64(7)/float64(RecSecRound))) * RecSecRound
		So(rtime, ShouldEqual, expectedTime)
	})
}

func assertNonEmptyFile(path string) {
	info, err := os.Stat(path)
	So(err, ShouldBeNil)

	if err != nil {
		return
	}

	size := info.Size()
	So(size, ShouldBeGreaterThan, int64(0))
}

func boltLiveJobs(path string) (liveJobs int, err error) {
	boltdb, err := bolt.Open(path, dbFilePermission, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return 0, err
	}

	defer func() {
		if closeErr := boltdb.Close(); err == nil {
			err = closeErr
		}
	}()

	err = boltdb.View(func(tx *bolt.Tx) error {
		jobs := tx.Bucket(bucketJobsLive)
		if jobs == nil {
			return errMissingLiveJobsBucket
		}

		return jobs.ForEach(func(_, encoded []byte) error {
			if encoded != nil {
				liveJobs++
			}

			return nil
		})
	})

	return liveJobs, err
}

func assertBoltLiveJobs(path string, expected int) {
	liveJobs, err := boltLiveJobs(path)
	So(err, ShouldBeNil)

	if err != nil {
		return
	}

	So(liveJobs, ShouldEqual, expected)
}

func waitForBoltLiveJobs(path string, expected int, maxWait time.Duration) error {
	var (
		liveJobs int
		err      error
	)

	deadline := time.Now().Add(maxWait)

	for {
		liveJobs, err = boltLiveJobs(path)
		if err == nil && liveJobs == expected {
			return nil
		}

		if time.Now().After(deadline) {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	if err != nil {
		return fmt.Errorf("expected %d live jobs in %s: %w", expected, path, err)
	}

	return fmt.Errorf("%w: expected %d live jobs in %s, got %d", errUnexpectedLiveJobs, expected, path, liveJobs)
}

func waitForFileToDisappear(path string, maxWait time.Duration) error {
	var err error

	deadline := time.Now().Add(maxWait)

	for {
		_, err = os.Stat(path)
		if os.IsNotExist(err) {
			return nil
		}

		if time.Now().After(deadline) {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	if err != nil {
		return fmt.Errorf("expected %s to disappear: %w", path, err)
	}

	return fmt.Errorf("%w: expected %s to disappear", errFileStillExists, path)
}

const (
	liveJTouchActualCwd = "/tmp/wr/job1"
	liveJTouchTTR       = time.Second
	liveStatusCloudUser = "cloud_user"
	liveStatusHost      = "worker1"
	liveStatusHostIP    = "10.0.0.8"
)

type liveJTouchFixture struct {
	server *Server
	sock   *captureSocket
	job    *Job
	item   *queue.Item
	token  []byte
	key    string
	client uuid.UUID
}

func newLiveJTouchFixture(ctx context.Context, webPort string) *liveJTouchFixture {
	ch := new(codec.BincHandle)
	sock := &captureSocket{ch: ch}
	clientID, err := uuid.NewV4()
	So(err, ShouldBeNil)

	token := []byte(strings.Repeat("x", tokenLength))
	job := &Job{
		Cmd:          "echo live jtouch",
		Cwd:          testCwd,
		RepGroup:     "live-jtouch",
		Requirements: &jqs.Requirements{RAM: 1, Time: time.Minute, Cores: 1},
		ReservedBy:   clientID,
		State:        JobStateRunning,
		StartTime:    time.Now(),
	}
	key := job.Key()
	q := queue.New(ctx, "live-jtouch")
	item, err := q.Add(ctx, key, "", job, 0, 0, liveJTouchTTR, queue.SubQueueRun)
	So(err, ShouldBeNil)

	return &liveJTouchFixture{
		server: &Server{
			ch:         ch,
			sock:       sock,
			token:      token,
			q:          q,
			up:         true,
			ServerInfo: &ServerInfo{WebPort: webPort},
		},
		sock:   sock,
		job:    job,
		item:   item,
		token:  token,
		key:    key,
		client: clientID,
	}
}

func (fixture *liveJTouchFixture) touch(
	ctx context.Context,
	token []byte,
	endState *JobEndState,
) (*serverResponse, error) {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, fixture.server.ch)
	err := enc.Encode(&clientRequest{
		Method:      requestMethodTouch,
		Token:       token,
		Keys:        []string{fixture.key},
		ClientID:    fixture.client,
		JobEndState: endState,
	})
	So(err, ShouldBeNil)

	err = fixture.server.handleRequest(ctx, &mangos.Message{Body: encoded})

	return fixture.sock.response(), err
}

func (fixture *liveJTouchFixture) remainingTTRAfterDelay() time.Duration {
	time.Sleep(20 * time.Millisecond)

	return fixture.item.Stats().Remaining
}

func assertLiveJTouchExtendedTTR(before, after time.Duration) {
	So(after.Nanoseconds(), ShouldBeGreaterThan, before.Nanoseconds())
}

func assertLiveJTouchDidNotExtendTTR(before, after time.Duration) {
	So(after.Nanoseconds(), ShouldBeLessThanOrEqualTo, before.Nanoseconds())
}

func assertLiveJTouchFields(
	job *Job,
	actualCwd string,
	peakRAM int,
	peakDisk int64,
	cpuTime time.Duration,
	stdout string,
	stderr string,
) {
	job.RLock()
	So(job.State, ShouldEqual, JobStateRunning)
	So(job.Exited, ShouldBeFalse)
	So(job.ActualCwd, ShouldEqual, actualCwd)
	So(job.PeakRAM, ShouldEqual, peakRAM)
	So(job.PeakDisk, ShouldEqual, peakDisk)
	So(job.CPUtime, ShouldEqual, cpuTime)
	job.RUnlock()

	out, err := job.StdOut()
	So(err, ShouldBeNil)
	So(out, ShouldEqual, stdout)

	errOut, err := job.StdErr()
	So(err, ShouldBeNil)
	So(errOut, ShouldEqual, stderr)
}

func TestManagerLiveJTouch(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("An authenticated live jtouch stores a live snapshot behind the secure gate", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, "1234")
		before := fixture.remainingTTRAfterDelay()
		endState := &JobEndState{
			Cwd:      liveJTouchActualCwd,
			PeakRAM:  321,
			PeakDisk: 9,
			CPUtime:  4 * time.Second,
			Stdout:   compressStd([]byte("out\n")),
			Stderr:   compressStd([]byte("err\n")),
		}

		resp, err := fixture.touch(ctx, fixture.token, endState)
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchExtendedTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(fixture.job, liveJTouchActualCwd, 321, 9, 4*time.Second, "out\n", "err\n")
	})

	Convey("An older runner jtouch with no live fields only extends TTR", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, "1234")
		setLiveJTouchFields(fixture.job, "/tmp/old", 111, 7, 2*time.Second, "old\n", "olderr\n")
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, fixture.token, &JobEndState{})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchExtendedTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(fixture.job, "/tmp/old", 111, 7, 2*time.Second, "old\n", "olderr\n")
	})

	Convey("An authenticated live jtouch with no HTTPS web port only extends TTR", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, "")
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, fixture.token, &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 321,
			CPUtime: 4 * time.Second,
			Stdout:  compressStd([]byte("out\n")),
			Stderr:  compressStd([]byte("err\n")),
		})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchExtendedTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(fixture.job, "", 0, 0, 0, "", "")
	})

	Convey("A live jtouch with an invalid token is denied without touching TTR or live fields", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, "1234")
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, []byte(strings.Repeat("y", tokenLength)), &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 321,
			CPUtime: 4 * time.Second,
			Stdout:  compressStd([]byte("out\n")),
			Stderr:  compressStd([]byte("err\n")),
		})
		So(err, ShouldNotBeNil)
		So(resp.Err, ShouldEqual, ErrPermissionDenied)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchDidNotExtendTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(fixture.job, "", 0, 0, 0, "", "")
	})

	Convey("A live jtouch with no token is denied before a KillCalled response", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, "1234")
		setLiveJTouchFields(fixture.job, "/tmp/old", 111, 7, 2*time.Second, "old\n", "olderr\n")
		fixture.job.Lock()
		fixture.job.killCalled = true
		fixture.job.Unlock()
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, nil, &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 321,
			CPUtime: 4 * time.Second,
			Stdout:  compressStd([]byte("out\n")),
			Stderr:  compressStd([]byte("err\n")),
		})
		So(err, ShouldNotBeNil)
		So(resp.Err, ShouldEqual, ErrPermissionDenied)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchDidNotExtendTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(fixture.job, "/tmp/old", 111, 7, 2*time.Second, "old\n", "olderr\n")
	})

	Convey("An authenticated live jtouch preserves KillCalled jobs and existing live fields", t, func() {
		ctx := context.Background()
		fixture := newKillCalledLiveJTouchFixture(ctx)
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, fixture.token, &JobEndState{
			Cwd:      liveJTouchActualCwd,
			PeakRAM:  321,
			PeakDisk: 9,
			CPUtime:  4 * time.Second,
			Stdout:   compressStd([]byte("out\n")),
			Stderr:   compressStd([]byte("err\n")),
		})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeTrue)
		assertLiveJTouchDidNotExtendTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(fixture.job, "/tmp/old", 111, 7, 2*time.Second, "old\n", "olderr\n")
	})

	Convey("An authenticated older-runner jtouch preserves KillCalled jobs and existing live fields", t, func() {
		ctx := context.Background()
		fixture := newKillCalledLiveJTouchFixture(ctx)
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, fixture.token, &JobEndState{})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeTrue)
		assertLiveJTouchDidNotExtendTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(fixture.job, "/tmp/old", 111, 7, 2*time.Second, "old\n", "olderr\n")
	})
}

func newKillCalledLiveJTouchFixture(ctx context.Context) *liveJTouchFixture {
	fixture := newLiveJTouchFixture(ctx, "1234")
	setLiveJTouchFields(fixture.job, "/tmp/old", 111, 7, 2*time.Second, "old\n", "olderr\n")
	fixture.job.Lock()
	fixture.job.killCalled = true
	fixture.job.Unlock()

	return fixture
}

func setLiveJTouchFields(
	job *Job,
	actualCwd string,
	peakRAM int,
	peakDisk int64,
	cpuTime time.Duration,
	stdout string,
	stderr string,
) {
	job.Lock()
	job.ActualCwd = actualCwd
	job.PeakRAM = peakRAM
	job.PeakDisk = peakDisk
	job.CPUtime = cpuTime
	job.StdOutC = compressStd([]byte(stdout))
	job.StdErrC = compressStd([]byte(stderr))
	job.Unlock()
}

func configureFastTestBackups(db *db) {
	db.Lock()
	defer db.Unlock()

	db.slowBackups = true
	db.backupWait = 0
}

func captureTimingGlobals() func() {
	interruptTime := ServerInterruptTime
	reserveTicker := ServerReserveTicker
	releaseDelayMin := ClientReleaseDelayMin
	itemTTR := ServerItemTTR

	return func() {
		ServerInterruptTime = interruptTime
		ServerReserveTicker = reserveTicker
		ClientReleaseDelayMin = releaseDelayMin
		ServerItemTTR = itemTTR
	}
}

func TestJobqueueUtils(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("CurrentIP() works", t, func() {
		ip, err := internal.CurrentIP("")
		So(err, ShouldBeNil)
		So(ip, ShouldNotBeBlank)
		ip, err = internal.CurrentIP("9.9.9.9/24")
		So(err, ShouldBeNil)
		So(ip, ShouldBeBlank)
		ip, err = internal.CurrentIP(ip + "/16")
		So(err, ShouldBeNil)
		So(ip, ShouldEqual, ip)
	})

	Convey("generateToken() and tokenMatches() work", t, func() {
		tokenFile, err := os.CreateTemp("", "wr.test.token")
		So(err, ShouldBeNil)
		tokenFile.Close()
		tokenPath := tokenFile.Name()
		defer func() {
			err = os.Remove(tokenPath)
			So(err, ShouldBeNil)
		}()

		err = os.Remove(tokenPath)
		So(err, ShouldBeNil)
		token, err := generateToken(tokenPath)
		So(err, ShouldBeNil)
		So(len(token), ShouldEqual, tokenLength)

		token2, err := generateToken(tokenPath)
		So(err, ShouldBeNil)
		So(len(token2), ShouldEqual, tokenLength)
		So(token, ShouldNotResemble, token2)
		So(tokenMatches(token, token2), ShouldBeFalse)
		So(tokenMatches(token, token), ShouldBeTrue)

		// if tokenPath is a file that contains a token, generateToken doesn't
		// generate a new token, but returns that one
		err = os.WriteFile(tokenPath, token2, 0o600)
		So(err, ShouldBeNil)

		token3, err := generateToken(tokenPath)
		So(err, ShouldBeNil)
		So(len(token3), ShouldEqual, tokenLength)
		So(token3, ShouldResemble, token2)
		So(tokenMatches(token2, token3), ShouldBeTrue)
	})

	Convey("GenerateCerts creates certificate files", t, func() {
		certtmpdir := t.TempDir()

		caFile := filepath.Join(certtmpdir, "ca.pem")
		certFile := filepath.Join(certtmpdir, "cert.pem")
		keyFile := filepath.Join(certtmpdir, "key.pem")
		certDomain := "localhost"
		err := internal.GenerateCerts(caFile, certFile, keyFile, certDomain, internal.DefaultBitsForRootRSAKey,
			internal.DefualtBitsForServerRSAKey, crand.Reader,
			internal.DefaultCertFileFlags)
		So(err, ShouldBeNil)
		_, err = os.Stat(caFile)
		So(err, ShouldBeNil)
		_, err = os.Stat(certFile)
		So(err, ShouldBeNil)
		_, err = os.Stat(keyFile)
		So(err, ShouldBeNil)

		Convey("CertExpiry shows they expire in a year", func() {
			expiry, err := internal.CertExpiry(caFile)
			So(err, ShouldBeNil)
			So(expiry, ShouldHappenBetween, time.Now().Add(364*24*time.Hour), time.Now().Add(366*24*time.Hour))
		})
	})

	Convey("currentDisk works recursively with ignores", t, func() {
		dir, err := os.MkdirTemp("", "wr_currentDisk_test")
		So(err, ShouldBeNil)
		defer os.RemoveAll(dir)

		subdir := filepath.Join(dir, ".mnt", "sub")
		err = os.MkdirAll(subdir, fs.ModePerm)
		So(err, ShouldBeNil)

		createLargeFile := func(path string, size int64) error {
			f, errc := os.Create(path)
			if errc != nil {
				return errc
			}
			errc = f.Truncate(size)
			if errc != nil {
				return errc
			}

			return f.Close()
		}

		err = createLargeFile(filepath.Join(dir, "a.txt"), 1024*1024)
		So(err, ShouldBeNil)
		err = createLargeFile(filepath.Join(subdir, "b.txt"), 1024*1024)
		So(err, ShouldBeNil)

		s, err := currentDisk(dir)
		So(err, ShouldBeNil)
		So(s, ShouldEqual, 2)

		s, err = currentDisk(dir, map[string]bool{subdir: true})
		So(err, ShouldBeNil)
		So(s, ShouldEqual, 1)
	})

	Convey("calculateItemDelay works", t, func() {
		relDelayMin := ClientReleaseDelayMin

		d := calculateItemDelay(0, relDelayMin)
		So(d, ShouldBeGreaterThanOrEqualTo, 30*time.Second)
		So(d, ShouldBeLessThan, 60*time.Second)

		d = calculateItemDelay(1, relDelayMin)
		So(d, ShouldBeGreaterThanOrEqualTo, 60*time.Second)
		So(d, ShouldBeLessThan, 90*time.Second)

		d = calculateItemDelay(2, relDelayMin)
		So(d, ShouldBeGreaterThanOrEqualTo, 120*time.Second)
		So(d, ShouldBeLessThan, 150*time.Second)

		d = calculateItemDelay(6, relDelayMin)
		So(d, ShouldBeGreaterThanOrEqualTo, 1800*time.Second)
		So(d, ShouldBeLessThan, 1830*time.Second)

		d = calculateItemDelay(7, relDelayMin)
		So(d, ShouldBeGreaterThanOrEqualTo, 1800*time.Second)
		So(d, ShouldBeLessThan, 1830*time.Second)

		d = calculateItemDelay(999999999, relDelayMin)
		So(d, ShouldBeGreaterThanOrEqualTo, 1800*time.Second)
		So(d, ShouldBeLessThan, 1830*time.Second)

		d = calculateItemDelay(-999999999, relDelayMin)
		So(d, ShouldBeGreaterThanOrEqualTo, 30*time.Second)
		So(d, ShouldBeLessThan, 60*time.Second)
	})

	Convey("normalizeRepGroupMatch defaults and preserves explicit match", t, func() {
		So(normalizeRepGroupMatch("", false), ShouldEqual, RepGroupMatchExact)
		So(normalizeRepGroupMatch("", true), ShouldEqual, RepGroupMatchSubStr)
		So(normalizeRepGroupMatch(RepGroupMatchPrefix, false), ShouldEqual, RepGroupMatchPrefix)
		So(normalizeRepGroupMatch(RepGroupMatchSuffix, true), ShouldEqual, RepGroupMatchSuffix)
		So(normalizeRepGroupMatch(RepGroupMatch("typo"), false), ShouldEqual, RepGroupMatchExact)
		So(normalizeRepGroupMatch(RepGroupMatch("typo"), true), ShouldEqual, RepGroupMatchSubStr)
	})

	Convey("RepGroupMatches applies exact, substring, prefix and suffix modes", t, func() {
		const value = "alpha-beta-gamma"

		So(RepGroupMatches(value, "alpha-beta-gamma", RepGroupMatchExact), ShouldBeTrue)
		So(RepGroupMatches(value, "alpha", RepGroupMatchPrefix), ShouldBeTrue)
		So(RepGroupMatches(value, "gamma", RepGroupMatchSuffix), ShouldBeTrue)
		So(RepGroupMatches(value, "beta", RepGroupMatchSubStr), ShouldBeTrue)

		So(RepGroupMatches(value, "beta", RepGroupMatchPrefix), ShouldBeFalse)
		So(RepGroupMatches(value, "alpha", RepGroupMatchSuffix), ShouldBeFalse)
		So(RepGroupMatches(value, "delta", RepGroupMatchSubStr), ShouldBeFalse)
	})
}

func TestSubscriptionStateChangeEvents(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Browser websocket and Go subscription both receive completion updates", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "subscription-f1-shared"
		ids, err := jq.AddAndReturnIDs([]*Job{{
			Cmd:          "echo subscription f1 shared",
			Cwd:          testCwd,
			ReqGroup:     repGroup,
			Requirements: standardReqs,
			RepGroup:     repGroup,
		}}, envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		testServer := httptest.NewServer(webInterfaceStatusWS(ctx, server))
		defer testServer.Close()

		wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http")
		header := http.Header{}
		header.Add("Authorization", "Bearer "+string(token))

		ws, err := drainWebSocket(wsURL, header)
		So(err, ShouldBeNil)

		defer func() {
			So(ws.Close(), ShouldBeNil)
		}()

		err = ws.WriteJSON(jstatusReq{
			Request:  jstatusRequestDetails,
			RepGroup: repGroup,
			State:    JobStateReady,
		})
		So(err, ShouldBeNil)

		limitedDrain(ws, 1)

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(jq.Started(job, os.Getpid()), ShouldBeNil)
		So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		var webStatus *JStatus

		for range 4 {
			status, errr := readUntilStatus(ws)
			if errr != nil {
				break
			}

			if status.State == JobStateComplete {
				webStatus = status

				break
			}
		}

		So(webStatus, ShouldNotBeNil)
		So(webStatus.Key, ShouldEqual, ids[0])
		So(webStatus.RepGroup, ShouldEqual, repGroup)
		So(webStatus.IsPushUpdate, ShouldBeTrue)

		var goUpdate *JobUpdate

		select {
		case update := <-sub.Updates():
			goUpdate = update

			So(update.Kind, ShouldEqual, JobUpdateTerminal)
			So(update.State, ShouldEqual, JobStateComplete)
			So(update.Key, ShouldEqual, ids[0])
			So(update.RepGroup, ShouldEqual, repGroup)
		case <-time.After(time.Second):
			So("timed out waiting for Go subscription update", ShouldBeBlank)
		}

		So(webStatus.Started, ShouldNotBeNil)
		So(webStatus.Ended, ShouldNotBeNil)
		So(goUpdate, ShouldNotBeNil)

		if webStatus.Started == nil || webStatus.Ended == nil || goUpdate == nil {
			return
		}

		So(goUpdate.Started, ShouldNotBeNil)
		So(goUpdate.Ended, ShouldNotBeNil)

		if goUpdate.Started == nil || goUpdate.Ended == nil {
			return
		}

		unixMilliseconds := time.Now().Add(-time.Hour).UnixNano() / int64(time.Millisecond)
		So(*goUpdate.Started, ShouldBeGreaterThan, unixMilliseconds)
		So(*goUpdate.Ended, ShouldBeGreaterThan, unixMilliseconds)
		So(*webStatus.Started, ShouldBeLessThan, unixMilliseconds)
		So(*webStatus.Ended, ShouldBeLessThan, unixMilliseconds)

		startedSeconds := *goUpdate.Started / int64(time.Second)
		endedSeconds := *goUpdate.Ended / int64(time.Second)

		So(*webStatus.Started, ShouldBeBetweenOrEqual, startedSeconds-1, startedSeconds+1)
		So(*webStatus.Ended, ShouldBeBetweenOrEqual, endedSeconds-1, endedSeconds+1)
	})

	Convey("SetChangedCallback emits browser and Go push updates from one per-job status loop", t, func() {
		guard, err := changedCallbackStatusGuardForFile("server.go")
		So(err, ShouldBeNil)

		So(guard.callbackCount, ShouldEqual, 1)
		So(guard.nonBuiltinDataArgumentCalls, ShouldEqual, 0)
		So(guard.pushStatusLoopCount, ShouldEqual, 1)
		So(guard.subscriptionOnlyDataLoops, ShouldEqual, 0)
		So(guard.pushStatusLoop.statusFromToStatusAssignments, ShouldEqual, 1)
		So(guard.pushStatusLoop.toStatusCalls, ShouldEqual, 1)
		So(guard.pushStatusLoop.jobUpdateFromStatusUsesStatus, ShouldBeTrue)
		So(guard.pushStatusLoop.subscriptionUpdateCalls, ShouldBeGreaterThan, 0)
		So(guard.pushStatusLoop.writesStatus, ShouldBeFalse)
	})
}

const (
	changedCallbackCreateQueue      = "createQueue"
	changedCallbackDataIdent        = "data"
	changedCallbackJobUpdateMaker   = "jobUpdateFromStatus"
	changedCallbackMethodName       = "SetChangedCallback"
	changedCallbackStatusIdent      = "status"
	changedCallbackSubscriptionName = "SubscriptionUpdate"
	changedCallbackToStatus         = "ToStatus"
	changedCallbackWriteJSON        = "WriteJSON"
)

var errChangedCallbackCreateQueueNotFound = errors.New("createQueue function not found")

type changedCallbackStatusGuard struct {
	callbackCount               int
	nonBuiltinDataArgumentCalls int
	pushStatusLoop              changedCallbackDataLoopGuard
	pushStatusLoopCount         int
	subscriptionOnlyDataLoops   int
}

type changedCallbackDataLoopGuard struct {
	jobUpdateFromStatusUsesStatus bool
	statusFromToStatusAssignments int
	subscriptionUpdateCalls       int
	toStatusCalls                 int
	writesStatus                  bool
}

func changedCallbackStatusGuardForFile(path string) (changedCallbackStatusGuard, error) {
	fileSet := token.NewFileSet()

	parsed, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		return changedCallbackStatusGuard{}, err
	}

	createQueue := findFuncDecl(parsed, changedCallbackCreateQueue)
	if createQueue == nil {
		return changedCallbackStatusGuard{}, errChangedCallbackCreateQueueNotFound
	}

	guard := changedCallbackStatusGuard{}

	ast.Inspect(createQueue.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isCallNamed(call, changedCallbackMethodName) {
			return true
		}

		guard.callbackCount++

		callback, ok := firstFuncLiteralArg(call)
		if ok {
			inspectChangedCallbackBody(callback.Body, &guard)
		}

		return false
	})

	return guard, nil
}

func inspectChangedCallbackBody(body *ast.BlockStmt, guard *changedCallbackStatusGuard) {
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && callReceivesDirectIdent(call, changedCallbackDataIdent) && !isCallNamed(call, "len") {
			guard.nonBuiltinDataArgumentCalls++
		}

		loop, ok := node.(*ast.RangeStmt)
		if !ok || !isIdent(loop.X, changedCallbackDataIdent) {
			return true
		}

		loopGuard := changedCallbackDataLoopGuardFor(loop)
		if loopGuard.toStatusCalls > 0 {
			guard.pushStatusLoopCount++
			guard.pushStatusLoop = loopGuard
		}

		if loopGuard.subscriptionUpdateCalls > 0 && loopGuard.toStatusCalls == 0 {
			guard.subscriptionOnlyDataLoops++
		}

		return false
	})
}

func changedCallbackDataLoopGuardFor(loop *ast.RangeStmt) changedCallbackDataLoopGuard {
	guard := changedCallbackDataLoopGuard{}

	ast.Inspect(loop.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			if assignsStatusFromToStatus(node) {
				guard.statusFromToStatusAssignments++
			}
		case *ast.CallExpr:
			guard.recordCall(node)
		}

		return true
	})

	return guard
}

func (g *changedCallbackDataLoopGuard) recordCall(call *ast.CallExpr) {
	if isCallNamed(call, changedCallbackToStatus) {
		g.toStatusCalls++
	}

	if isSubscriptionUpdateCall(call) {
		g.subscriptionUpdateCalls++
	}

	if callUsesStatusInJobUpdateFromStatus(call) {
		g.jobUpdateFromStatusUsesStatus = true
	}

	if callWritesStatus(call) {
		g.writesStatus = true
	}
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if function, ok := decl.(*ast.FuncDecl); ok && function.Name.Name == name {
			return function
		}
	}

	return nil
}

func firstFuncLiteralArg(call *ast.CallExpr) (*ast.FuncLit, bool) {
	if len(call.Args) == 0 {
		return nil, false
	}

	literal, ok := call.Args[0].(*ast.FuncLit)

	return literal, ok
}

func assignsStatusFromToStatus(assign *ast.AssignStmt) bool {
	if !assignLHSContainsIdent(assign, changedCallbackStatusIdent) {
		return false
	}

	for _, expr := range assign.Rhs {
		if call, ok := expr.(*ast.CallExpr); ok && isCallNamed(call, changedCallbackToStatus) {
			return true
		}
	}

	return false
}

func assignLHSContainsIdent(assign *ast.AssignStmt, name string) bool {
	for _, expr := range assign.Lhs {
		if isIdent(expr, name) {
			return true
		}
	}

	return false
}

func callReceivesDirectIdent(call *ast.CallExpr, name string) bool {
	for _, arg := range call.Args {
		if isIdent(arg, name) {
			return true
		}
	}

	return false
}

func isSubscriptionUpdateCall(call *ast.CallExpr) bool {
	return strings.Contains(callName(call), changedCallbackSubscriptionName)
}

func callUsesStatusInJobUpdateFromStatus(call *ast.CallExpr) bool {
	if !isCallNamed(call, changedCallbackJobUpdateMaker) || len(call.Args) == 0 {
		return false
	}

	return isIdent(call.Args[0], changedCallbackStatusIdent)
}

func callWritesStatus(call *ast.CallExpr) bool {
	if !isCallNamed(call, changedCallbackWriteJSON) || len(call.Args) != 1 {
		return false
	}

	return isIdent(call.Args[0], changedCallbackStatusIdent)
}

func isCallNamed(call *ast.CallExpr, name string) bool {
	return callName(call) == name
}

func callName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	default:
		return ""
	}
}

func isIdent(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)

	return ok && ident.Name == name
}

// isolateTestConfig rewrites the manager port, web port and directory (and the
// file paths derived from the directory) in config to test-private values: two
// free ports and a fresh temp dir. This lets each test use its own server
// without colliding on the fixed dev manager port or ~/.wr_development, which
// is what allows the tests to run concurrently. The temp dir is left for the
// Makefile (or the OS) to clean up.
func isolateTestConfig(config *internal.Config) {
	port, err := freeTestPort()
	if err != nil {
		log.Fatal(err)
	}

	webPort, err := freeTestPort()
	if err != nil {
		log.Fatal(err)
	}

	dir, err := os.MkdirTemp("", "wrtest")
	if err != nil {
		log.Fatal(err)
	}

	managerDir := filepath.Join(dir, ".wr_development")
	if err := os.MkdirAll(managerDir, 0o700); err != nil {
		log.Fatal(err)
	}

	config.ManagerPort = strconv.Itoa(port)
	config.ManagerWeb = strconv.Itoa(webPort)
	config.ManagerDir = managerDir
	config.ManagerDBFile = filepath.Join(managerDir, "db")
	config.ManagerDBBkFile = filepath.Join(managerDir, "db_bk")
	config.ManagerTokenFile = filepath.Join(managerDir, "client.token")
	config.ManagerCAFile = filepath.Join(managerDir, "ca.pem")
	config.ManagerCertFile = filepath.Join(managerDir, "cert.pem")
	config.ManagerKeyFile = filepath.Join(managerDir, "key.pem")
}

// exportConfigEnv exports the given config's manager port/web/dir as the
// WR_MANAGER* environment variables, so a config-reloading client
// (ConnectUsingConfig) in this same process finds this server. WR_MANAGERDIR
// is set without its deployment suffix, the form the config reader expects.
// This mutates process-wide state via os.Setenv, so only a test that runs
// serially (no t.Parallel) may call it.
func exportConfigEnv(config *internal.Config) {
	os.Setenv("WR_MANAGERPORT", config.ManagerPort)                                          //nolint:usetesting
	os.Setenv("WR_MANAGERWEB", config.ManagerWeb)                                            //nolint:usetesting
	os.Setenv("WR_MANAGERDIR", strings.TrimSuffix(config.ManagerDir, "_"+config.Deployment)) //nolint:usetesting
}

func jobqueueTestInit(shortTTR bool) (internal.Config, ServerConfig, string, *jqs.Requirements, time.Duration) {
	ctx := context.Background()
	// load our config to know where our development manager port is supposed to
	// be; we'll use that to test jobqueue
	config := internal.ConfigLoadFromParentDir(ctx, "development")

	// give each test its own free ports and manager directory, so tests don't
	// share the fixed dev port and ~/.wr_development and can run concurrently.
	// Subprocess children (--servermode/--runnermode) instead inherit these
	// from the parent via env vars that ConfigLoadFromParentDir reads, so we
	// leave their (already isolated) config alone.
	if !servermode && !runnermode {
		isolateTestConfig(config)
	}

	managerDBBkFile := config.ManagerDBFile + "_bk" // not config.ManagerDBBkFile in case it is an s3 url
	serverConfig := ServerConfig{
		Port:            config.ManagerPort,
		WebPort:         config.ManagerWeb,
		SchedulerName:   localSchedulerName,
		SchedulerConfig: &jqs.ConfigLocal{Shell: config.RunnerExecShell},
		DBFile:          config.ManagerDBFile,
		DBFileBackup:    managerDBBkFile,
		TokenFile:       config.ManagerTokenFile,
		CAFile:          config.ManagerCAFile,
		CertFile:        config.ManagerCertFile,
		CertDomain:      config.ManagerCertDomain,
		KeyFile:         config.ManagerKeyFile,
		Deployment:      config.Deployment,
	}
	addr := "localhost:" + config.ManagerPort

	// ensure the manager dir exists so Serve() can write its token and TLS
	// certs there. Normally this is ~/.wr_development (already present), but
	// when tests are isolated onto a per-process WR_MANAGERDIR (used to run
	// groups of these tests in parallel on separate ports) the dir won't
	// pre-exist, and Serve() does not create it itself.
	if err := os.MkdirAll(config.ManagerDir, 0o700); err != nil {
		log.Fatal(err)
	}

	// pre-generate the TLS certs (as Serve() would) if they don't already
	// exist. Some tests Connect() before starting a server (to check the
	// "no server" error), which needs the CA cert to be present; with the
	// normal ~/.wr_development dir it persists between runs, but a fresh
	// isolated WR_MANAGERDIR has none until a server first runs.
	if internal.CheckCerts(serverConfig.CertFile, serverConfig.KeyFile) != nil {
		if err := internal.GenerateCerts(serverConfig.CAFile, serverConfig.CertFile, serverConfig.KeyFile,
			config.ManagerCertDomain, internal.DefaultBitsForRootRSAKey, internal.DefualtBitsForServerRSAKey,
			crand.Reader, internal.DefaultCertFileFlags); err != nil {
			log.Fatal(err)
		}
	}

	setDomainIP(config.ManagerCertDomain)

	// configure faster timings for the server we're about to test (see
	// ServerConfig.Timings); these are per-server so independent test servers
	// don't clobber each other's settings.
	serverConfig.Timings.InterruptTime = 10 * time.Millisecond
	serverConfig.Timings.ReleaseDelayMin = 100 * time.Millisecond
	serverConfig.Timings.ShutdownSocketWait = 1 * time.Millisecond
	clientConnectTime := 1500 * time.Millisecond
	// NB: RetryWait is left at its 15s default here. Only the crash/shutdown
	// recovery tests (which wait it out before reconnecting) shorten it, via
	// their own serverConfig.Timings.RetryWait; doing it globally would make
	// runner clients in unrelated tests retry aggressively under load.

	if shortTTR {
		serverConfig.Timings.ItemTTR = 1 * time.Second
		serverConfig.Timings.TouchInterval = 500 * time.Millisecond
	}

	standardReqs := &jqs.Requirements{RAM: 10, Time: 10 * time.Second, Cores: 1, Disk: 0, Other: make(map[string]string)}

	return *config, serverConfig, addr, standardReqs, clientConnectTime
}

// compiledSelf returns a test binary to run in --servermode or --runnermode,
// caching the result process-wide. Normally that's just the running test binary
// (runnerBinary), but under -race it's a freshly compiled plain binary; see
// runnerBinary's build-tagged variants for why.
//
//nolint:gochecknoglobals // the compile result is intentionally cached process-wide
var compiledSelf = sync.OnceValues(runnerBinary)

// copyCompiledSelf compiles this test binary once (shared via compiledSelf) and
// copies it to dst, returning dst. The runner tests count the files left in
// their runner tmpdir and expect the runner executable to be one of them, so
// each test gets its own copy of the shared binary (a cheap file copy) rather
// than paying to recompile from scratch.
func copyCompiledSelf(dst string) (string, error) {
	src, err := compiledSelf()
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(dst, data, 0o700); err != nil { //nolint:gosec
		return "", err
	}

	return dst, nil
}

// startServer runs the given exe with the --servermode arg. It is assumed that
// doing so starts a jobqueue server in another process that will kill itself
// after some time or when signalled. We return a client that is connected to
// that server, along with the client token and the server's pid. If keepDB is
// true, the exe will be run with --keepdb arg as well. Same idea with
// enableRunners. This also creates config.ManagerDir dir on disk if necessary,
// and does not delete it afterwards.
func startServer(
	serverExe string, keepDB, enableRunners bool, config internal.Config, addr string,
) (*Client, []byte, *exec.Cmd, error) {
	err := os.MkdirAll(config.ManagerDir, 0o700)
	if err != nil {
		log.Fatal(err)
	}

	preStart := time.Now()

	args := []string{"--servermode"}
	if keepDB {
		args = append(args, "--keepdb")
	}

	if enableRunners {
		args = append(args, "--enablerunners")
	}

	// run the server in the background, telling it (and the runners it spawns,
	// which inherit this env) which isolated port and manager dir to use. The
	// dir is passed without its deployment suffix, the form the config reader
	// expects from WR_MANAGERDIR.
	cmd := exec.CommandContext(context.Background(), serverExe, args...)

	cmd.Env = append(os.Environ(),
		"WR_MANAGERPORT="+config.ManagerPort,
		"WR_MANAGERWEB="+config.ManagerWeb,
		"WR_MANAGERDIR="+strings.TrimSuffix(config.ManagerDir, "_"+config.Deployment),
	)

	err = cmd.Start()
	if err != nil {
		log.Fatal(err)
	}

	// wait a while for our server cmd to start serving and write its token
	mTimeout := 30 * time.Second

	token, err := readManagerToken(config.ManagerTokenFile, preStart, mTimeout)
	if err != nil || len(token) == 0 {
		return nil, nil, cmd, err
	}

	jq, err := connectWithRetry(addr, config, token, mTimeout)

	return jq, token, cmd, err
}

// runServer starts a jobqueue server, and is what calling this test script in
// --servermode runs.
func runServer(ctx context.Context) {
	// uncomment and set a log path to debug server issues in TestJobqueueSignal
	// fh, err := log15.FileHandler("/log", log15.LogfmtFormat())
	// if err != nil {
	// 	log.Fatalf("error opening file: %v", err)
	// }
	// h := l15h.CallerInfoHandler(fh)
	// testLogger.SetHandler(log15.LvlFilterHandler(log15.LvlDebug, h))
	// pid := os.Getpid()
	// testLogger = testLogger.New("pid", pid)
	_, serverConfig, _, _, _ := jobqueueTestInit(false) //nolint:dogsled

	if serverKeepDB {
		serverConfig.dontWipeDevDB = true
	}

	if serverEnableRunners {
		self, err := os.Executable()
		if err != nil {
			clog.Crit(ctx, "os.Executable() failed", "err", err)
			os.Exit(1)
		}

		// we can't use the --tmpdir option, since that means the runner cmds
		// won't match between invocations, so recovery won't be complete. We
		// don't need it anyway
		serverConfig.RunnerCmd = self +
			" --runnermode --schedgrp '%s' --rdeployment %s --rserver '%s' --rdomain %s" +
			" --rtimeout %d --maxmins %d"
	}

	serverConfig.Timings.ItemTTR = 200 * time.Millisecond
	serverConfig.Timings.TouchInterval = 50 * time.Millisecond
	// these signal-handling tests crash and restart the server; a short retry
	// wait makes the runner reconnect to the new server promptly.
	serverConfig.Timings.RetryWait = 1 * time.Second

	server, msg, _, err := serve(ctx, serverConfig)
	if err != nil {
		clog.Crit(ctx, "test daemon failed to start", "err", err)
		os.Exit(1)
	}

	if msg != "" {
		clog.Warn(ctx, msg)
	}

	// we'll Block() later, but just in case the parent tests bomb out
	// without killing us, we'll stop after 20s
	go func() {
		<-time.After(20 * time.Second)
		clog.Warn(ctx, "test daemon stopping after 20s")
		server.Stop(ctx, true)
	}()

	clog.Warn(ctx, "test daemon up, will block")

	// wait until we are killed
	err = server.Block()
	clog.Warn(ctx, "test daemon exiting", "reason", err)
	os.Exit(0)
}

// serve calls Serve() but with a retry for 5s on failure. This allows time for
// a server that we recently stopped in a prior test to really not be listening
// on the ports any more.
func serve(ctx context.Context, config ServerConfig) (*Server, string, []byte, error) {
	server, msg, token, err := Serve(ctx, config)
	if err != nil {
		limit := time.After(5 * time.Second)
		ticker := time.NewTicker(500 * time.Millisecond)

	RETRY:
		for {
			select {
			case <-ticker.C:
				server, msg, token, err = Serve(ctx, config)
				if err != nil {
					continue
				}

				ticker.Stop()

				break RETRY
			case <-limit:
				ticker.Stop()

				break RETRY
			}
		}
	}

	return server, msg, token, err
}

// waitUntilPidsAreGone waits up to the given number of seconds for the pids in
// the map to not exist. The pids are deleted from the map when they no longer
// exist. Returns true if all pids gone (and the map will be empty).
func waitUntilPidsAreGone(pids map[int]bool, seconds int) bool {
	for i := 0; i < seconds; i++ {
		for pid := range pids {
			process, errf := os.FindProcess(pid)
			if errf != nil && process == nil {
				delete(pids, pid)
			}

			errs := process.Signal(syscall.Signal(0))
			if errs != nil {
				delete(pids, pid)
			}
		}

		if len(pids) == 0 {
			break
		}

		<-time.After(1 * time.Second)
	}

	return len(pids) == 0
}

func TestJobqueueSignal(t *testing.T) {
	ctx := context.Background()

	if runnermode {
		return
	}

	if servermode {
		runServer(ctx)

		return
	}

	config, _, addr, _, clientConnectTime := jobqueueTestInit(false)

	// these tests need the server running in it's own pid so we can test signal
	// handling in the client. Our server will be ourself in --servermode, so
	// first we'll compile ourselves (shared with the other tests that need it)
	serverExe, err := compiledSelf()
	if err != nil {
		log.Fatal(err)
	}

	errr := os.Remove(config.ManagerTokenFile)
	if errr != nil && !os.IsNotExist(errr) {
		t.Fatalf("failed to delete token file before test: %s\n", errr)
	}
	defer func() {
		errr := os.Remove(config.ManagerTokenFile)
		if errr != nil && !os.IsNotExist(errr) {
			t.Fatalf("failed to delete token file after test: %s\n", errr)
		}
	}()

	alreadyKilled := make(map[int]bool)
	killServer := func(jq *Client, serverPid int, serverCmd *exec.Cmd) {
		if alreadyKilled[serverPid] {
			return
		}

		errd := jq.Disconnect()
		if errd != nil && !isClosedSocketError(errd) {
			t.Logf("failed to disconnect: %s", errd)
		}

		waited := make(chan bool)

		go func() {
			errw := serverCmd.Wait()
			if errw != nil {
				t.Logf("failed to reap server pid: %s", errw)
			}

			waited <- true
		}()

		<-time.After(500 * time.Millisecond)

		errk := syscall.Kill(serverPid, syscall.SIGTERM)
		if errk != nil {
			t.Logf("failed to send SIGTERM to server: %s", errk)
		}

		<-waited

		alreadyKilled[serverPid] = true
	}

	Convey("Once a jobqueue server is up as a daemon", t, func() {
		if skipInShard("a") {
			return
		}

		jq, token, serverCmd, errf := startServer(serverExe, false, false, config, addr)
		serverPid := serverCmd.Process.Pid

		So(errf, ShouldBeNil)

		defer killServer(jq, serverPid, serverCmd)

		So(jq.ServerInfo.PID, ShouldEqual, serverPid)

		Convey("You can set up a long-running job for execution", func() {
			cmd := "perl -e 'for (1..3) { sleep(1) }'"
			cmd2 := "perl -e 'for (2..4) { sleep(1) }'"

			jobs := []*Job{
				{
					Cmd:          cmd,
					Cwd:          testCwd,
					ReqGroup:     reqGroupFake,
					Requirements: &jqs.Requirements{RAM: 10, Time: 4 * time.Second, Cores: 1},
					Retries:      uint8(0),
					RepGroup:     "3secs_pass",
				},
				{
					Cmd:          cmd2,
					Cwd:          testCwd,
					ReqGroup:     reqGroupFake,
					Requirements: &jqs.Requirements{RAM: 10, Time: time.Second, Cores: 1},
					Retries:      uint8(0),
					RepGroup:     "3secs_fail",
				},
			}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			job, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job.Cmd, ShouldEqual, cmd)
			So(job.State, ShouldEqual, JobStateReserved)

			job2, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job2.Cmd, ShouldEqual, cmd2)
			So(job2.State, ShouldEqual, JobStateReserved)

			Convey("Signals are handled during execution, and we can see when jobs take too long", func() {
				// Send the process-wide SIGTERM that the running job is meant to
				// catch, but make the sender cancellable and stop it once the
				// jobs have finished. Otherwise, if this leaf's jobs return (or
				// the timer is delayed) before the signal fires, the stray
				// SIGTERM leaks into a later test's Execute (which also registers
				// a process-wide signal handler) and fails it spuriously.
				sigDone := make(chan struct{})

				var sigDoneOnce sync.Once

				cancelSig := func() {
					sigDoneOnce.Do(func() {
						close(sigDone)
					})
				}

				go func() {
					select {
					case <-time.After(2 * time.Second):
						if errk := syscall.Kill(os.Getpid(), syscall.SIGTERM); errk != nil {
							log.Printf("failed to send SIGTERM: %s\n", errk)
						}
					case <-sigDone:
					}
				}()

				defer cancelSig()

				j1worked := make(chan bool, 1)

				go func() {
					err := jq.Execute(ctx, job, config.RunnerExecShell)
					if err != nil {
						var jqerr Error

						gotSignalFailure := errors.As(err, &jqerr) && jqerr.Err == FailReasonSignal &&
							job.State == JobStateBuried && job.Exited && job.Exitcode == -1 &&
							job.FailReason == FailReasonSignal
						if gotSignalFailure {
							j1worked <- true

							return
						}
					}

					j1worked <- false
				}()

				j2worked := make(chan bool, 1)

				go func() {
					err := jq.Execute(ctx, job2, config.RunnerExecShell)
					if err != nil {
						var jqerr Error

						gotTimeFailure := errors.As(err, &jqerr) && jqerr.Err == FailReasonTime &&
							job2.State == JobStateBuried && job2.Exited && job2.Exitcode == -1 &&
							job2.FailReason == FailReasonTime
						if gotTimeFailure {
							j2worked <- true

							return
						}
					}

					j2worked <- false
				}()

				So(<-j1worked, ShouldBeTrue)
				So(<-j2worked, ShouldBeTrue)

				// the signal has now been delivered to and consumed by the jobs
				// above; stop the sender so it can never fire into a later test.
				cancelSig()

				jq2, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				defer disconnect(jq2)

				job, err = jq2.GetByEssence(&JobEssence{Cmd: cmd}, false, false)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Cmd, ShouldEqual, cmd)
				So(job.State, ShouldEqual, JobStateBuried)
				So(job.FailReason, ShouldEqual, FailReasonSignal)

				job2, err = jq2.GetByEssence(&JobEssence{Cmd: cmd2}, false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.Cmd, ShouldEqual, cmd2)
				So(job2.State, ShouldEqual, JobStateBuried)
				So(job2.FailReason, ShouldEqual, FailReasonTime)
				So(job2.Requirements.Time.Seconds(), ShouldEqual, 1)

				// requirements only change on becoming ready
				kicked, err := jq.Kick([]*JobEssence{job2.ToEssense()})
				So(err, ShouldBeNil)
				So(kicked, ShouldEqual, 1)

				job2, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.Cmd, ShouldEqual, cmd2)
				So(job2.State, ShouldEqual, JobStateReserved)
				So(job2.FailReason, ShouldEqual, FailReasonTime)
				So(job2.Requirements.Time.Seconds(), ShouldBeBetweenOrEqual, 3601, 3604)

				// all signals handled the same way, so no need for further
				// tests
			})
		})

		Convey("Running jobs are recovered after a hard server crash", func() {
			cmd := "sleep 10"
			// We want to kill this part way, but sleep processes do not seem to
			// die straight away when killed.
			cmd2 := "perl -e 'for (1..10) { sleep(1) }'"

			jobs := []*Job{
				{
					Cmd:          cmd,
					Cwd:          testCwd,
					ReqGroup:     reqGroupFake,
					Requirements: &jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: 0},
					Retries:      uint8(0),
					RepGroup:     "recover",
				},
				{
					Cmd:          cmd2,
					Cwd:          testCwd,
					ReqGroup:     reqGroupFake,
					Requirements: &jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: 0},
					Retries:      uint8(0),
					RepGroup:     "buried",
				},
			}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			job, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job.Cmd, ShouldEqual, cmd)
			So(job.State, ShouldEqual, JobStateReserved)

			job2, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job2.Cmd, ShouldEqual, cmd2)
			So(job2.State, ShouldEqual, JobStateReserved)

			serverCmdCh := make(chan *exec.Cmd)
			killRecoveredJob := func(gotJob *Job) {
				<-time.After(2 * time.Second)

				errk := syscall.Kill(gotJob.Pid, syscall.SIGKILL)
				if errk != nil {
					t.Logf("failed to send SIGKILL to job: %s", errk)
				}
			}

			go func() {
				<-time.After(2 * time.Second)

				gotJob, errg := jq.GetByEssence(&JobEssence{Cmd: cmd2}, false, false)
				killServer(jq, serverPid, serverCmd)

				if errg != nil || gotJob == nil {
					log.Printf("failed to get job: %s\n", errg)
				} else {
					killRecoveredJob(gotJob)
				}
				<-time.After(4 * time.Second)
				newJQ, _, newServerCmd, errf := startServer(serverExe, true, false, config, addr)
				if errf != nil {
					log.Printf("failed to start new server: %s\n", errf)
				} else if newJQ != nil {
					errd := newJQ.Disconnect()
					if errd != nil {
						t.Logf("failed to disconnect after making a new server: %s", errd)
					}
				}
				serverCmdCh <- newServerCmd
			}()

			j1worked := make(chan bool, 1)
			giveUp1 := time.After(30 * time.Second)
			go func() {
				errch := make(chan error, 1)
				go func() {
					errch <- jq.Execute(ctx, job, config.RunnerExecShell)
				}()
				select {
				case erre := <-errch:
					if erre != nil {
						// we expect that we lost the connection when we killed
						// the server, then reconnected to the new server and
						// therefore got ErrStopReserving, but otherwise
						// everything was fine
						var jqerr Error
						if !errors.As(erre, &jqerr) || jqerr.Err != ErrStopReserving {
							fmt.Printf("\nexecute had err: %s\n", erre)
							j1worked <- false
							return
						}
					} // though sometimes we manage to not lose the connection
					j1worked <- true
					return
				case <-giveUp1:
					fmt.Printf("\ngave up waiting for job to finish\n")
					j1worked <- false
				}
			}()

			j2worked := make(chan bool, 1)
			giveUp2 := time.After(30 * time.Second)
			go func() {
				errch := make(chan error, 1)
				go func() {
					errch <- jq.Execute(ctx, job2, config.RunnerExecShell)
				}()
				select {
				case erre := <-errch:
					expectedSignalErr := fmt.Sprintf(
						"terminated by signal %s (shell exit code %d)",
						syscall.SIGKILL,
						shellSignalExitCodeOffset+int(syscall.SIGKILL),
					)
					if erre != nil && strings.Contains(erre.Error(), expectedSignalErr) {
						j2worked <- true
						return
					}

					t.Logf("job2 had err %v, expected %q", erre, expectedSignalErr)
					j2worked <- false
					return
				case <-giveUp2:
					j2worked <- false
				}
			}()

			serverCmd = <-serverCmdCh
			serverPid = serverCmd.Process.Pid
			So(<-j1worked, ShouldBeTrue)
			So(<-j2worked, ShouldBeTrue)

			jq2, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer func() {
				errd := jq2.Disconnect()
				if errd != nil {
					fmt.Printf("failed to disconnect: %s\n", errd)
				}
			}()
			job, err = jq2.GetByEssence(&JobEssence{Cmd: cmd}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.Cmd, ShouldEqual, cmd)
			So(job.State, ShouldEqual, JobStateComplete)

			job2, err = jq2.GetByEssence(&JobEssence{Cmd: cmd2}, false, false)
			So(err, ShouldBeNil)
			So(job2, ShouldNotBeNil)
			So(job2.Cmd, ShouldEqual, cmd2)
			So(job2.State, ShouldEqual, JobStateBuried)
		})

		Reset(func() {
			killServer(jq, serverPid, serverCmd)
		})
	})

	// the next tests will have runners enabled so that we can see what happens
	// when we force kill both the server and a runner
	Convey("Once a jobqueue server using local scheduler is up as a daemon", t, func() {
		if skipInShard("b") {
			return
		}

		jq, token, serverCmd, errf := startServer(serverExe, false, true, config, addr)
		serverPid := serverCmd.Process.Pid
		So(errf, ShouldBeNil)
		defer killServer(jq, serverPid, serverCmd)

		So(jq.ServerInfo.PID, ShouldEqual, serverPid)

		Convey("Killed runners after a hard server crash come up lost, and new runners don't overcommit resources due to existing runners", func() {
			// Use jobs that block until released (via marker files in a temp
			// dir) instead of fixed-duration commands, so the test never depends
			// on real wall-clock timing: under heavy parallel-test load (e.g. CI's
			// 1-2 cpus) the kill sequence below can be delayed past when a
			// fixed-duration "lost" job would finish - it would then be recorded
			// "complete" before we kill its runner. Blocking jobs stay running
			// until the test decides, so the outcome is deterministic.
			markerDir, errMarker := os.MkdirTemp("", "wr_signal_marker")
			So(errMarker, ShouldBeNil)

			defer os.RemoveAll(markerDir)

			recoverStarted := filepath.Join(markerDir, "recover_started")
			recoverRelease := filepath.Join(markerDir, "recover_release")
			lostStarted := filepath.Join(markerDir, "lost_started")

			// the "recover" job holds all the CPUs and runs until the test
			// releases it (creating recover_release); the "lost" job runs until
			// cleanup removes markerDir. Both touch a marker when they start.
			cmd := fmt.Sprintf(
				"touch %s && while [ -d %s ] && [ ! -e %s ]; do sleep 0.1; done",
				recoverStarted, markerDir, recoverRelease)
			cmd2 := fmt.Sprintf("touch %s && while [ -d %s ]; do sleep 0.1; done", lostStarted, markerDir)
			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 1, Time: 10 * time.Second, Cores: float64(runtime.NumCPU())}, Retries: uint8(0), RepGroup: "recover"})
			jobs = append(jobs, &Job{Cmd: cmd2, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 1, Time: 10 * time.Second, Cores: 0}, Retries: uint8(0), RepGroup: "lost"})
			inserts, already, erra := jq.Add(jobs, envVars, true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			// wait until both jobs are actually running (their start markers
			// appear) instead of sleeping a fixed time, then get the pids of the
			// runners the server spawned
			So(waitUntilFileExists(recoverStarted, 60), ShouldBeTrue)
			So(waitUntilFileExists(lostStarted, 60), ShouldBeTrue)
			processes, err := process.Processes()
			So(err, ShouldBeNil)
			runnerPids := make(map[int]bool)
			var runnerPidToKill int
			for _, p := range processes {
				thisCmd, errc := p.Cmdline()
				if errc != nil {
					continue
				}

				if !strings.Contains(thisCmd, markerDir) {
					continue
				}

				parent, errp := p.Parent()
				if errp != nil {
					continue
				}

				parentCmd, errp := parent.Cmdline()
				if errp != nil {
					continue
				}

				if !strings.Contains(parentCmd, serverExe) {
					continue
				}

				pid := int(parent.Pid)
				if strings.Contains(thisCmd, lostStarted) {
					runnerPidToKill = pid
				}

				runnerPids[pid] = true
				if len(runnerPids) == 2 {
					break
				}
			}

			So(len(runnerPids), ShouldEqual, 2)
			So(runnerPidToKill, ShouldNotEqual, 0)
			So(runnerPidToKill, ShouldNotEqual, serverPid)

			// kill the server and then the lost job's runner, then wait for the old
			// server process to be gone before starting a new one. We don't use
			// killServer here because we don't want to jq.Disconnect and reap before
			// killing the runner.
			errk := syscall.Kill(serverPid, syscall.SIGKILL)
			if errk != nil {
				fmt.Printf("failed to send SIGKILL to server: %s\n", errk)
			}
			errk = syscall.Kill(runnerPidToKill, syscall.SIGKILL)
			if errk != nil {
				fmt.Printf("failed to send SIGKILL to runner: %s\n", errk)
			}
			errd := jq.Disconnect()
			if errd != nil && !isClosedSocketError(errd) {
				fmt.Printf("failed to disconnect: %s\n", errd)
			}
			errw := serverCmd.Wait()
			if errw != nil && !strings.Contains(errw.Error(), "signal: killed") {
				fmt.Printf("failed to reap server pid: %s\n", errw)
			}
			alreadyKilled[serverPid] = true

			// the killed runner's job (the lost one) is still running but can no longer
			// report; wait for the old server and that runner to be gone, then start a
			// fresh server reusing the same db.
			So(waitUntilPidsAreGone(map[int]bool{serverPid: true, runnerPidToKill: true}, 30), ShouldBeTrue)
			var errf error
			jq, _, serverCmd, errf = startServer(serverExe, true, true, config, addr)
			serverPid = serverCmd.Process.Pid
			So(errf, ShouldBeNil)
			So(jq, ShouldNotBeNil)

			// add a new job which should wait until the recover job completes, since the
			// recover job uses all the CPUs - proving new runners don't overcommit
			// because of the existing (surviving) runner.
			cmd3 := "echo 1"
			jobs = []*Job{{Cmd: cmd3, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 1, Time: 10 * time.Second, Cores: 1}, Retries: uint8(0), RepGroup: "wait"}}
			inserts, already, err = jq.Add(jobs, envVars, true)
			errd = jq.Disconnect()
			if errd != nil {
				fmt.Printf("failed to disconnect: %s\n", errd)
			}
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			jq2, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer func() {
				errd := jq2.Disconnect()
				if errd != nil {
					fmt.Printf("failed to disconnect: %s\n", errd)
				}
			}()

			// the lost job's runner was killed mid-run, so once the restarted server
			// notices it must come up lost (poll, since detection isn't instant)
			job2 := waitUntilJobState(jq2, &JobEssence{Cmd: cmd2}, JobStateLost, 60)
			So(job2, ShouldNotBeNil)
			So(job2.Cmd, ShouldEqual, cmd2)
			So(job2.State, ShouldEqual, JobStateLost)

			// the recover job's runner survived the crash; release the job and it should
			// complete, proving a surviving runner finishes its job across a restart.
			So(os.WriteFile(recoverRelease, nil, 0600), ShouldBeNil)

			job := waitUntilJobState(jq2, &JobEssence{Cmd: cmd}, JobStateComplete, 60)
			So(job, ShouldNotBeNil)
			So(job.Cmd, ShouldEqual, cmd)
			So(job.State, ShouldEqual, JobStateComplete)

			// only now that the recover job freed the CPUs can the waiting job run; it
			// must have started on or after the recover job ended.
			job3 := waitUntilJobState(jq2, &JobEssence{Cmd: cmd3}, JobStateComplete, 60)
			So(job3, ShouldNotBeNil)
			So(job3.Cmd, ShouldEqual, cmd3)
			So(job3.State, ShouldEqual, JobStateComplete)
			So(job3.StartTime, ShouldHappenOnOrAfter, job.EndTime)

			// for subsequent tests to work, we need to wait for the server to
			// really be gone (generous timeout for heavy parallel-test load;
			// polls and returns as soon as it's gone)
			killServer(jq, serverPid, serverCmd)
			So(waitUntilPidsAreGone(map[int]bool{serverPid: true}, 30), ShouldBeTrue)
		})

		Reset(func() {
			killServer(jq, serverPid, serverCmd)
		})
	})
}

func TestJobqueueBasics(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	// this test exercises ConnectUsingConfig, which reloads the config from the
	// environment, so export our isolated config there. Safe because this test
	// does not run in parallel with others.
	exportConfigEnv(&config)

	var server *Server
	var token []byte
	var errs error
	Convey("Without the jobserver being up, clients can't connect and time out", t, func() {
		os.Remove(config.ManagerTokenFile)

		_, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldNotBeNil)

		var jqerr Error

		ok := errors.As(err, &jqerr)
		So(ok, ShouldBeTrue)
		So(jqerr.Err, ShouldEqual, ErrNoServer)

		jq, err := ConnectUsingConfig(ctx, "development", clientConnectTime)
		So(jq, ShouldBeNil)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "could not read token file")
	})

	Convey("Once the jobqueue server is up", t, func() {
		server, _, token, errs = serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		server.rc = serverRC // ReserveScheduled() only works if we have an rc

		Convey("You can connect to the server using config", func() {
			jq, err := ConnectUsingConfig(ctx, "development", clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)
		})

		Convey("You can connect to the server and add jobs and get back their IDs", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			var jobs []*Job
			req := &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}
			jobs = append(jobs, &Job{Cmd: "echo 1", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: req, Retries: uint8(0), RepGroup: "test"})
			jobs = append(jobs, &Job{Cmd: "echo 2", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: req, Retries: uint8(0), RepGroup: "test"})
			ids, err := jq.AddAndReturnIDs(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(len(ids), ShouldEqual, 2)
			So(ids[0], ShouldEqual, "9a456dee1e351f82e3d562769c27d803")
			So(ids[1], ShouldEqual, "2bb7055e49e21ea85066899a5ba38d8e")
		})

		pickTestPriority := func(i int) uint8 {
			switch i {
			case 7:
				return uint8(4)
			case 4:
				return uint8(7)
			default:
				return uint8(i) //nolint:gosec
			}
		}

		Convey("You can connect to the server and add jobs to the queue", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			So(jq.ServerInfo.Port, ShouldEqual, serverConfig.Port)
			So(jq.ServerInfo.PID, ShouldBeGreaterThan, 0)
			So(jq.ServerInfo.Deployment, ShouldEqual, "development")

			var jobs []*Job

			for i := range 10 {
				pri := pickTestPriority(i)
				jobs = append(jobs, &Job{
					Cmd: fmt.Sprintf("test cmd %d", i),
					Cwd: "/fake/cwd", ReqGroup: "fake_group",
					Requirements: &jqs.Requirements{RAM: 1024, Time: 4 * time.Hour, Cores: 1},
					Priority:     pri, Retries: uint8(3), RepGroup: "manually_added"},
				)
			}

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 10)
			So(already, ShouldEqual, 0)

			Convey("You can't add the same jobs to the queue again", func() {
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 0)
				So(already, ShouldEqual, 10)
			})

			Convey("You can get back jobs you've just added", func() {
				job, err := jq.GetByEssence(&JobEssence{Cmd: "test cmd 3"}, false, false)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Cmd, ShouldEqual, "test cmd 3")
				So(job.State, ShouldEqual, JobStateReady)

				job, err = jq.GetByEssence(&JobEssence{Cmd: "test cmd x"}, false, false)
				So(err, ShouldBeNil)
				So(job, ShouldBeNil)

				var jes []*JobEssence
				for i := 0; i < 10; i++ {
					jes = append(jes, &JobEssence{Cmd: fmt.Sprintf("test cmd %d", i)})
				}
				jobs, err = jq.GetByEssences(jes)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 10)
				for i, job := range jobs {
					So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", i))
					So(job.State, ShouldEqual, JobStateReady)
				}

				jobs, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 10)

				jobs, err = jq.GetByRepGroup("foo", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 0)
			})

			Convey("You can store their (fake) runtime stats and get recommendations", func() {
				So(RecSecRound, ShouldBeGreaterThan, 0)

				// these are ignored by the learning system unless the job
				// failed due to running out of a resource
				for index, job := range jobs {
					job.PeakRAM = index + 1
					job.PeakDisk = int64(index + 2)
					job.StartTime = time.Now()
					job.EndTime = job.StartTime.Add(time.Duration(index+1) * time.Second)
					server.db.updateJobAfterExit(ctx, job, []byte{}, []byte{}, false)
				}
				<-time.After(200 * time.Millisecond)
				rmem, err := server.db.recommendedReqGroupMemory("fake_group")
				So(err, ShouldBeNil)
				So(rmem, ShouldEqual, 0)
				rdisk, err := server.db.recommendedReqGroupDisk("fake_group")
				So(err, ShouldBeNil)
				So(rdisk, ShouldEqual, 0)
				rtime, err := server.db.recommendedReqGroupTime("fake_group")
				So(err, ShouldBeNil)
				So(rtime, ShouldEqual, 0)

				for index, job := range jobs {
					job.PeakRAM = index + 1
					job.PeakDisk = int64(index + 2)
					job.StartTime = time.Now()
					job.EndTime = job.StartTime.Add(time.Duration(index+1) * time.Second)

					switch index {
					case 1, 2, 3:
						job.FailReason = FailReasonRAM
					case 4, 5, 6:
						job.FailReason = FailReasonDisk
					case 7, 8, 9:
						job.FailReason = FailReasonTime
					}

					server.db.updateJobAfterExit(ctx, job, []byte{}, []byte{}, false)
				}
				// the recommendations are recalculated asynchronously after the
				// stats are stored, and each per-resource value settles
				// independently, so poll for all of them to reach their expected
				// values rather than assuming a fixed delay (or that memory
				// settling implies disk and time have too).
				expectedShort := int(math.Ceil(float64(10)/float64(RecSecRound))) * RecSecRound

				So(pollUntil(func() bool {
					m, e1 := server.db.recommendedReqGroupMemory("fake_group")
					d, e2 := server.db.recommendedReqGroupDisk("fake_group")
					tm, e3 := server.db.recommendedReqGroupTime("fake_group")

					return e1 == nil && e2 == nil && e3 == nil && m == 100 && d == 100 && tm == expectedShort
				}), ShouldBeTrue)
				rmem, err = server.db.recommendedReqGroupMemory("fake_group")
				So(err, ShouldBeNil)
				So(rmem, ShouldEqual, 100)
				rdisk, err = server.db.recommendedReqGroupDisk("fake_group")
				So(err, ShouldBeNil)
				So(rdisk, ShouldEqual, 100)
				rtime, err = server.db.recommendedReqGroupTime("fake_group")
				So(err, ShouldBeNil)
				So(rtime, ShouldEqual, expectedShort)

				for i := 11; i <= 100; i++ {
					job := &Job{Cmd: fmt.Sprintf("test cmd %d", i), Cwd: "/fake/cwd", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 1024, Time: 4 * time.Hour, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"}
					job.PeakRAM = i * 100
					job.PeakDisk = int64(i * 200)
					job.StartTime = time.Now()
					job.EndTime = job.StartTime.Add(time.Duration(i*100) * time.Second)

					switch {
					case i < 40:
						job.FailReason = FailReasonRAM
					case i < 70:
						job.FailReason = FailReasonDisk
					default:
						job.FailReason = FailReasonTime
					}

					server.db.updateJobAfterExit(ctx, job, []byte{}, []byte{}, false)
				}
				// as above, wait for every per-resource recalculation to settle
				// on its expected value, not just memory.
				So(pollUntil(func() bool {
					m, e1 := server.db.recommendedReqGroupMemory("fake_group")
					d, e2 := server.db.recommendedReqGroupDisk("fake_group")
					tm, e3 := server.db.recommendedReqGroupTime("fake_group")

					return e1 == nil && e2 == nil && e3 == nil &&
						m == 3400 && d == 12800 && tm >= 9500 && tm%RecSecRound == 0
				}), ShouldBeTrue)
				rmem, err = server.db.recommendedReqGroupMemory("fake_group")
				So(err, ShouldBeNil)
				So(rmem, ShouldEqual, 3400)
				rdisk, err = server.db.recommendedReqGroupDisk("fake_group")
				So(err, ShouldBeNil)
				So(rdisk, ShouldEqual, 12800)
				rtime, err = server.db.recommendedReqGroupTime("fake_group")
				So(err, ShouldBeNil)
				So(rtime, ShouldBeGreaterThanOrEqualTo, 9500)
				So(rtime%RecSecRound, ShouldEqual, 0)
			})

			Convey("You can reserve jobs from the queue in the correct order", func() {
				for i := 9; i >= 0; i-- {
					jid := pickTestPriority(i)
					job, err := jq.ReserveScheduled(50*time.Millisecond, "1024:240:1:0")
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", jid))
					So(job.EnvC, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateReserved)
				}

				Convey("Reserving when all have been reserved returns nil", func() {
					job, err := jq.ReserveScheduled(50*time.Millisecond, "1024:240:1:0")
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					Convey("Adding one while waiting on a Reserve will return the new job", func() {
						worked := make(chan bool)
						go func() {
							job, err := jq.ReserveScheduled(1000*time.Millisecond, "1024:300:1:0")
							if err != nil {
								worked <- false
								return
							}
							if job == nil {
								worked <- false
								return
							}
							if job.Cmd == "new" {
								worked <- true
								return
							}
							worked <- false
						}()

						ok := make(chan bool)
						go func() {
							ticker := time.NewTicker(100 * time.Millisecond)
							ticks := 0
							for {
								select {
								case <-ticker.C:
									ticks++
									if ticks == 2 {
										jobs = append(jobs, &Job{Cmd: "new", Cwd: "/fake/cwd", ReqGroup: "add_group", Requirements: &jqs.Requirements{RAM: 1024, Time: 5 * time.Hour, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
										gojq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
										if errc != nil {
											fmt.Printf("Connect failed: %s\n", errc)
										}
										defer disconnect(gojq)
										_, _, erra := gojq.Add(jobs, envVars, true)
										if errc != nil {
											fmt.Printf("Add failed: %s\n", erra)
										}
									}
									continue
								case w := <-worked:
									ticker.Stop()
									if w && ticks <= 8 {
										ok <- true
									}
									ok <- false
									return
								}
							}
						}()

						<-time.After(2 * time.Second)
						So(<-ok, ShouldBeTrue)
					})
				})
			})

			if runtime.NumCPU() >= 2 {
				Convey("You can subsequently add more jobs", func() {
					for i := 10; i < 20; i++ {
						jobs = append(jobs, &Job{Cmd: fmt.Sprintf("test cmd %d", i), Cwd: "/fake/cwd", ReqGroup: "new_group", Requirements: &jqs.Requirements{RAM: 2048, Time: 1 * time.Hour, Cores: 2}, Retries: uint8(3), RepGroup: "manually_added"})
					}
					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 10)
					So(already, ShouldEqual, 10)

					Convey("You can reserve jobs for a particular scheduler group", func() {
						for i := 10; i < 20; i++ {
							job, err := jq.ReserveScheduled(20*time.Millisecond, "2048:60:2:0")
							So(err, ShouldBeNil)
							So(job, ShouldNotBeNil)
							So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", i))
						}
						job, err := jq.ReserveScheduled(10*time.Millisecond, "2048:60:2:0")
						So(err, ShouldBeNil)
						So(job, ShouldBeNil)

						for i := 9; i >= 0; i-- {
							jid := pickTestPriority(i)
							job, err = jq.ReserveScheduled(10*time.Millisecond, "1024:240:1:0")
							So(err, ShouldBeNil)
							So(job, ShouldNotBeNil)
							So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", jid))
						}
						job, err = jq.ReserveScheduled(10*time.Millisecond, "1024:240:1:0")
						So(err, ShouldBeNil)
						So(job, ShouldBeNil)
					})
				})
			}

			Convey("You can add more jobs, but without storing environment variables", func() {
				server.racmutex.Lock()
				server.rc = ""
				server.racmutex.Unlock()
				os.Setenv("wr_jobqueue_test_no_envvar", "a")

				inserts, already, err := jq.Add([]*Job{{
					Cmd:          "echo $wr_jobqueue_test_no_envvar && false",
					Cwd:          "/tmp",
					ReqGroup:     "new_group",
					Requirements: standardReqs,
					Priority:     uint8(100),
					RepGroup:     "noenvvar",
				}}, nil, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.RepGroup, ShouldEqual, "noenvvar")

				env, err := job.Env()
				So(err, ShouldBeNil)
				So(env, ShouldNotBeEmpty)

				os.Setenv("wr_jobqueue_test_no_envvar", "b")
				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldNotBeNil)
				So(job.FailReason, ShouldEqual, FailReasonExit)
				stdout, err := job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "b")

				// make sure the stdout is actually stored in the database
				retrieved, err := jq.GetByEssence(job.ToEssense(), true, false)
				So(err, ShouldBeNil)
				stdout, err = retrieved.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "b")

				// by comparison, compare normal behaviour, where the initial
				// value of the envvar gets used for the job
				os.Setenv("wr_jobqueue_test_no_envvar", "a")
				inserts, already, err = jq.Add([]*Job{{Cmd: "echo $wr_jobqueue_test_no_envvar && false && false", Cwd: "/tmp", ReqGroup: "new_group", Requirements: standardReqs, Priority: uint8(101), RepGroup: "withenvvar"}}, os.Environ(), true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.RepGroup, ShouldEqual, "withenvvar")

				env, err = job.Env()
				So(err, ShouldBeNil)
				So(env, ShouldNotBeEmpty)

				os.Setenv("wr_jobqueue_test_no_envvar", "b")
				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldNotBeNil)
				So(job.FailReason, ShouldEqual, FailReasonExit)
				stdout, err = job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "a")
			})

			Convey("You can add more jobs, overriding certain environment variables", func() {
				server.racmutex.Lock()
				server.rc = ""
				server.racmutex.Unlock()
				os.Setenv("wr_jobqueue_test_no_envvar", "a")
				compressed, err := jq.CompressEnv([]string{"wr_jobqueue_test_no_envvar=c", "wr_jobqueue_test_no_envvar2=d"})
				So(err, ShouldBeNil)
				inserts, already, err := jq.Add([]*Job{{
					Cmd:          "echo $wr_jobqueue_test_no_envvar && echo $wr_jobqueue_test_no_envvar2 && false",
					Cwd:          "/tmp",
					RepGroup:     "noenvvar",
					ReqGroup:     "new_group",
					Requirements: standardReqs,
					Priority:     uint8(100),
					Retries:      uint8(0),
					EnvOverride:  compressed,
				}}, []string{}, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.RepGroup, ShouldEqual, "noenvvar")

				env, err := job.Env()
				So(err, ShouldBeNil)
				So(env, ShouldNotBeEmpty)

				os.Setenv("wr_jobqueue_test_no_envvar", "b")
				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldNotBeNil)
				So(job.FailReason, ShouldEqual, FailReasonExit)
				stdout, err := job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "c\nd")
			})

			Convey("You can execute a job as a different group", func() {
				server.racmutex.Lock()
				server.rc = ""
				server.racmutex.Unlock()

				groups, err := os.Getgroups()
				So(err, ShouldBeNil)
				So(len(groups), ShouldBeGreaterThan, 1)

				second, err := user.LookupGroupId(strconv.Itoa(groups[1]))
				So(err, ShouldBeNil)

				inserts, already, err := jq.Add([]*Job{
					{Cmd: "id", Cwd: t.TempDir(), Requirements: standardReqs, RepGroup: "manually_added"},
					{Cmd: "id ", Group: second.Name, Cwd: t.TempDir(), Requirements: standardReqs, RepGroup: "manually_added"},
				}, []string{}, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 2)
				So(already, ShouldEqual, 0)

				job, err := jq.Reserve(0)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)

				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldBeNil)

				stdoutA, err := job.StdOut()
				So(err, ShouldBeNil)
				So(stdoutA, ShouldNotBeEmpty)

				job, err = jq.Reserve(0)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)

				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldBeNil)

				stdoutB, err := job.StdOut()
				So(err, ShouldBeNil)
				So(stdoutB, ShouldNotBeEmpty)

				So(stdoutA, ShouldNotEqual, stdoutB)
			})

			Convey("You can stop the server by sending it a SIGTERM or SIGINT", func() {
				err := jq.Disconnect()
				So(err, ShouldBeNil)

				errk := syscall.Kill(os.Getpid(), syscall.SIGTERM)
				if errk != nil {
					t.Logf("failed to send SIGTERM: %s", errk)
				}

				<-time.After(serverShutDownTime(serverConfig.Timings.TouchInterval))

				_, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldNotBeNil)

				var jqerr Error

				ok := errors.As(err, &jqerr)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrNoServer)

				server, _, token, errs = serve(ctx, serverConfig)
				So(errs, ShouldBeNil)

				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)
				err = jq.Disconnect()
				So(err, ShouldBeNil)

				errk = syscall.Kill(os.Getpid(), syscall.SIGINT)
				if errk != nil {
					t.Logf("failed to send SIGINT: %s", errk)
				}

				<-time.After(serverShutDownTime(serverConfig.Timings.TouchInterval))

				_, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldNotBeNil)
				ok = errors.As(err, &jqerr)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrNoServer)
			})

			Convey("You get a nice error if you send the server junk", func() {
				_, err := jq.request(&clientRequest{Method: "junk"})
				So(err, ShouldNotBeNil)

				var jqerr Error

				ok := errors.As(err, &jqerr)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrUnknownCommand)
				disconnect(jq)
			})
		})

		Reset(func() {
			server.Stop(ctx, true)
		})
	})

	if server != nil {
		server.Stop(ctx, true)
	}
}

func TestRerunDependentJobWaitsOnIncompleteDependencies(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("A rerun of a completed dependent job waits on an incomplete rerun dependency", t, func() {
		const (
			issue326A = "issue326-a"
			issue326B = "issue326-b"
		)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		jobA := &Job{
			Cmd:          "echo issue326 a",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     issue326A,
			DepGroups:    []string{issue326A},
		}
		jobB := &Job{
			Cmd:          "echo issue326 b",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     issue326B,
			DepGroups:    []string{issue326A, issue326B},
		}
		jobC := &Job{
			Cmd:          "echo issue326 c",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     "issue326-c",
			Dependencies: Dependencies{
				NewDepGroupDependency(issue326A),
				NewDepGroupDependency(issue326B),
			},
		}
		explicitJobC := &Job{
			Cmd:          jobC.Cmd,
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     "issue326-c-explicit",
			Priority:     10,
			Dependencies: Dependencies{
				NewDepGroupDependency(issue326A),
				NewDepGroupDependency(issue326B),
			},
		}

		added, existed, err := jq.Add([]*Job{jobA}, envVars, false)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(existed, ShouldEqual, 0)

		added, existed, err = jq.Add([]*Job{jobB}, envVars, false)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(existed, ShouldEqual, 0)

		added, existed, err = jq.Add([]*Job{jobC}, envVars, false)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(existed, ShouldEqual, 0)

		firstRunA, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(firstRunA.Key(), ShouldEqual, jobA.Key())
		So(jq.Execute(ctx, firstRunA, config.RunnerExecShell), ShouldBeNil)

		firstRunB, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(firstRunB.Key(), ShouldEqual, jobB.Key())
		So(jq.Execute(ctx, firstRunB, config.RunnerExecShell), ShouldBeNil)

		firstRunC, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(firstRunC.Key(), ShouldEqual, jobC.Key())
		So(jq.Execute(ctx, firstRunC, config.RunnerExecShell), ShouldBeNil)

		added, existed, err = jq.Add([]*Job{jobA}, envVars, false)
		So(err, ShouldBeNil)
		So(added, ShouldBeGreaterThanOrEqualTo, 1)
		So(existed, ShouldEqual, 0)

		added, existed, err = jq.Add([]*Job{jobB}, envVars, false)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(existed, ShouldEqual, 0)

		added, existed, err = jq.Add([]*Job{explicitJobC}, envVars, false)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(existed, ShouldEqual, 0)

		gottenJobs, err := jq.GetByRepGroup(explicitJobC.RepGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(gottenJobs, ShouldHaveLength, 1)
		So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

		nextJob, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(nextJob.Key(), ShouldEqual, jobA.Key())
		So(jq.Execute(ctx, nextJob, config.RunnerExecShell), ShouldBeNil)

		nextJob, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(nextJob.Key(), ShouldEqual, jobB.Key())
		So(jq.Execute(ctx, nextJob, config.RunnerExecShell), ShouldBeNil)

		nextJob, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(nextJob.Key(), ShouldEqual, explicitJobC.Key())
		So(nextJob.RepGroup, ShouldEqual, explicitJobC.RepGroup)
	})
}

func TestSeenCompletedDepGroupsDoNotBlock(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("A dep group that completed earlier does not block later dependents", t, func() {
		serverConfig.Timings.ItemTTR = 2 * time.Second
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer func() {
			if jq != nil {
				disconnect(jq)
			}
		}()

		carrier := &Job{
			Cmd:          "echo a2 done carrier",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(0),
			RepGroup:     "a2-done-carrier",
			DepGroups:    []string{"done"},
		}

		inserts, already, err := jq.Add([]*Job{carrier}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, carrier.Key())
		So(jq.Execute(ctx, reserved, config.RunnerExecShell), ShouldBeNil)

		dependent := &Job{
			Cmd:          "echo a2 done dependent",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(0),
			RepGroup:     "a2-done-dependent",
			Dependencies: Dependencies{NewDepGroupDependency("done")},
		}

		inserts, already, err = jq.Add([]*Job{dependent}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		got, err := jq.GetByRepGroup("a2-done-dependent", false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].State, ShouldEqual, JobStateReady)
		So(got[0].WaitingForDepGroups, ShouldBeNil)

		disconnect(jq)
		jq = nil

		server.Stop(ctx, true)

		serverConfig.dontWipeDevDB = true
		server, _, token, err = serve(ctx, serverConfig)
		serverConfig.dontWipeDevDB = false

		So(err, ShouldBeNil)

		jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		afterRestart := &Job{
			Cmd:          "echo a2 done dependent after restart",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(0),
			RepGroup:     "a2-done-dependent-restart",
			Dependencies: Dependencies{NewDepGroupDependency("done")},
		}

		inserts, already, err = jq.Add([]*Job{afterRestart}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		got, err = jq.GetByRepGroup("a2-done-dependent-restart", false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].State, ShouldEqual, JobStateReady)
		So(got[0].WaitingForDepGroups, ShouldBeNil)
	})
}

func TestNeverSeenDepGroupsWait(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	start := func() (internal.Config, *Server, *Client, *jqs.Requirements) {
		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
		serverConfig.Timings.ItemTTR = 2 * time.Second
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		return config, server, jq, standardReqs
	}

	dependentJob := func(repGroup string, reqs *jqs.Requirements) *Job {
		return &Job{
			Cmd:          "echo " + repGroup,
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: reqs,
			Retries:      uint8(0),
			RepGroup:     repGroup,
			Dependencies: Dependencies{NewDepGroupDependency(futureDepGroup)},
		}
	}

	carrierJob := func(repGroup string, reqs *jqs.Requirements) *Job {
		return &Job{
			Cmd:          "echo " + repGroup,
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: reqs,
			Retries:      uint8(0),
			RepGroup:     repGroup,
			DepGroups:    []string{futureDepGroup},
		}
	}

	Convey("A never-seen dep-group dependency starts dependent and unreservable", t, func() {
		_, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		dependent := dependentJob("a1-future-dependent", standardReqs)

		inserts, already, err := jq.Add([]*Job{dependent}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		got, err := jq.GetByRepGroup(dependent.RepGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].State, ShouldEqual, JobStateDependent)
		So(got[0].WaitingForDepGroups, ShouldResemble, []string{futureDepGroup})

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldBeNil)
	})

	Convey("A live carrier replaces the never-seen wait without releasing the dependent", t, func() {
		_, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		dependent := dependentJob("a1-carrier-dependent", standardReqs)
		carrier := carrierJob("a1-carrier", standardReqs)

		inserts, already, err := jq.Add([]*Job{dependent}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		inserts, already, err = jq.Add([]*Job{carrier}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		got, err := jq.GetByRepGroup(dependent.RepGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].State, ShouldEqual, JobStateDependent)
		So(got[0].WaitingForDepGroups, ShouldBeNil)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, carrier.Key())

		reserved, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldBeNil)
	})

	Convey("Completing the carrier releases a dependent that waited on a never-seen dep group", t, func() {
		config, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		dependent := dependentJob("a1-completed-carrier-dependent", standardReqs)
		carrier := carrierJob("a1-completed-carrier", standardReqs)

		inserts, already, err := jq.Add([]*Job{dependent}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		inserts, already, err = jq.Add([]*Job{carrier}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, carrier.Key())
		So(jq.Execute(ctx, reserved, config.RunnerExecShell), ShouldBeNil)

		got, err := jq.GetByRepGroup(dependent.RepGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].State, ShouldEqual, JobStateReady)
		So(got[0].WaitingForDepGroups, ShouldBeNil)

		reserved, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, dependent.Key())
	})
}

func TestAddWarnings(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	start := func() (internal.Config, *Server, *Client, *jqs.Requirements) {
		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
		serverConfig.Timings.ItemTTR = 2 * time.Second
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		return config, server, jq, standardReqs
	}

	dependentJob := func(repGroup string, reqs *jqs.Requirements) *Job {
		return &Job{
			Cmd:          "echo " + repGroup,
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: reqs,
			Retries:      uint8(0),
			RepGroup:     repGroup,
			Dependencies: Dependencies{NewDepGroupDependency(futureDepGroup)},
		}
	}

	Convey("AddWithWarnings returns never-seen dependency groups", t, func() {
		_, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		added, existed, warnings, err := jq.AddWithWarnings(
			[]*Job{dependentJob("b1-future-dependent", standardReqs)},
			envVars,
			true,
		)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(existed, ShouldEqual, 0)
		So(warnings.NeverSeenDepGroups, ShouldResemble, []string{futureDepGroup})
	})

	Convey("AddWithWarnings de-duplicates never-seen dependency groups", t, func() {
		_, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		first := dependentJob("b1-future-dependent-a", standardReqs)
		second := dependentJob("b1-future-dependent-b", standardReqs)

		_, _, warnings, err := jq.AddWithWarnings([]*Job{first, second}, envVars, true)
		So(err, ShouldBeNil)
		So(warnings.NeverSeenDepGroups, ShouldResemble, []string{futureDepGroup})
	})

	Convey("AddWithWarnings stays quiet for a completed seen dep group", t, func() {
		config, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		carrier := &Job{
			Cmd:          "echo b2 done carrier",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(0),
			RepGroup:     "b2-done-carrier",
			DepGroups:    []string{"done"},
		}

		added, existed, warnings, err := jq.AddWithWarnings([]*Job{carrier}, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(existed, ShouldEqual, 0)
		So(warnings.NeverSeenDepGroups, ShouldBeEmpty)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(jq.Execute(ctx, reserved, config.RunnerExecShell), ShouldBeNil)

		dependent := &Job{
			Cmd:          "echo b2 done dependent",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(0),
			RepGroup:     "b2-done-dependent",
			Dependencies: Dependencies{NewDepGroupDependency("done")},
		}

		_, _, warnings, err = jq.AddWithWarnings([]*Job{dependent}, envVars, true)
		So(err, ShouldBeNil)
		So(warnings.NeverSeenDepGroups, ShouldBeEmpty)
	})

	Convey("AddWithWarnings stays quiet for same-batch dep-group carriers", t, func() {
		_, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		dependent := dependentJob("b2-same-batch-dependent", standardReqs)
		dependent.Dependencies = Dependencies{NewDepGroupDependency("batch")}

		carrier := &Job{
			Cmd:          "echo b2 same batch carrier",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(0),
			RepGroup:     "b2-same-batch-carrier",
			DepGroups:    []string{"batch"},
		}

		added, existed, warnings, err := jq.AddWithWarnings([]*Job{dependent, carrier}, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 2)
		So(existed, ShouldEqual, 0)
		So(warnings.NeverSeenDepGroups, ShouldBeEmpty)
	})
}

func TestGetIncompleteWaitingForDepGroups(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("GetIncompleteWaitingForDepGroups returns only jobs waiting on never-seen dep groups", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
		serverConfig.Timings.ItemTTR = 2 * time.Second
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		missing := &Job{
			Cmd:          "echo c3 missing dependent",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(0),
			RepGroup:     testRepGroupA,
			Dependencies: Dependencies{NewDepGroupDependency(futureDepGroup)},
		}
		otherMissing := &Job{
			Cmd:          "echo c3 other missing dependent",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(0),
			RepGroup:     testOtherRepGroup,
			Dependencies: Dependencies{NewDepGroupDependency("elsewhere")},
		}
		liveDependent := &Job{
			Cmd:          "echo c3 live dependent",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(0),
			RepGroup:     testRepGroupA,
			Dependencies: Dependencies{NewDepGroupDependency(testLiveDepGroup)},
		}
		liveCarrier := &Job{
			Cmd:          "echo c3 live carrier",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(0),
			RepGroup:     testRepGroupA,
			DepGroups:    []string{testLiveDepGroup},
		}
		ready := &Job{
			Cmd:          "echo c3 ready",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(0),
			RepGroup:     testRepGroupA,
		}

		inserts, already, err := jq.Add(
			[]*Job{missing, otherMissing, liveDependent, liveCarrier, ready},
			envVars,
			true,
		)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 5)
		So(already, ShouldEqual, 0)

		allWaiting, err := jq.GetIncompleteWaitingForDepGroups("", RepGroupMatchExact, 0, false, false)
		So(err, ShouldBeNil)
		So(allWaiting, ShouldHaveLength, 2)

		exactWaiting, err := jq.GetIncompleteWaitingForDepGroups(testRepGroupA, RepGroupMatchExact, 0, false, false)
		So(err, ShouldBeNil)
		So(exactWaiting, ShouldHaveLength, 1)
		So(exactWaiting[0].Key(), ShouldEqual, missing.Key())
		So(exactWaiting[0].WaitingForDepGroups, ShouldResemble, []string{futureDepGroup})

		searchWaiting, err := jq.GetIncompleteWaitingForDepGroups("rg-", RepGroupMatchSubStr, 0, false, false)
		So(err, ShouldBeNil)
		So(searchWaiting, ShouldHaveLength, 1)
		So(searchWaiting[0].Key(), ShouldEqual, missing.Key())
	})
}

func TestSameBatchAndLiveDepGroupReblocking(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	start := func() (internal.Config, *Server, *Client, *jqs.Requirements) {
		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
		serverConfig.Timings.ItemTTR = 2 * time.Second
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		return config, server, jq, standardReqs
	}

	makeJob := func(cmd, repGroup string, reqs *jqs.Requirements) *Job {
		return &Job{
			Cmd:          cmd,
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: reqs,
			Retries:      uint8(0),
			RepGroup:     repGroup,
		}
	}

	Convey("Same-batch dep-group carriers keep dependents blocked on live jobs", t, func() {
		_, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		dependent := makeJob("echo a3 same batch dependent", "a3-same-batch-dependent", standardReqs)
		dependent.Dependencies = Dependencies{NewDepGroupDependency("batch")}
		carrier := makeJob("echo a3 same batch carrier", "a3-same-batch-carrier", standardReqs)
		carrier.DepGroups = []string{"batch"}

		inserts, already, err := jq.Add([]*Job{dependent, carrier}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)
		So(already, ShouldEqual, 0)

		got, err := jq.GetByRepGroup(dependent.RepGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].State, ShouldEqual, JobStateDependent)
		So(got[0].WaitingForDepGroups, ShouldBeNil)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, carrier.Key())

		reserved, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldBeNil)
	})

	Convey("A new live carrier reblocks a ready dependent until the carrier completes", t, func() {
		config, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		firstCarrier := makeJob("echo a3 first live carrier", "a3-first-live-carrier", standardReqs)
		firstCarrier.DepGroups = []string{testLiveDepGroup}

		inserts, already, err := jq.Add([]*Job{firstCarrier}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, firstCarrier.Key())
		So(jq.Execute(ctx, reserved, config.RunnerExecShell), ShouldBeNil)

		dependent := makeJob("echo a3 live dependent", "a3-live-dependent", standardReqs)
		dependent.Dependencies = Dependencies{NewDepGroupDependency(testLiveDepGroup)}

		inserts, already, err = jq.Add([]*Job{dependent}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		got, err := jq.GetByRepGroup(dependent.RepGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].State, ShouldEqual, JobStateReady)
		So(got[0].WaitingForDepGroups, ShouldBeNil)

		secondCarrier := makeJob("echo a3 second live carrier", "a3-second-live-carrier", standardReqs)
		secondCarrier.DepGroups = []string{testLiveDepGroup}

		inserts, already, err = jq.Add([]*Job{secondCarrier}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		got, err = jq.GetByRepGroup(dependent.RepGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].State, ShouldEqual, JobStateDependent)
		So(got[0].WaitingForDepGroups, ShouldBeNil)

		reserved, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, secondCarrier.Key())
		reservedCarrier := reserved

		blocked, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(blocked, ShouldBeNil)

		So(jq.Execute(ctx, reservedCarrier, config.RunnerExecShell), ShouldBeNil)

		got, err = jq.GetByRepGroup(dependent.RepGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].State, ShouldEqual, JobStateReady)

		reserved, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, dependent.Key())
	})

	Convey("Adding a new carrier resurrects a completed dependent chain with existing counts", t, func() {
		config, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		firstRoot := makeJob("echo a3 first root", "a3-chain", standardReqs)
		firstRoot.DepGroups = []string{"root"}

		inserts, already, err := jq.Add([]*Job{firstRoot}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, firstRoot.Key())
		So(jq.Execute(ctx, reserved, config.RunnerExecShell), ShouldBeNil)

		child := makeJob("echo a3 child", "a3-chain", standardReqs)
		child.DepGroups = []string{"child"}
		child.Dependencies = Dependencies{NewDepGroupDependency("root")}

		inserts, already, err = jq.Add([]*Job{child}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		reserved, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, child.Key())
		So(jq.Execute(ctx, reserved, config.RunnerExecShell), ShouldBeNil)

		grandchild := makeJob("echo a3 grandchild", "a3-chain", standardReqs)
		grandchild.Dependencies = Dependencies{NewDepGroupDependency("child")}

		inserts, already, err = jq.Add([]*Job{grandchild}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		reserved, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, grandchild.Key())
		So(jq.Execute(ctx, reserved, config.RunnerExecShell), ShouldBeNil)

		secondRoot := makeJob("echo a3 second root", "a3-chain", standardReqs)
		secondRoot.DepGroups = []string{"root"}

		inserts, already, err = jq.Add([]*Job{secondRoot}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 3)
		So(already, ShouldEqual, 0)

		ready, err := jq.GetIncomplete(0, JobStateReady, false, false)
		So(err, ShouldBeNil)
		So(ready, ShouldHaveLength, 1)
		So(ready[0].Key(), ShouldEqual, secondRoot.Key())

		dependent, err := jq.GetIncomplete(0, JobStateDependent, false, false)
		So(err, ShouldBeNil)
		So(dependent, ShouldHaveLength, 2)

		for _, job := range dependent {
			So(job.WaitingForDepGroups, ShouldBeNil)
		}

		complete, err := jq.GetByRepGroup("a3-chain", false, 0, JobStateComplete, false, false)
		So(err, ShouldBeNil)
		So(complete, ShouldHaveLength, 1)
		So(complete[0].Key(), ShouldEqual, firstRoot.Key())
	})
}

func TestCommandDependenciesStayStatic(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	start := func() (internal.Config, *Server, *Client, *jqs.Requirements) {
		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
		serverConfig.Timings.ItemTTR = 2 * time.Second
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		return config, server, jq, standardReqs
	}

	makeJob := func(cmd, repGroup string, reqs *jqs.Requirements) *Job {
		return &Job{
			Cmd:          cmd,
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: reqs,
			Retries:      uint8(0),
			RepGroup:     repGroup,
		}
	}

	Convey("An absent command dependency still resolves to no dependency", t, func() {
		_, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		job := makeJob("echo a4 actual", "a4-absent-command", standardReqs)
		job.Dependencies = Dependencies{NewEssenceDependency("echo missing", "")}

		inserts, already, err := jq.Add([]*Job{job}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		got, err := jq.GetByRepGroup(job.RepGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].State, ShouldEqual, JobStateReady)
		So(got[0].WaitingForDepGroups, ShouldBeNil)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, job.Key())
	})

	Convey("Same-batch command dependencies continue to wait for live command targets", t, func() {
		config, server, jq, standardReqs := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		dependent := makeJob("echo a4 command dependent", "a4-same-batch-command-dependent", standardReqs)
		dependent.Dependencies = Dependencies{NewEssenceDependency("echo later", "")}
		carrier := makeJob("echo later", "a4-same-batch-command-carrier", standardReqs)

		inserts, already, err := jq.Add([]*Job{dependent, carrier}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)
		So(already, ShouldEqual, 0)

		got, err := jq.GetByRepGroup(dependent.RepGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].State, ShouldEqual, JobStateDependent)
		So(got[0].WaitingForDepGroups, ShouldBeNil)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, carrier.Key())

		blocked, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(blocked, ShouldBeNil)

		So(jq.Execute(ctx, reserved, config.RunnerExecShell), ShouldBeNil)

		reserved, err = jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(reserved.Key(), ShouldEqual, dependent.Key())
	})
}

func TestRerunReplacementReadyCallbackBlocksReserve(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	_, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("A rerun replacement that becomes ready blocks reserves until ReadyAdded finishes", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		parent := &Job{
			Cmd:          "echo rerun replacement parent",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     "rerun-replacement-parent",
		}
		oldJob := &Job{
			Cmd:          "echo rerun replacement child",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     "rerun-replacement-old",
			State:        JobStateComplete,
		}
		newJob := &Job{
			Cmd:          oldJob.Cmd,
			Cwd:          oldJob.Cwd,
			ReqGroup:     oldJob.ReqGroup,
			Requirements: standardReqs,
			RepGroup:     "rerun-replacement-new",
		}

		parentKey := parent.Key()
		childKey := oldJob.Key()
		added, dups, err := server.q.AddMany(ctx, []*queue.ItemDef{
			{
				Key:        parentKey,
				Data:       parent,
				TTR:        server.itemTTRDuration(),
				StartQueue: queue.SubQueueBury,
			},
			{
				Key:          childKey,
				Data:         oldJob,
				TTR:          server.itemTTRDuration(),
				Dependencies: []string{parentKey},
			},
		})
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 2)
		So(dups, ShouldEqual, 0)

		server.rpl.Lock()
		server.rpl.Add(oldJob.RepGroup, childKey)
		server.rpl.Unlock()

		callbackStarted := make(chan struct{})
		releaseCallback := make(chan struct{})

		var (
			startOnce   sync.Once
			releaseOnce sync.Once
		)

		release := func() {
			releaseOnce.Do(func() {
				close(releaseCallback)
			})
		}
		defer release()

		server.q.SetReadyAddedCallback(func(string, []interface{}) {
			startOnce.Do(func() {
				close(callbackStarted)
			})
			<-releaseCallback
			server.finishRAC()
		})

		updated, err := server.replaceLiveRerunItem(ctx, &queue.ItemDef{
			Key:  childKey,
			Data: newJob,
			TTR:  server.itemTTRDuration(),
		})
		So(err, ShouldBeNil)
		So(updated, ShouldBeTrue)

		select {
		case <-callbackStarted:
		case <-time.After(time.Second):
			So("timed out waiting for ReadyAdded callback", ShouldBeBlank)

			return
		}

		type reserveResult struct {
			job *Job
			err error
		}

		reserved := make(chan reserveResult, 1)

		go func() {
			job, reserveErr := jq.Reserve(2 * time.Second)
			reserved <- reserveResult{job: job, err: reserveErr}
		}()

		select {
		case <-reserved:
			So("reserve returned before ReadyAdded callback completed", ShouldBeBlank)
			release()

			return
		case <-time.After(150 * time.Millisecond):
		}

		release()

		select {
		case result := <-reserved:
			So(result.err, ShouldBeNil)
			So(result.job, ShouldNotBeNil)
			So(result.job.Key(), ShouldEqual, childKey)
			So(result.job.RepGroup, ShouldEqual, newJob.RepGroup)
		case <-time.After(2 * time.Second):
			So("timed out waiting for reserve after ReadyAdded callback completed", ShouldBeBlank)
		}
	})
}

func TestJobqueueMedium(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	reserveWait := 5 * time.Second

	Convey("Once a new jobqueue server is up", t, func() {
		// Default to a TTR that comfortably outlasts the reserve->Started gap
		// even when CI is heavily oversubscribed. A reserved job whose runner
		// has not sent Started before the TTR elapses is auto-released from the
		// run queue as "lost", so a too-short default makes every plain
		// reserve+Execute scenario here intermittently fail with "bad job"
		// under load. The few scenarios that actually exercise short-TTR
		// behaviour (lost jobs, auto-revert) opt back into a short TTR locally.
		serverConfig.Timings.ItemTTR = 2 * time.Second
		serverConfig.Timings.TouchInterval = 50 * time.Millisecond
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		Convey("You can connect, and add some real jobs", func() {
			if skipInShard("a") {
				return
			}

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			jq2, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq2)

			const (
				sleepTrueCmd  = "sleep 0.1 && true"
				sleepFalseCmd = "sleep 0.1 && false"
			)

			jobs := make([]*Job, 0, 2)

			jobs = append(jobs, &Job{
				Cmd:          sleepTrueCmd,
				Cwd:          "/tmp",
				ReqGroup:     "fake_group",
				Requirements: standardReqs,
				Retries:      uint8(2),
				RepGroup:     "manually_added",
			})
			jobs = append(jobs, &Job{
				Cmd:          sleepFalseCmd,
				Cwd:          "/tmp",
				ReqGroup:     "fake_group",
				Requirements: standardReqs,
				Retries:      uint8(2),
				RepGroup:     "manually_added",
			})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			Convey("You can't execute a job without reserving it", func() {
				err := jq.Execute(ctx, jobs[0], config.RunnerExecShell)
				So(err, ShouldNotBeNil)

				var jqerr Error

				ok := errors.As(err, &jqerr)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrMustReserve)
				disconnect(jq)
			})

			Convey("Once reserved you can execute jobs, and other clients see the correct state on gets", func() {
				// job that succeeds, no std out
				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, sleepTrueCmd)
				So(job.State, ShouldEqual, JobStateReserved)
				So(job.Attempts, ShouldEqual, 0)
				So(job.UntilBuried, ShouldEqual, 3)

				job2, err := jq2.GetByEssence(&JobEssence{Cmd: sleepTrueCmd}, false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.Cmd, ShouldEqual, sleepTrueCmd)
				So(job2.State, ShouldEqual, JobStateReserved)

				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job.State, ShouldEqual, JobStateComplete)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)
				So(job.PeakRAM, ShouldBeGreaterThan, 0)
				So(job.PeakDisk, ShouldEqual, 0)
				So(job.Pid, ShouldBeGreaterThan, 0)

				host, err := os.Hostname()
				So(err, ShouldBeNil)
				So(job.Host, ShouldEqual, host)
				So(job.WallTime(), ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job.CPUtime, ShouldBeGreaterThanOrEqualTo, 0*time.Millisecond)
				So(job.Attempts, ShouldEqual, 1)
				So(job.UntilBuried, ShouldEqual, 3)
				stdout, err := job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "")

				stderr, err := job.StdErr()
				So(err, ShouldBeNil)
				So(stderr, ShouldEqual, "")

				actualCwd := job.ActualCwd
				expectedCwdPrefix := filepath.Join(
					"/tmp", "jobqueue_cwd", "7", "4", "7", "27e23009c78b126f274aa64416f30",
				)
				So(actualCwd, ShouldStartWith, expectedCwdPrefix)
				So(actualCwd, ShouldEndWith, "cwd")

				job2, err = jq2.GetByEssence(&JobEssence{Cmd: sleepTrueCmd}, false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.State, ShouldEqual, JobStateComplete)
				So(job2.Exited, ShouldBeTrue)
				So(job2.Exitcode, ShouldEqual, 0)
				So(job2.PeakRAM, ShouldEqual, job.PeakRAM)
				So(job2.Pid, ShouldEqual, job.Pid)
				So(job2.Host, ShouldEqual, host)
				So(job2.WallTime(), ShouldBeLessThanOrEqualTo, job.WallTime())
				So(job2.WallTime(), ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job2.CPUtime, ShouldEqual, job.CPUtime)
				So(job2.Attempts, ShouldEqual, 1)
				So(job2.ActualCwd, ShouldEqual, actualCwd)

				// job that fails, no std out
				job, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, sleepFalseCmd)
				So(job.State, ShouldEqual, JobStateReserved)
				So(job.Attempts, ShouldEqual, 0)
				So(job.UntilBuried, ShouldEqual, 3)

				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldNotBeNil)

				expectedErrPrefix := "command [" + sleepFalseCmd + "] exited with code 1, " +
					"which may be a temporary issue, so it will be tried again"
				So(err.Error(), ShouldStartWith, expectedErrPrefix)
				So(job.State, ShouldEqual, JobStateDelayed)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 1)
				So(job.FailReason, ShouldEqual, FailReasonExit)
				So(job.PeakRAM, ShouldBeGreaterThan, 0)

				if job.PeakRAM > job.Requirements.RAM {
					So(err.Error(), ShouldContainSubstring, FailReasonRAM)
				} else {
					So(err.Error(), ShouldNotContainSubstring, FailReasonRAM)
				}

				So(job.Pid, ShouldBeGreaterThan, 0)
				So(job.Host, ShouldEqual, host)
				So(job.WallTime(), ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job.CPUtime, ShouldBeGreaterThanOrEqualTo, 0*time.Millisecond)
				So(job.Attempts, ShouldEqual, 1)
				So(job.UntilBuried, ShouldEqual, 2)
				stdout, err = job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "")

				stderr, err = job.StdErr()
				So(err, ShouldBeNil)
				So(stderr, ShouldEqual, "")

				job2, err = jq2.GetByEssence(&JobEssence{Cmd: sleepFalseCmd}, false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.State, ShouldEqual, JobStateDelayed)
				So(job2.Exited, ShouldBeTrue)
				So(job2.Exitcode, ShouldEqual, 1)
				So(job2.PeakRAM, ShouldEqual, job.PeakRAM)
				So(job2.Pid, ShouldEqual, job.Pid)
				So(job2.Host, ShouldEqual, host)
				So(job2.WallTime(), ShouldBeLessThanOrEqualTo, job.WallTime())
				So(job2.WallTime(), ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job2.CPUtime, ShouldEqual, job.CPUtime)
				So(job2.Attempts, ShouldEqual, 1)

				Convey("Both current and archived jobs can be retrieved with GetByRepGroup", func() {
					jobs, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
					So(err, ShouldBeNil)
					So(len(jobs), ShouldEqual, 2)

					Convey("But only current jobs are retrieved with GetIncomplete", func() {
						jobs, err = jq.GetIncomplete(0, "", false, false)
						So(err, ShouldBeNil)
						So(len(jobs), ShouldEqual, 1)
						So(jobs[0].Cmd, ShouldEqual, sleepFalseCmd)
						// *** should probably have a better test, where there are incomplete jobs in each of the sub queues
					})
				})

				Convey("A temp failed job is reservable after a delay", func() {
					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					job2, err = jq2.GetByEssence(&JobEssence{Cmd: sleepFalseCmd}, false, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateDelayed)
					So(job2.DelayTime, ShouldBeGreaterThanOrEqualTo, serverConfig.Timings.ReleaseDelayMin)
					So(job2.DelayTime, ShouldBeLessThan, serverConfig.Timings.ReleaseDelayMin*2)

					<-time.After(serverConfig.Timings.ReleaseDelayMin)
					job, err = jq.Reserve(serverConfig.Timings.ReleaseDelayMin)
					So(err, ShouldBeNil)
					So(job, ShouldNotBeNil)
					So(job.Cmd, ShouldEqual, sleepFalseCmd)
					So(job.State, ShouldEqual, JobStateReserved)
					So(job.Attempts, ShouldEqual, 1)
					So(job.UntilBuried, ShouldEqual, 2)

					job2, err = jq2.GetByEssence(&JobEssence{Cmd: sleepFalseCmd}, false, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateReserved)

					Convey("After 2 retries (3 attempts) it gets buried", func() {
						err = jq.Execute(ctx, job, config.RunnerExecShell)
						So(err, ShouldNotBeNil)
						So(job.State, ShouldEqual, JobStateDelayed)
						So(job.Exited, ShouldBeTrue)
						So(job.Exitcode, ShouldEqual, 1)
						So(job.Attempts, ShouldEqual, 2)
						So(job.UntilBuried, ShouldEqual, 1)
						So(job.DelayTime, ShouldBeGreaterThanOrEqualTo, serverConfig.Timings.ReleaseDelayMin*2)
						So(job.DelayTime, ShouldBeLessThan, serverConfig.Timings.ReleaseDelayMin*3)
						delayEnd := job.EndTime.Add(job.DelayTime)

						<-time.After(serverConfig.Timings.ReleaseDelayMin)

						job, err = jq.Reserve(time.Until(delayEnd) - 10*time.Millisecond)
						So(err, ShouldBeNil)
						So(job, ShouldBeNil)
						job, err = jq2.GetByEssence(&JobEssence{Cmd: sleepFalseCmd}, false, false)
						So(err, ShouldBeNil)
						So(job, ShouldNotBeNil)
						So(job.State, ShouldEqual, JobStateDelayed)

						job, err = jq.Reserve(reserveWait)
						So(err, ShouldBeNil)
						So(job.Cmd, ShouldEqual, sleepFalseCmd)
						So(job.State, ShouldEqual, JobStateReserved)
						So(job.Attempts, ShouldEqual, 2)
						So(job.UntilBuried, ShouldEqual, 1)
						So(job.DelayTime, ShouldBeGreaterThanOrEqualTo, serverConfig.Timings.ReleaseDelayMin*4)
						So(job.DelayTime, ShouldBeLessThan, serverConfig.Timings.ReleaseDelayMin*5)

						err = jq.Execute(ctx, job, config.RunnerExecShell)
						So(err, ShouldNotBeNil)
						So(job.State, ShouldEqual, JobStateBuried)
						So(job.Exited, ShouldBeTrue)
						So(job.Exitcode, ShouldEqual, 1)
						So(job.Attempts, ShouldEqual, 3)
						So(job.UntilBuried, ShouldEqual, 0)

						<-time.After(400 * time.Millisecond)
						job, err = jq.Reserve(100 * time.Millisecond)
						So(err, ShouldBeNil)
						So(job, ShouldBeNil)

						Convey("Once buried it can be kicked back to ready state and be reserved again", func() {
							job2, err = jq2.GetByEssence(&JobEssence{Cmd: sleepFalseCmd}, false, false)
							So(err, ShouldBeNil)
							So(job2, ShouldNotBeNil)
							So(job2.State, ShouldEqual, JobStateBuried)

							kicked, err := jq.Kick([]*JobEssence{{Cmd: sleepFalseCmd}})
							So(err, ShouldBeNil)
							So(kicked, ShouldEqual, 1)

							job, err = jq.Reserve(5 * time.Millisecond)
							So(err, ShouldBeNil)
							So(job, ShouldNotBeNil)
							So(job.Cmd, ShouldEqual, sleepFalseCmd)
							So(job.State, ShouldEqual, JobStateReserved)
							So(job.Attempts, ShouldEqual, 3)
							So(job.UntilBuried, ShouldEqual, 3)

							job2, err = jq2.GetByEssence(&JobEssence{Cmd: sleepFalseCmd}, false, false)
							So(err, ShouldBeNil)
							So(job2, ShouldNotBeNil)
							So(job2.State, ShouldEqual, JobStateReserved)
							So(job2.Attempts, ShouldEqual, 3)
							So(job2.UntilBuried, ShouldEqual, 3)

							Convey("If you do nothing with a reserved job, it auto reverts back to delayed", func() {
								// With no touches the reserved job is auto-released
								// once its TTR expires; poll for that transition
								// instead of sampling at a fixed offset, which
								// races the server's TTR timer under load.
								job2 = waitUntilJobState(jq2, &JobEssence{Cmd: sleepFalseCmd}, JobStateDelayed, 30)
								So(job2, ShouldNotBeNil)
								So(job2.State, ShouldEqual, JobStateDelayed)
								So(job2.Attempts, ShouldEqual, 3)
								So(job2.UntilBuried, ShouldEqual, 3)
							})
						})
					})
				})

				Convey("A job with retries that fails after NoRetriesOverWalltime is immediately buried", func() {
					noRetriesCmd := "sleep 0.5 && false"
					noRetriesEssence := &JobEssence{Cmd: noRetriesCmd}
					var jobs2 []*Job

					jobs2 = append(jobs2, &Job{
						Cmd:                   noRetriesCmd,
						Cwd:                   "/tmp",
						ReqGroup:              "fake_group",
						Requirements:          standardReqs,
						Retries:               uint8(1),
						NoRetriesOverWalltime: 10 * time.Millisecond,
						RepGroup:              "manually_added",
					})
					inserts, already, err := jq.Add(jobs2, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)
					So(already, ShouldEqual, 0)

					noRetriesJob, err := jq.GetByEssence(noRetriesEssence, false, false)
					So(err, ShouldBeNil)
					So(noRetriesJob, ShouldNotBeNil)

					noRetriesJobKey := noRetriesEssence.Key()
					if noRetriesJob != nil {
						noRetriesJobKey = noRetriesJob.Key()
					}

					for range 5 {
						job, err = jq.Reserve(50 * time.Millisecond)
						if err != nil || job == nil || job.Key() == noRetriesJobKey {
							break
						}

						deleted, errd := jq.Delete([]*JobEssence{job.ToEssense()})
						So(errd, ShouldBeNil)
						So(deleted, ShouldEqual, 1)

						if errd != nil || deleted != 1 {
							return
						}
					}

					So(err, ShouldBeNil)

					if err != nil {
						return
					}

					So(job, ShouldNotBeNil)

					if job == nil {
						return
					}

					So(job.Key(), ShouldEqual, noRetriesJobKey)

					if job.Key() != noRetriesJobKey {
						return
					}

					So(job.Cmd, ShouldEqual, noRetriesCmd)
					So(job.State, ShouldEqual, JobStateReserved)
					So(job.Attempts, ShouldEqual, 0)
					So(job.UntilBuried, ShouldEqual, 2)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)

					expectedErrPrefix := "command [sleep 0.5 && false] exited with code 1, " +
						"after the noretries time, so will not be tried again"
					So(err.Error(), ShouldStartWith, expectedErrPrefix)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.FailReason, ShouldEqual, FailReasonExit)

					if job.PeakRAM > job.Requirements.RAM {
						So(err.Error(), ShouldContainSubstring, FailReasonRAM)
					} else {
						So(err.Error(), ShouldNotContainSubstring, FailReasonRAM)
					}
				})
			})

			Convey("Jobs can be deleted in any state except running", func() {
				for _, added := range jobs {
					job, err := jq.GetByEssence(&JobEssence{Cmd: added.Cmd}, false, false)
					So(err, ShouldBeNil)
					So(job, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateReady)

					deleted, err := jq.Delete([]*JobEssence{{Cmd: added.Cmd}})
					So(err, ShouldBeNil)
					So(deleted, ShouldEqual, 1)

					job, err = jq.GetByEssence(&JobEssence{Cmd: added.Cmd}, false, false)
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					//*** add tests to show this doesn't work if running...
				}
				job, err := jq.Reserve(5 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldBeNil)

				Convey("Cmds with pipes in them are handled correctly", func() {
					jobs = nil
					jobs = append(jobs, &Job{Cmd: "sleep 0.1 && true | true", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "should_pass"})
					jobs = append(jobs, &Job{Cmd: "sleep 0.1 && true | false | true", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "should_fail"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 2)

					// pipe job that succeeds
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "sleep 0.1 && true | true")
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)
					So(job.FailReason, ShouldEqual, "")

					// pipe job that fails in the middle
					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "sleep 0.1 && true | false | true")
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)

					expectedErrPrefix := "command [sleep 0.1 && true | false | true] exited with code 1, " +
						"which may be a temporary issue, so it will be tried again"
					So(err.Error(), ShouldStartWith, expectedErrPrefix) // *** can fail with a receive time out; why?!
					So(job.State, ShouldEqual, JobStateDelayed)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					So(job.FailReason, ShouldEqual, FailReasonExit)

					if job.PeakRAM > job.Requirements.RAM {
						So(err.Error(), ShouldContainSubstring, FailReasonRAM)
					} else {
						So(err.Error(), ShouldNotContainSubstring, FailReasonRAM)
					}
				})

				Convey("Invalid commands are immediately buried", func() {
					jobs = nil
					jobs = append(jobs, &Job{Cmd: "awesjnalakjf --foo", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "should_fail"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					// job that fails because of non-existent exe
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "awesjnalakjf --foo")
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldStartWith, "command [awesjnalakjf --foo] exited with code 127 (command not found), which seems permanent, so it has been buried")
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 127)
					So(job.FailReason, ShouldEqual, FailReasonCFound)

					job2, err := jq2.GetByEssence(&JobEssence{Cmd: "awesjnalakjf --foo"}, false, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateBuried)
					So(job2.FailReason, ShouldEqual, FailReasonCFound)

					//*** how to test the other bury cases of invalid exit code
					// and permission problems on the exe?
				})

				Convey("If a job uses more memory than expected it is not killed, but we recommend more next time", func() {
					jobs = nil
					cmd := "perl -e '@a; for (1..3) { push(@a, q[a] x 50000000); sleep(1) }'"
					jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "highmem", Requirements: standardReqs, Retries: uint8(0), RepGroup: "too_much_mem"})

					server.db.recMBRound = 1
					defer func() {
						server.db.recMBRound = 100 // revert back to normal
					}()
					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)
					So(already, ShouldEqual, 0)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, JobStateReserved)
					So(job.Requirements.RAM, ShouldEqual, 10)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)

					cmd2 := "echo another high mem job"
					jobs = append(jobs, &Job{Cmd: cmd2, Cwd: "/tmp", ReqGroup: "highmem", Requirements: standardReqs, Retries: uint8(0), RepGroup: "too_much_mem"})
					inserts, already, err = jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)
					So(already, ShouldEqual, 1)

					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd2)
					So(job.State, ShouldEqual, JobStateReserved)
					So(job.Requirements.RAM, ShouldBeGreaterThanOrEqualTo, 100)
					err = jq.Release(job, &JobEndState{}, "")
					So(err, ShouldBeNil)

					deleted, errd := jq.Delete([]*JobEssence{{Cmd: cmd}})
					So(errd, ShouldBeNil)
					So(deleted, ShouldEqual, 0)
					deleted, errd = jq.Delete([]*JobEssence{{Cmd: cmd2}})
					So(errd, ShouldBeNil)
					So(deleted, ShouldEqual, 1)
				})

				Convey("Jobs that fork and change processgroup can still be fully killed", func() {
					jobs = nil
					tmpdir, err := os.MkdirTemp("", "wr_kill_test")
					So(err, ShouldBeNil)
					defer os.RemoveAll(tmpdir)

					cmd := fmt.Sprintf("perl -Mstrict -we 'open(OUT, qq[>%s/$$]); my $pid = fork; if ($pid == 0) { setpgrp; my $subpid = fork; if ($subpid == 0) { sleep(60); exit 0; } open(OUT, qq[>%s/$subpid]); waitpid $subpid, 0; exit 0; }  open(OUT, qq[>%s/$pid]); sleep(30); waitpid $pid, 0'", tmpdir, tmpdir, tmpdir)
					jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(0), RepGroup: "forker"})
					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)
					So(already, ShouldEqual, 0)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, JobStateReserved)

					ich := make(chan int, 1)
					ech := make(chan error, 1)
					go func() {
						<-time.After(1 * time.Second)
						i, errk := jq.Kill([]*JobEssence{job.ToEssense()})
						ich <- i
						ech <- errk
					}()

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					var jqerr Error
					if !errors.As(err, &jqerr) {
						fmt.Printf("\ngot err %+v\n", err)
					}
					So(jqerr, ShouldNotBeNil)
					So(jqerr.Err, ShouldEqual, FailReasonKilled)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, -1)
					So(job.FailReason, ShouldEqual, FailReasonKilled)

					i := <-ich
					So(i, ShouldEqual, 1)
					err = <-ech
					So(err, ShouldBeNil)

					files, err := os.ReadDir(tmpdir)
					So(err, ShouldBeNil)
					count := 0
					for _, file := range files {
						if file.IsDir() {
							continue
						}
						count++
						pid, err := strconv.Atoi(file.Name())
						So(err, ShouldBeNil)
						process, err := os.FindProcess(pid)
						So(err, ShouldBeNil)
						err = process.Signal(syscall.Signal(0))
						So(err, ShouldNotBeNil)
						So(err.Error(), ShouldContainSubstring, "process already finished")
					}
					So(count, ShouldEqual, 3)

					deleted, errd := jq.Delete([]*JobEssence{{Cmd: cmd}})
					So(errd, ShouldBeNil)
					So(deleted, ShouldEqual, 1)
				})

				Convey("Jobs that fork and change processgroup have correct memory usage reported", func() {
					jobs = nil
					cmd := `perl -Mstrict -we 'my $pid = fork; if ($pid == 0) { setpgrp; my $subpid = fork; if ($subpid == 0) { my @a; for (1..100) { push(@a, q[a] x 10000000); } exit 0; } waitpid $subpid, 0; exit 0; } my @b; for (1..100) { push(@b, q[b] x 1000000); } waitpid $pid, 0'`
					jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(0), RepGroup: "forker"})
					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)
					So(already, ShouldEqual, 0)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)
					So(job.PeakRAM, ShouldBeGreaterThan, 500)
				})

				if runtime.NumCPU() >= 2 {
					Convey("Jobs that fork and change processgroup have correct CPU time reported", func() {
						jobs = nil
						cmd := `perl -Mstrict -we 'my $pid = fork; if ($pid == 0) { setpgrp; my $subpid = fork; if ($subpid == 0) { my $a = 2; for (1..10000000) { $a *= $a } exit 0; } waitpid $subpid, 0; exit 0; } my $b = 2; for (1..10000000) { $b *= $b } waitpid $pid, 0'`
						jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(0), RepGroup: "forker"})
						inserts, already, err := jq.Add(jobs, envVars, true)
						So(err, ShouldBeNil)
						So(inserts, ShouldEqual, 1)
						So(already, ShouldEqual, 0)

						job, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(job.Cmd, ShouldEqual, cmd)
						So(job.State, ShouldEqual, JobStateReserved)

						err = jq.Execute(ctx, job, config.RunnerExecShell)
						So(err, ShouldBeNil)
						So(job.State, ShouldEqual, JobStateComplete)
						So(job.Exited, ShouldBeTrue)
						So(job.Exitcode, ShouldEqual, 0)
						So(job.CPUtime, ShouldBeGreaterThanOrEqualTo, job.WallTime()/10) // *** this is a bad test that fails all the time;
						// we actually expect it be greater than walltime, but sometimes it's less
					})
				}

				Convey("The stdout/err of archived jobs is retained, and cwd&TMPDIR&HOME get set appropriately", func() {
					jobs = nil
					baseDir := t.TempDir()
					So(err, ShouldBeNil)
					tmpDir := filepath.Join(baseDir, "jobqueue tmpdir") // testing that it works with spaces in the name
					err = os.Mkdir(tmpDir, os.ModePerm)
					So(err, ShouldBeNil)
					jobs = append(jobs, &Job{Cmd: "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; warn File::Spec->tmpdir, qq[\\n]'", Cwd: tmpDir, CwdMatters: true, ChangeHome: true, ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_pass"})
					jobs = append(jobs, &Job{Cmd: "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; die File::Spec->tmpdir, qq[\\n]'", Cwd: tmpDir, CwdMatters: false, ChangeHome: true, ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_fail"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 2)

					// job that outputs to stdout and stderr but succeeds
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; warn File::Spec->tmpdir, qq[\\n]'")
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)
					stdout, err := job.StdOut()
					So(err, ShouldBeNil)
					home, herr := os.UserHomeDir()
					So(herr, ShouldBeNil)
					So(stdout, ShouldEqual, tmpDir+"-"+home)
					stderr, err := job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, os.TempDir())

					job2, err := jq2.GetByEssence(&JobEssence{Cmd: "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; warn File::Spec->tmpdir, qq[\\n]'", Cwd: tmpDir}, true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateComplete)
					stdout, err = job2.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, tmpDir+"-"+home)
					stderr, err = job2.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, os.TempDir())

					// job that outputs to stdout and stderr and fails
					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; die File::Spec->tmpdir, qq[\\n]'")
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 255)
					So(job.FailReason, ShouldEqual, FailReasonExit)
					stdout, err = job.StdOut()
					So(err, ShouldBeNil)
					actualCwd := job.ActualCwd
					So(actualCwd, ShouldStartWith, filepath.Join(tmpDir, "jobqueue_cwd", "d", "4", "1", "7364d743329da784e74f2d69d438d"))
					So(actualCwd, ShouldEndWith, "cwd")
					So(stdout, ShouldEqual, actualCwd+"-"+actualCwd)
					stderr, err = job.StdErr()
					So(err, ShouldBeNil)
					tmpDir = actualCwd[:len(actualCwd)-3] + "tmp"
					So(stderr, ShouldEqual, tmpDir)

					job2, err = jq2.GetByEssence(&JobEssence{Cmd: "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; die File::Spec->tmpdir, qq[\\n]'"}, true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateBuried)
					So(job2.FailReason, ShouldEqual, FailReasonExit)
					stdout, err = job2.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, actualCwd+"-"+actualCwd)
					stderr, err = job2.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, tmpDir)
				})

				Convey("The stdout/err of jobs is limited in size", func() {
					jobs = nil
					jobs = append(jobs, &Job{Cmd: "perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_fail"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					// job that outputs tons of stdout and stderr and fails
					expectedout := ""
					expectederr := ""
					for i := 1; i <= 60; i++ {
						if i > 21 && i < 46 {
							continue
						}
						if i == 21 {
							expectedout += "21212121212121212121212121\n... omitting 6358 bytes ...\n45454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545p\n"
							expectederr += "21212121212121212121212121\n... omitting 6377 bytes ...\n5454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545w\n"
						} else {
							s := strconv.Itoa(i)
							for j := 1; j <= 130; j++ {
								expectedout += s
								expectederr += s
							}
							expectedout += "p\n"
							expectederr += "w\n"
						}
					}
					expectederr += "Died at -e line 1."
					expectedout = strings.TrimSpace(expectedout)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'")
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 255)
					stdout, err := job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, expectedout)
					stderr, err := job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, expectederr)

					job2, err := jq2.GetByEssence(&JobEssence{Cmd: "perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'"}, true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateBuried)
					stdout, err = job2.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, expectedout)
					stderr, err = job2.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, expectederr)

					Convey("If you don't ask for stdout, you don't get it", func() {
						job2, err = jq2.GetByEssence(&JobEssence{Cmd: "perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'"}, false, false)
						So(err, ShouldBeNil)
						So(job2, ShouldNotBeNil)
						So(job2.State, ShouldEqual, JobStateBuried)
						stdout, err = job2.StdOut()
						So(err, ShouldBeNil)
						So(stdout, ShouldEqual, "")
						stderr, err = job2.StdErr()
						So(err, ShouldBeNil)
						So(stderr, ShouldEqual, "")
					})
				})

				Convey("The stdout/err of jobs is filtered for \\r blocks", func() {
					jobs = nil
					progressCmd := "perl -e '$|++; print qq[a\nb\n\nprogress: 98%\r]; for (99..100) { print qq[progress: $_%\r]; sleep(1); } print qq[\n\nc\n]; exit(1)'"
					jobs = append(jobs, &Job{Cmd: progressCmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_fail"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					expectedout := "a\nb\n\nprogress: 98%\n[...]\nprogress: 100%\n\nc\n"
					expectedout = strings.TrimSpace(expectedout)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, progressCmd)
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					stdout, err := job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, expectedout)
					stderr, err := job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "")

					jobs = nil
					progressCmd = "perl -e '$|++; print qq[a\nb\n\n]; for (99..100) { print qq[progress: $_%\r]; sleep(1); } print qq[\n\nc\n]; exit(1)'"
					jobs = append(jobs, &Job{Cmd: progressCmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_fail"})
					inserts, _, err = jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					expectedout = "a\nb\n\nprogress: 99%\nprogress: 100%\n\nc\n"
					expectedout = strings.TrimSpace(expectedout)

					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, progressCmd)
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					stdout, err = job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, expectedout)
					stderr, err = job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "")
				})

				Convey("Jobs with long lines of stderr do not cause execution to hang", func() {
					jobs = nil
					bigerrCmd := `perl -e 'for (1..10) { for (1..65536) { print STDERR qq[e] } print STDERR qq[\n] }' && false`
					jobs = append(jobs, &Job{Cmd: bigerrCmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "bigerr"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, bigerrCmd)
					So(job.State, ShouldEqual, JobStateReserved)

					// wait for the job to finish executing
					done := make(chan bool, 1)
					go func() {
						go execute(ctx, jq, job, config.RunnerExecShell, true)

						limit := time.After(10 * time.Second)
						ticker := time.NewTicker(500 * time.Millisecond)
						for {
							select {
							case <-ticker.C:
								jobs, err = jq.GetByRepGroup("bigerr", false, 0, JobStateBuried, false, false)
								if err != nil {
									continue
								}
								if len(jobs) == 1 {
									ticker.Stop()
									done <- true
									return
								}
								continue
							case <-limit:
								ticker.Stop()
								done <- false
								return
							}
						}
					}()
					So(<-done, ShouldBeTrue)

					job, err = jq.GetByEssence(&JobEssence{Cmd: bigerrCmd}, true, false)
					So(err, ShouldBeNil)
					So(job, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					stdout, err := job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldBeBlank)
					stderr, err := job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldStartWith, "eeeeeeeeeeeeeeeeeeeeeeeeeeee")
					So(stderr, ShouldContainSubstring, "... omitting 647178 bytes ...")
					So(stderr, ShouldEndWith, "eeeeeeeeeeeeeeeeeeeeeeeeeeee")
				})

				Convey("Job behaviours trigger correctly", func() {
					jobs = nil
					cwd := t.TempDir()
					So(err, ShouldBeNil)
					b1 := &Behaviour{When: OnSuccess, Do: CleanupAll}
					b2 := &Behaviour{When: OnFailure, Do: Run, Arg: "touch foo"}
					bs := Behaviours{b1, b2}
					b3 := &Behaviour{When: OnFailure, Do: Remove}
					bs2 := Behaviours{b3}
					jobs = append(jobs, &Job{Cmd: "touch bar", Cwd: cwd, ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_pass", Behaviours: bs})
					jobs = append(jobs, &Job{Cmd: "touch bar && false", Cwd: cwd, ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_fail", Behaviours: bs})
					jobs = append(jobs, &Job{Cmd: "touch car && false", Cwd: cwd, ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_delete", Behaviours: bs2})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 3)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "touch bar")
					So(job.State, ShouldEqual, JobStateReserved)
					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)

					actualCwd := job.ActualCwd
					So(actualCwd, ShouldStartWith, filepath.Join(cwd, "jobqueue_cwd", "6", "0", "8", "3ab5943a1918a9774e4644acb36f6"))
					So(actualCwd, ShouldEndWith, "cwd")
					_, err = os.Stat(filepath.Join(actualCwd, "bar"))
					So(err, ShouldNotBeNil)
					_, err = os.Stat(filepath.Join(actualCwd, "foo"))
					So(err, ShouldNotBeNil)
					entries, err := os.ReadDir(cwd)
					So(err, ShouldBeNil)
					So(len(entries), ShouldEqual, 0)

					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "touch bar && false")
					So(job.State, ShouldEqual, JobStateReserved)
					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					So(job.FailReason, ShouldEqual, FailReasonExit)

					actualCwd = job.ActualCwd
					So(actualCwd, ShouldStartWith, filepath.Join(cwd, "jobqueue_cwd", "4", "4", "a", "758484033bddc46a51d3ec7517f2c"))
					So(actualCwd, ShouldEndWith, "cwd")
					_, err = os.Stat(filepath.Join(actualCwd, "bar"))
					So(err, ShouldBeNil)
					_, err = os.Stat(filepath.Join(actualCwd, "foo"))
					So(err, ShouldBeNil)
					entries, err = os.ReadDir(cwd)
					So(err, ShouldBeNil)
					So(len(entries), ShouldEqual, 1)
					So(entries[0].Name(), ShouldEqual, "jobqueue_cwd")

					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "touch car && false")
					So(job.State, ShouldEqual, JobStateReserved)
					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					So(job.FailReason, ShouldEqual, FailReasonExit)

					// poll for the buried job to become visible by rep group
					// instead of assuming the server's view updates within a
					// fixed delay.
					So(pollUntil(func() bool {
						jobs, err = jq.GetByRepGroup("should_fail", false, 0, JobStateBuried, false, false)

						return err == nil && len(jobs) == 1
					}), ShouldBeTrue)
					So(len(jobs), ShouldEqual, 1)
					jobs, err = jq.GetByRepGroup("should_delete", false, 0, JobStateBuried, false, false)
					So(err, ShouldBeNil)
					So(len(jobs), ShouldEqual, 0)
				})

				Convey("Jobs that take longer than the ttr can execute successfully, even if clienttouchinterval is > ttr", func() {
					// This scenario deliberately exercises TTR/touch timing (a
					// job that runs longer than the TTR, and a client whose
					// touch interval exceeds the TTR so the job is briefly lost),
					// so it opts into the short TTR that the suite no longer uses
					// by default.
					server.SetItemTTR(200 * time.Millisecond)

					jobs = nil
					cmd := "perl -MTime::HiRes=sleep -e 'sleep 0.8'"
					jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "should_pass"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)

					job2, err := jq2.GetByEssence(&JobEssence{Cmd: cmd}, true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateComplete)

					// same again, but we'll alter this client's touch interval to
					// be > ttr (per-client override of the server-provided value)
					jq.touchInterval = 500 * time.Millisecond
					inserts, _, err = jq.Add(jobs, envVars, false)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, JobStateReserved)

					lostCh := make(chan bool)
					go func() {
						<-time.After(300 * time.Millisecond)
						// after ttr but before first touch, it becomes lost
						job2f, errf := jq2.GetByEssence(&JobEssence{Cmd: cmd}, true, false)
						if errf != nil || job2f == nil || job2f.State != JobStateLost || job2f.FailReason != FailReasonLost || job2f.Exited {
							lostCh <- false
						}

						<-time.After(250 * time.Millisecond)
						// after the first touch, it becomes running again
						job2f, errf = jq2.GetByEssence(&JobEssence{Cmd: cmd}, true, false)
						if errf != nil || job2f == nil || job2f.State != JobStateRunning || job2f.FailReason != FailReasonLost || job2f.Exited {
							lostCh <- false
						}
						lostCh <- true
					}()

					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldBeNil)

					job2, err = jq2.GetByEssence(&JobEssence{Cmd: cmd}, true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)

					So(job2.State, ShouldEqual, JobStateComplete)
					So(job2.Exited, ShouldBeTrue)
					So(<-lostCh, ShouldBeTrue)
				})
			})
		})

		Convey("After connecting and adding some jobs under one RepGroup", func() {
			if skipInShard("b") {
				return
			}

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			var jobs []*Job
			for i := 0; i < 3; i++ {
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("echo rgduptest %d", i), Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "rp1"})
			}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 3)
			So(already, ShouldEqual, 0)

			Convey("You can reserve and execute those", func() {
				for i := 0; i < 3; i++ {
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					err = jq.Execute(ctx, job, config.RunnerExecShell)
					So(err, ShouldBeNil)
				}

				Convey("Then you can add dups and a new one under a new RepGroup and reserve/execute all of them", func() {
					jobs = nil
					for i := 0; i < 4; i++ {
						jobs = append(jobs, &Job{Cmd: fmt.Sprintf("echo rgduptest %d", i), Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "rp2"})
					}
					inserts, already, err := jq.Add(jobs, envVars, false)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 4)
					So(already, ShouldEqual, 0)

					for i := 0; i < 4; i++ {
						job, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						err = jq.Execute(ctx, job, config.RunnerExecShell)
						So(err, ShouldBeNil)
					}

					Convey("The jobs can be retrieved by either RepGroup and will have the expected RepGroup", func() {
						jobsg, err := jq.GetByRepGroup("rp1", false, 0, JobStateComplete, false, false)
						So(err, ShouldBeNil)
						So(len(jobsg), ShouldEqual, 3)
						So(jobsg[0].RepGroup, ShouldEqual, "rp1")

						jobsg, err = jq.GetByRepGroup("rp2", false, 0, JobStateComplete, false, false)
						So(err, ShouldBeNil)
						So(len(jobsg), ShouldEqual, 4)
						So(jobsg[0].RepGroup, ShouldEqual, "rp2")
					})
				})

				Convey("Previously complete jobs are rejected when adding with ignoreComplete", func() {
					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 0)
					So(already, ShouldEqual, 3)
				})
			})

			Convey("You can add dups and a new one under a new RepGroup", func() {
				jobs = nil
				for i := 0; i < 4; i++ {
					jobs = append(jobs, &Job{Cmd: fmt.Sprintf("echo rgduptest %d", i), Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "rp2"})
				}
				inserts, already, err := jq.Add(jobs, envVars, false)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 3)

				Convey("You can then reserve and execute the only 4 jobs", func() {
					for i := 0; i < 4; i++ {
						job, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						err = jq.Execute(ctx, job, config.RunnerExecShell)
						So(err, ShouldBeNil)
					}
					job, err := jq.Reserve(10 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					Convey("The jobs can be retrieved by either RepGroup and will have the expected RepGroup", func() {
						jobs, err := jq.GetByRepGroup("rp1", false, 0, JobStateComplete, false, false)
						So(err, ShouldBeNil)
						So(len(jobs), ShouldEqual, 3)
						So(jobs[0].RepGroup, ShouldEqual, "rp1")

						jobs, err = jq.GetByRepGroup("rp2", false, 0, JobStateComplete, false, false)
						So(err, ShouldBeNil)
						So(len(jobs), ShouldEqual, 4)
						So(jobs[0].RepGroup, ShouldEqual, "rp2")
					})
				})
			})
		})

		Convey("After connecting and adding some jobs under some RepGroups", func() {
			if skipInShard("b") {
				return
			}

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo deptest1", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep1"})
			jobs = append(jobs, &Job{Cmd: "echo deptest2", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep2"})
			jobs = append(jobs, &Job{Cmd: "echo deptest3", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep3"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 3)
			So(already, ShouldEqual, 0)

			Convey("You can search for the jobs using a common substring of their repgroups", func() {
				gottenJobs, err := jq.GetByRepGroup("dep", true, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 3)
				gottenJobs, err = jq.GetByRepGroup("2", true, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 1)
			})

			Convey("You can retrieve jobs by RepGroup using prefix and suffix server-side", func() {
				gottenJobs, err := jq.GetByRepGroupMatch("dep", RepGroupMatchPrefix, 0,
					"", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 3)

				gottenJobs, err = jq.GetByRepGroupMatch("3", RepGroupMatchSuffix, 0,
					"", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 1)
				So(gottenJobs[0].RepGroup, ShouldEqual, "dep3")
			})

			Convey("You can retrieve incomplete jobs by RepGroup exact match or substring", func() {
				gottenJobs, err := jq.GetIncompleteByRepGroupMatch("dep2",
					RepGroupMatchExact, 0,
					"", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 1)
				So(gottenJobs[0].RepGroup, ShouldEqual, "dep2")

				gottenJobs, err = jq.GetIncompleteByRepGroupMatch("dep",
					RepGroupMatchSubStr, 0,
					"", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 3)

				gottenJobs, err = jq.GetIncompleteByRepGroupMatch("dep",
					RepGroupMatchSubStr, 0,
					JobStateReady, false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 3)

				gottenJobs, err = jq.GetIncompleteByRepGroupMatch("dep",
					RepGroupMatchSubStr, 0,
					JobStateComplete, false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 0)
			})

			Convey("You can retrieve incomplete jobs by RepGroup prefix and suffix", func() {
				gottenJobs, err := jq.GetIncompleteByRepGroupMatch("dep",
					RepGroupMatchPrefix, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 3)

				gottenJobs, err = jq.GetIncompleteByRepGroupMatch("2",
					RepGroupMatchSuffix, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 1)
				So(gottenJobs[0].RepGroup, ShouldEqual, "dep2")

				gottenJobs, err = jq.GetIncompleteByRepGroupMatch("dep",
					RepGroupMatchPrefix, 0, JobStateComplete, false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 0)
			})

			mkLCTJob := func(cmd, repGroup string, endTime time.Time) *Job {
				return &Job{
					Cmd:       cmd,
					Cwd:       "/tmp",
					ReqGroup:  "fake_group",
					RepGroup:  repGroup,
					StartTime: endTime.Add(-1 * time.Second),
					EndTime:   endTime,
				}
			}

			Convey("You can retrieve latest completion times by rep group in db", func() {
				base := time.Now().Truncate(time.Second)

				j1 := mkLCTJob("echo lct1", "lct-rg", base.Add(1*time.Second))
				j2 := mkLCTJob("echo lct2", "lct-rg", base.Add(3*time.Second))
				j3 := mkLCTJob("echo lct3", "lct-rg", base.Add(2*time.Second))

				err = server.db.archiveJob(ctx, j1.Key(), j1)
				So(err, ShouldBeNil)
				err = server.db.archiveJob(ctx, j2.Key(), j2)
				So(err, ShouldBeNil)
				err = server.db.archiveJob(ctx, j3.Key(), j3)
				So(err, ShouldBeNil)

				completionTimes, errf := server.db.retrieveLastCompletionTimeByRepGroup(
					[]string{"lct-rg"})
				So(errf, ShouldBeNil)
				So(len(completionTimes), ShouldEqual, 1)
				So(completionTimes["lct-rg"].Unix(), ShouldEqual,
					base.Add(3*time.Second).Unix())
			})

			Convey("You can retrieve latest completion times by rep group prefix", func() {
				base := time.Now().Truncate(time.Second)

				jA := mkLCTJob("echo lctA", "lct-rgA", base.Add(2*time.Second))
				jB1 := mkLCTJob("echo lctB1", "lct-rgB", base.Add(1*time.Second))
				jB2 := mkLCTJob("echo lctB2", "lct-rgB", base.Add(4*time.Second))

				err = server.db.storeLookups(bucketRGs, sobsd{
					{[]byte("lct-rgA"), nil},
					{[]byte("lct-rgB"), nil},
				})
				So(err, ShouldBeNil)

				err = server.db.archiveJob(ctx, jA.Key(), jA)
				So(err, ShouldBeNil)
				err = server.db.archiveJob(ctx, jB1.Key(), jB1)
				So(err, ShouldBeNil)
				err = server.db.archiveJob(ctx, jB2.Key(), jB2)
				So(err, ShouldBeNil)

				completionTimes, err := jq.GetLastCompletionTimeByRepGroup("lct-rg",
					RepGroupMatchPrefix)
				So(err, ShouldBeNil)
				So(len(completionTimes), ShouldEqual, 2)
				So(completionTimes["lct-rgA"].Unix(), ShouldEqual,
					base.Add(2*time.Second).Unix())
				So(completionTimes["lct-rgB"].Unix(), ShouldEqual,
					base.Add(4*time.Second).Unix())
			})

			Convey("You get an empty map when no rep group has completed jobs", func() {
				completionTimes, err := jq.GetLastCompletionTimeByRepGroup("does-not-exist",
					RepGroupMatchExact)
				So(err, ShouldBeNil)
				So(completionTimes, ShouldResemble, map[string]time.Time{})
			})

			Convey("You can reserve and execute one of them", func() {
				j1, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(j1.RepGroup, ShouldEqual, "dep1")
				err = jq.Execute(ctx, j1, config.RunnerExecShell)
				So(err, ShouldBeNil)

				// poll for the server's view to reflect the completion instead
				// of assuming it lands within a fixed few ms.
				var gottenJobs []*Job

				So(pollUntil(func() bool {
					gottenJobs, err = jq.GetByRepGroup("dep1", false, 0, "", false, false)

					return err == nil && len(gottenJobs) == 1 && gottenJobs[0].State == JobStateComplete
				}), ShouldBeTrue)
				So(len(gottenJobs), ShouldEqual, 1)
				So(gottenJobs[0].State, ShouldEqual, JobStateComplete)

				Convey("You can then add jobs dependent on the initial jobs and themselves", func() {
					// https://i-msdn.sec.s-msft.com/dynimg/IC332764.gif
					jobs = nil
					d1 := NewEssenceDependency("echo deptest1", "")
					d2 := NewEssenceDependency("echo deptest2", "")
					d3 := NewEssenceDependency("echo deptest3", "")
					jobs = append(jobs, &Job{Cmd: "echo deptest4", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep4", Dependencies: Dependencies{d1}})
					d4 := NewEssenceDependency("echo deptest4", "")
					jobs = append(jobs, &Job{Cmd: "echo deptest5", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep5", Dependencies: Dependencies{d1, d2, d3}})
					d5 := NewEssenceDependency("echo deptest5", "")
					jobs = append(jobs, &Job{Cmd: "echo deptest6", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep6", Dependencies: Dependencies{d3, d4}})
					d6 := NewEssenceDependency("echo deptest6", "")
					jobs = append(jobs, &Job{Cmd: "echo deptest7", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep7", Dependencies: Dependencies{d5, d6}})
					jobs = append(jobs, &Job{Cmd: "echo deptest8", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep8", Dependencies: Dependencies{d5}})

					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 5)
					So(already, ShouldEqual, 0)

					// dep4 was added with a dependency on dep1, but after dep1
					// was already completed; it should start off in the ready
					// queue, not the dependent queue
					gottenJobs, err = jq.GetByRepGroup("dep4", false, 0, "", false, false)
					So(err, ShouldBeNil)
					So(len(gottenJobs), ShouldEqual, 1)
					So(gottenJobs[0].State, ShouldEqual, JobStateReady)

					Convey("They are then only reservable according to the dependency chain", func() {
						j2, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j2.RepGroup, ShouldEqual, "dep2")
						j3, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j3.RepGroup, ShouldEqual, "dep3")
						j4, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j4.RepGroup, ShouldEqual, "dep4")
						jNil, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(jNil, ShouldBeNil)

						// since things can take a while, we keep our reserved
						// jobs touched
						touchJ2 := true
						touchJ3 := true
						var touchLock sync.Mutex
						go func() {
							ticker := time.NewTicker(50 * time.Millisecond)
							for range ticker.C {
								touchLock.Lock()
								if touchJ2 {
									touch(jq, j2)
								}
								if touchJ3 {
									touch(jq, j3)
								}
								if !touchJ2 && !touchJ3 {
									ticker.Stop()
									touchLock.Unlock()
									return
								}
								touchLock.Unlock()
								continue
							}
						}()

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						err = jq.Execute(ctx, j4, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ3 = false
						touchLock.Unlock()
						err = jq.Execute(ctx, j3, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						gottenJobs, err = jq.GetByRepGroup("dep5", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ2 = false
						touchLock.Unlock()
						err = jq.Execute(ctx, j2, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep5", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						j5, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j5.RepGroup, ShouldEqual, "dep5")
						j6, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j6.RepGroup, ShouldEqual, "dep6")
						jNil, err = jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(jNil, ShouldBeNil)

						touchJ6 := true
						go func() {
							ticker := time.NewTicker(50 * time.Millisecond)
							for range ticker.C {
								touchLock.Lock()
								if touchJ6 {
									touch(jq, j6)
								} else {
									ticker.Stop()
									touchLock.Unlock()
									return
								}
								touchLock.Unlock()
								continue
							}
						}()

						gottenJobs, err = jq.GetByRepGroup("dep8", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						err = jq.Execute(ctx, j5, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep8", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						gottenJobs, err = jq.GetByRepGroup("dep7", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ6 = false
						touchLock.Unlock()
						err = jq.Execute(ctx, j6, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep7", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)
					})
				})

				Convey("You can add jobs with non-existent dependencies", func() {
					// first get rid of the jobs added earlier
					for i := 0; i < 2; i++ {
						job, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						err = jq.Execute(ctx, job, config.RunnerExecShell)
						So(err, ShouldBeNil)
					}

					jobs = nil
					d5 := NewEssenceDependency("echo deptest5", "")
					jobs = append(jobs, &Job{Cmd: "echo deptest4", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep4", Dependencies: Dependencies{d5}})
					jobs = append(jobs, &Job{Cmd: "echo deptest5", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep5"})

					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 2)
					So(already, ShouldEqual, 0)

					j5, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(j5.RepGroup, ShouldEqual, "dep5")
					jNil, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(jNil, ShouldBeNil)

					err = jq.Execute(ctx, j5, config.RunnerExecShell)
					So(err, ShouldBeNil)

					j4, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(j4.RepGroup, ShouldEqual, "dep4")

					err = jq.Release(j4, nil, "")
					So(err, ShouldBeNil)

					// *** we should implement rejection of dependency cycles
					// and test for that
				})
			})
		})

		Convey("After connecting you can add some jobs with DepGroups", func() {
			if skipInShard("b") {
				return
			}

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			// This scenario checks dependency transitions, not TTR expiry. Keep
			// reserved dependency-chain jobs alive under race/CI load while the
			// test inspects intermediate states before executing them.
			server.SetItemTTR(2 * time.Second)

			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo deptest1", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep1", DepGroups: []string{"dep1", "dep1+2+3"}})
			jobs = append(jobs, &Job{Cmd: "echo deptest2", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep2", DepGroups: []string{"dep2", "dep1+2+3"}})
			jobs = append(jobs, &Job{Cmd: "echo deptest3", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep3", DepGroups: []string{"dep3", "dep1+2+3"}})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 3)
			So(already, ShouldEqual, 0)

			Convey("You can reserve and execute one of them", func() {
				j1, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(j1.RepGroup, ShouldEqual, "dep1")
				err = jq.Execute(ctx, j1, config.RunnerExecShell)
				So(err, ShouldBeNil)

				// poll for the server's view to reflect the completion instead
				// of assuming it lands within a fixed few ms.
				var gottenJobs []*Job

				So(pollUntil(func() bool {
					gottenJobs, err = jq.GetByRepGroup("dep1", false, 0, "", false, false)

					return err == nil && len(gottenJobs) == 1 && gottenJobs[0].State == JobStateComplete
				}), ShouldBeTrue)
				So(len(gottenJobs), ShouldEqual, 1)
				So(gottenJobs[0].State, ShouldEqual, JobStateComplete)

				Convey("You can then add jobs dependent on the initial jobs and themselves", func() {
					// https://i-msdn.sec.s-msft.com/dynimg/IC332764.gif
					jobs = nil
					d1 := NewDepGroupDependency("dep1")
					d123 := NewDepGroupDependency("dep1+2+3")
					d3 := NewDepGroupDependency("dep3")
					jobs = append(jobs, &Job{Cmd: "echo deptest4", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep4", DepGroups: []string{"dep4"}, Dependencies: Dependencies{d1}})
					d4 := NewDepGroupDependency("dep4")
					jobs = append(jobs, &Job{Cmd: "echo deptest5", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep5", DepGroups: []string{"dep5"}, Dependencies: Dependencies{d123}})
					d5 := NewDepGroupDependency("dep5")
					jobs = append(jobs, &Job{Cmd: "echo deptest6", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep6", DepGroups: []string{"dep6"}, Dependencies: Dependencies{d3, d4}})
					d6 := NewDepGroupDependency("dep6")
					jobs = append(jobs, &Job{Cmd: "echo deptest7", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep7", DepGroups: []string{"final"}, Dependencies: Dependencies{d5, d6}})
					jobs = append(jobs, &Job{Cmd: "echo deptest8", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep8", DepGroups: []string{"final"}, Dependencies: Dependencies{d5}})

					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 5)
					So(already, ShouldEqual, 0)

					// dep4 was added with a dependency on dep1, but after dep1
					// was already completed; it should start off in the ready
					// queue, not the dependent queue
					gottenJobs, err = jq.GetByRepGroup("dep4", false, 0, "", false, false)
					So(err, ShouldBeNil)
					So(len(gottenJobs), ShouldEqual, 1)
					So(gottenJobs[0].State, ShouldEqual, JobStateReady)

					Convey("They are then only reservable according to the dependency chain", func() {
						j2, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j2.RepGroup, ShouldEqual, "dep2")
						j3, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j3.RepGroup, ShouldEqual, "dep3")
						j4, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j4.RepGroup, ShouldEqual, "dep4")
						jNil, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(jNil, ShouldBeNil)

						// since things can take a while, we keep our reserved
						// jobs touched
						touchJ2 := true
						touchJ3 := true
						var touchLock sync.Mutex
						go func() {
							ticker := time.NewTicker(50 * time.Millisecond)
							for range ticker.C {
								touchLock.Lock()
								if touchJ2 {
									touch(jq, j2)
								}
								if touchJ3 {
									touch(jq, j3)
								}
								if !touchJ2 && !touchJ3 {
									ticker.Stop()
									touchLock.Unlock()
									return
								}
								touchLock.Unlock()
								continue
							}
						}()

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						err = jq.Execute(ctx, j4, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ3 = false
						touchLock.Unlock()
						err = jq.Execute(ctx, j3, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						gottenJobs, err = jq.GetByRepGroup("dep5", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ2 = false
						touchLock.Unlock()
						err = jq.Execute(ctx, j2, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep5", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						j5, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j5.RepGroup, ShouldEqual, "dep5")
						j6, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j6.RepGroup, ShouldEqual, "dep6")
						jNil, err = jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(jNil, ShouldBeNil)

						touchJ6 := true
						go func() {
							ticker := time.NewTicker(50 * time.Millisecond)
							for range ticker.C {
								touchLock.Lock()
								if touchJ6 {
									touch(jq, j6)
								} else {
									ticker.Stop()
									touchLock.Unlock()
									return
								}
								touchLock.Unlock()
								continue
							}
						}()

						gottenJobs, err = jq.GetByRepGroup("dep8", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						err = jq.Execute(ctx, j5, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep8", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						gottenJobs, err = jq.GetByRepGroup("dep7", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ6 = false
						touchLock.Unlock()
						err = jq.Execute(ctx, j6, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep7", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						Convey("DepGroup dependencies are live, bringing back jobs if new jobs are added that match their dependencies", func() {
							jobs = nil
							dfinal := NewDepGroupDependency("final")
							jobs = append(jobs, &Job{Cmd: "echo after final", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "afterfinal", DepGroups: []string{"afterfinal"}, Dependencies: Dependencies{dfinal}})
							dafinal := NewDepGroupDependency("afterfinal")
							jobs = append(jobs, &Job{Cmd: "echo after after-final", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "after-afterfinal", Dependencies: Dependencies{dafinal}})
							inserts, already, err := jq.Add(jobs, envVars, true)
							So(err, ShouldBeNil)
							So(inserts, ShouldEqual, 2)
							So(already, ShouldEqual, 0)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							j7, err := jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(j7.RepGroup, ShouldEqual, "dep7")
							err = jq.Execute(ctx, j7, config.RunnerExecShell)
							So(err, ShouldBeNil)
							j8, err := jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(j8.RepGroup, ShouldEqual, "dep8")
							err = jq.Execute(ctx, j8, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateReady)

							jobs = nil
							jobs = append(jobs, &Job{Cmd: "echo deptest9", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep9", DepGroups: []string{"final"}})
							inserts, already, err = jq.Add(jobs, envVars, true)
							So(err, ShouldBeNil)
							So(inserts, ShouldEqual, 1)
							So(already, ShouldEqual, 0)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							j9, err := jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(j9.RepGroup, ShouldEqual, "dep9")
							err = jq.Execute(ctx, j9, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateReady)

							faf, err := jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(faf.RepGroup, ShouldEqual, "afterfinal")
							err = jq.Execute(ctx, faf, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateComplete)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateReady)

							inserts, already, err = jq.Add(jobs, envVars, false)
							So(err, ShouldBeNil)
							So(inserts, ShouldEqual, 2) // the job I added, and the resurrected afterfinal job
							So(already, ShouldEqual, 0)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							j9, err = jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(j9.RepGroup, ShouldEqual, "dep9")
							err = jq.Execute(ctx, j9, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateReady)

							faf, err = jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(faf.RepGroup, ShouldEqual, "afterfinal")
							err = jq.Execute(ctx, faf, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateComplete)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateReady)

							faaf, err := jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(faaf.RepGroup, ShouldEqual, "after-afterfinal")
							err = jq.Execute(ctx, faaf, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateComplete)

							jobs = nil
							jobs = append(jobs, &Job{Cmd: "echo deptest10", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep10", DepGroups: []string{"final"}})
							inserts, already, err = jq.Add(jobs, envVars, true)
							So(err, ShouldBeNil)
							So(inserts, ShouldEqual, 3)
							So(already, ShouldEqual, 0)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)
						})
					})
				})

				Convey("You can add jobs with non-existent depgroup dependencies", func() {
					// first get rid of the jobs added earlier
					for i := 0; i < 2; i++ {
						job, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						err = jq.Execute(ctx, job, config.RunnerExecShell)
						So(err, ShouldBeNil)
					}

					jobs = nil
					d5 := NewDepGroupDependency("dep5")
					jobs = append(jobs, &Job{Cmd: "echo deptest4", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep4", Dependencies: Dependencies{d5}})
					jobs = append(jobs, &Job{Cmd: "echo deptest5", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep5", DepGroups: []string{"dep5"}})

					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 2)
					So(already, ShouldEqual, 0)

					j5, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(j5.RepGroup, ShouldEqual, "dep5")
					jNil, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(jNil, ShouldBeNil)

					err = jq.Execute(ctx, j5, config.RunnerExecShell)
					So(err, ShouldBeNil)

					j4, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(j4.RepGroup, ShouldEqual, "dep4")

					err = jq.Release(j4, nil, "")
					So(err, ShouldBeNil)

					// *** we should implement rejection of dependency cycles
					// and test for that
				})
			})
		})

		Reset(func() {
			server.Stop(ctx, true)
		})
	})
}

func TestJobqueueLimitGroups(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("Once a new jobqueue server is up", t, func() {
		serverConfig.Timings.ItemTTR = 1 * time.Second
		serverConfig.Timings.TouchInterval = 2500 * time.Millisecond
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)
		defer func() {
			server.Stop(ctx, true)
		}()

		server.rc = serverRC

		Convey("You can connect, and add jobs with LimitGroups", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer func() {
				errd := jq.Disconnect()
				if errd != nil {
					fmt.Printf("Disconnect failed: %s\n", errd)
				}
			}()

			var addJobs []*Job
			for i := 1; i <= 5; i++ {
				addJobs = append(addJobs, &Job{Cmd: fmt.Sprintf("echo %d", i), Cwd: "/tmp", ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "ab", LimitGroups: []string{"b:2", "a:3"}})
			}
			inserts, already, err := jq.Add(addJobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 5)
			So(already, ShouldEqual, 0)

			reserveJobs := func() []*Job {
				var jobs []*Job
				for i := 1; i <= 5; i++ {
					job, errr := jq.ReserveScheduled(25*time.Millisecond, "110:30:1:0~a,b")
					So(errr, ShouldBeNil)
					if job != nil {
						jobs = append(jobs, job)
					}
				}
				return jobs
			}

			Convey("You can't reserve more than limit", func() {
				jobs := reserveJobs()
				So(len(jobs), ShouldEqual, 2)

				finalJob := jobs[1]

				stopTouching := make(chan bool, 1)
				go func() {
					// touch this periodically because it might take more than 1
					// second from reserving it to executing it later
					ticker := time.NewTicker(250 * time.Millisecond)
					for {
						select {
						case <-ticker.C:
							jq.Touch(finalJob)
						case <-stopTouching:
							return
						}
					}
				}()
				defer func() {
					stopTouching <- true
				}()

				for i := 1; i <= 3; i++ {
					err = jq.Execute(ctx, jobs[0], config.RunnerExecShell)
					So(err, ShouldBeNil)
					jobs = reserveJobs()
					So(len(jobs), ShouldEqual, 1)
				}

				err = jq.Execute(ctx, jobs[0], config.RunnerExecShell)
				So(err, ShouldBeNil)

				jobs = reserveJobs()
				So(len(jobs), ShouldEqual, 0)

				stopTouching <- true
				err = jq.Execute(ctx, finalJob, config.RunnerExecShell)
				So(err, ShouldBeNil)
				jobs = reserveJobs()
				So(len(jobs), ShouldEqual, 0)
			})

			Convey("You can change the limit by adding a new Job", func() {
				jobs := reserveJobs()
				So(len(jobs), ShouldEqual, 2)

				jobs = []*Job{}
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("echo %d", 6), Cwd: "/tmp", ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "ab", LimitGroups: []string{"a:3", "b:4"}})
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				jobs = reserveJobs()
				So(len(jobs), ShouldEqual, 1)
			})

			Convey("You can get and change the limit using GetOrSetLimitGroup()", func() {
				l, err := jq.GetOrSetLimitGroup("b")
				So(err, ShouldBeNil)
				So(l, ShouldEqual, 2)

				jobs := reserveJobs()
				So(len(jobs), ShouldEqual, 2)

				l, err = jq.GetOrSetLimitGroup("b:4")
				So(err, ShouldBeNil)
				So(l, ShouldEqual, 4)

				jobs = reserveJobs()
				So(len(jobs), ShouldEqual, 1)

				l, err = jq.GetOrSetLimitGroup("b")
				So(err, ShouldBeNil)
				So(l, ShouldEqual, 4)
			})

			Convey("You can even add Jobs with bad LimitGroup names", func() {
				var jobs []*Job
				jobs = append(jobs, &Job{Cmd: "echo bad", Cwd: "/tmp", ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "ab", LimitGroups: []string{"b:2", "a:d3"}})
				_, _, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
			})

			Convey("Failing to start a job after reserving it does not use up the limit", func() {
				jobs := reserveJobs()
				So(len(jobs), ShouldEqual, 2)

				<-time.After(2 * time.Second)
				jobs = reserveJobs()
				So(len(jobs), ShouldEqual, 2)
			})

			Convey("Burying jobs after reserving them does not use up the limit", func() {
				jobs := reserveJobs()
				So(len(jobs), ShouldEqual, 2)

				jq.Bury(jobs[0], nil, "foo")
				jq.Bury(jobs[1], nil, "foo")

				<-time.After(2 * time.Second)
				jobs = reserveJobs()
				So(len(jobs), ShouldEqual, 2)
			})
		})

		Reset(func() {
			server.Stop(ctx, true)
		})
	})
}

func jobsToJobEssenses(jobs []*Job) []*JobEssence {
	jes := make([]*JobEssence, 0, len(jobs))
	for _, job := range jobs {
		jes = append(jes, job.ToEssense())
	}
	return jes
}

func TestJobqueueModules(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	testModulesStr := os.Getenv("WR_TEST_MODULES")
	if testModulesStr == "" {
		SkipConvey("Skipping TestJobqueueModules because WR_TEST_MODULES is not set", t, func() {})

		return
	}

	testModules := strings.Split(testModulesStr, ",")

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("Once a new jobqueue server is up", t, func() {
		serverConfig.Timings.ItemTTR = 1 * time.Second
		serverConfig.Timings.TouchInterval = 2500 * time.Millisecond

		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		server.rc = serverRC

		Convey("You can connect, and add a job with Modules", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer func() {
				errd := jq.Disconnect()

				if errd != nil {
					t.Logf("Disconnect failed: %s\n", errd)
				}
			}()

			cmds := make([]string, 0, len(testModules))
			for _, m := range testModules {
				cmds = append(cmds, "module is-loaded "+m)
			}

			addJobs := []*Job{{
				Cmd: strings.Join(cmds, " && "),
				Cwd: "/tmp", ReqGroup: "rgroup", Requirements: standardReqs,
				Override: uint8(2), Retries: uint8(0), RepGroup: "moduletest",
				Modules: testModules}}

			inserts, already, err := jq.Add(addJobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			Convey("Which can then execute successfully after loading their modules", func() {
				job, errr := jq.ReserveScheduled(25*time.Millisecond, "110:30:1:0")
				So(errr, ShouldBeNil)
				So(job, ShouldNotBeNil)

				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldBeNil)
			})
		})
	})
}

func TestJobqueueModify(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	rtime := 50 * time.Millisecond
	// reserveWait is used when we expect a reservation to succeed: ReserveScheduled
	// returns as soon as a job is available, so a generous timeout is free on the
	// happy path but tolerates the scheduler taking a while to make a job
	// reservable when the machine is under load (which was a source of flakiness).
	reserveWait := 15 * time.Second
	rgroup := "110:30:1:0"
	learnedRgroup := "200:30:1:0"
	learnedRAMNormal := 100
	learnedRAMExtraRange := []int{200, 500}
	tmp := "/tmp"
	echoACmd := "echo a"

	Convey("Once a new jobqueue server is up and client is connected", t, func() {
		serverConfig.Timings.ItemTTR = 5 * time.Second
		serverConfig.Timings.TouchInterval = 2500 * time.Millisecond
		serverConfig.Timings.ReleaseDelayMin = 1 * time.Nanosecond
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)
		defer func() {
			server.Stop(ctx, true)
		}()

		server.rc = serverRC

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)
		defer func() {
			errd := jq.Disconnect()
			if errd != nil {
				fmt.Printf("Disconnect failed: %s\n", errd)
			}
		}()

		var addJobs []*Job
		jm := NewJobModifer()

		add := func(expected int) {
			inserts, already, err := jq.Add(addJobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, expected)
			So(already, ShouldEqual, 0)
		}

		reserve := func(schedStr, expected string, skip ...int) *Job {
			s := 1
			if len(skip) == 1 {
				s = skip[0]
			}
			_, _, line, _ := runtime.Caller(s)

			// poll until the job becomes reservable or we hit a generous
			// deadline: each attempt is short, but the scheduler can take a
			// while to make a job reservable under its scheduler group when the
			// machine is under load, which was a source of flakiness.
			var (
				job  *Job
				errr error
			)

			for deadline := time.Now().Add(reserveWait); ; {
				job, errr = jq.ReserveScheduled(rtime, schedStr)
				So(errr, ShouldBeNil)

				if job == nil && schedStr == learnedRgroup {
					// *** not sure why the memory is sometimes higher when
					// running under Travis or race...
					for _, alt := range []string{"300:30:1:0", "400:30:1:0", "500:30:1:0", "600:30:1:0"} {
						if job != nil {
							break
						}

						job, errr = jq.ReserveScheduled(rtime, alt)
						So(errr, ShouldBeNil)
					}
				}

				if job != nil || time.Now().After(deadline) {
					break
				}
			}

			if job == nil {
				schedDetails := server.schedulerGroupDetails()
				if len(schedDetails) > 0 {
					fmt.Printf("\nschedgrp %s not found, we have:\n", schedStr)
					for _, val := range schedDetails {
						fmt.Printf(" - %s\n", val)
					}
				} else {
					fmt.Printf("\nschedgrp %s not found, and nothing in the scheduler.\n", schedStr)
				}
				fmt.Printf(" *** test from line %d failed\n", line)
			}
			So(job, ShouldNotBeNil)
			So(job.Cmd, ShouldEqual, expected)
			return job
		}

		modify := func(repgroup string, expected int) {
			jobs, err := jq.GetByRepGroup(repgroup, false, 0, "", false, false)
			So(err, ShouldBeNil)
			jes := jobsToJobEssenses(jobs)
			modified, err := jq.Modify(jes, jm)
			So(err, ShouldBeNil)
			So(len(modified), ShouldEqual, expected)
		}

		execute := func(job *Job, shouldWork bool, expectedStdout string) *Job {
			err := jq.Execute(ctx, job, config.RunnerExecShell)
			if shouldWork {
				So(err, ShouldBeNil)
			} else {
				So(err, ShouldNotBeNil)
			}

			jobs, err := jq.GetByRepGroup(job.RepGroup, false, 0, "", true, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			if !shouldWork && expectedStdout != "" {
				stdout, err := jobs[0].StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, expectedStdout)
			}
			return jobs[0]
		}

		kick := func(repgroup string, schedStr, expectedCmd string, expectedStdout string) *Job {
			jobs, err := jq.GetByRepGroup(repgroup, false, 0, "", false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			kicked, err := jq.Kick(jobsToJobEssenses(jobs))
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			job := reserve(schedStr, expectedCmd, 2)
			return execute(job, false, expectedStdout)
		}

		release := func(job *Job) {
			err := jq.Release(job, &JobEndState{}, "")
			So(err, ShouldBeNil)
		}

		groupsToDeps := func(groups string) (deps Dependencies) {
			for _, depgroup := range strings.Split(groups, ",") {
				deps = append(deps, NewDepGroupDependency(depgroup))
			}
			return
		}

		Convey("You can modify the priority and limit of jobs", func() {
			if skipInShard("a") {
				return
			}

			for i := 1; i <= 3; i++ {
				addJobs = append(addJobs, &Job{Cmd: fmt.Sprintf("echo %d", i), Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a", Priority: uint8(5)})
			}
			for i := 4; i <= 7; i++ {
				addJobs = append(addJobs, &Job{Cmd: fmt.Sprintf("echo %d", i), Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "b", Priority: uint8(6)})
			}

			add(7)

			// wait for every added job to become ready (and so have its
			// scheduler group assigned) before reserving, so the highest-priority
			// job is the one reserved; a fixed wait races the scheduler under
			// load. The reserve helper then still polls for reservability.
			So(pollUntil(func() bool {
				a, e1 := jq.GetByRepGroup("a", false, 0, JobStateReady, false, false)
				b, e2 := jq.GetByRepGroup("b", false, 0, JobStateReady, false, false)

				return e1 == nil && e2 == nil && len(a) == 3 && len(b) == 4
			}), ShouldBeTrue)

			reserve(rgroup, "echo 4")

			jm.SetPriority(uint8(4))
			modify("b", 3)
			reserve(rgroup, "echo 1")

			jm = NewJobModifer()
			jm.SetLimitGroups([]string{"foo:0"})
			modify("a", 2)
			reserve(rgroup, "echo 5")
		})

		Convey("You can modify the command line of a job", func() {
			if skipInShard("b") {
				return
			}

			addJobs = append(addJobs, &Job{Cmd: "echo a && false", Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a"})
			add(1)

			job := reserve(rgroup, "echo a && false")
			job = execute(job, false, "a")
			So(job.Attempts, ShouldEqual, 1)

			jm.SetCmd("echo b && false")
			modify("a", 1)

			job = kick("a", rgroup, "echo b && false", "b")
			So(job.Attempts, ShouldEqual, 2)
		})

		testModulesStr := os.Getenv("WR_TEST_MODULES")
		if testModulesStr == "" {
			SkipConvey("Skipping TestJobqueueModules because WR_TEST_MODULES is not set", func() {})
		} else {
			testModules := strings.Split(testModulesStr, ",")
			testModule := testModules[0]
			cmd := "module is-loaded " + testModule
			repgrp := "moduletest"

			Convey("You can modify the modules of a job", func() {
				addJobs = append(addJobs, &Job{
					Cmd: cmd, Cwd: tmp, ReqGroup: "rgroup",
					Requirements: standardReqs, Override: uint8(2), Retries: uint8(0),
					RepGroup: repgrp,
				})

				add(1)

				job := reserve(rgroup, cmd)
				job = execute(job, false, "")
				So(job.Attempts, ShouldEqual, 1)

				jm.SetModules([]string{testModule})
				modify(repgrp, 1)

				jobs, err := jq.GetByRepGroup(repgrp, false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 1)

				kicked, err := jq.Kick(jobsToJobEssenses(jobs))
				So(err, ShouldBeNil)
				So(kicked, ShouldEqual, 1)

				job = reserve(rgroup, cmd)
				job = execute(job, true, "")
				So(job.Attempts, ShouldEqual, 2)
			})
		}

		Convey("You can't modify the command line of a job to match another job", func() {
			if skipInShard("a") {
				return
			}

			addJobs = append(addJobs, &Job{
				Cmd: "echo a && false", Cwd: tmp, ReqGroup: "rgroup",
				Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a",
			})
			addJobs = append(addJobs, &Job{
				Cmd: "echo b && false", Cwd: tmp, ReqGroup: "rgroup",
				Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "b",
			})
			add(2)

			jm.SetCmd("echo b && false")
			modify("a", 0)

			jm.SetCmd("true")
			modify("a", 1)
			modify("b", 0)
		})

		Convey("You can modify the cwd of a job, with and without cwd_matters", func() {
			if skipInShard("b") {
				return
			}

			dir, err := os.Getwd()
			So(err, ShouldBeNil)
			cmd := "pwd && false"
			addJobs = append(addJobs, &Job{Cmd: cmd, Cwd: dir, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a"})
			add(1)

			job := reserve(rgroup, cmd)
			job = execute(job, false, "")
			stdout, err := job.StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldNotEqual, dir)
			So(stdout, ShouldStartWith, dir)

			jm.SetCwd(tmp)
			modify("a", 1)

			job = kick("a", rgroup, cmd, "")
			stdout, err = job.StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldNotEqual, tmp)
			So(stdout, ShouldStartWith, tmp)
			So(job.ActualCwd, ShouldNotEqual, tmp)
			So(job.ActualCwd, ShouldStartWith, tmp)

			cmd = "pwd && true && false"
			addJobs = []*Job{{Cmd: cmd, Cwd: dir, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "b", CwdMatters: true}}
			add(1)

			job = reserve(rgroup, cmd)
			execute(job, false, dir)

			jm.SetCwd(tmp)
			modify("b", 1)

			job = kick("b", rgroup, cmd, tmp)
			So(job.Cwd, ShouldEqual, tmp)
		})

		Convey("You can modify the req_group of a job", func() {
			if skipInShard("a") {
				return
			}

			cmd := echoACmd
			addJobs = append(addJobs, &Job{Cmd: cmd, Cwd: tmp, ReqGroup: "initial", Requirements: standardReqs, Override: uint8(0), Retries: uint8(0), RepGroup: "a"})
			add(1)

			jm.SetReqGroup("modified")
			modify("a", 1)

			job := reserve(rgroup, cmd)
			execute(job, true, "a")

			cmd = "echo b"
			addJobs = []*Job{{Cmd: cmd, Cwd: tmp, ReqGroup: "modified", Requirements: &jqs.Requirements{RAM: 300, Time: 10 * time.Second, Cores: 1, Disk: 0, Other: make(map[string]string)}, Override: uint8(0), Retries: uint8(0), RepGroup: "b"}}
			add(1)

			// if the modify of initial didn't work, we'd have no learning of
			// the modified reqgroup, so it would get 400:30:1:0 as its scheduler
			// group. But due to learning, the RAM is 100
			job = reserve(learnedRgroup, cmd)
			if job.Requirements.RAM != learnedRAMNormal {
				So(job.Requirements.RAM, ShouldBeBetweenOrEqual, learnedRAMExtraRange[0], learnedRAMExtraRange[1])
			} else {
				So(job.Requirements.RAM, ShouldEqual, learnedRAMNormal)
			}
		})

		Convey("You can modify the requirements of a job", func() {
			if skipInShard("b") {
				return
			}

			// not actually using a scheduler to determine when and if jobs are
			// allowed to run, so we're just doing a basic test that the reqs
			// of the job change appropriately
			cmd := echoACmd
			addJobs = append(addJobs, &Job{Cmd: cmd, Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a"})
			add(1)

			jm.SetRequirements(&jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: 0, CoresSet: true, Disk: 0, Other: make(map[string]string)})
			modify("a", 1)

			job := reserve("200:30:0:0", cmd)
			So(job.Requirements.RAM, ShouldEqual, 100)
			So(job.Requirements.Cores, ShouldEqual, 0)
			So(job.Requirements.Disk, ShouldEqual, 0)
			So(len(job.Requirements.Other), ShouldEqual, 0)
			release(job)

			other := make(map[string]string)
			other["foo"] = "bar"
			jm = NewJobModifer()
			jm.SetRequirements(&jqs.Requirements{RAM: 600, Time: 40 * time.Minute, Cores: 0, Disk: 5, DiskSet: true, Other: other, OtherSet: true})
			modify("a", 1)

			job = reserve("700:60:0:5:cfd399e4a9dba25ac14a2454ce3e8d24", cmd)
			So(job.Requirements.RAM, ShouldEqual, 600)
			So(job.Requirements.Cores, ShouldEqual, 0)
			So(job.Requirements.Disk, ShouldEqual, 5)
			So(len(job.Requirements.Other), ShouldEqual, 1)
			release(job)

			jm = NewJobModifer()
			jm.SetRequirements(&jqs.Requirements{Cores: 0.5, CoresSet: true, Disk: 0, DiskSet: true, Other: make(map[string]string), OtherSet: true})
			modify("a", 1)

			job = reserve("700:60:0.5:0", cmd)
			So(job.Requirements.RAM, ShouldEqual, 600)
			So(job.Requirements.Cores, ShouldEqual, 0.5)
			So(job.Requirements.Disk, ShouldEqual, 0)
			So(len(job.Requirements.Other), ShouldEqual, 0)
			release(job)
		})

		Convey("You can modify the override of a job", func() {
			if skipInShard("a") {
				return
			}

			addJobs = append(addJobs, &Job{Cmd: "echo pre", Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(0), Retries: uint8(0), RepGroup: "pre"})
			cmd := "echo a && false"
			addJobs = append(addJobs, &Job{Cmd: cmd, Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a"})
			add(2)

			job := reserve(rgroup, "echo pre")
			execute(job, true, "")
			job = reserve(rgroup, cmd)
			job = execute(job, false, "a")
			So(job.Requirements.Time, ShouldEqual, 10*time.Second)

			jm.SetOverride(uint8(0))
			modify("a", 1)

			// by turning off override, we enable the learned values

			job = kick("a", learnedRgroup, cmd, "a")
			if job.Requirements.Time != 1*time.Second {
				//*** Travis consistently gets 30m, and I don't know why...
				SkipSo(job.Requirements.Time, ShouldEqual, 30*time.Minute)
			} else {
				So(job.Requirements.Time, ShouldEqual, 1*time.Second)
			}
			stats := server.GetServerStats()
			So(stats.ETC, ShouldEqual, 0*time.Second)

			_, err := jq.Kick(jobsToJobEssenses([]*Job{job}))
			So(err, ShouldBeNil)
			job = reserve(learnedRgroup, cmd)
			jq.Started(job, 1)
			stats = server.GetServerStats()
			So(stats.ETC, ShouldEqual, job.Requirements.Time)
		})

		Convey("You can modify the retries and noretries of a job", func() {
			if skipInShard("b") {
				return
			}

			cmd := "false"
			addJobs = append(addJobs, &Job{Cmd: cmd, Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a"})
			add(1)

			job := reserve(rgroup, cmd)
			job = execute(job, false, "")
			So(job.State, ShouldEqual, JobStateBuried)
			So(job.Retries, ShouldEqual, 0)

			jm.SetRetries(uint8(3))
			modify("a", 1)

			job = kick("a", rgroup, cmd, "")
			So(job.State, ShouldEqual, JobStateReady)
			So(job.Retries, ShouldEqual, 3)

			jm.SetNoRetriesOverWalltime(1 * time.Millisecond)
			modify("a", 1)

			job = reserve(rgroup, cmd)
			So(job.NoRetriesOverWalltime, ShouldEqual, 1*time.Millisecond)
		})

		Convey("You can modify the dependencies of a job", func() {
			if skipInShard("a") {
				return
			}

			addJobs = append(addJobs, &Job{
				Cmd: echoACmd, Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs,
				Override: uint8(2), Retries: uint8(0), RepGroup: "a", DepGroups: []string{"a"},
			})
			addJobs = append(addJobs, &Job{Cmd: "echo b", Cwd: tmp, ReqGroup: "rgroup", Requirements: &jqs.Requirements{RAM: 400, Time: 10 * time.Second, Cores: 1, Disk: 0, Other: make(map[string]string)}, Override: uint8(2), Retries: uint8(0), RepGroup: "b", DepGroups: []string{"b"}})
			addJobs = append(addJobs, &Job{Cmd: "echo c", Cwd: tmp, ReqGroup: "rgroup", Requirements: &jqs.Requirements{RAM: 800, Time: 10 * time.Second, Cores: 1, Disk: 0, Other: make(map[string]string)}, Override: uint8(2), Retries: uint8(0), RepGroup: "c", Dependencies: groupsToDeps("a,b")})
			add(3)

			jobA := reserve(rgroup, echoACmd)
			reserve("500:30:1:0", "echo b")

			jobC, err := jq.ReserveScheduled(rtime, "900:30:1:0")
			So(err, ShouldBeNil)
			So(jobC, ShouldBeNil)

			jm.SetDependencies(groupsToDeps("a"))
			modify("c", 1)

			execute(jobA, true, "")

			// without the modification, we'd also need to execute job b before
			// the following reserve would work

			reserve("900:30:1:0", "echo c")
		})

		Convey("You can modify the command line of a job that other jobs depend on", func() {
			if skipInShard("b") {
				return
			}

			addJobs = append(addJobs, &Job{Cmd: "echo a && false", Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a", DepGroups: []string{"a"}})
			addJobs = append(addJobs, &Job{Cmd: "echo b && true", Cwd: tmp, ReqGroup: "rgroup", Requirements: &jqs.Requirements{RAM: 400, Time: 10 * time.Second, Cores: 1, Disk: 0, Other: make(map[string]string)}, Override: uint8(2), Retries: uint8(0), RepGroup: "b", Dependencies: groupsToDeps("a")})
			add(2)

			job := reserve(rgroup, "echo a && false")
			job = execute(job, false, "a")
			So(job.State, ShouldEqual, JobStateBuried)

			job, err := jq.ReserveScheduled(rtime, "700:30:1:0")
			So(err, ShouldBeNil)
			So(job, ShouldBeNil)

			jm.SetCmd("echo a && true")
			modify("a", 1)

			// (kick() assumes the command will fail again)
			jobs, err := jq.GetByRepGroup("a", false, 0, "", false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			kicked, err := jq.Kick(jobsToJobEssenses(jobs))
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)
			job = reserve(rgroup, "echo a && true")
			execute(job, true, "")

			reserve("500:30:1:0", "echo b && true")
		})

		Convey("You can modify the behaviours of a job", func() {
			if skipInShard("a") {
				return
			}

			dir, err := os.MkdirTemp("", "wr_jobqueue_mod_test")
			So(err, ShouldBeNil)
			defer os.RemoveAll(dir)

			cmd := "touch a && false"
			addJobs = append(addJobs, &Job{Cmd: cmd, Cwd: dir, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a", Behaviours: []*Behaviour{{When: OnExit, Do: Cleanup}}})
			add(1)

			job := reserve(rgroup, cmd)
			job = execute(job, false, "")
			path := filepath.Join(job.ActualCwd, "a")
			_, err = os.Stat(path)
			So(err, ShouldNotBeNil)

			jm.SetBehaviours([]*Behaviour{{When: OnExit, Do: Nothing}})
			modify("a", 1)

			job = kick("a", rgroup, cmd, "")
			path = filepath.Join(job.ActualCwd, "a")
			_, err = os.Stat(path)
			So(err, ShouldBeNil)

			jm = NewJobModifer()
			cpPath := filepath.Join(dir, "copied")
			jm.SetBehaviours([]*Behaviour{{When: OnExit, Do: Cleanup}, {When: OnFailure, Do: Run, Arg: fmt.Sprintf("cp a %s", cpPath)}})
			modify("a", 1)

			job = kick("a", rgroup, cmd, "")
			path = filepath.Join(job.ActualCwd, "a")
			_, err = os.Stat(path)
			So(err, ShouldNotBeNil)
			_, err = os.Stat(cpPath)
			So(err, ShouldBeNil)
		})

		Convey("You can modify the env of a job", func() {
			if skipInShard("b") {
				return
			}

			cmd := "echo $wrmodtestfoo && false"
			addJobs = append(addJobs, &Job{Cmd: cmd, Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a"})
			add(1)

			job := reserve(rgroup, cmd)
			execute(job, false, "")

			errs := jm.SetEnvOverride("wrmodtestfoo=bar")
			So(errs, ShouldBeNil)
			modify("a", 1)

			kick("a", rgroup, cmd, "bar")

			jm = NewJobModifer()
			errs = jm.SetEnvOverride("")
			So(errs, ShouldBeNil)
			modify("a", 1)

			kick("a", rgroup, cmd, "")
		})

		Convey("You can modify the cwd_matters of a job", func() {
			if skipInShard("a") {
				return
			}

			cmd := "pwd && false"
			addJobs = append(addJobs, &Job{Cmd: cmd, Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a"})
			add(1)

			job := reserve(rgroup, cmd)
			job = execute(job, false, "")
			stdout, err := job.StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldNotEqual, tmp)
			So(stdout, ShouldStartWith, tmp)
			So(job.ActualCwd, ShouldNotEqual, tmp)
			So(job.ActualCwd, ShouldStartWith, tmp)
			So(job.Cwd, ShouldEqual, tmp)

			jm.SetCwdMatters(true)
			modify("a", 1)

			job = kick("a", rgroup, cmd, tmp)
			So(job.ActualCwd, ShouldEqual, tmp)
			So(job.Cwd, ShouldEqual, tmp)

			jm = NewJobModifer()
			jm.SetCwdMatters(false)
			modify("a", 1)

			job = kick("a", rgroup, cmd, "")
			stdout, err = job.StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldNotEqual, tmp)
			So(stdout, ShouldStartWith, tmp)
			So(job.ActualCwd, ShouldNotEqual, tmp)
			So(job.ActualCwd, ShouldStartWith, tmp)
			So(job.Cwd, ShouldEqual, tmp)
		})

		Convey("You can modify the change_home of a job", func() {
			if skipInShard("b") {
				return
			}

			home := os.Getenv("HOME")
			cmd := "echo $HOME && false"
			addJobs = append(addJobs, &Job{Cmd: cmd, Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a"})
			add(1)

			job := reserve(rgroup, cmd)
			execute(job, false, home)

			jm.SetChangeHome(true)
			modify("a", 1)

			job = kick("a", rgroup, cmd, "")
			stdout, err := job.StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldNotEqual, tmp)
			So(stdout, ShouldStartWith, tmp)
			So(stdout, ShouldEqual, job.ActualCwd)

			jm = NewJobModifer()
			jm.SetChangeHome(false)
			modify("a", 1)

			kick("a", rgroup, cmd, home)
		})

		Convey("Modifying does not resume a paused server", func() {
			if skipInShard("a") {
				return
			}

			for i := 1; i <= 3; i++ {
				addJobs = append(addJobs, &Job{Cmd: fmt.Sprintf("echo %d", i), Cwd: tmp, ReqGroup: "rgroup", Requirements: standardReqs, Override: uint8(2), Retries: uint8(0), RepGroup: "a", Priority: uint8(5)})
			}

			add(3)

			<-time.After(1000 * time.Millisecond)

			reserve(rgroup, "echo 1")

			_, _, errp := jq.PauseServer()
			So(errp, ShouldBeNil)
			job, errr := jq.ReserveScheduled(rtime, rgroup)
			So(job, ShouldBeNil)
			So(errr, ShouldBeNil)

			jm = NewJobModifer()
			jm.SetPriority(uint8(4))
			modify("a", 2)

			job, errr = jq.ReserveScheduled(rtime, rgroup)
			So(job, ShouldBeNil)
			So(errr, ShouldBeNil)

			errr = jq.ResumeServer()
			So(errr, ShouldBeNil)

			reserve(rgroup, "echo 2")

			// *** want an inverse test that ResumeServer() in the middle of
			// carriying out a Modify() does not cause issues, but ~impossible
			// without mocks
		})

		// *** untested: SetDepGroups(), SetBsubMode(). These are not yet fully
		// implemented.

		// *** want to test that modifications survive a server crash and
		// restart

		Reset(func() {
			server.Stop(ctx, true)
		})
	})
}

func TestJobqueueHighMem(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	// start these tests anew because they need a long TTR
	maxRAM, errp := internal.ProcMeminfoMBs()
	if errp == nil && maxRAM > 80000 { // authors high memory system
		Convey("If a job uses close to all memory on machine it is killed and we recommend more next time", t, func() {
			serverConfig.Timings.ItemTTR = 200 * time.Second
			serverConfig.Timings.TouchInterval = 50 * time.Millisecond
			server, _, token, errs := serve(ctx, serverConfig)
			So(errs, ShouldBeNil)
			defer func() {
				server.Stop(ctx, true)
			}()

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)
			jq2, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq2)

			var jobs []*Job
			cmd := "perl -e '@a; for (1..1000) { push(@a, q[a] x 800000000) }'"
			jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(0), RepGroup: "run_out_of_mem"})

			server.db.recMBRound = 1
			defer func() {
				server.db.recMBRound = 100 // revert back to normal
			}()
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			job, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job.Cmd, ShouldEqual, cmd)
			So(job.State, ShouldEqual, JobStateReserved)

			jq.percentMemoryKill = 1
			err = jq.Execute(ctx, job, config.RunnerExecShell)
			jq.percentMemoryKill = 90
			So(err, ShouldNotBeNil)

			var jqerr Error

			ok := errors.As(err, &jqerr)
			So(ok, ShouldBeTrue)
			So(jqerr.Err, ShouldEqual, FailReasonRAM)
			So(job.State, ShouldEqual, JobStateBuried)
			So(job.Exited, ShouldBeTrue)
			So(job.Exitcode, ShouldEqual, -1)
			So(job.FailReason, ShouldEqual, FailReasonRAM)
			So(job.Requirements.RAM, ShouldEqual, 10)

			// requirements only change on becoming ready
			kicked, err := jq.Kick([]*JobEssence{job.ToEssense()})
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)
			job, err = jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.Cmd, ShouldEqual, cmd)
			So(job.State, ShouldEqual, JobStateReserved)
			So(job.Requirements.RAM, ShouldBeGreaterThanOrEqualTo, 1000)

			errr := jq.Release(job, &JobEndState{}, "")
			So(errr, ShouldBeNil)
			deleted, errd := jq.Delete([]*JobEssence{{Cmd: cmd}})
			So(errd, ShouldBeNil)
			So(deleted, ShouldEqual, 1)
		})
	} else {
		SkipConvey("Skipping test that uses most of machine memory", t, func() {})
	}
}

func TestJobqueueProduction(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)

	managerDBBkFile := serverConfig.DBFileBackup

	// start these tests anew because I need to disable dev-mode wiping of the
	// db to test some behaviours
	Convey("Once a new jobqueue server is up it creates a db file", t, func() {
		serverConfig.Timings.ItemTTR = 2 * time.Second
		// a couple of these leaves stop/restart the server and rely on the
		// client reconnecting promptly to report a job's final state.
		serverConfig.Timings.RetryWait = 1 * time.Second

		serverConfig.forceBackups = true
		defer func() {
			serverConfig.forceBackups = false
		}()
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)
		defer func() {
			server.Stop(ctx, true)
		}()

		_, err := os.Stat(config.ManagerDBFile)
		So(err, ShouldBeNil)
		_, err = os.Stat(managerDBBkFile)
		So(err, ShouldNotBeNil)

		Convey("A kill requested after reservation survives job start", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			jobs := []*Job{{
				Cmd:          "sleep 10",
				Cwd:          "/tmp",
				ReqGroup:     "pending_kill",
				Requirements: &jqs.Requirements{RAM: 1, Time: time.Second, Cores: 1},
				Retries:      uint8(0),
				RepGroup:     "pending_kill",
			}}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			job, err := jq.Reserve(time.Second)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)

			if job == nil {
				return
			}

			killCount, err := jq.Kill([]*JobEssence{job.ToEssense()})
			So(err, ShouldBeNil)
			So(killCount, ShouldEqual, 1)

			So(jq.Started(job, os.Getpid()), ShouldBeNil)

			killCalled, err := jq.Touch(job)
			So(err, ShouldBeNil)
			So(killCalled, ShouldBeTrue)

			err = jq.Bury(job, &JobEndState{
				Exited:   true,
				Exitcode: -1,
				EndTime:  time.Now(),
			}, FailReasonKilled)
			So(err, ShouldBeNil)
		})

		Convey("You can connect, and add 2 jobs, which creates a db backup", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			// do this in 2 separate Add() calls to better test how backups
			// work
			configureFastTestBackups(server.db)
			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo 1", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)
			jobs = append(jobs, &Job{Cmd: "echo 2", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
			inserts, already, err = jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 1)

			tmpPath := managerDBBkFile + ".tmp"
			So(waitForBoltLiveJobs(managerDBBkFile, 2, 5*time.Second), ShouldBeNil)
			So(waitForFileToDisappear(tmpPath, 5*time.Second), ShouldBeNil)

			assertNonEmptyFile(config.ManagerDBFile)
			assertNonEmptyFile(managerDBBkFile)
			assertBoltLiveJobs(managerDBBkFile, 2)
			_, err = os.Stat(tmpPath)
			So(err, ShouldNotBeNil)

			Convey("You can create manual backups that work correctly", func() {
				manualBackup := managerDBBkFile + ".manual"
				err = jq.BackupDB(manualBackup)
				So(err, ShouldBeNil)
				assertNonEmptyFile(manualBackup)
				assertBoltLiveJobs(manualBackup, 2)

				server.Stop(ctx, true)
				server, _, token, errs = serve(ctx, serverConfig)
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err := jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 0)

				server.Stop(ctx, true)
				err = os.Rename(manualBackup, config.ManagerDBFile)
				So(err, ShouldBeNil)

				serverConfig.dontWipeDevDB = true

				defer func() {
					serverConfig.dontWipeDevDB = false
				}()
				server, _, token, errs = serve(ctx, serverConfig)
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 2)
			})

			Convey("You can stop the server, delete or corrupt the database, and it will be restored from backup", func() {
				jobsByRepGroup, err := jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 2)

				server.Stop(ctx, true)

				serverConfig.dontWipeDevDB = true
				defer func() {
					serverConfig.dontWipeDevDB = false
				}()
				server, _, token, errs = serve(ctx, serverConfig)
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 2)

				server.Stop(ctx, true)
				os.Remove(config.ManagerDBFile)
				_, err = os.Stat(config.ManagerDBFile)
				So(err, ShouldNotBeNil)
				_, err = os.Stat(managerDBBkFile)
				So(err, ShouldBeNil)

				server, _, token, errs = serve(ctx, serverConfig)
				So(errs, ShouldBeNil)

				assertNonEmptyFile(config.ManagerDBFile)
				assertNonEmptyFile(managerDBBkFile)
				assertBoltLiveJobs(managerDBBkFile, 2)

				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 2)

				server.Stop(ctx, true)
				f, err := os.OpenFile(config.ManagerDBFile, os.O_TRUNC|os.O_RDWR, dbFilePermission)
				So(err, ShouldBeNil)
				_, err = f.WriteString("corrupt!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				So(err, ShouldBeNil)
				err = f.Sync()
				So(err, ShouldBeNil)
				err = f.Close()
				So(err, ShouldBeNil)

				server, _, token, errs = serve(ctx, serverConfig)
				So(errs, ShouldBeNil)

				assertNonEmptyFile(config.ManagerDBFile)
				assertNonEmptyFile(managerDBBkFile)
				assertBoltLiveJobs(managerDBBkFile, 2)

				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 2)
			})

			Convey("You can reserve & execute just 1 of the jobs, stop the server, restart it, and then reserve & execute the other", func() {
				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, "echo 1")
				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job.State, ShouldEqual, JobStateComplete)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)

				server.Stop(ctx, true)

				serverConfig.dontWipeDevDB = true
				server, _, token, errs = serve(ctx, serverConfig)
				serverConfig.dontWipeDevDB = false
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				job, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Cmd, ShouldEqual, "echo 2")
				err = jq.Execute(ctx, job, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job.State, ShouldEqual, JobStateComplete)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)
			})
		})

		Convey("You can connect, add a job, then immediately shutdown, and the db backup still completes", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			configureFastTestBackups(server.db)
			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo 1", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)
			server.Stop(ctx, true)

			assertNonEmptyFile(config.ManagerDBFile)
			assertBoltLiveJobs(config.ManagerDBFile, 1)
			assertNonEmptyFile(managerDBBkFile)
			assertBoltLiveJobs(managerDBBkFile, 1)

			Convey("You can restart the server with that existing job, delete it, and it stays deleted when restoring from backup", func() {
				serverConfig.dontWipeDevDB = true
				defer func() {
					serverConfig.dontWipeDevDB = false
				}()
				errr := os.Remove(config.ManagerDBFile)
				So(errr, ShouldBeNil)
				server, _, token, errs = serve(ctx, serverConfig)
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)
				job, err := jq.Reserve(15 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				errb := jq.Bury(job, nil, "")
				So(errb, ShouldBeNil)
				deleted, err := jq.Delete([]*JobEssence{{JobKey: job.Key()}})
				server.Stop(ctx, true)
				So(deleted, ShouldEqual, 1)
				So(err, ShouldBeNil)

				errr = os.Remove(config.ManagerDBFile)
				So(errr, ShouldBeNil)
				server, _, token, errs = serve(ctx, serverConfig)
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)
				job, err = jq.Reserve(15 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldBeNil)
			})
		})

		Convey("You can connect and add a non-instant job", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			var jobs []*Job
			job1Cmd := "sleep 1 && echo noninstant"
			jobs = append(jobs, &Job{Cmd: job1Cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "nij"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			Convey("You can reserve & execute the job, drain the server, add a new job while draining, restart it, and then reserve & execute the new one", func() {
				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, job1Cmd)
				go execute(ctx, jq, job, config.RunnerExecShell)
				So(job.Exited, ShouldBeFalse)

				running, etc, err := jq.DrainServer()
				So(err, ShouldBeNil)
				So(running, ShouldEqual, 1)
				So(etc.Minutes(), ShouldBeLessThanOrEqualTo, 30)

				jobs = append(jobs, &Job{Cmd: "echo added", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "nij"})
				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 1)

				job2, err := jq.Reserve(10 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job2, ShouldBeNil)

				// wait for the drain (the running ~1s job finishing, then the
				// server shutting down) to complete. We must not poll Ping here:
				// pinging mid-shutdown can catch the socket in a state where the
				// underlying Send blocks while holding the client lock, hanging
				// the test, so we wait for the worst-case settle and then Ping
				// once, by which point the server is reliably gone.
				<-time.After(3 * time.Second)

				_, err = jq.Ping(10 * time.Millisecond)
				So(err, ShouldNotBeNil)

				serverConfig.dontWipeDevDB = true
				server, _, token, errs = serve(ctx, serverConfig)
				serverConfig.dontWipeDevDB = false
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				job, err = jq.GetByEssence(&JobEssence{Cmd: job1Cmd}, false, false)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)

				job2, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.Cmd, ShouldEqual, "echo added")
				err = jq.Execute(ctx, job2, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job2.State, ShouldEqual, JobStateComplete)
				So(job2.Exited, ShouldBeTrue)
				So(job2.Exitcode, ShouldEqual, 0)
			})

			Convey("You can reserve & execute the job, shut down, reject new jobs, and let the started job recover", func() {
				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, job1Cmd)

				started := make(chan bool)
				done := make(chan error)

				go func() {
					started <- true

					erre := jq.Execute(ctx, job, config.RunnerExecShell)
					done <- erre
				}()

				So(job.Exited, ShouldBeFalse)

				<-started
				<-time.After(200 * time.Millisecond)

				ok := jq.ShutdownServer()
				So(ok, ShouldBeTrue)

				jobs = append(jobs, &Job{
					Cmd:          "echo added",
					Cwd:          testCwd,
					ReqGroup:     reqGroupFake,
					Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1},
					Retries:      uint8(3),
					RepGroup:     "nij",
				})
				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldNotBeNil)

				_, err = jq.Ping(10 * time.Millisecond)
				So(err, ShouldNotBeNil)

				err = jq.Disconnect()
				if err != nil {
					So(isClosedSocketError(err), ShouldBeTrue)
				}

				serverConfig.dontWipeDevDB = true
				server, _, token, errs = serve(ctx, serverConfig)
				startedAt := time.Now()
				serverConfig.dontWipeDevDB = false

				So(errs, ShouldBeNil)

				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				job, err = jq.GetByEssence(&JobEssence{Cmd: job1Cmd}, false, false)
				So(err, ShouldBeNil)

				shouldBeLost := time.Since(startedAt) > serverConfig.Timings.ItemTTR

				job.RLock()
				jobExited := job.Exited
				jobLost := job.Lost
				job.RUnlock()

				notLost := false

				if jobExited {
					// sometimes the existing runner manages to reconnect to the
					// new server before this test
					So(jobLost, ShouldEqual, shouldBeLost)
					notLost = !jobLost
				} else {
					So(jobExited, ShouldBeFalse)
				}

				erre := <-done
				So(erre, ShouldNotBeNil)
				So(erre.Error(), ShouldContainSubstring, "recovered on a new server") // or "receive time out"?

				job, err = jq.GetByEssence(&JobEssence{Cmd: job1Cmd}, false, false)
				So(err, ShouldBeNil)
				job.RLock()
				jobExited = job.Exited
				jobLost = job.Lost
				job.RUnlock()

				So(jobExited, ShouldBeTrue)

				shouldBeLost = false
				if !notLost && time.Since(startedAt) > serverConfig.Timings.ItemTTR {
					shouldBeLost = true
				}

				So(jobLost, ShouldEqual, shouldBeLost)
			})
		})

		Convey("You can connect and add a failing job that stays buried after a restart", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			var jobs []*Job

			job1Cmd := "false"
			jobs = append(jobs, &Job{
				Cmd:          job1Cmd,
				Cwd:          testCwd,
				ReqGroup:     reqGroupFake,
				Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1},
				Retries:      uint8(0),
				RepGroup:     "false",
			})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			job, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job.Cmd, ShouldEqual, job1Cmd)
			err = jq.Execute(ctx, job, config.RunnerExecShell)
			So(err, ShouldNotBeNil)
			So(job.Exited, ShouldBeTrue)

			job, err = jq.GetByEssence(&JobEssence{Cmd: job1Cmd}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.Exited, ShouldBeTrue)
			So(job.State, ShouldEqual, JobStateBuried)

			ok := jq.ShutdownServer()
			So(ok, ShouldBeTrue)

			err = jq.Disconnect()
			So(err, ShouldBeNil)

			serverConfig.dontWipeDevDB = true
			server, _, token, errs = serve(ctx, serverConfig)
			serverConfig.dontWipeDevDB = false

			So(errs, ShouldBeNil)

			jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			job, err = jq.GetByEssence(&JobEssence{Cmd: job1Cmd}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.Exited, ShouldBeTrue)
			So(job.State, ShouldEqual, JobStateBuried)
		})

		Reset(func() {
			server.Stop(ctx, true)
		})
	})
}

func TestJobqueueRunners(t *testing.T) {
	ctx := context.Background()

	if servermode {
		return
	}

	runtime.GOMAXPROCS(runtime.NumCPU())

	if runnermode {
		// we have a full test of Serve() below that needs a client executable;
		// we say this test script is that exe, and when --runnermode is passed
		// to us we skip all tests and just act like a runner
		runner(ctx)

		return
	}

	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)

	// start these tests anew because these tests have the server spawn runners
	Convey("Once a new jobqueue server is up", t, func() {
		serverConfig.Timings.ItemTTR = 10 * time.Second
		serverConfig.Timings.CheckRunnerTime = 10 * time.Second
		serverConfig.Timings.TouchInterval = 50 * time.Millisecond
		runnertmpdir := t.TempDir()

		// our runnerCmd will be running ourselves in --runnermode, so first
		// we'll compile ourselves to the tmpdir
		runnerCmd, err := copyCompiledSelf(filepath.Join(runnertmpdir, "runner"))
		if err != nil {
			log.Fatal(err)
		}

		runningConfig := serverConfig
		rmd := strings.TrimSuffix(config.ManagerDir, "_"+config.Deployment)
		runningConfig.RunnerCmd = runnerCmd +
			" --runnermode --schedgrp '%s' --rdeployment %s --rserver '%s' --rdomain %s" +
			" --rtimeout %d --maxmins %d --rmanagerdir " + rmd + " --tmpdir " + runnertmpdir
		server, _, token, errs := serve(ctx, runningConfig)
		So(errs, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		maxCPU := runtime.NumCPU()
		runtime.GOMAXPROCS(maxCPU)

		Convey("You can connect, and add a job and then manually kill both the runner and process", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			// this leaf waits for a killed job to be detected as lost, which
			// happens ~TTR after its last touch. The group-wide TTR (10s) only
			// needs to be that high so jobs survive scheduling load in the other
			// leaves; here we shorten just this server's TTR (it takes effect
			// for jobs queued after this point) to speed up the lost detection.
			server.SetItemTTR(3 * time.Second)

			cmd := "perl -e 'for (1..20) { sleep(1) }'"
			jobs := []*Job{{
				Cmd:          cmd,
				Cwd:          testCwd,
				ReqGroup:     reqGroupSleep,
				Requirements: &jqs.Requirements{RAM: 1, Time: 20 * time.Second, Cores: 1},
				Retries:      uint8(0),
				Override:     uint8(2),
				RepGroup:     manuallyAdded,
			}}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			// wait for the job process to start running
			waitForStartedJobPID := func() int {
				limit := time.After(30 * time.Second)

				ticker := time.NewTicker(50 * time.Millisecond)
				defer ticker.Stop()

				for {
					select {
					case <-ticker.C:
						jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateRunning, false, false)
						if err != nil {
							continue
						}

						if len(jobs) == 1 && jobs[0].Pid > 0 && !jobs[0].StartTime.IsZero() {
							if errp := syscall.Kill(jobs[0].Pid, 0); errp == nil {
								return jobs[0].Pid
							}
						}

					case <-limit:
						jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, "", true, false)
						timelimitDebug(jobs, err)

						return 0
					}
				}
			}
			jobPID := waitForStartedJobPID()
			So(jobPID, ShouldNotEqual, 0)

			if jobPID == 0 {
				return
			}

			jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateRunning, false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].Pid, ShouldEqual, jobPID)

			lostJobCheckRetry := 2 * time.Second

			// initially, we force us to fail to be able to check if the job
			// is really dead or not, so that we can test this scenario
			server.SetLostJobCheckTimeout(1 * time.Nanosecond)
			server.SetLostJobCheckRetryTime(lostJobCheckRetry)

			defer func() {
				server.SetLostJobCheckTimeout(5 * time.Second)
				server.SetLostJobCheckRetryTime(1 * time.Hour)
			}()

			pgid, err := syscall.Getpgid(jobPID)
			So(err, ShouldBeNil)

			if err != nil {
				t.Logf("get process group failed for pid %d: %s", jobPID, err)

				return
			}

			err = syscall.Kill(-pgid, syscall.SIGKILL)
			So(err, ShouldBeNil)

			// wait for the job to become lost and then buried
			killed := make(chan bool, 1)
			checkLost := true

			var timeToBury time.Duration

			lostStatePollInterval := 50 * time.Millisecond

			go func() {
				var lostTime time.Time

				limit := time.After(8 * time.Second) // this server's TTR was shortened to 3s above
				ticker := time.NewTicker(lostStatePollInterval)
				markLostJobSeen := func() bool {
					jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateLost, false, false)
					if err != nil || len(jobs) != 1 {
						return false
					}

					checkLost = false
					lostTime = time.Now()

					// re-enable our ability to check the job is really dead
					jobs[0].Lock()
					server.SetLostJobCheckTimeout(5 * time.Second)
					jobs[0].Unlock()

					return true
				}

				for {
					select {
					case <-ticker.C:
						if checkLost && !markLostJobSeen() {
							continue
						}

						jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateBuried, false, false)
						if err != nil {
							continue
						}

						if len(jobs) == 1 {
							ticker.Stop()

							timeToBury = time.Since(lostTime)

							killed <- true

							return
						}

						continue
					case <-limit:
						ticker.Stop()

						jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, "", true, false)
						timelimitDebug(jobs, err)

						killed <- false

						return
					}
				}
			}()

			So(<-killed, ShouldBeTrue)

			jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateBuried)
			So(jobs[0].FailReason, ShouldEqual, FailReasonLost)
			So(jobs[0].Exitcode, ShouldEqual, -1)
			So(timeToBury, ShouldBeGreaterThanOrEqualTo, lostJobCheckRetry-(2*lostStatePollInterval))
		})

		Convey("You can connect, and add some jobs where reserved resources depend on override", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			tmpdir := t.TempDir()

			zeroReq := &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 0}
			fallocateJob := func(cmd string, req *jqs.Requirements, override uint8, repGroup string) *Job {
				return &Job{
					Cmd:          cmd,
					Cwd:          tmpdir,
					ReqGroup:     reqGroupFallocate,
					Requirements: req,
					Retries:      uint8(0),
					Override:     override,
					RepGroup:     repGroup,
				}
			}

			jobs := make([]*Job, 0, 5)
			jobs = append(jobs, fallocateJob("fallocate -l 200M foo && echo 1", zeroReq, 2, reqGroupFallocate))
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			// run the first job by itself, so learning occurs (even when disk
			// is 0 and override is 2)
			waitToFinish := func() bool {
				done := make(chan bool, 1)

				go func() {
					limit := time.After(10 * time.Second)
					ticker := time.NewTicker(500 * time.Millisecond)
					for {
						select {
						case <-ticker.C:
							if !server.HasRunners(ctx) {
								ticker.Stop()

								done <- true
								return
							}
							continue
						case <-limit:
							ticker.Stop()

							done <- false
							return
						}
					}
				}()

				return <-done
			}

			So(waitToFinish(), ShouldBeTrue)

			complete, errj := jq.GetByRepGroup(reqGroupFallocate, false, 0, JobStateComplete, false, false)
			So(errj, ShouldBeNil)
			So(len(complete), ShouldEqual, 1)
			So(complete[0].Requirements, ShouldResemble, zeroReq)
			So(complete[0].PeakDisk, ShouldEqual, 200)

			// add 3 similar jobs that only really differ in override behaviour
			jobs = append(jobs,
				fallocateJob("fallocate -l 200M foo && echo 2", zeroReq, 0, "learns"),
				fallocateJob("fallocate -l 200M foo && echo 3", zeroReq, 2, "learnsDiskNotMem"),
			)
			// following is the main test: specifying Disk of 0 and override 2
			// should result in 0 overriding learned value, even though its a
			// zero value, if DiskSet is true
			notOverrideReq := &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 0, Disk: 0}
			overrideReq := &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 0, Disk: 0, DiskSet: true}

			jobs = append(jobs,
				fallocateJob("fallocate -l 200M foo && echo 4", notOverrideReq, 2, "learnsDiskNotMem2"),
				fallocateJob("fallocate -l 200M foo && echo 5", overrideReq, 2, "nolearning"),
			)

			inserts, already, err = jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 4)
			So(already, ShouldEqual, 1)

			So(waitToFinish(), ShouldBeTrue)

			complete, errj = jq.GetByRepGroup("learns", false, 0, JobStateComplete, false, false)
			So(errj, ShouldBeNil)
			So(len(complete), ShouldEqual, 1)
			So(complete[0].Requirements, ShouldNotResemble, zeroReq)
			So(complete[0].Requirements.Disk, ShouldEqual, 1)
			So(complete[0].Requirements.RAM, ShouldEqual, 100)
			So(complete[0].PeakDisk, ShouldEqual, 200)

			complete, errj = jq.GetByRepGroup("learnsDiskNotMem", false, 0, JobStateComplete, false, false)
			So(errj, ShouldBeNil)
			So(len(complete), ShouldEqual, 1)
			So(complete[0].Requirements, ShouldNotResemble, zeroReq)
			So(complete[0].Requirements.Disk, ShouldEqual, 1)
			So(complete[0].Requirements.RAM, ShouldEqual, 1)
			So(complete[0].PeakDisk, ShouldEqual, 200)

			complete, errj = jq.GetByRepGroup("learnsDiskNotMem2", false, 0, JobStateComplete, false, false)
			So(errj, ShouldBeNil)
			So(len(complete), ShouldEqual, 1)
			So(complete[0].Requirements, ShouldNotResemble, zeroReq)
			So(complete[0].Requirements.Disk, ShouldEqual, 1)
			So(complete[0].Requirements.RAM, ShouldEqual, 1)
			So(complete[0].PeakDisk, ShouldEqual, 200)

			complete, errj = jq.GetByRepGroup("nolearning", false, 0, JobStateComplete, false, false)
			So(errj, ShouldBeNil)
			So(len(complete), ShouldEqual, 1)
			So(complete[0].Requirements, ShouldResemble, overrideReq)
			So(complete[0].PeakDisk, ShouldEqual, 200)
		})

		Convey("You can connect, and add a job that you can kill while it's running", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			cmd := "perl -e 'for (1..20) { sleep(1) }'"
			jobs := []*Job{{
				Cmd:          cmd,
				Cwd:          testCwd,
				ReqGroup:     reqGroupSleep,
				Requirements: &jqs.Requirements{RAM: 1, Time: 20 * time.Second, Cores: 1},
				Retries:      uint8(0),
				Override:     uint8(2),
				RepGroup:     manuallyAdded,
			}}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			// wait for the job to start running
			started := make(chan bool, 1)

			go func() {
				limit := time.After(10 * time.Second)
				ticker := time.NewTicker(50 * time.Millisecond)

				for {
					select {
					case <-ticker.C:
						jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateRunning, false, false)
						if err != nil {
							continue
						}

						if len(jobs) == 1 {
							ticker.Stop()

							started <- true

							return
						}

						continue
					case <-limit:
						ticker.Stop()

						started <- false

						return
					}
				}
			}()

			So(<-started, ShouldBeTrue)
			So(len(jobs), ShouldEqual, 1)

			killCount, err := jq.Kill([]*JobEssence{{JobKey: jobs[0].Key()}})
			So(err, ShouldBeNil)
			So(killCount, ShouldEqual, 1)

			// wait for the job to get killed
			killed := make(chan bool, 1)

			go func() {
				limit := time.After(40 * time.Second)
				ticker := time.NewTicker(50 * time.Millisecond)

				for {
					select {
					case <-ticker.C:
						jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateBuried, false, false)
						if err != nil {
							continue
						}

						if len(jobs) == 1 {
							ticker.Stop()

							killed <- true

							return
						}

						continue
					case <-limit:
						ticker.Stop()

						jobs, err = jq.GetByRepGroup("manually_added", false, 0, "", true, false)
						timelimitDebug(jobs, err)

						killed <- false

						return
					}
				}
			}()

			So(<-killed, ShouldBeTrue)

			jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateBuried)
			So(jobs[0].FailReason, ShouldEqual, FailReasonKilled)
			So(jobs[0].Exitcode, ShouldEqual, -1)
		})

		Convey("You can connect, and add some real jobs", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			tmpdir := t.TempDir()

			count := maxCPU * 2
			jobs := make([]*Job, 0, count)

			for i := 0; i < count; i++ {
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("perl -e 'open($fh, q[>%d]); print $fh q[foo]; close($fh)'", i), Cwd: tmpdir, ReqGroup: reqGroupPerl, Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), Override: 2, RepGroup: manuallyAdded}) //nolint:lll
			}

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, count)
			So(already, ShouldEqual, 0)

			Convey("After some time the jobs get automatically run", func() {
				// wait for the jobs to get run
				done := make(chan bool, 1)
				go func() {
					limit := time.After(30 * time.Second)
					ticker := time.NewTicker(500 * time.Millisecond)
					for {
						select {
						case <-ticker.C:
							if !server.HasRunners(ctx) {
								ticker.Stop()

								done <- true

								return
							}
							continue
						case <-limit:
							ticker.Stop()
							done <- false
							return
						}
					}
				}()

				So(<-done, ShouldBeTrue) // we shouldn't have hit our time limit

				jobs, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, count)
				ran := 0
				for _, job := range jobs {
					files, err := os.ReadDir(job.ActualCwd)
					if err != nil {
						log.Fatal(err)
					}
					for range files {
						ran++
					}
				}

				So(ran, ShouldEqual, count)

				// we shouldn't have executed any unnecessary runners, and those
				// we did run should have exited without error, even if there
				// were no more jobs left
				files, err := os.ReadDir(runnertmpdir)
				if err != nil {
					log.Fatal(err)
				}

				ranClean := 0
				for range files {
					ranClean++
				}

				So(ranClean, ShouldEqual, maxCPU+1) // +1 for the runner exe
			})
		})

		Reset(func() {
			if server != nil {
				server.Stop(ctx, true)
			}
		})
	})

	// start these tests anew because these tests have the server spawn runners
	// that fail, simulating some network issue
	Convey("Once a new jobqueue server is up with bad runners", t, func() {
		serverConfig.Timings.ItemTTR = 1 * time.Second
		serverConfig.Timings.CheckRunnerTime = 2 * time.Second
		serverConfig.Timings.TouchInterval = 50 * time.Millisecond
		runnertmpdir := t.TempDir()

		// our runnerCmd will be running ourselves in --runnermode, so first
		// we'll compile ourselves to the tmpdir
		runnerCmd, err := copyCompiledSelf(filepath.Join(runnertmpdir, "runner"))
		if err != nil {
			log.Fatal(err)
		}

		runningConfig := serverConfig
		rmd := strings.TrimSuffix(config.ManagerDir, "_"+config.Deployment)
		runningConfig.RunnerCmd = runnerCmd +
			" --runnermode --runnerfail --schedgrp '%s' --rdeployment %s --rserver '%s'" +
			" --rdomain %s --rtimeout %d --maxmins %d --rmanagerdir " + rmd +
			" --tmpdir " + runnertmpdir
		server, _, token, errs := serve(ctx, runningConfig)
		So(errs, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		Convey("You can connect, and add a job", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			tmpdir := t.TempDir()

			var jobs []*Job

			jobs = append(jobs, &Job{Cmd: "true", Cwd: tmpdir, ReqGroup: "true", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(0), Override: uint8(2), RepGroup: manuallyAdded}) //nolint:goconst,lll
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			Convey("After some time the manager will have tried to spawn runners more than once", func() {
				runnerCheck := func() (runners int) {
					files, errf := os.ReadDir(runnertmpdir)
					if errf != nil {
						log.Fatal(errf)
					}

					ranFailed := 0

					for _, file := range files {
						if !strings.HasPrefix(file.Name(), "fail") {
							continue
						}

						ranFailed++
					}

					return ranFailed
				}

				So(runnerCheck(), ShouldEqual, 0)

				hadRunner := make(chan bool, 1)
				go func() {
					limit := time.After(3 * time.Second)
					ticker := time.NewTicker(100 * time.Millisecond)
					for {
						select {
						case <-ticker.C:
							if server.HasRunners(ctx) {
								ticker.Stop()

								hadRunner <- true
								return
							}
							continue
						case <-limit:
							ticker.Stop()

							hadRunner <- false
							return
						}
					}
				}()

				So(<-hadRunner, ShouldBeTrue)

				// the failed runner releases its job back to ready, and the
				// manager keeps retrying; poll for these instead of assuming fixed
				// timings, which flake when the box is under heavy load.
				So(pollUntil(func() bool {
					jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateReady, false, false)

					return err == nil && len(jobs) == 1
				}), ShouldBeTrue)

				// the manager spawns (and fails) runners more than once
				So(pollUntil(func() bool { return runnerCheck() >= 2 }), ShouldBeTrue)

				err = server.Drain(ctx)
				So(err, ShouldBeNil)
				So(pollUntil(func() bool { return !server.HasRunners(ctx) }), ShouldBeTrue)
			})
		})

		Reset(func() {
			if server != nil {
				server.Stop(ctx, true)
			}
		})
	})
}

func TestJobqueueWithOpenStack(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	osPrefix := os.Getenv("OS_OS_PREFIX")
	osUser := os.Getenv("OS_OS_USERNAME")
	localUser := os.Getenv("OS_LOCAL_USERNAME")
	flavorRegex := os.Getenv("OS_FLAVOR_REGEX")

	host, err := os.Hostname()
	if err != nil || !strings.HasPrefix(host, "wr-dev-"+localUser) || osPrefix == "" || osUser == "" || flavorRegex == "" {
		SkipConvey("Skipping the OpenStack tests", t, func() {})

		return
	}

	restoreTimingGlobals := captureTimingGlobals()
	defer restoreTimingGlobals()

	ServerInterruptTime = 10 * time.Millisecond
	ServerReserveTicker = 10 * time.Millisecond
	ClientReleaseDelayMin = 100 * time.Millisecond
	clientConnectTime := 10 * time.Second
	ServerItemTTR = 1 * time.Second

	var server *Server
	var token []byte
	var errs error

	config := internal.ConfigLoadFromParentDir(ctx, internal.Development)

	addr := "localhost:" + config.ManagerPort

	setDomainIP(config.ManagerCertDomain)

	runnertmpdir := t.TempDir()

	// our runnerCmd will be running ourselves in --runnermode, so first
	// we'll compile ourselves to the tmpdir
	runnerCmd, err := copyCompiledSelf(filepath.Join(runnertmpdir, "runner"))
	if err != nil {
		log.Fatal(err)
	}

	resourceName := "wr-testing-" + localUser
	cloudConfig := &jqs.ConfigOpenStack{
		ResourceName:         resourceName,
		OSPrefix:             osPrefix,
		OSUser:               osUser,
		OSRAM:                2048,
		FlavorRegex:          flavorRegex,
		FlavorSets:           os.Getenv("OS_FLAVOR_SETS"),
		ServerPorts:          []int{22},
		ServerKeepTime:       3 * time.Second,
		StateUpdateFrequency: 1 * time.Second,
		Shell:                "bash",
		MaxInstances:         -1,
		Umask:                config.ManagerUmask,
	}
	cloudConfig.AddConfigFile(config.ManagerTokenFile + ":~/.wr_" + config.Deployment + "/client.token")
	if config.ManagerCAFile != "" {
		cloudConfig.AddConfigFile(config.ManagerCAFile + ":~/.wr_" + config.Deployment + "/ca.pem")
	}

	osConfig := ServerConfig{
		Port:            config.ManagerPort,
		WebPort:         config.ManagerWeb,
		SchedulerName:   "openstack",
		SchedulerConfig: cloudConfig,
		UploadDir:       config.ManagerUploadDir,
		DBFile:          config.ManagerDBFile,
		DBFileBackup:    config.ManagerDBBkFile,
		TokenFile:       config.ManagerTokenFile,
		CAFile:          config.ManagerCAFile,
		CertFile:        config.ManagerCertFile,
		CertDomain:      config.ManagerCertDomain,
		KeyFile:         config.ManagerKeyFile,
		Deployment:      config.Deployment,
		RunnerCmd: runnerCmd +
			" --runnermode --schedgrp '%s' --rdeployment %s --rserver '%s' --rdomain %s" +
			" --rtimeout %d --maxmins %d --rmanagerdir " +
			strings.TrimSuffix(config.ManagerDir, "_"+config.Deployment) +
			" --tmpdir " + runnertmpdir,
	}

	dockerInstallScript := `sudo mkdir -p /etc/docker/
sudo bash -c "echo '{ \"bip\": \"192.168.3.3/24\", \"dns\": [\"8.8.8.8\",\"8.8.4.4\"], \"mtu\": 1380 }' > /etc/docker/daemon.json"
sudo DEBIAN_FRONTEND=noninteractive apt-get -yq update
sudo DEBIAN_FRONTEND=noninteractive apt-get -y install apt-transport-https ca-certificates curl gnupg lsb-release && >&2 echo installed deps
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg
echo "deb [arch=amd64 signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo DEBIAN_FRONTEND=noninteractive apt-get -yq update
sudo >&2 apt-get -y install docker-ce docker-ce-cli containerd.io && >&2 echo installed docker
sudo usermod -aG docker ` + osUser

	Convey("You can connect with an OpenStack scheduler", t, func() {
		server, _, token, errs = serve(ctx, osConfig)
		So(errs, ShouldBeNil)
		defer func() {
			<-time.After(1 * time.Second) // give runners a chance to exit to avoid extraneous warnings
			server.Stop(ctx, true)
		}()

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)
		defer disconnect(jq)

		waitRun := func(done chan bool) {
			limit := time.After(maxSpawnTime)
			ticker := time.NewTicker(1 * time.Second)
			for {
				select {
				case <-ticker.C:
					got, errg := jq.GetIncomplete(0, JobStateBuried, false, false)
					if errg != nil {
						fmt.Printf("GetIncomplete failed: %s\n", errg)
					}
					if len(got) == 1 {
						ticker.Stop()
						done <- true
						return
					}
					continue
				case <-limit:
					ticker.Stop()
					done <- false
					return
				}
			}
		}

		Convey("You can add a job that runs on localhost", func() {
			buff := clog.ToBufferAtLevel("debug")
			tmpdir := t.TempDir()

			zeroReq := &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 0}

			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo 1", Cwd: tmpdir, ReqGroup: "test1", Requirements: zeroReq, Retries: uint8(0), Override: uint8(2), RepGroup: "chain", DepGroups: []string{"1"}})

			insert, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(insert, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			done := make(chan bool, 1)
			go func() {
				limit := time.After(30 * time.Second)
				ticker := time.NewTicker(500 * time.Millisecond)
				for {
					select {
					case <-ticker.C:
						if !server.HasRunners(ctx) {
							ticker.Stop()
							done <- true
							return
						}
						continue
					case <-limit:
						ticker.Stop()
						done <- false
						return
					}
				}
			}()
			So(<-done, ShouldBeTrue)

			jobs, err = jq.GetByRepGroup("chain", false, 0, JobStateComplete, false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)

			for _, m := range strings.Split(buff.String(), "\n") {
				if strings.Contains(m, "server allocate") {
					So(m, ShouldContainSubstring, "serverid=localhost")
				}

				if strings.Contains(m, "server release") {
					So(m, ShouldContainSubstring, "serverid=localhost")
				}
			}
		})

		Convey("You can add a chain of jobs that run quickly one after the other", func() {
			tmpdir := t.TempDir()

			zeroReq := &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 0}
			oneReq := &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}
			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo 1", Cwd: tmpdir, ReqGroup: "test1", Requirements: zeroReq, Retries: uint8(0), Override: uint8(2), RepGroup: "chain", DepGroups: []string{"1"}})
			d1 := NewDepGroupDependency("1")
			jobs = append(jobs, &Job{Cmd: "echo 2", Cwd: tmpdir, ReqGroup: "test2", Requirements: oneReq, Retries: uint8(0), Override: uint8(2), RepGroup: "chain", DepGroups: []string{"2"}, Dependencies: Dependencies{d1}})
			d2 := NewDepGroupDependency("2")
			jobs = append(jobs, &Job{Cmd: "echo 3", Cwd: tmpdir, ReqGroup: "test3", Requirements: zeroReq, Retries: uint8(0), Override: uint8(2), RepGroup: "chain", DepGroups: []string{"3"}, Dependencies: Dependencies{d2}})

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 3)
			So(already, ShouldEqual, 0)

			// wait for the jobs to get run
			done := make(chan bool, 1)
			go func() {
				limit := time.After(30 * time.Second)
				ticker := time.NewTicker(500 * time.Millisecond)
				for {
					select {
					case <-ticker.C:
						if !server.HasRunners(ctx) {
							ticker.Stop()
							done <- true
							return
						}
						continue
					case <-limit:
						ticker.Stop()
						done <- false
						return
					}
				}
			}()
			So(<-done, ShouldBeTrue)

			jobs, err = jq.GetByRepGroup("chain", false, 0, JobStateComplete, false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 3)
			var e1, s2, e2, s3 time.Time
			for _, job := range jobs {
				So(job.State, ShouldEqual, JobStateComplete)
				switch job.Cmd {
				case "echo 1":
					e1 = job.EndTime
				case "echo 2":
					s2 = job.StartTime
					e2 = job.EndTime
				case "echo 3":
					s3 = job.StartTime
				}
			}
			// (the below used to be over a second ; these tests show we
			//  improved the behaviour and now react instantly)
			So(s2.Sub(e1), ShouldBeLessThan, 150*time.Millisecond)
			So(s3.Sub(e2), ShouldBeLessThan, 150*time.Millisecond)
		})

		Convey("You can modify cloud_config_files of a job", func() {
			var jobs []*Job
			other := make(map[string]string)

			rg := "ccfmod"
			ccfmodPath := "/tmp/ccfmod"
			_, erro := os.OpenFile(ccfmodPath, os.O_RDONLY|os.O_CREATE, 0o666)
			So(erro, ShouldBeNil)
			defer func() {
				errr := os.Remove(ccfmodPath)
				So(errr, ShouldBeNil)
			}()
			cores := float64(runtime.NumCPU() + 1) // ensure the job doesn't run on this instance
			jobs = append(jobs, &Job{Cmd: "ls " + ccfmodPath, Cwd: "/tmp", ReqGroup: "rg", Requirements: &jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: cores, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: rg})

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			done := make(chan bool, 1)
			waitRun(done)
			So(<-done, ShouldBeTrue)

			got, err := jq.GetByRepGroup(rg, false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)

			jm := NewJobModifer()
			other = make(map[string]string)
			other["cloud_config_files"] = ccfmodPath
			jm.SetRequirements(&jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: cores, Other: other, OtherSet: true})
			got, err = jq.GetByRepGroup(rg, false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			jes := jobsToJobEssenses(got)
			modified, err := jq.Modify(jes, jm)
			So(err, ShouldBeNil)
			So(len(modified), ShouldEqual, 1)

			kicked, err := jq.Kick(jes)
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			// now that the "config" file is copied to where we're trying to
			// ls, the job should complete
			go func() {
				limit := time.After(maxSpawnTime)
				ticker := time.NewTicker(1 * time.Second)
				for {
					select {
					case <-ticker.C:
						got, errg := jq.GetByRepGroup(rg, false, 0, JobStateComplete, false, false)
						if errg != nil {
							fmt.Printf("GetIncomplete failed: %s\n", errg)
						}
						if len(got) == 1 {
							ticker.Stop()
							done <- true
							return
						}
						continue
					case <-limit:
						ticker.Stop()
						done <- false
						return
					}
				}
			}()
			So(<-done, ShouldBeTrue)
		})

		Convey("You can modify cloud_script of a job", func() {
			var jobs []*Job
			other := make(map[string]string)

			rg := "scmod"
			csmodPath := "/tmp/csmod"
			jobs = append(jobs, &Job{Cmd: "ls " + csmodPath, Cwd: "/tmp", ReqGroup: "rg", Requirements: &jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: float64(1), Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: rg})

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			done := make(chan bool, 1)
			waitRun(done)
			So(<-done, ShouldBeTrue)

			got, err := jq.GetByRepGroup(rg, false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)

			jm := NewJobModifer()
			other = make(map[string]string)
			other["cloud_script"] = "touch " + csmodPath
			jm.SetRequirements(&jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: float64(1), Other: other, OtherSet: true})
			got, err = jq.GetByRepGroup(rg, false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			jes := jobsToJobEssenses(got)
			modified, err := jq.Modify(jes, jm)
			So(err, ShouldBeNil)
			So(len(modified), ShouldEqual, 1)

			kicked, err := jq.Kick(jes)
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			// now that the cloud script touches the file we're trying to
			// ls, the job should complete
			go func() {
				limit := time.After(maxSpawnTime)
				ticker := time.NewTicker(1 * time.Second)
				for {
					select {
					case <-ticker.C:
						got, errg := jq.GetByRepGroup(rg, false, 0, JobStateComplete, false, false)
						if errg != nil {
							fmt.Printf("GetIncomplete failed: %s\n", errg)
						}
						if len(got) == 1 {
							ticker.Stop()
							done <- true
							return
						}
						continue
					case <-limit:
						ticker.Stop()
						done <- false
						return
					}
				}
			}()
			So(<-done, ShouldBeTrue)
		})

		Convey("You can modify cloud_flavor of a job", func() {
			var jobs []*Job
			other := make(map[string]string)

			cores := runtime.NumCPU()
			p, err := cloud.New(ctx, "openstack", resourceName, filepath.Join(runnertmpdir, "os_resources"))
			So(err, ShouldBeNil)
			flavor, err := p.CheapestServerFlavor(ctx, cores, 2048, flavorRegex)
			So(err, ShouldBeNil)
			flavor, err = p.CheapestServerFlavor(ctx, flavor.Cores+1, 2048, flavorRegex)
			So(err, ShouldBeNil)
			coresMore := flavor.Cores

			rg := "rg"
			jobs = append(jobs, &Job{Cmd: "getconf _NPROCESSORS_ONLN && false", Cwd: "/tmp", ReqGroup: "rg", Requirements: &jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: float64(cores), Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: rg})

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			// wait for the job to run
			done := make(chan bool, 1)
			waitRun(done)
			So(<-done, ShouldBeTrue)

			got, err := jq.GetByRepGroup(rg, false, 0, JobStateBuried, true, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
			stdout, err := got[0].StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldEqual, fmt.Sprintf("%d", cores))

			jm := NewJobModifer()
			other = make(map[string]string)
			other["cloud_flavor"] = flavor.Name
			jm.SetRequirements(&jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: float64(cores), Other: other, OtherSet: true})
			got, err = jq.GetByRepGroup(rg, false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			jes := jobsToJobEssenses(got)
			modified, err := jq.Modify(jes, jm)
			So(err, ShouldBeNil)
			So(len(modified), ShouldEqual, 1)

			kicked, err := jq.Kick(jes)
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			waitRun(done)
			So(<-done, ShouldBeTrue)

			got, err = jq.GetByRepGroup(rg, false, 0, JobStateBuried, true, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
			stdout, err = got[0].StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldEqual, fmt.Sprintf("%d", coresMore))

			jm = NewJobModifer()
			other = make(map[string]string)
			jm.SetRequirements(&jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: float64(cores), Other: other, OtherSet: true})
			got, err = jq.GetByRepGroup(rg, false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			jes = jobsToJobEssenses(got)
			modified, err = jq.Modify(jes, jm)
			So(err, ShouldBeNil)
			So(len(modified), ShouldEqual, 1)

			<-time.After(4 * time.Second) // wait for the flavor.Name node to terminate, or the job in next test might run on it randomly

			kicked, err = jq.Kick(jes)
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			waitRun(done)
			So(<-done, ShouldBeTrue)

			got, err = jq.GetByRepGroup(rg, false, 0, JobStateBuried, true, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
			stdout, err = got[0].StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldEqual, fmt.Sprintf("%d", cores))
		})

		Convey("You can modify MonitorDocker of a job", func() {
			var jobs []*Job
			other := make(map[string]string)
			other["cloud_script"] = dockerInstallScript

			rg := "first_docker"
			jobs = append(jobs, &Job{Cmd: "docker run sendu/usememory:v1 && false", Cwd: "/tmp", ReqGroup: "docker", Requirements: &jqs.Requirements{RAM: 3, Time: 5 * time.Second, Cores: 1, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: rg})

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			// wait for the job to run
			done := make(chan bool, 1)
			waitRun(done)
			So(<-done, ShouldBeTrue)

			expectedRAM := 2000
			got, err := jq.GetByRepGroup(rg, false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
			So(got[0].PeakRAM, ShouldBeBetweenOrEqual, 1, 500)

			jm := NewJobModifer()
			jm.SetMonitorDocker("?")
			jes := jobsToJobEssenses(got)
			modified, err := jq.Modify(jes, jm)
			So(err, ShouldBeNil)
			So(len(modified), ShouldEqual, 1)

			kicked, err := jq.Kick(jes)
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			waitRun(done)
			So(<-done, ShouldBeTrue)

			got, err = jq.GetByRepGroup(rg, false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
			So(got[0].PeakRAM, ShouldBeGreaterThanOrEqualTo, expectedRAM)

			jm = NewJobModifer()
			jm.SetMonitorDocker("")
			modified, err = jq.Modify(jes, jm)
			So(err, ShouldBeNil)
			So(len(modified), ShouldEqual, 1)

			kicked, err = jq.Kick(jes)
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			waitRun(done)
			So(<-done, ShouldBeTrue)

			got, err = jq.GetByRepGroup(rg, false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
			So(got[0].PeakRAM, ShouldBeBetweenOrEqual, 1, 500)
		})

		Convey("You can run cmds that have fractional or 0 CPU requirements simultaneously on 1 CPU", func() {
			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "sleep 4 && echo 1", Cwd: "/tmp", ReqGroup: "sleep", Requirements: &jqs.Requirements{RAM: 1024, Time: 1 * time.Hour, Cores: 0.9, Disk: 0}, Retries: uint8(0), RepGroup: "fraction"})
			jobs = append(jobs, &Job{Cmd: "sleep 4 && echo 2", Cwd: "/tmp", ReqGroup: "sleep", Requirements: &jqs.Requirements{RAM: 1024, Time: 1 * time.Hour, Cores: 0.1, Disk: 0}, Retries: uint8(0), RepGroup: "fraction"})
			jobs = append(jobs, &Job{Cmd: "sleep 4 && echo 3", Cwd: "/tmp", ReqGroup: "sleep", Requirements: &jqs.Requirements{RAM: 1024, Time: 1 * time.Hour, Cores: 0, Disk: 0}, Retries: uint8(0), RepGroup: "fraction"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 3)
			So(already, ShouldEqual, 0)

			// wait for the jobs to get run
			done := make(chan bool, 1)
			var simultaneous int
			go func() {
				limit := time.After(10 * time.Second)
				ticker := time.NewTicker(50 * time.Millisecond)
				for {
					select {
					case <-ticker.C:
						if !server.HasRunners(ctx) {
							ticker.Stop()
							done <- true
							return
						}
						running, errj := jq.GetByRepGroup("fraction", false, 0, JobStateRunning, false, false)
						if errj == nil && len(running) > simultaneous {
							simultaneous = len(running)
						}
						continue
					case <-limit:
						ticker.Stop()
						done <- false
						return
					}
				}
			}()
			So(<-done, ShouldBeTrue)
			So(simultaneous, ShouldEqual, 3)
		})

		Convey("You can run cmds that start docker containers and get correct memory and cpu usage", func() {
			var jobs []*Job
			other := make(map[string]string)
			other["cloud_script"] = dockerInstallScript + "\necho 1"

			jobs = append(jobs, &Job{Cmd: "docker run sendu/usememory:v1", Cwd: "/tmp", ReqGroup: "docker", Requirements: &jqs.Requirements{RAM: 3, Time: 5 * time.Second, Cores: 1, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "first_docker", MonitorDocker: "?"})

			other = make(map[string]string)
			other["cloud_script"] = dockerInstallScript + "\necho 2"
			dockerName := "jobqueue_test." + internal.RandomString()
			jobs = append(jobs, &Job{Cmd: "docker run --name " + dockerName + " sendu/usememory:v1", Cwd: "/tmp", ReqGroup: "docker", Requirements: &jqs.Requirements{RAM: 3, Time: 5 * time.Second, Cores: 1, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "named_docker", MonitorDocker: dockerName})

			other = make(map[string]string)
			other["cloud_script"] = dockerInstallScript + "\necho 3"
			dockerCidFile := "jobqueue_test.cidfile"
			jobs = append(jobs, &Job{Cmd: "docker run --cidfile " + dockerCidFile + " sendu/usecpu:v1 && rm " + dockerCidFile, Cwd: "/tmp", ReqGroup: "docker2", Requirements: &jqs.Requirements{RAM: 1, Time: 5 * time.Second, Cores: 2, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "cidfile_docker", MonitorDocker: dockerCidFile})

			other = make(map[string]string)
			other["cloud_script"] = dockerInstallScript + "\necho 4"
			dockerCidFile = "uuid-20181127.cidfile"
			jobs = append(jobs, &Job{Cmd: "docker run --cidfile " + dockerCidFile + " sendu/usecpu:v1 && rm " + dockerCidFile, Cwd: "/tmp", ReqGroup: "docker2", Requirements: &jqs.Requirements{RAM: 1, Time: 5 * time.Second, Cores: 2, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "cidglob_docker", MonitorDocker: "uuid-*.cidfile"})

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 4)
			So(already, ShouldEqual, 0)

			// wait for the jobs to get run
			done := make(chan bool, 1)
			go func() {
				limit := time.After(maxSpawnTime)
				ticker := time.NewTicker(1 * time.Second)
				for {
					select {
					case <-ticker.C:
						if !server.HasRunners(ctx) {
							got, errg := jq.GetIncomplete(0, "", false, false)
							if errg != nil {
								fmt.Printf("GetIncomplete failed: %s\n", errg)
							}
							if len(got) == 0 {
								ticker.Stop()
								done <- true
								return
							}
						}
						continue
					case <-limit:
						ticker.Stop()
						done <- false
						return
					}
				}
			}()
			So(<-done, ShouldBeTrue)

			expectedRAM := 2000
			got, err := jq.GetByRepGroup("first_docker", false, 0, JobStateComplete, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
			So(got[0].PeakRAM, ShouldBeGreaterThanOrEqualTo, expectedRAM)
			So(got[0].WallTime(), ShouldBeBetweenOrEqual, 5*time.Second, 25*time.Second)
			So(got[0].CPUtime, ShouldBeLessThan, 5*time.Second)

			got, err = jq.GetByRepGroup("named_docker", false, 0, JobStateComplete, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
			So(got[0].PeakRAM, ShouldBeGreaterThanOrEqualTo, expectedRAM)

			got, err = jq.GetByRepGroup("cidfile_docker", false, 0, JobStateComplete, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
			So(got[0].PeakRAM, ShouldBeLessThan, 100)
			So(got[0].WallTime(), ShouldBeBetweenOrEqual, 5*time.Second, 25*time.Second)
			So(got[0].CPUtime, ShouldBeGreaterThan, 5*time.Second)

			got, err = jq.GetByRepGroup("cidglob_docker", false, 0, JobStateComplete, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
			So(got[0].PeakRAM, ShouldBeLessThan, 100)
			So(got[0].WallTime(), ShouldBeBetweenOrEqual, 5*time.Second, 25*time.Second)
			So(got[0].CPUtime, ShouldBeGreaterThan, 5*time.Second)

			// *** want to test that when we kill a running job, its docker
			// is also immediately killed...
		})

		Convey("You can run a cmd to get the memory and cpu usage when no docker containers are running", func() {
			var jobs []*Job

			Convey("when docker is not installed", func() {
				jobs = append(jobs, &Job{Cmd: "docker run sendu/usememory:v1", Cwd: "/tmp", ReqGroup: "docker", Requirements: &jqs.Requirements{RAM: 3, Time: 5 * time.Second, Cores: 1}, Override: uint8(2), Retries: uint8(0), RepGroup: "noDocker", MonitorDocker: "?"})

				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				// wait for the jobs to get run
				done := make(chan bool, 1)
				waitRun(done)

				got, err := jq.GetByRepGroup("noDocker", false, 0, JobStateComplete, false, false)
				So(len(got), ShouldBeZeroValue)
				So(err, ShouldBeNil)
			})

			Convey("when no relevant containers are running", func() {
				other := make(map[string]string)
				other["cloud_script"] = dockerInstallScript + "\necho 1"
				jobs = append(jobs, &Job{Cmd: "sleep 30", Cwd: "/tmp", ReqGroup: "nodocker", Requirements: &jqs.Requirements{RAM: 3, Time: 5 * time.Second, Cores: 1, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "no_docker", MonitorDocker: "?"})

				other = make(map[string]string)
				other["cloud_script"] = dockerInstallScript + "\necho 2"
				dockerName := "jobqueue_test." + internal.RandomString()
				wrongDockerName := internal.RandomString()
				jobs = append(jobs, &Job{Cmd: "docker run --name " + dockerName + " sendu/usememory:v1", Cwd: "/tmp", ReqGroup: "docker", Requirements: &jqs.Requirements{RAM: 3, Time: 5 * time.Second, Cores: 1, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "wrongnamed_docker", MonitorDocker: wrongDockerName})

				other = make(map[string]string)
				other["cloud_script"] = dockerInstallScript + "\necho 3"
				dockerCidFile := "jobqueue_test.cidfile"
				wrongDockerCidFile := "jobqueue_wrong.cidfile"
				jobs = append(jobs, &Job{Cmd: "docker run --cidfile " + dockerCidFile + " sendu/usecpu:v1 && rm " + dockerCidFile, Cwd: "/tmp", ReqGroup: "docker2", Requirements: &jqs.Requirements{RAM: 1, Time: 5 * time.Second, Cores: 2, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "wrongcidfile_docker", MonitorDocker: wrongDockerCidFile})

				other = make(map[string]string)
				other["cloud_script"] = dockerInstallScript + "\necho 4"
				dockerCidFile = "uuid-20181127.cidfile"
				wrongDockerUUID := internal.RandomString() + "*" + internal.RandomString()
				jobs = append(jobs, &Job{Cmd: "docker run --cidfile " + dockerCidFile + " sendu/usecpu:v1 && rm " + dockerCidFile, Cwd: "/tmp", ReqGroup: "docker2", Requirements: &jqs.Requirements{RAM: 1, Time: 5 * time.Second, Cores: 2, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "wrongcidglob_docker", MonitorDocker: wrongDockerUUID})

				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 4)
				So(already, ShouldEqual, 0)

				// wait for the jobs to get run
				done := make(chan bool, 1)
				waitRun(done)

				usedMinRAM := 100
				got, err := jq.GetByRepGroup("no_docker", false, 0, JobStateComplete, false, false)
				So(err, ShouldBeNil)
				So(len(got), ShouldEqual, 1)
				So(got[0].PeakRAM, ShouldBeLessThanOrEqualTo, usedMinRAM)
				So(got[0].CPUtime, ShouldBeLessThan, 5*time.Millisecond)

				got, err = jq.GetByRepGroup("wrongnamed_docker", false, 0, JobStateComplete, false, false)
				So(err, ShouldBeNil)
				So(len(got), ShouldEqual, 1)
				So(got[0].PeakRAM, ShouldBeLessThanOrEqualTo, usedMinRAM)
				So(got[0].CPUtime, ShouldBeLessThan, 100*time.Millisecond)

				got, err = jq.GetByRepGroup("wrongcidfile_docker", false, 0, JobStateComplete, false, false)
				So(err, ShouldBeNil)
				So(len(got), ShouldEqual, 1)
				So(got[0].PeakRAM, ShouldBeLessThanOrEqualTo, usedMinRAM)
				So(got[0].CPUtime, ShouldBeLessThan, 100*time.Millisecond)

				got, err = jq.GetByRepGroup("wrongcidglob_docker", false, 0, JobStateComplete, false, false)
				So(err, ShouldBeNil)
				So(len(got), ShouldEqual, 1)
				So(got[0].PeakRAM, ShouldBeLessThanOrEqualTo, usedMinRAM)
				So(got[0].CPUtime, ShouldBeLessThan, 100*time.Millisecond)
			})
		})

		Convey("You can run a cmd with a per-cmd set of config files", func() {
			// create a config file locally
			localConfigPath := filepath.Join(runnertmpdir, "test.config")
			configContent := []byte("myconfig\n")
			err := os.WriteFile(localConfigPath, configContent, 0o600)
			So(err, ShouldBeNil)

			// pretend the server is remote to us, and upload our config
			// file first
			remoteConfigPath, err := jq.UploadFile(localConfigPath, "")
			So(err, ShouldBeNil)
			home, herr := os.UserHomeDir()
			So(herr, ShouldBeNil)
			So(remoteConfigPath, ShouldEqual, filepath.Join(home, ".wr_development", "uploads", "4", "2", "5", "a65424cddbee3271f937530c6efc6"))

			// check the remote config file was saved properly
			content, err := os.ReadFile(remoteConfigPath)
			So(err, ShouldBeNil)
			So(content, ShouldResemble, configContent)

			defer func() {
				err = os.RemoveAll(filepath.Join(home, ".wr_development", "uploads"))
				So(err, ShouldBeNil)
			}()

			// create a job that cats a config file that should only exist
			// if the supplied cloud_config_files option worked. It then
			// fails so we can check the stdout afterwards.
			var jobs []*Job
			other := make(map[string]string)
			configPath := "~/.wr_test.config"
			other["cloud_config_files"] = remoteConfigPath + ":" + configPath
			jobs = append(jobs, &Job{Cmd: "cat " + configPath + " && false", Cwd: "/tmp", ReqGroup: "cat", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Hour, Cores: 1, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "with_config_file"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			// wait for the job to get run
			done := make(chan bool, 1)
			go func() {
				limit := time.After(maxSpawnTime)
				ticker := time.NewTicker(1 * time.Second)
				for {
					select {
					case <-ticker.C:
						if !server.HasRunners(ctx) {
							ticker.Stop()
							done <- true
							return
						}
						continue
					case <-limit:
						ticker.Stop()
						done <- false
						return
					}
				}
			}()
			So(<-done, ShouldBeTrue)

			got, err := jq.GetByRepGroup("with_config_file", false, 0, JobStateBuried, true, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
			stderr, err := got[0].StdErr()
			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "")
			stdout, err := got[0].StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldEqual, strings.TrimSuffix(string(configContent), "\n"))
		})

		Convey("You can run commands with different hardware requirements while dropping the count", func() {
			var jobs []*Job
			dropReq := &jqs.Requirements{RAM: 1024, Time: 1 * time.Hour, Cores: 1, Disk: 0}
			jobs = append(jobs, &Job{Cmd: "sleep 1", Cwd: "/tmp", ReqGroup: "sleep", Requirements: dropReq, Retries: uint8(3), RepGroup: "manually_added"})
			jobs = append(jobs, &Job{Cmd: "echo 2", Cwd: "/tmp", ReqGroup: "echo", Requirements: &jqs.Requirements{RAM: 2048, Time: 1 * time.Hour, Cores: 1}, Override: uint8(2), Retries: uint8(3), RepGroup: "manually_added"})
			jobs = append(jobs, &Job{Cmd: "echo 3", Cwd: "/tmp", ReqGroup: "echo", Requirements: &jqs.Requirements{RAM: 1024, Time: 1 * time.Hour, Cores: 2, Disk: 0}, Retries: uint8(3), RepGroup: "manually_added"})
			jobs = append(jobs, &Job{Cmd: "echo 4", Cwd: "/tmp", ReqGroup: "echo", Requirements: dropReq, Priority: uint8(255), Retries: uint8(3), RepGroup: "manually_added"})
			jobs = append(jobs, &Job{Cmd: "echo 5", Cwd: "/tmp", ReqGroup: "echo", Requirements: &jqs.Requirements{RAM: 1024, Time: 1 * time.Hour, Cores: 1, Disk: 20}, Retries: uint8(3), RepGroup: "manually_added"})
			count := 100
			for i := 6; i <= count; i++ {
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("echo %d", i), Cwd: "/tmp", ReqGroup: "sleep", Requirements: dropReq, Retries: uint8(3), RepGroup: "manually_added"})
			}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, count)
			So(already, ShouldEqual, 0)

			// wait for the jobs to get run
			done := make(chan bool, 1)
			go func() {
				limit := time.After(maxSpawnTime)
				ticker := time.NewTicker(1 * time.Second)
				for {
					select {
					case <-ticker.C:
						if !server.HasRunners(ctx) {
							ticker.Stop()
							done <- true
							return
						}
						continue
					case <-limit:
						ticker.Stop()
						done <- false
						return
					}
				}
			}()
			So(<-done, ShouldBeTrue)
		})

		Convey("The manager reacts correctly to spawned servers going down", func() {
			p, err := cloud.New(ctx, "openstack", resourceName, filepath.Join(runnertmpdir, "os_resources"))
			So(err, ShouldBeNil)

			// for this test to work, we need 1 job to run on another
			// server, so we need to use all the cores of this server per
			// job
			cores := runtime.NumCPU()

			flavor, err := p.CheapestServerFlavor(ctx, cores, 2048, flavorRegex)
			So(err, ShouldBeNil)

			destroyedBadServer := 0
			var dbsMutex sync.Mutex
			badServerCB := func(server *cloud.Server) {
				errf := server.Destroy(ctx)
				if errf == nil {
					dbsMutex.Lock()
					destroyedBadServer++
					dbsMutex.Unlock()
				}
			}

			server.scheduler.SetBadServerCallBack(ctx, badServerCB)

			var jobs []*Job
			req := &jqs.Requirements{RAM: 2048, Time: 1 * time.Hour, Cores: float64(cores), Disk: 0}
			schedGrp := fmt.Sprintf("%d:60:%f:0", flavor.RAM, float64(cores))
			jobs = append(jobs, &Job{Cmd: "sleep 300", Cwd: "/tmp", ReqGroup: "sleep", Requirements: req, Retries: uint8(1), Override: uint8(2), RepGroup: "sleep"})
			jobs = append(jobs, &Job{Cmd: "sleep 301", Cwd: "/tmp", ReqGroup: "sleep", Requirements: req, Retries: uint8(1), Override: uint8(2), RepGroup: "sleep"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			// wait for the jobs to start running
			started := make(chan bool, 1)
			waitForBothRunning := func() {
				limit := time.After(maxSpawnTime)
				ticker := time.NewTicker(1 * time.Second)
				for {
					select {
					case <-ticker.C:
						if server.HasRunners(ctx) {
							running, errf := jq.GetByRepGroup("sleep", false, 0, JobStateRunning, false, false)
							if errf != nil {
								ticker.Stop()
								started <- false
								return
							}
							complete, errf := jq.GetByRepGroup("sleep", false, 0, JobStateComplete, false, false)
							if errf != nil {
								ticker.Stop()
								started <- false
								return
							}
							if len(running)+len(complete) == 2 {
								ticker.Stop()
								started <- true
								return
							}
						}
						continue
					case <-limit:
						ticker.Stop()
						started <- false
						return
					}
				}
			}
			go waitForBothRunning()
			So(<-started, ShouldBeTrue)

			// pretend a server went down by manually terminating one of
			// them, while monitoring that we never request more than 2
			// runners, and that we eventually spawn exactly 1 new server
			// to get the killed job running again
			got, err := jq.GetByRepGroup("sleep", false, 0, JobStateRunning, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 2)

			moreThan2 := make(chan bool, 1)
			stopChecking := make(chan bool, 1)
			go func() {
				ticker := time.NewTicker(10 * time.Millisecond)
				for {
					select {
					case <-ticker.C:
						server.psgmutex.RLock()
						group, exists := server.previouslyScheduledGroups[schedGrp]
						if exists && group.count > 2 {
							ticker.Stop()
							moreThan2 <- true
							server.psgmutex.RUnlock()
							return
						}
						server.psgmutex.RUnlock()
					case <-stopChecking:
						ticker.Stop()
						moreThan2 <- false
						return
					}
				}
			}()

			destroyed := false
			var killedJobEssence *JobEssence
			for _, job := range got {
				if job.Host != host {
					So(job.HostID, ShouldNotBeBlank)
					So(job.HostIP, ShouldNotBeBlank)
					err = p.DestroyServer(ctx, job.HostID)
					So(err, ShouldBeNil)
					destroyed = true
					killedJobEssence = &JobEssence{JobKey: job.Key()}
					break
				}
			}
			So(destroyed, ShouldBeTrue)

			// wait for the killed job to be marked as lost and then release
			// it
			gotLost := make(chan bool, 1)
			go func() {
				limit := time.After(20 * time.Second)
				ticker := time.NewTicker(10 * time.Millisecond)
				for {
					select {
					case <-ticker.C:
						job, err := jq.GetByEssence(killedJobEssence, false, false)
						if err != nil {
							ticker.Stop()
							gotLost <- false
							return
						}
						if job.State == JobStateLost {
							ticker.Stop()
							e, err := server.killJob(ctx, killedJobEssence.JobKey)
							if !e || err != nil {
								gotLost <- false
							}
							gotLost <- true

							return
						}
					case <-limit:
						ticker.Stop()
						gotLost <- false

						return
					}
				}
			}()
			So(<-gotLost, ShouldBeTrue)

			// wait until they both start running again
			go waitForBothRunning()
			So(<-started, ShouldBeTrue)
			stopChecking <- true
			So(<-moreThan2, ShouldBeFalse)
			dbsMutex.Lock()
			So(destroyedBadServer, ShouldEqual, 1)
			dbsMutex.Unlock()
		})

		Reset(func() {
			if server != nil {
				<-time.After(1 * time.Second)
				server.Stop(ctx, true)
			}
		})
	})
}

func TestJobqueueWithMounts(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	if runtime.NumCPU() == 1 {
		// we lock up with only 1 proc
		runtime.GOMAXPROCS(2)
	}

	// for these tests to work, JOBQUEUE_REMOTES3_PATH must be the bucket name
	// and path to an S3 directory set up as per TestS3RemoteIntegration in
	// github.com/VertebrateResequencing/muxfys/s3_test.go. You must also have
	// an ~/.s3cfg file with a default section containing your s3 configuration.

	s3Path := os.Getenv("JOBQUEUE_REMOTES3_PATH")
	home, herr := os.UserHomeDir()
	if herr != nil {
		SkipConvey("home directory not known, so can't run tests")
		return
	}
	_, s3cfgErr := os.Stat(filepath.Join(home, ".s3cfg"))

	if s3Path == "" || s3cfgErr != nil {
		SkipConvey("Without the JOBQUEUE_REMOTES3_PATH environment variable and an ~/.s3cfg file, we'll skip jobqueue S3 tests", t, func() {})
		return
	}

	restoreTimingGlobals := captureTimingGlobals()
	defer restoreTimingGlobals()

	ServerInterruptTime = 10 * time.Millisecond
	ServerReserveTicker = 10 * time.Millisecond
	ClientReleaseDelayMin = 100 * time.Millisecond
	clientConnectTime := 10 * time.Second
	ServerItemTTR = 10 * time.Second

	config := internal.ConfigLoadFromParentDir(ctx, internal.Development)
	addr := "localhost:" + config.ManagerPort
	serverConfig := ServerConfig{
		Port:            config.ManagerPort,
		WebPort:         config.ManagerWeb,
		SchedulerName:   localSchedulerName,
		SchedulerConfig: &jqs.ConfigLocal{Shell: config.RunnerExecShell},
		DBFile:          config.ManagerDBFile,
		DBFileBackup:    config.ManagerDBBkFile,
		TokenFile:       config.ManagerTokenFile,
		CAFile:          config.ManagerCAFile,
		CertFile:        config.ManagerCertFile,
		CertDomain:      config.ManagerCertDomain,
		KeyFile:         config.ManagerKeyFile,
		Deployment:      config.Deployment,
	}

	Convey("You can bring up a server configured with an S3 db backup", t, func() {
		s3ServerConfig := serverConfig
		s3ServerConfig.DBFileBackup = fmt.Sprintf("s3://default@%s/db.bk", s3Path)
		localBkPath := config.ManagerDBFile + ".s3backup_tmp"
		s3BkPath := filepath.Join(s3Path, "db.bk.development")
		s3BkPath, err := stripBucketFromS3Path(s3BkPath)
		So(err, ShouldBeNil)

		os.Remove(config.ManagerDBFile)

		s3ServerConfig.forceBackups = true
		server, _, token, errs := serve(ctx, s3ServerConfig)
		So(errs, ShouldBeNil)

		defer func() {
			// stop the server
			server.Stop(ctx, true)

			errd := server.db.s3accessor.DeleteFile(s3BkPath)
			if errd != nil {
				t.Logf("deleting s3 db backup failed: %s", errd)
			}
		}()

		_, err = os.Stat(config.ManagerDBFile)
		So(err, ShouldBeNil)
		_, err = os.Stat(localBkPath)
		So(err, ShouldNotBeNil)

		Convey("You can connect and add a job, which creates a db backup", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer disconnect(jq)

			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo 1", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			<-time.After(8 * time.Second)

			assertNonEmptyFile(config.ManagerDBFile)
			_, err = os.Stat(localBkPath)
			So(err, ShouldNotBeNil)

			err = server.db.s3accessor.DownloadFile(s3BkPath, localBkPath)
			So(err, ShouldBeNil)
			assertNonEmptyFile(localBkPath)
			assertBoltLiveJobs(localBkPath, 1)
			err = os.Remove(localBkPath)
			So(err, ShouldBeNil)

			Convey("You can stop the server, delete the database, and it will be restored from S3 backup", func() {
				jobsByRepGroup, err := jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 1)

				server.Stop(ctx, true)

				s3ServerConfig.dontWipeDevDB = true
				server, _, token, errs = serve(ctx, s3ServerConfig)
				So(errs, ShouldBeNil)
				defer server.Stop(ctx, true)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 1)

				server.Stop(ctx, true)
				os.Remove(config.ManagerDBFile)
				_, err = os.Stat(config.ManagerDBFile)
				So(err, ShouldNotBeNil)
				_, err = os.Stat(localBkPath)
				So(err, ShouldNotBeNil)

				server, _, token, errs = serve(ctx, s3ServerConfig)
				So(errs, ShouldBeNil)
				defer server.Stop(ctx, true)

				assertNonEmptyFile(config.ManagerDBFile)
				_, err = os.Stat(localBkPath)
				So(err, ShouldNotBeNil)

				err = server.db.s3accessor.DownloadFile(s3BkPath, localBkPath)
				So(err, ShouldBeNil)
				assertNonEmptyFile(localBkPath)
				assertBoltLiveJobs(localBkPath, 1)
				err = os.Remove(localBkPath)
				So(err, ShouldBeNil)

				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 1)
			})
		})
	})

	Convey("You can connect and run commands that rely on files in a remote S3 object store", t, func() {
		cwd := t.TempDir()

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		standardReqs := &jqs.Requirements{RAM: 10, Time: 10 * time.Second, Cores: 1, Disk: 0, Other: make(map[string]string)}

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)
		defer disconnect(jq)

		var jobs []*Job
		mcs := MountConfigs{
			{Targets: []MountTarget{
				{Path: s3Path, Cache: true},
				{Path: s3Path + "/sub/deep", Cache: true},
			}, Verbose: true},
		}
		b := &Behaviour{When: OnExit, Do: CleanupAll}
		bs := Behaviours{b}

		Convey("Commands can read remote data and the cache gets deleted afterwards", func() {
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt && cat bar", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "s3", MountConfigs: mcs, Behaviours: bs})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			job, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.RepGroup, ShouldEqual, "s3")

			// muxfys.SetLogHandler(log15.StderrHandler)
			jeerr := jq.Execute(ctx, job, config.RunnerExecShell)
			So(jeerr, ShouldBeNil)

			job, err = jq.GetByEssence(&JobEssence{Cmd: "cat numalphanum.txt && cat bar"}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldBeNil)

			job, err = jq.GetByEssence(&JobEssence{Cmd: "cat numalphanum.txt && cat bar", MountConfigs: mcs}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.State, ShouldEqual, JobStateComplete)

			// test that the cache dirs get deleted; this test fails if cwd is based
			// in an nfs mount, the problem confirmed upstream in muxfys but with no
			// solution apparent...
			So(job.ActualCwd, ShouldNotBeEmpty)
			_, err = os.Stat(job.ActualCwd)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)
			f, err := os.Open(job.Cwd)
			So(err, ShouldBeNil)
			defer f.Close()
			_, err = f.Readdirnames(100)
			So(err, ShouldEqual, io.EOF) // ie. the whole created working dir got wiped
		})

		t1 := MountTarget{Path: s3Path + "/sub", Cache: true}
		t2 := MountTarget{Path: s3Path, Cache: true}
		Convey("You can add identical commands with different mounts", func() {
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "a", MountConfigs: mcs, Behaviours: bs})

			mcs2 := MountConfigs{
				{Targets: []MountTarget{t1}, Verbose: true},
			}

			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "b", MountConfigs: mcs2, Behaviours: bs})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			mcs3 := MountConfigs{
				{Targets: []MountTarget{t1, t2}},
			}
			mcs4 := MountConfigs{
				{Targets: []MountTarget{t2, t1}},
			}

			jobs = nil
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "c", MountConfigs: mcs3})
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "d", MountConfigs: mcs4})
			inserts, already, err = jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			job, err := jq.GetByEssence(&JobEssence{Cmd: "cat numalphanum.txt", MountConfigs: mcs3}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.RepGroup, ShouldEqual, "c")
		})

		Convey("You can't add identical commands with the same mounts", func() {
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "a", MountConfigs: mcs, Behaviours: bs})
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "b", MountConfigs: mcs})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 1)

			mcs5 := MountConfigs{
				{Targets: []MountTarget{t1}, Verbose: true, Mount: "a"},
				{Targets: []MountTarget{t2}, Verbose: true, Mount: "b"},
			}
			mcs6 := MountConfigs{
				{Targets: []MountTarget{t2}, Verbose: true, Mount: "b"},
				{Targets: []MountTarget{t1}, Verbose: true, Mount: "a"},
			}

			jobs = nil
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "c", MountConfigs: mcs5})
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "d", MountConfigs: mcs6})
			inserts, already, err = jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 1)
		})

		Convey("You can modify the mounts", func() {
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "s3"})

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			job, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.RepGroup, ShouldEqual, "s3")

			jeerr := jq.Execute(ctx, job, config.RunnerExecShell)
			So(jeerr, ShouldNotBeNil)

			got, err := jq.GetByRepGroup("s3", false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)

			jm := NewJobModifer()
			jm.SetMountConfigs(MountConfigs{
				{Targets: []MountTarget{
					{Path: s3Path},
				}, Verbose: true},
			})
			got, err = jq.GetByRepGroup("s3", false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			jes := jobsToJobEssenses(got)
			modified, err := jq.Modify(jes, jm)
			So(err, ShouldBeNil)
			So(len(modified), ShouldEqual, 1)

			got, err = jq.GetByRepGroup("s3", false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			jes = jobsToJobEssenses(got)
			kicked, err := jq.Kick(jes)
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			job, err = jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.RepGroup, ShouldEqual, "s3")

			jeerr = jq.Execute(ctx, job, config.RunnerExecShell)
			So(jeerr, ShouldBeNil)

			got, err = jq.GetByRepGroup("s3", false, 0, JobStateComplete, false, false)
			So(err, ShouldBeNil)
			So(len(got), ShouldEqual, 1)
		})

		Reset(func() {
			if server != nil {
				server.Stop(ctx, true)
			}
		})
	})
}

func TestJobqueueSpeed(t *testing.T) {
	if runnermode || servermode {
		return
	}

	if true { // testing.Short()
		t.Skip("skipping speed test")
	}

	ctx := context.Background()
	config := internal.ConfigLoadFromParentDir(ctx, "development")
	serverConfig := ServerConfig{
		Port:            config.ManagerPort,
		WebPort:         config.ManagerWeb,
		SchedulerName:   localSchedulerName,
		SchedulerConfig: &jqs.ConfigLocal{Shell: config.RunnerExecShell},
		DBFile:          config.ManagerDBFile,
		DBFileBackup:    config.ManagerDBBkFile,
		TokenFile:       config.ManagerTokenFile,
		CAFile:          config.ManagerCAFile,
		CertFile:        config.ManagerCertFile,
		CertDomain:      config.ManagerCertDomain,
		KeyFile:         config.ManagerKeyFile,
		Deployment:      config.Deployment,
	}
	addr := "localhost:" + config.ManagerPort

	// some manual speed tests (don't like the way the benchmarking feature
	// works)
	runtime.GOMAXPROCS(runtime.NumCPU())
	n := 50000

	server, _, token, err := serve(ctx, serverConfig)
	if err != nil {
		log.Fatal(err)
	}

	clientConnectTime := 10 * time.Second
	jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	if err != nil {
		log.Fatal(err)
	}
	defer disconnect(jq)

	before := time.Now()
	jobs := make([]*Job, 0, n)
	for i := 0; i < n; i++ {
		jobs = append(jobs, &Job{Cmd: fmt.Sprintf("test cmd %d", i), Cwd: "/fake/cwd", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 1024, Time: 4 * time.Hour, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
	}
	inserts, already, err := jq.Add(jobs, envVars, true)
	if err != nil {
		log.Fatal(err)
	}
	e := time.Since(before)
	per := e.Nanoseconds() / int64(n)
	log.Printf("Added %d jobqueue jobs (%d inserts, %d dups) in %s == %d per\n", n, inserts, already, e, per)

	err = jq.Disconnect()
	if err != nil {
		log.Fatal(err)
	}

	reserves := make(chan int, n)
	beginat := time.Now().Add(1 * time.Second)
	o := runtime.NumCPU() // from here up to 1650 the time taken is around 6-7s, but beyond 1675 it suddenly drops to 14s, and seems to just hang forever at much higher values
	m := int(math.Ceil(float64(n) / float64(o)))
	for i := 1; i <= o; i++ {
		go func(i int) {
			start := time.After(time.Until(beginat))
			gjq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			if err != nil {
				log.Fatal(err)
			}
			defer disconnect(gjq)
			<-start
			reserved := 0
			na := 0
			for j := 0; j < m; j++ {
				job, err := gjq.Reserve(5 * time.Second)
				if err != nil || job == nil {
					for k := j; k < m; k++ {
						na++
						reserves <- -i
					}
					break
				} else {
					reserved++
					reserves <- i
				}
			}
		}(i)
	}

	r := 0
	na := 0
	for i := 0; i < n; i++ {
		res := <-reserves
		if res >= 0 {
			r++
		} else {
			na++
		}
	}

	e = time.Since(beginat)
	per = e.Nanoseconds() / int64(n)
	log.Printf("Reserved %d jobqueue jobs (%d not available) in %s == %d per\n", r, na, e, per)

	// q := queue.New("myqueue")
	// before = time.Now()
	// for i := 0; i < n; i++ {
	// 	_, err := q.Add(fmt.Sprintf("test job %d", i), "data", 0, 0*time.Second, 30*time.Second)
	// 	if err != nil {
	// 		log.Fatal(err)
	// 	}
	// }
	// e = time.Since(before)
	// per = int64(e.Nanoseconds() / int64(n))
	// log.Printf("Added %d items to queue in %s == %d per\n", n, e, per)

	server.Stop(ctx, true)

	/* test speed of bolt db when there are lots of jobs already stored
	n := 10000000 // num jobs to start with
	b := 10000    // jobs per identifier

	server, _, err := serve(serverConfig)
	if err != nil {
		log.Fatal(err)
	}

	jq, err := Connect(addr, "cmds", 60*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer disconnect(jq)

	// get timings when the bolt db is empty
	total := 0
	batchNum := 1
	timeDealingWithBatch(addr, jq, batchNum, b)
	batchNum++
	total += b

	// add n jobs in b batches to the completed bolt db bucket to simulate
	// a well used database
	before := time.Now()
	q := server.getOrCreateQueue("cmds")
	for total < n {
		var jobs []*Job
		for i := 0; i < b; i++ {
			jobs = append(jobs, &Job{Cmd: fmt.Sprintf("test cmd %d", i+((batchNum-1)*b)), Cwd: "/fake/cwd", ReqGroup: "reqgroup", Requirements: &jqs.Requirements{RAM: 1024, Time: 4*time.Hour, Cores: 1}, Retries: uint8(3), RepGroup: fmt.Sprintf("batch_%d", batchNum)})
		}
		_, _, err := jq.Add(jobs, envVars, true)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("\nadded batch %d", batchNum)

		// it's too slow to reserve and archive things properly in this
		// test; this is not a real-world performance concern though, since
		// normally you wouldn't archive so many jobs in a row in a single
		// process...
		// for {
		// 	job, _ := jq.Reserve(1 * time.Millisecond)
		// 	if job == nil {
		// 		break
		// 	}
		// 	jq.Started(job, 123)
		// 	jq.Ended(job, 0, 5, 1*time.Second, []byte{}, []byte{})
		// 	err = jq.Archive(job)
		// 	if err != nil {
		// 		log.Fatal(err)
		// 	}
		// 	fmt.Print(".")
		// }

		// ... Instead we bypass the client interface and directly add to
		// bolt db
		err = server.db.bolt.Batch(func(tx *bolt.Tx) error {
			bl := tx.Bucket(bucketJobsLive)
			b := tx.Bucket(bucketJobsComplete)

			var puterr error
			for _, job := range jobs {
				key := job.key()
				var encoded []byte
				enc := codec.NewEncoderBytes(&encoded, server.db.ch)
				enc.Encode(job)

				bl.Delete([]byte(key))
				q.Remove(key)

				puterr = b.Put([]byte(key), encoded)
				if puterr != nil {
					break
				}
			}
			return puterr
		})
		if err != nil {
			log.Fatal(err)
		}

		batchNum++
		total += b
	}
	e := time.Since(before)
	log.Printf("Archived %d jobqueue jobs in %d sized groups in %s\n", n, b, e)

	// now re-time how long it takes to deal with a single new batch
	timeDealingWithBatch(addr, jq, batchNum, b)

	server.Stop()
	*/
}

/* this func is used by the commented out test above
func timeDealingWithBatch(addr string, jq *Client, batchNum int, b int) {
	before := time.Now()
	var jobs []*Job
	batchName := fmt.Sprintf("batch_%d", batchNum)
	for i := 0; i < b; i++ {
        jobs = append(jobs, &Job{Cmd: fmt.Sprintf("test cmd %d", i+((batchNum-1)*b)), Cwd: "/fake/cwd", ReqGroup: "reqgroup", Requirements: &jqs.Requirements{RAM: 1024, Time: 4*time.Hour, Cores: 1}, Retries: uint8(3), RepGroup: batchName})
	}
	_, _, err := jq.Add(jobs, envVars, true)
	if err != nil {
		log.Fatal(err)
	}
	e := time.Since(before)
	log.Printf("\nAdded a new batch of %d jobs in %s\n", b, e)

	before = time.Now()
	runtime.GOMAXPROCS(runtime.NumCPU())
	var wg sync.WaitGroup
	wg.Add(runtime.NumCPU())
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			defer wg.Done()
			gojq, _ := Connect(addr, "cmds", 10*time.Second)
            defer gojq.Disconnect()
			for {
				job, _ := gojq.Reserve(1 * time.Millisecond)
				if job == nil {
					break
				}
				gojq.Started(job, 123)
				gojq.Ended(job, 0, 5, 1*time.Second, []byte{}, []byte{})
				err = gojq.Archive(job)
				if err != nil {
					log.Fatal(err)
				}
			}
		}()
	}
	wg.Wait()
	e = time.Since(before)
	log.Printf("Reserved and Archived that batch of jobs in %s\n", e)

	before = time.Now()
	jobs, err = jq.GetByRepGroup(batchName, 1, JobStateComplete, false, false) // without a limit this takes longer than 60s, so would time out
	if err != nil {
		log.Fatal(err)
	}
	e = time.Since(before)
	log.Printf("Was able to get all %d jobs in that batch in %s\n", 1+jobs[0].Similar, e)
}
*/

func timelimitDebug(jobs []*Job, err error) {
	if err != nil {
		fmt.Printf("\ntime limit reached, but err getting jobs: %s\n", err)
	} else {
		fmt.Printf("\ntime limit reached, jobs:\n")
		for _, job := range jobs {
			stderr, errs := job.StdErr()
			if errs != nil {
				fmt.Printf(" problem getting stderr: %s\n", errs)
			}
			fmt.Printf(" [%s]: %s (%s)\n", job.Cmd, job.State, stderr)
		}
	}
}

func disconnect(client *Client) {
	err := client.Disconnect()
	if err != nil && !isClosedSocketError(err) {
		fmt.Printf("client.Disconnect() failed: %s", err)
	}
}

func execute(ctx context.Context, client *Client, job *Job, shell string, failExpected ...bool) {
	err := client.Execute(ctx, job, shell)
	if err != nil && !(len(failExpected) == 1 && failExpected[0]) {
		fmt.Printf("client.Execute() failed: %s", err)
	}
}

func touch(client *Client, job *Job) {
	_, err := client.Touch(job)
	if err != nil {
		fmt.Printf("client.Touch() failed: %s", err)
	}
}

func runner(ctx context.Context) {
	if runnerfail && runnermodetmpdir != "" {
		// simulate loss of network connectivity between a spawned runner and
		// the manager by just exiting without reserving any job
		<-time.After(250 * time.Millisecond)
		tmpfile, err := os.CreateTemp(runnermodetmpdir, "fail")
		if err == nil {
			tmpfile.Close()
		}
		return
	}

	ServerItemTTR = 10 * time.Second

	if runnerdebug {
		logfile, errlog := os.CreateTemp("", "wrrunnerlog")
		if errlog == nil {
			defer logfile.Close()
			log.SetOutput(logfile)
		}
	}

	if schedgrp == "" {
		log.Fatal("schedgrp missing")
	}
	log.Printf("runner working on schedgrp %s\n", schedgrp)

	// when the server that spawned us runs on an isolated manager dir (in-process
	// test servers can't pass it via our inherited env), it tells us via
	// --rmanagerdir; point our config there so we read the right token and CA.
	if rmanagerdir != "" {
		os.Setenv("WR_MANAGERDIR", rmanagerdir) //nolint:usetesting
	}

	config := internal.ConfigLoadFromParentDir(ctx, rdeployment)

	token, err := os.ReadFile(config.ManagerTokenFile)
	if err != nil {
		log.Fatalf("token read err: %s\n", err)
	}

	timeout := 6 * time.Second
	rtimeoutd := time.Duration(rtimeout) * time.Second
	// (we don't bother doing anything with maxmins in this test, but in a real
	//  runner client it would be used to end the below for loop before hitting
	//  this limit)

	jq, err := Connect(rserver, config.ManagerCAFile, rdomain, token, timeout)
	if err != nil {
		log.Fatalf("connect err: %s\n", err)
	}
	defer disconnect(jq)

	clean := true
	n := 0
	i := 0
	for {
		i++
		job, err := jq.ReserveScheduled(rtimeoutd, schedgrp)
		if err != nil {
			log.Fatalf("reserve err: %s\n", err)
		}
		if job == nil {
			log.Printf("reserve gave no job after %s\n", rtimeoutd)
			break
		}

		log.Printf("working on job %s\n", job.Cmd)
		n++

		// actually run the cmd
		err = jq.Execute(ctx, job, config.RunnerExecShell)
		if err != nil {
			var jqerr Error
			if errors.As(err, &jqerr) && jqerr.Err == FailReasonSignal {
				break
			} else {
				log.Printf("execute err: %s\n", err) // make this a Fatalf if you want to see these in the test output
				clean = false
				break
			}
		} else {
			err := jq.Archive(job, nil)
			if err != nil {
				log.Printf("archive err: %s\n", err)
			}
		}
	}

	log.Printf("ran %d jobs in %d loops\n", n, i)

	// if everything ran cleanly, create a tmpfile in our tmp dir
	if clean && runnermodetmpdir != "" {
		log.Printf("creating ok file in %s\n", runnermodetmpdir)
		tmpfile, err := os.CreateTemp(runnermodetmpdir, "ok")
		if err == nil {
			tmpfile.Close()
		}
	}
}

// setDomainIP is an author-only func to ensure that domain points to localhost
func setDomainIP(domain string) {
	if domain == "localhost" {
		return
	}
	host, err := os.Hostname()
	if err != nil {
		fmt.Printf("failed to get Hostname: %s", err)
		return
	}
	if host == "vr-2-2-02" {
		ip, err := internal.CurrentIP("")
		if err != nil {
			fmt.Printf("failed to get CurrentIP: %s", err)
			return
		}
		err = internal.InfobloxSetDomainIP(domain, ip)
		if err != nil {
			fmt.Printf("InfobloxSetDomainIP failed: %s", err)
		}
	}
}

// Test reliability conventions
//
// The server/runner integration tests run as many concurrent `go test`
// processes (see the Makefile), so on a busy or oversubscribed machine any
// single test can be starved of CPU at an arbitrary moment. Tests must
// therefore never depend on real-clock timing to observe asynchronous state.
// When adding or changing tests, prefer these patterns over fixed delays:
//
//   - Don't assert on asynchronously-updated state (a job's server-side state,
//     a count, a file, a websocket message) after a fixed sleep. Poll for the
//     condition with a generous upper bound instead: pollUntil, waitUntilJobState
//     and waitUntilFileExists here, waitForJobState in mockrunner_test.go, and
//     the waitFor* helpers in jobqueue_runners2_test.go. A poll returns as soon
//     as the condition holds, so it doesn't slow the happy path. A fixed sleep
//     is only justified when the wait itself is under test, and even then poll
//     for the resulting state rather than sampling once at a fixed offset.
//
//   - Give test servers timings that tolerate load by default. An ItemTTR short
//     enough that a slow reserve->Started gap (under load) trips the lost-job
//     logic makes unrelated reserve+Execute scenarios fail with "bad job"; use a
//     TTR with headroom and set a short one only in the scenario that actually
//     exercises TTR/lost-job behaviour.
//
//   - Don't fire-and-forget a goroutine with a process-wide effect (sending an
//     OS signal, killing a shared server) on a fixed timer. Make it cancellable
//     and stop it before the test returns, so a delayed effect can't leak into a
//     later test.
//
//   - When reading from a stream that carries unsolicited messages as well as
//     responses (e.g. the status websocket, which interleaves count broadcasts
//     with request responses), read until the message that matches your request
//     rather than asserting on the next one read - otherwise the assertion races
//     whatever broadcast happens to arrive first.

// testPortNext is the per-lane sequential offset used by freeTestPort. The
// tests in a lane run sequentially, so it needs no synchronisation.
var testPortNext int //nolint:gochecknoglobals

// freeTestPort returns a port for a test server to bind. A global free-port
// picker (bind :0, note the port, close it, hand it back) has a time-of-check
// to time-of-use race: when many `go test` lanes run at once, two lanes can be
// handed the same "free" port before either binds it, and one then fails to
// start its server (this showed up as intermittent "address already in use"
// failures). So when the Makefile runs a lane it sets WR_TEST_LANE, and each
// lane draws from its own disjoint port range; since a lane's tests run
// sequentially, an incrementing counter never repeats a port before it would
// wrap (far more ports than any lane uses), so no two servers anywhere contend
// for a port. When WR_TEST_LANE is unset (e.g. a direct `go test` run) it falls
// back to the global picker, whose race only matters with many concurrent
// lanes.
func freeTestPort() (int, error) {
	const (
		laneBasePort = 10000
		laneSpan     = 1000
	)

	laneStr := os.Getenv("WR_TEST_LANE")
	if laneStr == "" {
		return freeport.GetFreePort()
	}

	lane, err := strconv.Atoi(laneStr)
	if err != nil {
		return freeport.GetFreePort()
	}

	testPortNext++

	return laneBasePort + lane*laneSpan + testPortNext%laneSpan, nil
}

// skipInShard lets a long test split its independent scenarios across parallel
// `go test` lanes without moving code: a scenario calls it at the top of its
// Convey with its assigned shard ("a" or "b") and returns early when
// WR_TEST_SHARD names a different shard. With WR_TEST_SHARD unset every scenario
// runs, so a direct `go test -run` (or a single unsharded lane) still covers the
// whole test. The Makefile runs such a test once per shard so the scenarios'
// real-time work happens concurrently.
func skipInShard(homeShard string) bool {
	shard := os.Getenv("WR_TEST_SHARD")

	return shard != "" && shard != homeShard
}

// pollUntil polls cond every 20ms for up to 30s, returning true as soon as cond
// returns true and false on timeout. Used to wait for an async count/state to
// reach an expected value instead of sleeping a fixed (load-sensitive) time.
func pollUntil(cond func() bool) bool {
	limit := time.After(30 * time.Second)

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		if cond() {
			return true
		}

		select {
		case <-limit:
			return false
		case <-ticker.C:
		}
	}
}

// waitUntilFileExists polls for up to maxWait seconds for path to exist,
// returning true as soon as it does and false on timeout. Used to wait for a
// test job to actually start (the job touches a marker file) without relying on
// a fixed sleep, which is unreliable under heavy parallel-test load.
func waitUntilFileExists(path string, maxWait int) bool {
	limit := time.After(time.Duration(maxWait) * time.Second)

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}

		select {
		case <-limit:
			return false
		case <-ticker.C:
		}
	}
}

// waitUntilJobState polls (up to maxWait seconds) until the job matching essence
// reaches wantState, returning that job (or the last-seen job, possibly nil, on
// timeout). It returns as soon as the state matches, so it doesn't slow the
// happy path while tolerating however long a state transition takes under load.
func waitUntilJobState(jq *Client, essence *JobEssence, wantState JobState, maxWait int) *Job {
	limit := time.After(time.Duration(maxWait) * time.Second)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var job *Job

	for {
		if got, err := jq.GetByEssence(essence, false, false); err == nil {
			job = got
			if job != nil && job.State == wantState {
				return job
			}
		}

		select {
		case <-limit:
			return job
		case <-ticker.C:
		}
	}
}

// connectWithRetry retries Connect until the server responds or timeout elapses,
// keeping the per-attempt 2s deadline. A freshly-started daemon under heavy load
// (e.g. many test lanes sharing few cpus) may not accept connections the instant
// its token file appears; retrying avoids returning a nil client that callers
// would then dereference.
func connectWithRetry(addr string, config internal.Config, token []byte, timeout time.Duration) (*Client, error) {
	deadline := time.Now().Add(timeout)

	for {
		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, 2*time.Second)
		if err == nil || time.Now().After(deadline) {
			return jq, err
		}

		<-time.After(100 * time.Millisecond)
	}
}

// readManagerToken waits for the manager's token file to appear and reads it,
// retrying until the token is non-empty or timeout elapses. The server writes
// the token with a non-atomic os.WriteFile, so a reader racing startup can
// briefly see the freshly-created file empty; retrying avoids returning an
// empty token (and so a nil client) under load.
func readManagerToken(file string, preStart time.Time, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)

	for {
		if internal.WaitForFile(file, preStart, time.Until(deadline)) {
			token, err := os.ReadFile(file)
			if err == nil && len(token) > 0 {
				return token, nil
			}
		}

		if time.Now().After(deadline) {
			return os.ReadFile(file)
		}

		<-time.After(50 * time.Millisecond)
	}
}
