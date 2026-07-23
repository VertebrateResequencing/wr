/*******************************************************************************
 * Copyright (c) 2016-2022, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: [Theo Barber-Bany] <theobarberbany@gmail.com>
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

// This file contains the functions to implement a jobqueue server.

import (
	"cmp"
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/backoff"
	backofftime "github.com/VertebrateResequencing/wr/backoff/time"
	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	_ "github.com/VertebrateResequencing/wr/internal/mangostlstcp" // register race-clean tls+tcp transport
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gorilla/websocket"
	"github.com/inconshreveable/log15/v3"
	logext "github.com/inconshreveable/log15/v3/ext"
	"github.com/lindell/go-ordered-set/orderedset"
	"github.com/sb10/waitgroup"
	"github.com/ugorji/go/codec"
	mangos "go.nanomsg.org/mangos/v3"
	"go.nanomsg.org/mangos/v3/protocol/xrep"
)

// Err* constants are found in our returned Errors under err.Err, so you can
// cast and check if it's a certain type of error. ServerMode* constants are
// used to report on the status of the server, found inside ServerInfo.
const (
	ErrInternalError    = "internal error"
	ErrUnknownCommand   = "unknown command"
	ErrBadRequest       = "bad request (missing arguments?)"
	ErrBadJob           = "bad job (not in queue or correct sub-queue)"
	ErrMissingJob       = "corresponding job not found"
	ErrUnknown          = "unknown error"
	ErrClosedInt        = "queues closed due to SIGINT"
	ErrClosedTerm       = "queues closed due to SIGTERM"
	ErrClosedCert       = "queues closed due to certificate expiry"
	ErrClosedStop       = "queues closed due to manual Stop()"
	ErrQueueClosed      = "queue closed"
	ErrNoHost           = "could not determine the non-loopback ip address of this host"
	ErrNoServer         = "could not reach the server"
	ErrMustReserve      = "you must Reserve() a Job before passing it to other methods"
	ErrDBError          = "failed to use database"
	ErrS3DBBackupPath   = "invalid S3 database backup path"
	ErrPermissionDenied = "bad token: permission denied"
	ErrBeingDrained     = "server is being drained"
	ErrStopReserving    = "recovered on a new server; you should stop reserving"
	ErrRecovering       = "server is recovering prior state, please retry"
	ServerModeNormal    = "started"
	ServerModePause     = "paused"
	ServerModeDrain     = "draining"
)

const (
	maxJobsForStd                  = 1000
	serverWaitPeriodToStartRunning = 1 * time.Millisecond
	serverMaxRetriesToStartRunning = 50
	serverSocketWait               = 50 * time.Millisecond
	serverQueueName                = "cmds"
	jobOverridePreferSystemReqs    = uint8(0)
	jobOverridePreferHigherReqs    = uint8(1)
	jobOverrideAlwaysUseJobReqs    = uint8(2)
	mbPerGB                        = 1024
	defaultUploadDir               = "/tmp"

	// signalChanBuffer is the buffer size for the OS signal channel, large
	// enough to hold a SIGINT and a SIGTERM.
	signalChanBuffer = 2

	// serverListenWait is how long Serve() waits for ListenAndServe() to start
	// listening before declaring itself ready.
	serverListenWait = 10 * time.Millisecond

	// ownerReadWrite is the file mode for files only the owning user may read
	// or write (used for the auth token and uploaded files).
	ownerReadWrite = 0o600

	postUpgradeStartupState  = internal.DBUpgradePostStartupState
	postUpgradeStartupDetail = internal.DBUpgradePostStartupDetail

	// ttrReleaseWait is how long the TTR callback waits for a lost item to
	// return to the run queue before releasing it.
	ttrReleaseWait = 50 * time.Millisecond

	// webSocketIDLength is the number of characters in a generated websocket
	// connection identifier.
	webSocketIDLength = 8

	// httpReadHeaderTimeout bounds how long the web server will wait for
	// request headers, mitigating Slowloris-style attacks.
	httpReadHeaderTimeout = 60 * time.Second

	// pprofMutexProfileFraction is the rate passed to
	// runtime.SetMutexProfileFraction when the WR_PPROF_ADDR endpoint is
	// enabled: on average 1 in this many mutex contention events is reported.
	pprofMutexProfileFraction = 5

	// pprofBlockProfileRate is the rate (in nanoseconds) passed to
	// runtime.SetBlockProfileRate when the WR_PPROF_ADDR endpoint is enabled:
	// on average one blocking event is sampled per this many nanoseconds blocked.
	pprofBlockProfileRate = 10000

	// portCheckDialTimeout is how long shutdown waits when probing whether a
	// server port is still being listened to.
	portCheckDialTimeout = 10 * time.Millisecond

	// drainPollInterval is how often Drain() polls the queue to see whether all
	// runners have finished.
	drainPollInterval = 1 * time.Second
)

// ServerVersion gets set during build:
// go build -ldflags "-X github.com/VertebrateResequencing/wr/jobqueue.ServerVersion=\
// `git describe --tags --always --long --dirty`".
var ServerVersion string //nolint:gochecknoglobals // set at build time via -ldflags

// these global variables hold the default values for the corresponding
// ServerTimings fields (and a few non-timing knobs). The timing ones are no
// longer read directly during operation -- a Server resolves its own
// ServerTimings in Serve() (see ServerConfig.Timings) -- so different servers
// (eg. in parallel tests) can use different values without racing on shared
// globals.
//
//nolint:gochecknoglobals // mutable package defaults for ServerTimings; tests override them per-server
var (
	ServerInterruptTime                             = 1 * time.Second
	ServerItemTTR                                   = 60 * time.Second
	ServerReserveTicker                             = 1 * time.Second
	ServerCheckRunnerTime                           = 1 * time.Minute
	ServerShutdownWaitTime                          = 5 * time.Second
	ServerLostJobCheckTimeout                       = 15 * time.Second
	ServerLostJobCheckRetryTime                     = 30 * time.Minute
	ServerMaximumRunForResourceRecommendation       = 100
	ServerMinimumScheduledForResourceRecommendation = 10
	ServerLogClientErrors                           = true
	serverShutdownRunnerTickerTime                  = 50 * time.Millisecond

	// ServerDBBatchDelay is the default DB.MaxBatchDelay applied to the
	// manager's live BoltDB: how long a write transaction may wait for
	// concurrent writes to coalesce into a single fsync'd commit. It is left at
	// bbolt's 10ms default: on normal/fast disks the manager's per-job
	// bottleneck is CPU/lock contention rather than fsync, so a wider window
	// only adds latency to the synchronous archive commit with no fsync benefit
	// (measured on an 8-core VM, raising it to 25-50ms made 10000 jobs
	// dramatically slower). Operators whose manager DB is on high-fsync-latency
	// storage (NFS/Lustre) AND whose workload is genuinely fsync-bound can raise
	// it per-server via ServerTimings.DBBatchDelay (and operationally via
	// WR_MANAGERDBBATCHDELAY); we just don't impose that latency by default.
	ServerDBBatchDelay = 10 * time.Millisecond

	// ServerDBBatchSize is the default DB.MaxBatchSize applied to the manager's
	// live BoltDB: the number of concurrent write transactions that may
	// coalesce into one commit before it commits early. It is set well above
	// the number of writes expected in flight at once so the delay, not this
	// cap, governs coalescing. Overridable via ServerTimings.DBBatchSize (and
	// operationally via WR_MANAGERDBBATCHSIZE).
	ServerDBBatchSize = 10000

	// httpServerShutdownTime is the time we'll wait before forcing
	// http.Server{}.Shutdown() to complete, otherwise it takes 500ms if there
	// were listeners.
	httpServerShutdownTime = 1 * time.Millisecond
)

// BsubID is used to give added jobs a unique (atomically incremented) id when
// pretending to be bsub.
var BsubID uint64 //nolint:gochecknoglobals

// numRPCReaders is the number of concurrent goroutines that call RecvMsg() on
// the single command socket to admit client RPCs (spec B1). A small fixed value
// lets control/status RPCs (wr status, wr limit, wr suspend) be admitted without
// queuing behind a burst of reserve/touch/archive traffic, since Go channel
// receives fan out one message per receiver and mangos routes each reply by the
// message's pipe-ID header regardless of which reader admitted it. It is a
// package var (not user-configurable) purely so tests can lower it to 1 or raise
// it; production always uses the default.
//
//nolint:gochecknoglobals // internal tuning knob; a var only so tests can vary it
var numRPCReaders = 6

// envPprofAddr is the environment variable that, when set to a host:port (eg.
// "localhost:6060"), makes Serve() start an opt-in net/http/pprof endpoint for
// profiling the manager. It is unset by default, in which case no endpoint is
// started and there is no profiling overhead.
const envPprofAddr = "WR_PPROF_ADDR"

// recoveryPauseHookForTest, if non-nil, is copied into each new Server's
// recoveryPauseHook during Serve so a test can install a hook that blocks
// background recovery before recovery has a chance to run. It is a test-only
// seam and is nil in production.
//
//nolint:gochecknoglobals // deliberate test seam, mirroring statusWSDetailsHook
var recoveryPauseHookForTest func()

// sgroup represents a scheduler group.
const (
	// persistentScheduleFailures is the number of consecutive scheduling
	// failures for a group after which the failure is logged at Error level
	// (rather than Warn) so a permanently-failing submit becomes visible.
	persistentScheduleFailures = 3

	// scheduleRetryBackoffMax caps the jittered exponential backoff between
	// retries of a failing scheduling attempt (used as the retry backoff's Max).
	scheduleRetryBackoffMax = 30 * time.Minute

	// scheduleRetryBackoffFactor is the multiplier applied to the retry backoff
	// after each consecutive scheduling failure (used as the backoff's Factor).
	scheduleRetryBackoffFactor = 2
)

const (
	errMissingSubscriptionScope subscriptionRequestError = "missing subscription scope"
	errSubscriptionClosed       subscriptionRequestError = "subscription closed"
	errUnknownSubscription      subscriptionRequestError = "unknown subscription"
)

// ServerTimings holds the timing parameters a Server operates with. These were
// previously package-level globals that tests mutated, which prevented running
// servers concurrently; they are now per-Server config. Set any subset on
// ServerConfig.Timings; each non-positive duration or rounding value is
// replaced with its default (the matching Server*/Client* package variable
// above, or Rec*Round constant) by Serve().
// Most are fixed once the server starts; ItemTTR, LostJobCheckTimeout, and
// LostJobCheckRetryTime can additionally be adjusted at runtime via the Server's
// setter methods.
type ServerTimings struct {
	// InterruptTime is how long the server blocks waiting to receive from
	// clients before checking for signals etc. (default ServerInterruptTime).
	InterruptTime time.Duration

	// ItemTTR is the time-to-release given to queued items: a reserved job not
	// touched within this long is considered lost (default ServerItemTTR).
	ItemTTR time.Duration

	// CheckRunnerTime is how often the server re-checks whether it needs to
	// spawn runners (default ServerCheckRunnerTime).
	CheckRunnerTime time.Duration

	// LostJobCheckTimeout is how long the server gives a "lost" job's host to
	// respond when confirming the job is really dead (default
	// ServerLostJobCheckTimeout). Adjustable at runtime.
	LostJobCheckTimeout time.Duration

	// LostJobCheckRetryTime is how long the server waits before re-checking a
	// lost job that could not be confirmed dead (default
	// ServerLostJobCheckRetryTime). Adjustable at runtime.
	LostJobCheckRetryTime time.Duration

	// ReleaseDelayMin is the minimum backoff before a released job becomes
	// runnable again (default ClientReleaseDelayMin).
	ReleaseDelayMin time.Duration

	// TouchInterval is how often clients should touch their running jobs. It is
	// sent to connecting clients (which use it as their default touch
	// frequency) and used by the server to decide how long to wait for runners
	// during shutdown (default ClientTouchInterval).
	TouchInterval time.Duration

	// RetryWait is how long a client waits between attempts to reconnect to the
	// server (eg. to report a finished job after the server restarted); sent to
	// connecting clients (default ClientRetryWait).
	RetryWait time.Duration

	// RetryTime is the total time a client keeps retrying to reach the server
	// before giving up; sent to connecting clients (default ClientRetryTime).
	RetryTime time.Duration

	// RecSecRound is the number of seconds that recommended reserve times are
	// rounded up to (default RecSecRound).
	RecSecRound int

	// RecMBRound is the number of megabytes that recommended memory and disk
	// reservations are rounded up to (default RecMBRound).
	RecMBRound int

	// DBBatchDelay is the BoltDB DB.MaxBatchDelay applied to the manager's live
	// database: how long a write transaction may wait for concurrent writes to
	// coalesce into a single fsync'd commit (default ServerDBBatchDelay).
	// Durability is unaffected (every commit still fsyncs); a larger value only
	// widens the coalescing window, trading per-write latency for fewer fsyncs
	// when many writes are in flight.
	DBBatchDelay time.Duration

	// DBBatchSize is the BoltDB DB.MaxBatchSize applied to the manager's live
	// database: the number of concurrent write transactions that may coalesce
	// into one commit before it commits early (default ServerDBBatchSize).
	DBBatchSize int

	// ShutdownSocketWait is how long shutdown waits, after client handling has
	// stopped, before closing the command socket, to let in-flight messages
	// drain (default serverSocketWait). Tests set this low to shut servers down
	// faster.
	ShutdownSocketWait time.Duration
}

// dfltDuration returns v, or def if v is not positive.
func dfltDuration(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}

	return v
}

// withDefaults returns a copy of t with every non-positive duration or
// rounding value replaced by its package-default value.
func (t ServerTimings) withDefaults() ServerTimings {
	t.InterruptTime = dfltDuration(t.InterruptTime, ServerInterruptTime)
	t.ItemTTR = dfltDuration(t.ItemTTR, ServerItemTTR)
	t.CheckRunnerTime = dfltDuration(t.CheckRunnerTime, ServerCheckRunnerTime)
	t.LostJobCheckTimeout = dfltDuration(t.LostJobCheckTimeout, ServerLostJobCheckTimeout)
	t.LostJobCheckRetryTime = dfltDuration(t.LostJobCheckRetryTime, ServerLostJobCheckRetryTime)
	t.ReleaseDelayMin = dfltDuration(t.ReleaseDelayMin, ClientReleaseDelayMin)
	t.TouchInterval = dfltDuration(t.TouchInterval, ClientTouchInterval)
	t.RetryWait = dfltDuration(t.RetryWait, ClientRetryWait)
	t.RetryTime = dfltDuration(t.RetryTime, ClientRetryTime)

	if t.RecSecRound <= 0 {
		t.RecSecRound = RecSecRound
	}

	if t.RecMBRound <= 0 {
		t.RecMBRound = RecMBRound
	}

	t.DBBatchDelay = dfltDuration(t.DBBatchDelay, ServerDBBatchDelay)

	if t.DBBatchSize <= 0 {
		t.DBBatchSize = ServerDBBatchSize
	}

	t.ShutdownSocketWait = dfltDuration(t.ShutdownSocketWait, serverSocketWait)

	return t
}

type subscriptionRequestError string

func (e subscriptionRequestError) Error() string {
	return string(e)
}

// Error records an error and the operation and item that caused it.
type Error struct {
	Op   string // name of the method
	Item string // the item's key
	Err  string // one of our Err* vars
}

func (e Error) Error() string {
	return "jobqueue " + e.Op + "(" + e.Item + "): " + e.Err
}

// serverResponse is the struct that the server sends to clients over the
// network in response to their clientRequest.
type serverResponse struct {
	Err             string // string instead of error so we can decode on the client side
	Added           int
	Existed         int
	AddedIDs        []string
	AddWarnings     AddWarnings
	Modified        map[string]string
	KillCalled      bool
	Job             *Job
	Jobs            []*Job
	Limit           int
	LimitGroups     map[string]int
	SInfo           *ServerInfo
	SStats          *ServerStats
	CompletionTimes map[string]time.Time
	StatusSummaries map[string]*RepGroupStatus
	DB              []byte
	Path            string
	BadServers      []*BadServer
	SubscriptionID  string
	JobUpdates      []*JobUpdate
}

// ServerInfo holds basic addressing info about the server.
type ServerInfo struct {
	Addr       string // ip:port
	Host       string // hostname
	FQDN       string // fully qualified domain name
	Port       string // port
	WebPort    string // port of the web interface
	PID        int    // process id of server
	Deployment string // deployment the server is running under
	Scheduler  string // the name of the scheduler that jobs are being submitted to
	// Mode is ServerModeNormal if the server is running normally, or
	// ServerModeDrain|Paused if draining or paused.
	Mode string

	// the following timing parameters are sent to clients on connection, so
	// that client behaviour (touch frequency, reconnection backoff) defaults to
	// what the server's config specifies rather than to client-side globals. A
	// client may still override them locally (eg. to touch slower than the TTR
	// in a test).
	TouchInterval time.Duration // how often clients should touch running jobs
	RetryWait     time.Duration // how long clients wait between reconnect attempts
	RetryTime     time.Duration // total time clients keep retrying to reach the server
}

// ServerVersions holds the server version (git tag) and API version supported.
type ServerVersions struct {
	Version string
	API     string
}

// ServerStats holds information about the jobqueue server for sending to
// clients.
type ServerStats struct {
	Delayed int           // how many jobs are waiting following a possibly transient error
	Ready   int           // how many jobs are ready to begin running
	Running int           // how many jobs are currently running
	Buried  int           // how many jobs are no longer being processed because of seemingly permanent errors
	ETC     time.Duration // how long until the slowest of the currently running jobs is expected to complete
}

// rgToKeys is a thread-safe map of RepGroup to a PList of keys.
type rgToKeys struct {
	sync.RWMutex
	lookup map[string]*orderedset.OrderedSet[string]
}

func newRGToKeys() *rgToKeys {
	return &rgToKeys{lookup: make(map[string]*orderedset.OrderedSet[string])}
}

// Add adds the key to the list of keys for the given RepGroup. You must hold a
// Lock() when using this method!
func (r *rgToKeys) Add(rg string, key string) {
	if _, ok := r.lookup[rg]; !ok {
		r.lookup[rg] = orderedset.New[string]()
	}

	r.lookup[rg].Add(key)
}

// Delete removes the key from the list of keys for the given RepGroup. You must
// hold a Lock() when using this method!
func (r *rgToKeys) Delete(rg string, key string) {
	if _, ok := r.lookup[rg]; !ok {
		return
	}

	r.lookup[rg].Delete(key)
}

// Values gets the keys for the given RepGroup. It does its own RLock(); do not
// try to RLock() before calling this.
func (r *rgToKeys) Values(rg string) []string {
	r.RLock()
	defer r.RUnlock()

	plist, ok := r.lookup[rg]
	if !ok {
		return nil
	}

	return plist.Values()
}

// BadServer is the details of servers that have gone bad that we send to the
// status webpage. Previously bad servers can also be sent if they become good
// again, hence the IsBad boolean.
type BadServer struct {
	ID      string
	Name    string
	IP      string
	Date    int64 // seconds since Unix epoch
	IsBad   bool
	Problem string
}

// jstateCount is a from->to state-count delta sent to the status web page: the
// count in FromState drops by Count, the count in ToState rises by Count. This
// is v0.36.5's status-bar feed, restored (alongside statusCaster) in place of
// the removed absolute per-RepGroup counter. RepGroup "+all+" aggregates all
// live jobs across all RepGroups.
type jstateCount struct {
	RepGroup  string
	FromState JobState
	ToState   JobState
	Count     int
}

// SchedulerIssue is the details of a scheduler problem encountered that we send
// to the status webpage and expose to clients.
type SchedulerIssue struct {
	Msg       string
	FirstDate int64 // seconds since Unix epoch
	LastDate  int64
	Count     int // the number of identical Msg sent
}

// schedulerIssue is retained as the package-internal name used by the existing
// web UI paths.
type schedulerIssue = SchedulerIssue

// SchedulerAlerts contains every scheduler alert currently surfaced by the web
// UI: dismissible scheduler issues and cloud servers thought to be bad.
type SchedulerAlerts struct {
	Issues     []*SchedulerIssue
	BadServers []*BadServer
}

type sgroup struct {
	name     string
	count    int
	skipped  int
	req      *scheduler.Requirements
	priority uint8
	// failures counts consecutive scheduling failures for this group. It drives
	// only the Warn->Error log escalation (at persistentScheduleFailures); the
	// retry delay itself is driven by retryBackoff. It is only accumulated within
	// a single persistent retry loop (clone/snapshot deliberately reset it to 0),
	// and is accessed under the same locking discipline as count.
	failures int
	// retryBackoff is the lazily-created, per-group jittered exponential backoff
	// used to space out schedule retries. clone/snapshot deliberately leave it
	// nil so a fresh group starts with a fresh backoff, while the retry chain
	// reuses the same object it was handed. Accessed under the same locking
	// discipline as failures.
	retryBackoff *backoff.Backoff
	sync.RWMutex
}

// ensureRetryBackoff returns the group's schedule-retry backoff, lazily creating
// it on first use with the given Min and the package-level retry Max/Factor,
// using a real-time Sleeper (whose Sleep aborts on context cancellation). The
// caller must have the same exclusive access as for failures (the group lock, or
// exclusive ownership of a fresh clone/snapshot).
func (s *sgroup) ensureRetryBackoff(minDelay time.Duration) *backoff.Backoff {
	if s.retryBackoff == nil {
		s.retryBackoff = &backoff.Backoff{
			Min:     minDelay,
			Max:     scheduleRetryBackoffMax,
			Factor:  scheduleRetryBackoffFactor,
			Sleeper: &backofftime.Sleeper{},
		}
	}

	return s.retryBackoff
}

// resetRetryState clears the group's consecutive failure count and, if a retry
// backoff has been created, resets it so the next retry sleeps for Min again.
// Called on a successful schedule, under the same locking discipline as
// failures.
func (s *sgroup) resetRetryState() {
	s.failures = 0

	if s.retryBackoff != nil {
		s.retryBackoff.Reset()
	}
}

// clone creates a new copy of the sgroup with the given count.
func (s *sgroup) clone(count int) *sgroup {
	s.RLock()
	defer s.RUnlock()

	return &sgroup{
		name:     s.name,
		count:    count,
		skipped:  s.skipped,
		req:      s.req.Clone(),
		priority: s.priority,
	}
}

// snapshot returns a clone of the sgroup carrying its current count, taken under
// a single read lock. Use this (rather than clone) when the current count is
// wanted, so scheduling can operate on a stable copy without holding any sgroup
// lock across slow scheduler operations.
func (s *sgroup) snapshot() *sgroup {
	s.RLock()
	defer s.RUnlock()

	return &sgroup{
		name:     s.name,
		count:    s.count,
		skipped:  s.skipped,
		req:      s.req.Clone(),
		priority: s.priority,
	}
}

// getCount is a thread-safe way of getting the current count.
func (s *sgroup) getCount() int {
	s.RLock()
	defer s.RUnlock()

	return s.count
}

// decrement is a thread-safe way of dropping the count of the group by the
// given amount.
//
// If the sgroup's skipped is greater than 0, first decrements that and only
// decrements count if given drop is greater than skipped.
//
// Returns the new count, or -1 if the count didn't change.
func (s *sgroup) decrement(drop int) int {
	if drop < 1 {
		return -1
	}

	s.Lock()
	defer s.Unlock()

	if s.skipped > 0 {
		if drop <= s.skipped {
			s.skipped -= drop

			return -1
		}

		drop -= s.skipped
		s.skipped = 0
	}

	prev := s.count

	s.count -= drop
	if s.count < 0 {
		s.count = 0
	}

	if s.count == prev {
		return -1
	}

	return s.count
}

// hasSkips is a thread-safe way of seeing if skipped is greater than 0.
func (s *sgroup) hasSkips() bool {
	s.RLock()
	defer s.RUnlock()

	return s.skipped > 0
}

type casterMember struct {
	group *caster
	In    chan any
	done  chan struct{}
	send  sync.Mutex
	once  sync.Once
}

func (cm *casterMember) Close() {
	cm.once.Do(func() {
		cm.group.Lock()
		delete(cm.group.members, cm)
		cm.group.Unlock()
		close(cm.done)
	})
}

type caster struct {
	members map[*casterMember]struct{}
	closed  bool
	sync.RWMutex
}

func newCaster() *caster {
	return &caster{members: make(map[*casterMember]struct{})}
}

func (c *caster) Broadcasting(time.Duration) {}

func (c *caster) Join() *casterMember {
	member := &casterMember{
		group: c,
		In:    make(chan any, 1),
		done:  make(chan struct{}),
	}

	c.Lock()
	if !c.closed {
		c.members[member] = struct{}{}
	}
	c.Unlock()

	return member
}

func (c *caster) Send(val any) {
	c.RLock()

	if c.closed {
		c.RUnlock()

		return
	}

	members := make([]*casterMember, 0, len(c.members))
	for member := range c.members {
		members = append(members, member)
	}

	c.RUnlock()

	for _, member := range members {
		member.trySend(val)
	}
}

func (c *caster) Close() {
	c.Lock()
	c.closed = true

	members := make([]*casterMember, 0, len(c.members))
	for member := range c.members {
		members = append(members, member)
	}

	c.members = make(map[*casterMember]struct{})
	c.Unlock()

	for _, member := range members {
		member.Close()
	}
}

// trySend serialises sends to this member via send.Lock (so a concurrent Send
// may block briefly), then performs a non-blocking send of val into the
// member's 1-slot buffer, dropping val if the buffer is already full or the
// member is done. The remaining casters (bad servers and scheduler issues) are
// recoverable: a client re-requests "current", which re-broadcasts the latest
// set, so a dropped update is harmless. The status counts use the same caster
// mechanism via statusCaster, broadcasting non-idempotent jstateCount deltas
// (the accepted v0.36.5 flicker/overcount quality): a dropped delta is not
// re-derivable, but a client reconnect re-seeds from the scan-on-connect, so
// there is no overflow-to-resync conversion anywhere.
func (cm *casterMember) trySend(val any) {
	cm.send.Lock()
	defer cm.send.Unlock()

	select {
	case <-cm.done:
	case cm.In <- val:
	default:
	}
}

type lostJobRetryCheck struct {
	jobKey       string
	jobHost      string
	jobPID       int
	checkTimeout time.Duration
}

type repGroupStatusOptions struct {
	RepGroup             string
	Match                RepGroupMatch
	States               []JobState
	IncludeComplete      bool
	IncludeStatusDetails bool
}

// Server represents the server side of the socket that clients Connect() to.
type Server struct {
	token     []byte
	uploadDir string
	sock      mangos.Socket
	ch        codec.Handle
	// runner command string compatible with fmt.Sprintf(..., schedulerGroup,
	// deployment, serverAddr, reserveTimeout, maxMinsAllowed).
	rc string

	ServerInfo         *ServerInfo
	ServerVersions     *ServerVersions
	db                 *db
	done               chan error
	stopSigHandling    chan bool
	stopClientHandling chan bool
	clientHandlingDone chan struct{}
	wg                 *waitgroup.WaitGroup
	// bgWG tracks only the background startup goroutines (prior-state recovery
	// and the one-time complete-counter backfill), separately from wg, so
	// shutdown can wait for just them early - before scheduler cleanup, DB close
	// and queue destroy - rather than at the final wg.Wait. bgCancel cancels the
	// context those goroutines observe, so shutdown can tell them to abort
	// promptly and quietly instead of racing the teardown.
	bgWG                      *waitgroup.WaitGroup
	bgCancel                  context.CancelFunc
	q                         *queue.Queue
	rpl                       *rgToKeys
	limiter                   *limiter.Limiter
	scheduler                 *scheduler.Scheduler
	previouslyScheduledGroups map[string]*sgroup
	httpServer                *http.Server
	pprofServer               *http.Server
	statusCaster              *caster
	badServerCaster           *caster
	schedCaster               *caster
	racCheckTimer             *time.Timer
	statusWSDetailsHook       func()
	// recoveryPauseHook, if non-nil, is called at the top of the background
	// prior-state recovery goroutine so a test can block recovery and observe
	// the recovering window (modelled on statusWSDetailsHook). nil in production.
	recoveryPauseHook   func()
	pauseRequests       int
	wsconns             map[string]*websocket.Conn
	wsWriteMutexes      map[string]*sync.Mutex // mutex per websocket connection
	wsHandlerWG         sync.WaitGroup
	clientSubscriptions map[string]*serverSubscription
	badServers          map[string]*cloud.Server
	schedIssues         map[string]*schedulerIssue
	racmutex            sync.RWMutex // to protect the readyaddedcallback
	bsmutex             sync.RWMutex
	simutex             sync.RWMutex
	krmutex             sync.RWMutex
	ssmutex             sync.RWMutex // up, drain, blocking, Mode, shutdown's q-nil, recovering state
	rrjMu               sync.RWMutex // leaf lock guarding recoveredRunningJobs
	psgmutex            sync.RWMutex // to protect previouslyScheduledGroups
	csmutex             sync.RWMutex // to protect clientSubscriptions
	rpmutex             sync.Mutex   // to protect racPending, racRunning and waitingReserves
	sync.Mutex
	wsmutex  sync.RWMutex
	up       bool
	drain    bool
	blocking bool
	// recovering, recoveryTotal and recoveryRestored track background prior-state
	// recovery (spec B1); all guarded by ssmutex.
	recovering           bool
	recoveryTotal        int
	recoveryRestored     int
	racChecking          bool
	killRunners          bool
	racPending           bool
	racRunning           bool
	waitingReserves      []chan struct{}
	recoveredRunningJobs map[string]bool
	nextSubscriptionID   uint64

	// timings holds this server's resolved timing parameters. The fixed ones
	// are set once in Serve() and then only read; the three below
	// (itemTTR, which is read each time a job is queued, and the two
	// lost-job-check durations) can be adjusted at runtime and so are copied
	// out into dedicated fields guarded by timingMu.
	timings               ServerTimings
	timingMu              sync.RWMutex
	itemTTR               time.Duration
	lostJobCheckTimeout   time.Duration
	lostJobCheckRetryTime time.Duration
}

// itemTTRDuration returns the current (runtime-adjustable) time-to-release given
// to newly queued items.
func (s *Server) itemTTRDuration() time.Duration {
	s.timingMu.RLock()
	defer s.timingMu.RUnlock()

	return s.itemTTR
}

// SetItemTTR sets the time-to-release given to subsequently queued items. Safe
// to call while the server is running.
func (s *Server) SetItemTTR(d time.Duration) {
	s.timingMu.Lock()
	s.itemTTR = d
	s.timingMu.Unlock()
}

// lostJobCheckDurations returns the current (runtime-adjustable) lost-job-check
// timeout and retry time.
func (s *Server) lostJobCheckDurations() (timeout, retry time.Duration) {
	s.timingMu.RLock()
	defer s.timingMu.RUnlock()

	return s.lostJobCheckTimeout, s.lostJobCheckRetryTime
}

// SetLostJobCheckTimeout sets how long the server gives a lost job's host to
// respond when confirming the job is really dead. Safe to call while the server
// is running.
func (s *Server) SetLostJobCheckTimeout(d time.Duration) {
	s.timingMu.Lock()
	s.lostJobCheckTimeout = d
	s.timingMu.Unlock()
}

// SetLostJobCheckRetryTime sets how long the server waits before re-checking a
// lost job that could not be confirmed dead. Safe to call while the server is
// running.
func (s *Server) SetLostJobCheckRetryTime(d time.Duration) {
	s.timingMu.Lock()
	s.lostJobCheckRetryTime = d
	s.timingMu.Unlock()
}

func (s *Server) queueIfPresent() *queue.Queue {
	s.ssmutex.RLock()
	defer s.ssmutex.RUnlock()

	return s.q
}

// isRecovering reports whether the background prior-state recovery goroutine is
// still running (spec B1).
func (s *Server) isRecovering() bool {
	s.ssmutex.RLock()
	defer s.ssmutex.RUnlock()

	return s.recovering
}

// recoveryProgress returns how many prior jobs have been restored so far and the
// total to restore, for observing recovery progress (spec B1). Because recovery
// enqueues in a single batch, restored is all-or-nothing: it reads 0 until the
// batch completes, then jumps straight to total (it never climbs job by job).
func (s *Server) recoveryProgress() (restored, total int) {
	s.ssmutex.RLock()
	defer s.ssmutex.RUnlock()

	return s.recoveryRestored, s.recoveryTotal
}

// setRecovering marks the server as recovering, records the total number of
// prior jobs to restore and resets the restored count to 0 (spec B1).
func (s *Server) setRecovering(total int) {
	s.ssmutex.Lock()
	defer s.ssmutex.Unlock()

	s.recovering = true
	s.recoveryTotal = total
	s.recoveryRestored = 0
}

// setRecoveryTotal records the real total number of prior jobs to restore once
// the cheap live-bucket scan has completed, without touching the recovering
// flag or the restored count. This lets Serve mark recovering=true before it
// starts accepting client RPCs (so the recovery window is closed with no false
// losses, spec B2) while the true total is filled in later, before the
// background recovery goroutine (and thus recoveryPauseHook) runs (spec B1).
func (s *Server) setRecoveryTotal(total int) {
	s.ssmutex.Lock()
	defer s.ssmutex.Unlock()

	s.recoveryTotal = total
}

// noteRecovered adds n to the count of prior jobs restored so far (spec B1).
// In practice it is called once, with the full total, after the single-batch
// enqueue completes, so the restored count goes from 0 straight to the total in
// one step rather than climbing incrementally. The additive shape is kept so
// the accounting stays correct should recovery ever move to multiple batches.
func (s *Server) noteRecovered(n int) {
	s.ssmutex.Lock()
	defer s.ssmutex.Unlock()

	s.recoveryRestored += n
}

// finishRecovering marks background prior-state recovery as complete (spec B1).
func (s *Server) finishRecovering() {
	s.ssmutex.Lock()
	defer s.ssmutex.Unlock()

	s.recovering = false
}

func (s *Server) setRACPending() {
	s.rpmutex.Lock()
	s.racPending = true
	s.rpmutex.Unlock()
}

func (s *Server) clearRACWaiters() {
	for _, ch := range s.waitingReserves {
		close(ch)
	}

	s.waitingReserves = nil
}

func (s *Server) clearRACPending() {
	s.rpmutex.Lock()
	s.racPending = false
	s.clearRACWaiters()
	s.rpmutex.Unlock()
}

func (s *Server) finishRAC() {
	s.rpmutex.Lock()
	s.racPending = false
	s.racRunning = false
	s.clearRACWaiters()
	s.rpmutex.Unlock()
}

func (s *Server) triggerReadyAddedCallback(ctx context.Context) {
	s.setRACPending()
	s.q.TriggerReadyAddedCallback(ctx)
}

func (s *Server) jobsNotAlreadyQueued(inputJobs []*Job, ignoreComplete bool) ([]*Job, int) {
	if !ignoreComplete {
		return inputJobs, 0
	}

	filtered := make([]*Job, 0, len(inputJobs))
	queuedDups := 0

	for _, job := range inputJobs {
		if _, err := s.q.Get(job.Key()); err == nil {
			queuedDups++

			continue
		}

		filtered = append(filtered, job)
	}

	return filtered, queuedDups
}

// getJobsRecent returns archived jobs that finished within period of now,
// across all rep groups, after applying the shared limit/std/env filtering.
func (s *Server) getJobsRecent(ctx context.Context, period time.Duration,
	limit int, getStd, getEnv bool) (jobs []*Job, srerr string, qerr string) {
	cutoff := time.Now().Add(-period)

	jobs, err := s.db.retrieveCompleteJobsRecent(cutoff)
	if err != nil {
		return nil, ErrDBError, err.Error()
	}

	jobs = s.limitJobs(ctx, jobs, limitJobsOptions{Limit: limit, GetStd: getStd, GetEnv: getEnv})

	return jobs, "", ""
}

// startPriorStateRecovery reads the prior incomplete jobs (a cheap live-bucket
// scan), fills in that total on the already-active recovering state, and
// launches a background goroutine that re-enqueues them so Serve returns while
// recovery is still running (spec B1). The recovering flag was set true by
// Serve before it began accepting client RPCs (so the recovery window is closed
// with no false losses, spec B2); here we only fill in the true total, which
// happens before the background goroutine (and thus recoveryPauseHook) runs so
// progress reporting is correct at the pause (spec B1 acceptance test 4). The
// goroutine calls recoveryPauseHook (if set) at the top so a test can block and
// observe the recovering window, updates progress via noteRecovered, and calls
// finishRecovering when done. Recovery keeps the single-batch enqueue
// (recoverPriorJobs -> enqueueItems).
func (s *Server) startPriorStateRecovery(ctx context.Context, config ServerConfig, db *db) error {
	priorJobs, err := db.recoverIncompleteJobs()
	if err != nil {
		return err
	}

	s.setRecoveryTotal(len(priorJobs))

	wgk := s.bgWG.Add(1)

	go s.recoverInBackground(ctx, config, wgk, priorJobs)

	return nil
}

// recoverInBackground is the body of the background prior-state recovery
// goroutine launched by startPriorStateRecovery (spec B1). It calls
// recoveryPauseHook (if set) at the top so a test can block and observe the
// recovering window, re-enqueues the prior jobs in a single batch, records the
// result via noteRecovered, and marks recovery finished on return.
//
// Because the enqueue is a single batch, progress is all-or-nothing: the
// restored count set by noteRecovered goes from 0 straight to the total in one
// step once the batch has been enqueued (it is never incremented job by job).
//
// It is registered on s.bgWG (not s.wg) and observes ctx (s.bgCtx), so shutdown
// can cancel and await it early, before scheduler cleanup, DB close and queue
// destroy. On cancellation it returns quietly (finishRecovering still runs, so
// the recovering flag is cleared) without calling scheduler.Recover, enqueuing,
// or logging the shutdown as a failure.
func (s *Server) recoverInBackground(ctx context.Context, config ServerConfig, wgk string, priorJobs []*Job) {
	defer internal.LogPanic(ctx, "jobqueue prior-state recovery", true)
	defer s.bgWG.Done(wgk)
	// re-schedule ready work once recovery has finished. Defers run LIFO, so
	// finishRecovering (registered after this) runs first: by the time this
	// fires, isRecovering() is already false, so any jobs added during the
	// recovery window are now scheduled against capacity that accounts for the
	// recovered running jobs (spec B1: no overcommit during recovery).
	defer s.rescheduleReadyAfterRecovery(ctx)
	defer s.finishRecovering()

	if s.recoveryPauseHook != nil {
		s.recoveryPauseHook()
	}

	s.recoverPriorJobsAndNote(ctx, config, priorJobs)
}

// recoverPriorJobsAndNote re-enqueues the prior jobs (in a single batch) and
// records the result via noteRecovered, logging the outcome. It first bails if
// shutdown has cancelled ctx (so we never touch the scheduler, DB or queue
// during teardown), and treats a cancellation surfaced by recoverPriorJobs as
// an expected quiet return rather than a failure.
func (s *Server) recoverPriorJobsAndNote(ctx context.Context, config ServerConfig, priorJobs []*Job) {
	if err := ctx.Err(); err != nil {
		clog.Debug(ctx, "prior-state recovery aborted during shutdown", "err", err)

		return
	}

	clog.Info(ctx, "recovering prior state", "total", len(priorJobs))

	err := s.recoverPriorJobs(ctx, config, priorJobs)
	if err != nil && errors.Is(err, context.Canceled) {
		clog.Debug(ctx, "prior-state recovery aborted during shutdown", "err", err)

		return
	}

	if err != nil {
		clog.Error(ctx, "prior-state recovery failed", "err", err)

		return
	}

	s.noteRecovered(len(priorJobs))

	restored, total := s.recoveryProgress()
	clog.Info(ctx, "recovering: prior state recovered", "restored", restored, "total", total)
}

// rescheduleReadyAfterRecovery re-triggers the ready-added callback when the
// queue holds ready jobs, so any jobs added during the recovery window (whose
// dispatch was gated by isRecovering) are now scheduled. It only fires when
// there is ready work, mirroring waitThenRecheckRAC, so recovery never provokes
// a spurious callback invocation. Must be called after finishRecovering.
func (s *Server) rescheduleReadyAfterRecovery(ctx context.Context) {
	q := s.queueIfPresent()
	if q == nil {
		return
	}

	if q.Stats().Ready > 0 {
		s.triggerReadyAddedCallback(ctx)
	}
}

// stopBackgroundStartupTasks cancels and then waits for the background startup
// goroutines (prior-state recovery and the one-time complete-counter backfill)
// registered on s.bgWG. It is called early in shutdown, before scheduler
// cleanup, DB close and queue destroy, so those goroutines finish (or abort on
// the cancellation) before the resources they use are torn down. bgCancel fires
// first and both goroutines check for cancellation at safe points, so they
// return promptly; ServerShutdownWaitTime is only the threshold after which
// bgWG.Wait logs any still-outstanding tasks (the wait itself does not time
// out). It holds no server locks, so waiting cannot deadlock against the
// goroutines' own lock acquisitions (queue mutex, rrjMu, ssmutex, db locks).
func (s *Server) stopBackgroundStartupTasks() {
	if s.bgCancel != nil {
		s.bgCancel()
	}

	if s.bgWG != nil {
		s.bgWG.Wait(ServerShutdownWaitTime)
	}
}

// serveClientsReader is one RPC reader: it receives client requests from the
// command socket and handles each in its own goroutine until stopClientHandling
// is signalled.
func (s *Server) serveClientsReader(ctx context.Context, sock mangos.Socket, wg *waitgroup.WaitGroup,
	readers *sync.WaitGroup, stopClientHandling <-chan bool) {
	// log panics and die
	defer internal.LogPanic(ctx, "jobqueue serving", true)
	defer readers.Done()

	for {
		select {
		case <-stopClientHandling: // s.shutdown() closes this
			return
		default:
			// receive a clientRequest from a client
			m, ok := s.receiveClientMessage(ctx, sock)
			if !ok {
				continue
			}

			// parse the request, do the desired work and respond to the client
			wgk2 := wg.Add(1)
			go s.dispatchClientRequest(ctx, m, wg, wgk2)
		}
	}
}

func warnUnexpectedSetReserveGroupError(ctx context.Context, err error) {
	if err == nil {
		return
	}

	// We could be trying to set the reserve group after the job has already
	// completed, if it completed almost instantly.
	var qerr queue.Error
	if errors.As(err, &qerr) && errors.Is(qerr.Err, queue.ErrNotFound) {
		return
	}

	clog.Warn(ctx, "readycallback queue setreservegroup failed", "err", err)
}

func (s *Server) waitForClientHandling(ctx context.Context) {
	timer := time.NewTimer(ServerShutdownWaitTime)
	defer timer.Stop()

	select {
	case <-s.clientHandlingDone:
	case <-timer.C:
		clog.Warn(ctx, "server shutdown timed out waiting for client handling to stop")
	}
}

// getStatusByRepGroup gets compact per-state status summaries for jobs in the
// given group match.
func (s *Server) getStatusByRepGroup(opts repGroupStatusOptions) (map[string]*RepGroupStatus, string, string) {
	rgs, srerr, qerr := s.getStatusRepGroups(opts)
	if srerr != "" {
		return nil, srerr, qerr
	}

	summaries := make(map[string]*RepGroupStatus)
	if opts.RepGroup == "" {
		s.addAllQueueJobStatuses(summaries, opts)
	} else {
		for _, rg := range rgs {
			s.addQueueJobStatusesByRepGroup(summaries, rg, opts)
		}
	}

	srerr, qerr = s.addCompleteJobStatuses(summaries, rgs, opts)
	if srerr != "" {
		return nil, srerr, qerr
	}

	return summaries, "", ""
}

func (s *Server) addCompleteJobStatuses(summaries map[string]*RepGroupStatus, rgs []string,
	opts repGroupStatusOptions) (string, string) {
	if !opts.IncludeComplete || !statusStateMatches(JobStateComplete, opts.States) {
		return "", ""
	}

	for _, rg := range rgs {
		complete, err := s.db.retrieveCompleteJobStatusByRepGroup(rg, opts.IncludeStatusDetails)
		if err != nil {
			return ErrDBError, err.Error()
		}

		if !statusSummaryEmpty(complete) {
			statusSummaryForRepGroup(summaries, rg).Merge(complete)
		}
	}

	return "", ""
}

func statusStateMatches(state JobState, filters []JobState) bool {
	if len(filters) == 0 {
		return true
	}

	for _, filter := range filters {
		if normalizedStatusFilter(filter) == state {
			return true
		}
	}

	return false
}

func statusSummaryEmpty(summary *RepGroupStatus) bool {
	if summary == nil {
		return true
	}

	for _, count := range summary.Counts {
		if count > 0 {
			return false
		}
	}

	return true
}

func statusSummaryForRepGroup(summaries map[string]*RepGroupStatus, repGroup string) *RepGroupStatus {
	summary, ok := summaries[repGroup]
	if ok {
		return summary
	}

	summary = NewRepGroupStatus()
	summaries[repGroup] = summary

	return summary
}

func (s *Server) getStatusRepGroups(opts repGroupStatusOptions) ([]string, string, string) {
	if opts.RepGroup != "" {
		return s.getRepGroupsList(opts.RepGroup, opts.Match)
	}

	if !opts.IncludeComplete {
		return nil, "", ""
	}

	rgs, err := s.db.retrieveRepGroups()
	if err != nil {
		return nil, ErrDBError, err.Error()
	}

	return rgs, "", ""
}

func (s *Server) addAllQueueJobStatuses(summaries map[string]*RepGroupStatus,
	opts repGroupStatusOptions) {
	for _, item := range s.q.AllItems() {
		s.addQueueItemStatus(summaries, item, opts)
	}
}

func (s *Server) addQueueJobStatusesByRepGroup(summaries map[string]*RepGroupStatus, repGroup string,
	opts repGroupStatusOptions) {
	for _, key := range s.rpl.Values(repGroup) {
		item, _ := s.q.Get(key) //nolint:errcheck
		if item == nil {
			continue
		}

		s.addQueueItemStatus(summaries, item, opts)
	}
}

func (s *Server) addQueueItemStatus(summaries map[string]*RepGroupStatus, item *queue.Item,
	opts repGroupStatusOptions) {
	job, ok := item.Data().(*Job)
	if !ok {
		return
	}

	itemState := item.State()

	job.RLock()
	repGroup := job.RepGroup
	state := queueItemStatusState(itemState, job.Lost)
	exitCode := job.Exitcode
	failReason := job.FailReason
	job.RUnlock()

	if !statusStateMatches(state, opts.States) {
		return
	}

	summary := statusSummaryForRepGroup(summaries, repGroup)
	summary.AddState(state, 1)

	if opts.IncludeStatusDetails && state == JobStateBuried {
		group := fmt.Sprintf("exitcode.%d,\"%s\"", exitCode, failReason)
		summary.AddBuried(group, item.Key)
	}
}

func queueItemStatusState(itemState queue.ItemState, lost bool) JobState {
	state := itemsStateToJobState[itemState]
	if state == "" {
		return JobStateUnknown
	}

	if state != JobStateReserved {
		return state
	}

	if lost {
		return JobStateLost
	}

	return JobStateRunning
}

func (s *Server) replaceLiveRerunItems(
	ctx context.Context,
	itemdefs []*queue.ItemDef,
	ignoreComplete bool,
) ([]*queue.ItemDef, int, error) {
	if ignoreComplete {
		return itemdefs, 0, nil
	}

	var (
		remaining []*queue.ItemDef
		replaced  int
	)

	for _, itemdef := range itemdefs {
		updated, err := s.replaceLiveRerunItem(ctx, itemdef)
		if err != nil {
			return nil, replaced, err
		}

		if updated {
			replaced++

			continue
		}

		remaining = append(remaining, itemdef)
	}

	return remaining, replaced, nil
}

func (s *Server) replaceLiveRerunItem(ctx context.Context, itemdef *queue.ItemDef) (bool, error) {
	item, err := s.q.Get(itemdef.Key)
	if err != nil {
		if queueErrorIs(err, queue.ErrNotFound) {
			return false, nil
		}

		return false, err
	}

	oldRepGroup, ok := resurrectedCompleteRepGroup(item)
	if !ok {
		return false, nil
	}

	newJob, ok := itemdef.Data.(*Job)
	if !ok {
		return false, nil
	}

	if err = s.updateLiveRerunItemWithReadyPending(ctx, item, itemdef); err != nil {
		return false, err
	}

	newRepGroup := newJob.RepGroup
	s.rememberRerunReplacementRepGroup(oldRepGroup, newRepGroup, itemdef.Key)

	return true, nil
}

func queueErrorIs(err error, target error) bool {
	var qerr queue.Error

	return errors.As(err, &qerr) && errors.Is(qerr.Err, target)
}

func resurrectedCompleteRepGroup(item *queue.Item) (string, bool) {
	job, ok := item.Data().(*Job)
	if !ok {
		return "", false
	}

	job.RLock()
	defer job.RUnlock()

	return job.RepGroup, job.State == JobStateComplete
}

func (s *Server) updateLiveRerunItem(ctx context.Context, itemdef *queue.ItemDef) error {
	return s.q.Update(
		ctx,
		itemdef.Key,
		itemdef.ReserveGroup,
		itemdef.Data,
		itemdef.Priority,
		itemdef.Delay,
		itemdef.TTR,
		itemdef.Dependencies,
	)
}

func (s *Server) updateLiveRerunItemWithReadyPending(
	ctx context.Context,
	item *queue.Item,
	itemdef *queue.ItemDef,
) error {
	readyCallbackExpected := itemDefTriggersReadyAdded(itemdef) &&
		itemWillBecomeReadyAfterDependencyUpdate(item, nil)
	if readyCallbackExpected {
		s.setRACPending()
	}

	err := s.updateLiveRerunItem(ctx, itemdef)
	if err != nil && readyCallbackExpected {
		s.clearRACPending()
	}

	return err
}

func (s *Server) rememberRerunReplacementRepGroup(oldRepGroup, newRepGroup, key string) {
	repGroupChanged := oldRepGroup != newRepGroup

	s.rpl.Lock()
	if repGroupChanged {
		s.rpl.Delete(oldRepGroup, key)
	}

	s.rpl.Add(newRepGroup, key)
	s.rpl.Unlock()

	if repGroupChanged {
		s.removeRepGroupSubscriptionKey(oldRepGroup, key)
	}

	s.rememberRepGroupSubscriptionKey(newRepGroup, key)
}

// suspendJobs suspends matching eligible jobs and returns the number affected.
func (s *Server) suspendJobs(ctx context.Context, keys []string) (suspended int) {
	readyCallbackNeeded := false

	for _, key := range keys {
		changed, wasReady := s.suspendJob(ctx, key)
		if !changed {
			continue
		}

		suspended++
		readyCallbackNeeded = readyCallbackNeeded || wasReady
	}

	if readyCallbackNeeded {
		s.triggerReadyAddedCallback(ctx)
	}

	return suspended
}

func (s *Server) suspendJob(ctx context.Context, key string) (bool, bool) {
	item, err := s.q.Get(key)
	if err != nil || item == nil {
		return false, false
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return false, false
	}

	wasReady := item.Stats().State == queue.ItemStateReady

	if err = s.q.Suspend(ctx, key); err != nil {
		return false, false
	}

	job.Lock()
	job.State = JobStateSuspended
	job.Unlock()

	s.db.updateJobAfterChange(ctx, job)

	return true, wasReady
}

// resumeJobs resumes matching suspended jobs and returns the number affected.
func (s *Server) resumeJobs(ctx context.Context, keys []string) (resumed int) {
	for _, key := range keys {
		if s.resumeJob(ctx, key) {
			resumed++
		}
	}

	return resumed
}

func (s *Server) resumeJob(ctx context.Context, key string) bool {
	item, job, ok := s.suspendedItem(key)
	if !ok {
		return false
	}

	if !s.resumeQueueItem(ctx, item, key) {
		return false
	}

	job.Lock()
	job.State = s.itemStateToJobState(item.Stats().State, job.Lost)
	job.Unlock()

	s.db.updateJobAfterChange(ctx, job)

	return true
}

func (s *Server) suspendedItem(key string) (*queue.Item, *Job, bool) {
	item, err := s.q.Get(key)
	if err != nil || item == nil || item.Stats().State != queue.ItemStateSuspended {
		return nil, nil, false
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return nil, nil, false
	}

	return item, job, true
}

func (s *Server) resumeQueueItem(ctx context.Context, item *queue.Item, key string) bool {
	s.setRACPending()

	err := s.q.Resume(ctx, key)
	if err != nil {
		s.clearRACPending()

		return false
	}

	if item.Stats().State != queue.ItemStateReady {
		s.clearRACPending()
	}

	return true
}

func failureMayUpdateJobRequirements(job *Job) bool {
	if job == nil {
		return false
	}

	return shouldIncreaseJobRAMAfterHighPeak(job) ||
		job.FailReason == FailReasonDisk ||
		job.FailReason == FailReasonTime
}

func updateJobRequirementsForRetry(
	job *Job,
	jobOverride uint8,
	recommendedReq *scheduler.Requirements,
) {
	if job.RequirementsOrig == nil {
		job.RequirementsOrig = &scheduler.Requirements{
			RAM:     job.Requirements.RAM,
			Time:    job.Requirements.Time,
			Disk:    job.Requirements.Disk,
			DiskSet: job.Requirements.DiskSet,
		}
	}

	applyRecommendedJobRequirements(job, jobOverride, recommendedReq)

	if jobOverride == jobOverrideAlwaysUseJobReqs {
		return
	}

	if shouldIncreaseJobRAMAfterHighPeak(job) {
		increaseJobRAMAfterHighPeak(job)
	}

	switch job.FailReason {
	case FailReasonDisk:
		increaseJobDiskAfterFailure(job)
	case FailReasonTime:
		increaseJobTimeAfterFailure(job)
	}
}

func queueClosedError(op, key string) error {
	return queue.Error{Queue: serverQueueName, Op: op, Item: key, Err: queue.ErrQueueClosed}
}

func matchesWaitingForDepGroupsFilter(job *Job, filter bool) bool {
	if !filter {
		return true
	}

	job.RLock()
	defer job.RUnlock()

	return len(job.WaitingForDepGroups) > 0
}

// runStatusWebSocketWorker runs one of a status websocket connection's
// workers, marking it complete so shutdown can wait before closing the database.
func (s *Server) runStatusWebSocketWorker(worker func()) {
	defer s.wsHandlerWG.Done()

	worker()
}

// setRC sets the runner command template under racmutex, matching the locking
// used by the readers (runnerCommand, readyAddedCallback). Production sets rc
// once at construction before any reader can run, but tests reconfigure it on a
// live server, so this synchronised setter keeps those writes race-free.
func (s *Server) setRC(rc string) {
	s.racmutex.Lock()
	s.rc = rc
	s.racmutex.Unlock()
}

// shutdownPprofServer gracefully shuts down the pprof endpoint started by
// maybeStartPprofServer (which makes its srv.Serve goroutine return
// http.ErrServerClosed), closing the listener. It is a no-op when srv is nil
// (pprof disabled). Shutdown is forced to complete after httpServerShutdownTime
// so a stuck profiling client can't hold up the manager's shutdown. Once the
// server is down it also disables the global mutex and block profiling that
// maybeStartPprofServer enabled, so the profiling overhead does not persist for
// the rest of the process (and does not pollute other tests in the same run).
func shutdownPprofServer(ctx context.Context, srv *http.Server) {
	if srv == nil {
		return
	}

	httpCtx, cancel := context.WithTimeout(ctx, httpServerShutdownTime)
	defer cancel()

	if err := srv.Shutdown(httpCtx); err != nil && !errors.Is(err, context.Canceled) {
		clog.Warn(ctx, "pprof endpoint shutdown failed", "err", err)
	}

	disablePprofProfiling()
}

// maybeStartPprofServer starts a dedicated net/http/pprof endpoint if the
// WR_PPROF_ADDR environment variable is set (eg. "localhost:6060"), and does
// nothing (returning nil) otherwise. The pprof handlers are registered on a
// private ServeMux (never http.DefaultServeMux, and never the manager's web
// server) served on the given address only, so operators should bind to
// localhost.
//
// The listener is bound synchronously first: if binding fails (eg. the address
// is already in use or invalid) it logs a warning and returns nil WITHOUT
// enabling profiling, so a failed endpoint never leaves the manager paying
// profiling overhead with nothing to reach. Only once the bind succeeds does it
// enable mutex and block profiling (so contention can be diagnosed) and serve;
// profiling is therefore on if and only if the endpoint is actually serving,
// and disablePprofProfiling() turns it back off on every exit path.
//
// The returned server (nil when disabled or on bind failure) must be passed to
// shutdownPprofServer when the manager stops, so the listener is closed rather
// than left running for the lifetime of the process.
func maybeStartPprofServer(ctx context.Context) *http.Server {
	addr := os.Getenv(envPprofAddr)
	if addr == "" {
		return nil
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		clog.Warn(ctx, "pprof endpoint not started; could not bind", "addr", addr, "env", envPprofAddr, "err", err)

		return nil
	}

	runtime.SetMutexProfileFraction(pprofMutexProfileFraction)
	runtime.SetBlockProfileRate(pprofBlockProfileRate)

	srv := &http.Server{Addr: ln.Addr().String(), Handler: newPprofMux(), ReadHeaderTimeout: httpReadHeaderTimeout}

	clog.Warn(ctx, "pprof profiling endpoint enabled", "addr", srv.Addr, "env", envPprofAddr)

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			clog.Warn(ctx, "pprof endpoint stopped", "err", err)
			disablePprofProfiling()
		}
	}()

	return srv
}

// newPprofMux returns a private ServeMux with the net/http/pprof handlers
// registered on it (never http.DefaultServeMux), for maybeStartPprofServer to
// serve on its dedicated listener.
func newPprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	return mux
}

// disablePprofProfiling turns off the global mutex and block profiling that
// maybeStartPprofServer enables. It is the disable half of that enable, and is
// called from every path that ends the endpoint (clean shutdown and an
// unexpected Serve exit) so profiling never lingers once the endpoint is gone.
// Calling it when profiling is already off is harmless.
func disablePprofProfiling() {
	runtime.SetMutexProfileFraction(0)
	runtime.SetBlockProfileRate(0)
}

func shouldIncreaseJobRAMAfterHighPeak(job *Job) bool {
	if job == nil || job.Requirements == nil || job.FailReason == "" {
		return false
	}

	if job.FailReason == FailReasonRAM {
		return true
	}

	return job.State == JobStateDelayed &&
		commandExceededMemoryEstimate(job.PeakRAM, job.Requirements.RAM)
}

func applyRecommendedJobRequirements(
	job *Job,
	jobOverride uint8,
	recommendedReq *scheduler.Requirements,
) {
	if recommendedReq == nil {
		return
	}

	applyRecommendedJobRAM(job, jobOverride, recommendedReq.RAM)
	applyRecommendedJobDisk(job, jobOverride, recommendedReq.Disk)
	applyRecommendedJobTime(job, jobOverride, recommendedReq.Time)
}

func applyRecommendedJobRAM(job *Job, jobOverride uint8, recommendedRAM int) {
	if recommendedRAM <= 0 {
		return
	}

	if job.RequirementsOrig.RAM == 0 {
		job.Requirements.RAM = recommendedRAM

		return
	}

	job.Requirements.RAM = preferredIntRequirement(
		job.Requirements.RAM,
		recommendedRAM,
		jobOverride,
	)
}

func applyRecommendedJobDisk(job *Job, jobOverride uint8, recommendedDisk int) {
	if recommendedDisk <= 0 {
		return
	}

	if job.RequirementsOrig.Disk == 0 && !job.RequirementsOrig.DiskSet {
		job.Requirements.Disk = recommendedDisk

		return
	}

	job.Requirements.Disk = preferredIntRequirement(
		job.Requirements.Disk,
		recommendedDisk,
		jobOverride,
	)
}

func preferredIntRequirement(current, recommended int, jobOverride uint8) int {
	switch jobOverride {
	case jobOverridePreferSystemReqs:
		return recommended
	case jobOverridePreferHigherReqs:
		if recommended > current {
			return recommended
		}
	}

	return current
}

func applyRecommendedJobTime(job *Job, jobOverride uint8, recommendedTime time.Duration) {
	if recommendedTime <= 0 {
		return
	}

	if job.RequirementsOrig.Time == 0 {
		job.Requirements.Time = recommendedTime

		return
	}

	job.Requirements.Time = preferredDurationRequirement(
		job.Requirements.Time,
		recommendedTime,
		jobOverride,
	)
}

func preferredDurationRequirement(
	current,
	recommended time.Duration,
	jobOverride uint8,
) time.Duration {
	switch jobOverride {
	case jobOverridePreferSystemReqs:
		return recommended
	case jobOverridePreferHigherReqs:
		if recommended > current {
			return recommended
		}
	}

	return current
}

func increaseJobRAMAfterHighPeak(job *Job) {
	const ramIncreaseRoundMB = 100

	// increase by 1GB or [100% if under 8GB, 30% if over],
	// whichever is greater, and round up to nearest 100MB.
	// increase to greater than max seen for jobs in our ReqGroup?
	updatedMB := float64(job.PeakRAM)
	if updatedMB <= RAMIncreaseMultBreakpoint {
		updatedMB *= RAMIncreaseMultLow
	} else {
		updatedMB *= RAMIncreaseMultHigh
	}

	if updatedMB < float64(job.PeakRAM)+RAMIncreaseMin {
		updatedMB = float64(job.PeakRAM) + RAMIncreaseMin
	}

	newRAM := int(math.Ceil(updatedMB/ramIncreaseRoundMB) * ramIncreaseRoundMB)
	if newRAM > job.Requirements.RAM {
		job.Requirements.RAM = newRAM
	}
}

func subscriptionUpdateState(_, to JobState) (JobState, bool) {
	switch to {
	case JobStateDelayed, JobStateDependent, JobStateReady, JobStateReserved,
		JobStateRunning, JobStateComplete, JobStateBuried, JobStateSuspended,
		JobStateDeleted:
		return to, true
	default:
	}

	return "", false
}

func waitForJobStartTime(job *Job) {
	for range serverMaxRetriesToStartRunning {
		<-time.After(serverWaitPeriodToStartRunning)

		job.RLock()

		if !job.StartTime.IsZero() {
			job.RUnlock()

			return
		}

		job.RUnlock()
	}
}

func jobUpdateTimes(job *Job) (*int64, *int64) {
	job.RLock()
	defer job.RUnlock()

	return jobUnixNano(job.StartTime), jobUnixNano(job.EndTime)
}

func jobUpdateFromStatus(status JStatus, state JobState, started, ended *int64) *JobUpdate {
	return &JobUpdate{
		Started:    started,
		Ended:      ended,
		Kind:       jobUpdateKind(state),
		Key:        status.Key,
		RepGroup:   status.RepGroup,
		State:      state,
		Exitcode:   status.Exitcode,
		FailReason: status.FailReason,
	}
}

func jobUpdateFromLockedJob(job *Job, state JobState) *JobUpdate {
	return &JobUpdate{
		Started:    jobUnixNano(job.StartTime),
		Ended:      jobUnixNano(job.EndTime),
		Kind:       jobUpdateKind(state),
		Key:        job.Key(),
		RepGroup:   job.RepGroup,
		State:      state,
		Exitcode:   job.Exitcode,
		FailReason: job.FailReason,
	}
}

func (s *Server) lostJobRetryCheck(jobKey string) (lostJobRetryCheck, bool) {
	item, err := s.q.Get(jobKey)
	if err != nil || item.Stats().State != queue.ItemStateRun {
		return lostJobRetryCheck{}, false
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return lostJobRetryCheck{}, false
	}

	job.RLock()
	defer job.RUnlock()

	if job.State != JobStateRunning || !job.Lost {
		return lostJobRetryCheck{}, false
	}

	timeout, _ := s.lostJobCheckDurations()

	return lostJobRetryCheck{
		jobKey:       job.Key(),
		jobHost:      job.Host,
		jobPID:       job.Pid,
		checkTimeout: timeout,
	}, true
}

func itemDefTriggersReadyAdded(itemdef *queue.ItemDef) bool {
	if itemdef.StartQueue == queue.SubQueueRun || itemdef.StartQueue == queue.SubQueueBury {
		return false
	}

	if itemdef.StartQueue == queue.SubQueueSuspended {
		return false
	}

	return itemdef.Delay == 0 && len(itemdef.Dependencies) == 0
}

func itemWillBecomeReadyAfterDependencyUpdate(item *queue.Item, err error) bool {
	if err != nil || item == nil {
		return false
	}

	return item.Stats().State == queue.ItemStateDependent && len(item.UnresolvedDependencies()) > 0
}

// ServerConfig is supplied to Serve() to configure your jobqueue server. All
// fields are required with no working default unless otherwise noted.
type ServerConfig struct {
	// Port for client-server communication.
	Port string

	// Port for the web interface.
	WebPort string

	// Name of the desired scheduler (eg. "local" or "lsf" or "openstack") that
	// jobs will be submitted to.
	SchedulerName string

	// SchedulerConfig should define the config options needed by the chosen
	// scheduler, eg. scheduler.ConfigLocal{Deployment: "production", Shell:
	// "bash"} if using the local scheduler.
	SchedulerConfig any

	// The command line needed to bring up a jobqueue runner client, which
	// should contain 6 %s parts which will be replaced with the scheduler
	// group, deployment, ip:host address of the server, domain name that the
	// server's certificate should be valid for, reservation time out and
	// maximum number of minutes allowed, eg. "my_jobqueue_runner_client --group
	// '%s' --deployment %s --server '%s' --domain %s --reserve_timeout %d
	// --max_mins %d". If you supply an empty string (the default), runner
	// clients will not be spawned; for any work to be done you will have to run
	// your runner client yourself manually.
	RunnerCmd string

	// Absolute path to where the database file should be saved. The database is
	// used to ensure no loss of added commands, to keep a permanent history of
	// all jobs completed, and to keep various stats, amongst other things.
	DBFile string

	// Absolute path to where the database file should be backed up to.
	DBFileBackup string

	// Absolute path to where the server will store the authorization token
	// needed by clients to communicate with the server. Storing it in a file
	// could make using any CLI clients more convenient. The file will be
	// read-only by the user starting the server. The default of empty string
	// means the token is not saved to disk.
	TokenFile string

	// Absolute path to where CA PEM file is that will be used for
	// securing access to the web interface. If the given file does not exist,
	// a certificate will be generated for you at this path.
	CAFile string

	// Absolute path to where certificate PEM file is that will be used for
	// securing access to the web interface. If the given file does not exist,
	// a certificate will be generated for you at this path.
	CertFile string

	// Absolute path to where key PEM file is that will be used for securing
	// access to the web interface. If the given file does not exist, a
	// key will be generated for you at this path.
	KeyFile string

	// Domain that a generated CertFile should be valid for. If not supplied,
	// defaults to "localhost".
	//
	// When using your own CertFile, this should be set to a domain that the
	// certifcate is valid for, as when the server spawns clients, those clients
	// will validate the server's certifcate based on this domain. For the web
	// interface and REST API, it is up to you to ensure that your DNS has an
	// entry for this domain that points to the IP address of the machine
	// running your server.
	CertDomain string

	// If using a CertDomain, and if you have (or very soon will) set the domain
	// to point to the server's IP address, set this to true. This will result
	// in runner clients spawned by the server being told to access the server
	// at CertDomain (instead of the current IP address), which means if the
	// server's host is lost and you bring it back at a different IP address and
	// update the domain again, those clients will be able to reconnect and
	// continue running.
	DomainMatchesIP bool

	// AutoConfirmDead is the time that a spawned server must be considered
	// dead before it is automatically destroyed and jobs running on it are
	// confirmed lost. The default of 0 time disables automatic destruction.
	// Only relevant when using a scheduler that spawns servers on which to
	// execute jobs.
	AutoConfirmDead time.Duration

	// Name of the deployment ("development" or "production"); development
	// databases are deleted and recreated on start up by default.
	Deployment string

	// CIDR is the IP address range of your network. When the server needs to
	// know its own IP address, it uses this CIDR to confirm it got it correct
	// (ie. it picked the correct network interface). You can leave this unset,
	// in which case it will do its best to pick correctly. (This is only a
	// possible issue if you have multiple network interfaces.)
	CIDR string

	// UploadDir is the directory where files uploaded to the Server will be
	// stored. They get given unique names based on the MD5 checksum of the file
	// uploaded. Defaults to /tmp.
	UploadDir string

	// Logger is a logger object that will be used to log uncaught errors and
	// debug statements. "Uncought" errors are all errors generated during
	// operation that either shouldn't affect the success of operations, and can
	// be ignored (logged at the Warn level, and which is why the errors are not
	// returned by the methods generating them), or errors that could not be
	// returned (logged at the Error level, eg. generated during a go routine,
	// such as errors by the server handling a particular client request).
	// We attempt to recover from panics during server operation and log these
	// at the Crit level.
	//
	// If your logger is levelled and set to the debug level, you will also get
	// information tracking the inner workings of the server.
	//
	// If this is unset, nothing is logged (defaults to a logger using a
	// log15.DiscardHandler()).
	Logger log15.Logger

	// Timings optionally overrides the server's timing parameters. Any zero
	// field uses its package default. Mainly useful for testing (to speed
	// scenarios up) and lets independent servers run with different timings.
	Timings ServerTimings

	// dontWipeDevDB stops a development-deployment server wiping any existing
	// database on startup (production never wipes regardless). Only set by
	// tests that restart a server and want to keep its database.
	dontWipeDevDB bool

	// forceBackups enables database backups even for a development deployment
	// (production always backs up regardless). Only set by tests.
	forceBackups bool
}

// resolveServerLogger returns the logger to use for the server: the one
// configured (namespaced), or a new logger that discards everything if none
// was configured.
func resolveServerLogger(config ServerConfig) log15.Logger {
	if config.Logger == nil {
		// log debug statements and "harmless" errors not worth returning (or
		// not possible to return), along with panics, to a discarding logger
		logger := log15.New()
		logger.SetHandler(log15.DiscardHandler())

		return logger
	}

	return config.Logger.New()
}

// persistToken writes the auth token to tokenFile, doing nothing if tokenFile
// is empty.
func persistToken(tokenFile string, token []byte) error {
	if tokenFile == "" {
		return nil
	}

	return os.WriteFile(tokenFile, token, ownerReadWrite)
}

// joinStartupMessages combines the certificate message (if any) with the
// database startup message, separating them with a full stop when both are set.
func joinStartupMessages(certMsg, dbMsg string) string {
	switch {
	case certMsg == "":
		return dbMsg
	case dbMsg == "":
		return certMsg
	default:
		return certMsg + ". " + dbMsg
	}
}

func keepPostUpgradeStartupStatus(dbFile string, upgradedOnOpen bool, logger log15.Logger) func() {
	if !upgradedOnOpen {
		return func() {}
	}

	if err := internal.WriteDBUpgradeStatus(dbFile, internal.DBUpgradeStatus{
		State:  postUpgradeStartupState,
		Detail: postUpgradeStartupDetail,
	}); err != nil {
		logger.Warn("failed to write post-upgrade startup status", "path", internal.DBUpgradeStatusPath(dbFile),
			"err", err)
	}

	return func() {
		if err := internal.RemoveDBUpgradeStatus(dbFile); err != nil {
			logger.Warn("failed to remove post-upgrade startup status", "path", internal.DBUpgradeStatusPath(dbFile),
				"err", err)
		}
	}
}

// closeOnError calls closeFn (typically a deferred resource close) only when
// *errp is already non-nil, wrapping any close error into *errp under name so
// the original error is preserved.
func closeOnError(errp *error, name string, closeFn func() error) {
	if *errp == nil {
		return
	}

	if errc := closeFn(); errc != nil {
		*errp = fmt.Errorf("%w; %s close also failed: %w", *errp, name, errc)
	}
}

// ensureCertificates checks that the configured TLS cert/key files exist,
// generating self-signed ones rooted at certDomain if they don't. It returns a
// message describing any certificate that was created (empty if none was).
func ensureCertificates(config ServerConfig, certDomain string, serverLogger log15.Logger) (string, error) {
	if internal.CheckCerts(config.CertFile, config.KeyFile) == nil {
		return "", nil
	}

	// if not, generate our own
	err := internal.GenerateCerts(config.CAFile, config.CertFile, config.KeyFile, certDomain,
		internal.DefaultBitsForRootRSAKey, internal.DefualtBitsForServerRSAKey, crand.Reader, internal.DefaultCertFileFlags)
	if err != nil {
		serverLogger.Error("GenerateCerts failed", "err", err)

		return "", err
	}

	return "created a new key and certificate for TLS", nil
}

// earliestCertExpiry returns the sooner of the CA and server certificate expiry
// times, erroring if either has already expired or cannot be read.
func earliestCertExpiry(caFile, certFile string) (time.Time, error) {
	expiry, err := internal.CertExpiry(caFile)
	if err != nil {
		return time.Time{}, err
	}

	if time.Now().After(expiry) {
		return time.Time{}, internal.CertError{Type: internal.ErrExpiredCert, Path: caFile}
	}

	expiry2, err := internal.CertExpiry(certFile)
	if err != nil {
		return time.Time{}, err
	}

	if time.Now().After(expiry2) {
		return time.Time{}, internal.CertError{Type: internal.ErrExpiredCert, Path: certFile}
	}

	if expiry2.Before(expiry) {
		expiry = expiry2
	}

	return expiry, nil
}

// configureAndListen sets the command socket's receive options, verifies the
// certificates have not expired, and starts listening for TLS connections,
// returning the earliest certificate expiry time.
func configureAndListen(sock mangos.Socket, interruptTime time.Duration,
	caFile, certFile, keyFile, port string) (time.Time, error) {
	// we open ourselves up to possible denial-of-service attack if a client
	// sends us tons of data, but at least the client doesn't silently hang
	// forever when it legitimately wants to Add() a ton of jobs
	// unlimited Recv() length
	if err := sock.SetOption(mangos.OptionMaxRecvSize, 0); err != nil {
		return time.Time{}, err
	}

	// we'll wait ServerInterruptTime to recv from clients before trying again,
	// allowing us to check if signals have been passed
	if err := sock.SetOption(mangos.OptionRecvDeadline, interruptTime); err != nil {
		return time.Time{}, err
	}

	// check certificate expiry, because everything breaks with generic errors
	// when it expires
	expiry, err := earliestCertExpiry(caFile, certFile)
	if err != nil {
		return time.Time{}, err
	}

	// have mangos listen using TLS over TCP
	if err := listenTLS(sock, caFile, certFile, keyFile, port); err != nil {
		return time.Time{}, err
	}

	return expiry, nil
}

// listenTLS makes the given socket listen for TLS-over-TCP connections on port,
// using the configured certificate and (if readable) CA pool.
func listenTLS(sock mangos.Socket, caFile, certFile, keyFile, port string) error {
	cer, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cer}}
	listenOpts := make(map[string]any)

	caCert, err := os.ReadFile(caFile)
	if err == nil {
		certPool := x509.NewCertPool()
		certPool.AppendCertsFromPEM(caCert)
		tlsConfig.RootCAs = certPool
	}

	listenOpts[mangos.OptionTLSConfig] = tlsConfig

	return sock.ListenOptions("tls+tcp://0.0.0.0:"+port, listenOpts)
}

// currentServerIP determines the non-loopback IP address other machines should
// use to reach this server, honouring config.DomainMatchesIP.
func currentServerIP(config ServerConfig, serverLogger log15.Logger) (string, error) {
	if config.DomainMatchesIP {
		return config.CertDomain, nil
	}

	ip, err := internal.CurrentIP(config.CIDR)
	if err != nil {
		serverLogger.Error("getting current IP failed", "err", err)
	}

	if ip == "" {
		return "", Error{"Serve", "", ErrNoHost}
	}

	return ip, nil
}

// Serve is for use by a server executable and makes it start listening on
// localhost at the configured port for Connect()ions from clients, and then
// handles those clients.
//
// It returns a *Server that you will typically call Block() on to block until
// your executable receives a SIGINT or SIGTERM, or you call Stop(), at which
// point the queues will be safely closed (you'd probably just exit at that
// point).
//
// If it creates a db file or recreates one from backup, and if it creates TLS
// certificates, it will say what it did in the returned msg string.
//
// The returned token must be provided by any client to authenticate. The server
// is a single user system, so there is only 1 token kept for its entire
// lifetime. If config.TokenFile has been set, the token will also be written to
// that file, potentially making it easier for any CLI clients to authenticate
// with this returned Server. If that file already exists prior to calling this,
// the token in that file will be re-used, allowing reconnection of existing
// clients if this server dies ungracefully.
//
// The possible errors from Serve() will be related to not being able to start
// up at the supplied address; errors encountered while dealing with clients are
// logged but otherwise ignored.
//
// It also spawns your runner clients as needed, running them via the configured
// job scheduler, using the configured shell. It determines the command line to
// execute for your runner client from the configured RunnerCmd string you
// supplied.
//
//nolint:gocyclo,funlen // entry point: sequential fallible setup + large Server literal, already split into helpers
func Serve(ctx context.Context, config ServerConfig) (s *Server, msg string, token []byte, err error) {
	serverLogger := resolveServerLogger(config)

	defer internal.LogPanic(ctx, "jobqueue serve", true)

	// optionally enable a profiling endpoint (off unless WR_PPROF_ADDR is set).
	// It is stored on the Server below and shut down in s.shutdown(); this defer
	// (via closeOnError, which no-ops unless err is set) only covers the
	// error-return paths before the Server is constructed, after which shutdown()
	// owns it, so the listener is never leaked.
	pprofServer := maybeStartPprofServer(ctx)

	defer func() {
		closeOnError(&err, "pprof", func() error {
			shutdownPprofServer(ctx, pprofServer)

			return nil
		})
	}()

	// resolve our timing parameters (config overrides, otherwise defaults)
	timings := config.Timings.withDefaults()

	// generate a secure token for clients to authenticate with
	token, err = generateToken(config.TokenFile)
	if err != nil {
		return s, msg, token, err
	}

	// check if the cert files are available
	httpAddr := "0.0.0.0:" + config.WebPort
	caFile := config.CAFile
	certFile := config.CertFile
	keyFile := config.KeyFile

	certDomain := cmp.Or(config.CertDomain, localhost)

	certMsg, err := ensureCertificates(config, certDomain, serverLogger)
	if err != nil {
		return s, msg, token, err
	}

	// we need to persist stuff to disk, and we do so using boltdb
	db, msg, err := initDB(
		ctx, config.DBFile, config.DBFileBackup, config.Deployment,
		!config.dontWipeDevDB, config.forceBackups,
	)
	msg = joinStartupMessages(certMsg, msg)

	if err != nil {
		return s, msg, token, err
	}

	db.recSecRound = timings.RecSecRound
	db.recMBRound = timings.RecMBRound
	db.setBatchTuning(timings.DBBatchDelay, timings.DBBatchSize)

	defer func() { closeOnError(&err, "db", func() error { return db.close(ctx) }) }()
	defer keepPostUpgradeStartupStatus(config.DBFile, db.upgradedOnOpen, serverLogger)()

	sock, err := xrep.NewSocket()
	if err != nil {
		return s, msg, token, err
	}

	defer func() { closeOnError(&err, "socket", sock.Close) }()

	var expiry time.Time

	expiry, err = configureAndListen(sock, timings.InterruptTime, caFile, certFile, keyFile, config.Port)
	if err != nil {
		return s, msg, token, err
	}

	// serving will happen in a goroutine that will stop on SIGINT or SIGTERM,
	// or if something is sent on the stopSigHandling channel. The done channel
	// is used to report back to a user that called Block() when and why we
	// stopped serving. stopClientHandling is used to stop client handling at
	// the right moment during the shutdown process. To know when all the
	// goroutines we start actually finish, the shutdown process will check a
	// waitgroup as well.
	sigs := make(chan os.Signal, signalChanBuffer)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

	stopSigHandling := make(chan bool, 1)
	stopClientHandling := make(chan bool)
	clientHandlingDone := make(chan struct{})
	done := make(chan error, 1)
	wg := waitgroup.New()

	// if we end up spawning clients on other machines, they'll need to know
	// our non-loopback ip address so they can connect to us
	//nolint:contextcheck // transitively calls internal.CurrentIP, a self-contained local-IP lookup with its own context
	ip, err := currentServerIP(config, serverLogger)
	if err != nil {
		return s, msg, token, err
	}

	// we will spawn runner clients via the requested job scheduler
	sch, err := scheduler.New(ctx, config.SchedulerName, config.SchedulerConfig)
	if err != nil {
		return s, msg, token, err
	}

	uploadDir := cmp.Or(config.UploadDir, defaultUploadDir)

	// our limiter will use a callback that gets group limits from our database
	l := limiter.New(db.retrieveLimitGroup)

	// bgCtx is the context the background startup goroutines (prior-state
	// recovery and the one-time complete-counter backfill) observe; shutdown
	// cancels it via s.bgCancel so those goroutines abort promptly and quietly
	// rather than racing scheduler cleanup, DB close or queue destroy.
	bgCtx, bgCancel := context.WithCancel(ctx)

	s = &Server{
		ServerInfo: &ServerInfo{
			Addr:          ip + ":" + config.Port,
			Host:          certDomain,
			FQDN:          fqdn(ctx),
			Port:          config.Port,
			WebPort:       config.WebPort,
			PID:           os.Getpid(),
			Deployment:    config.Deployment,
			Scheduler:     config.SchedulerName,
			Mode:          ServerModeNormal,
			TouchInterval: timings.TouchInterval,
			RetryWait:     timings.RetryWait,
			RetryTime:     timings.RetryTime,
		},
		ServerVersions:            &ServerVersions{Version: ServerVersion, API: restAPIVersion},
		token:                     token,
		uploadDir:                 uploadDir,
		sock:                      sock,
		ch:                        new(codec.BincHandle),
		rpl:                       newRGToKeys(),
		limiter:                   l,
		db:                        db,
		pprofServer:               pprofServer,
		stopSigHandling:           stopSigHandling,
		stopClientHandling:        stopClientHandling,
		clientHandlingDone:        clientHandlingDone,
		done:                      done,
		wg:                        wg,
		bgWG:                      waitgroup.New(),
		bgCancel:                  bgCancel,
		up:                        true,
		scheduler:                 sch,
		previouslyScheduledGroups: make(map[string]*sgroup),
		rc:                        config.RunnerCmd,
		wsconns:                   make(map[string]*websocket.Conn),
		statusCaster:              newCaster(),
		badServerCaster:           newCaster(),
		wsWriteMutexes:            make(map[string]*sync.Mutex),
		clientSubscriptions:       make(map[string]*serverSubscription),
		badServers:                make(map[string]*cloud.Server),
		schedCaster:               newCaster(),
		schedIssues:               make(map[string]*schedulerIssue),
		recoveredRunningJobs:      make(map[string]bool),
		recoveryPauseHook:         recoveryPauseHookForTest,
		timings:                   timings,
		itemTTR:                   timings.ItemTTR,
		lostJobCheckTimeout:       timings.LostJobCheckTimeout,
		lostJobCheckRetryTime:     timings.LostJobCheckRetryTime,
	}

	// create the queue now (its ready-added callback, which recovery's enqueue
	// relies on, is registered here rather than in serveWebInterface, so it is
	// safe for recovery to run in the background below).
	s.createQueue(ctx)

	// wait for signal or s.Stop() and call s.shutdown(). (We don't use the
	// waitgroup here since we call shutdown, which waits on the group)
	certExpired := time.After(time.Until(expiry))

	go s.handleSignals(ctx, sigs, certExpired, stopSigHandling)

	// set up the web interface
	ready := make(chan bool)
	wgk := wg.Add(1)

	go s.serveWebInterface(ctx, config, httpAddr, certFile, keyFile, wg, wgk, ready)

	<-ready

	// store token on disk
	if err = persistToken(config.TokenFile, token); err != nil {
		return s, msg, token, err
	}

	// mark ourselves recovering (total unknown for now) BEFORE we start
	// accepting client RPCs, so a pre-crash runner reconnecting during the
	// cheap live-bucket scan below gets a retryable ErrRecovering rather than a
	// terminal ErrBadJob for a to-be-restored job (spec B2: recovery timing
	// never causes a new false loss). This only sets an O(1) flag, so readiness
	// is not delayed; the true total is filled in by startPriorStateRecovery
	// after the scan, before the background goroutine runs (spec B1).
	s.setRecovering(0)

	// now that we're ready, set up responding to command-line clients
	wgk = wg.Add(1)

	go s.serveClients(ctx, sock, wg, wgk, stopClientHandling, clientHandlingDone)

	// recover any prior incomplete jobs in the background, so the manager
	// answers clients (ping/status/add) immediately regardless of how much
	// history or how many running jobs the db holds (spec B1). We read the
	// prior jobs synchronously (a cheap live-bucket scan) so recoveryProgress's
	// total is known before the background goroutine runs, then re-enqueue them
	// in the goroutine. Recovery keeps the single-batch enqueue so AddMany
	// resolves dependencies within the one batch.
	if err = s.startPriorStateRecovery(bgCtx, config, db); err != nil {
		// the scan failed before the background goroutine (which would clear
		// the flag) was launched, so clear the recovering flag we set above to
		// avoid leaving it stuck true (production die()s on this error, but keep
		// it clean).
		s.finishRecovering()

		return s, msg, token, err
	}

	return s, msg, token, err
}

// serveWebInterface runs the server's HTTP web interface and REST API, starts
// the status broadcasters, and registers the scheduler callbacks, signalling
// ready once ListenAndServe has had time to start.
func (s *Server) serveWebInterface(ctx context.Context, config ServerConfig, httpAddr, certFile,
	keyFile string, wg *waitgroup.WaitGroup, wgk string, ready chan<- bool) {
	// log panics and die
	defer internal.LogPanic(ctx, "jobqueue web server", true)
	defer wg.Done(wgk)

	srv := &http.Server{Addr: httpAddr, Handler: s.newWebMux(ctx), ReadHeaderTimeout: httpReadHeaderTimeout}

	wgk2 := wg.Add(1)
	go s.runHTTPServer(ctx, srv, certFile, keyFile, wg, wgk2)

	s.httpServer = srv

	s.startBroadcasters(ctx, wg)

	s.scheduler.SetBadServerCallBack(ctx, func(server *cloud.Server) {
		s.handleBadServerUpdate(ctx, server, config.AutoConfirmDead)
	})

	s.scheduler.SetMessageCallBack(ctx, s.recordSchedulerMessage)

	// wait a while for ListenAndServe() to start listening
	<-time.After(serverListenWait)

	ready <- true
}

// runHTTPServer serves the web interface over TLS until the server is shut
// down, logging any unexpected error.
func (s *Server) runHTTPServer(ctx context.Context, srv *http.Server, certFile, keyFile string,
	wg *waitgroup.WaitGroup, wgk string) {
	defer internal.LogPanic(ctx, "jobqueue web server listenAndServe", true)
	defer wg.Done(wgk)

	errs := srv.ListenAndServeTLS(certFile, keyFile)
	if errs != nil && !errors.Is(errs, http.ErrServerClosed) {
		clog.Error(ctx, "server web interface had problems", "err", errs)
	}
}

// newWebMux builds the HTTP request multiplexer for the web interface and REST
// API.
func (s *Server) newWebMux(ctx context.Context) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", webInterfaceStatic(ctx, s))
	mux.HandleFunc("/status_ws", webInterfaceStatusWS(ctx, s))
	mux.HandleFunc(restJobsEndpoint, restJobs(ctx, s))
	mux.HandleFunc(restWarningsEndpoint, restWarnings(ctx, s))
	mux.HandleFunc(restBadServersEndpoint, restBadServers(ctx, s))
	mux.HandleFunc(restFileUploadEndpoint, restFileUpload(ctx, s))
	mux.HandleFunc(restInfoEndpoint, restInfo(ctx, s))
	mux.HandleFunc(restVersionEndpoint, restVersion(ctx, s))

	return mux
}

// startBroadcasters launches the goroutines that broadcast bad-server and
// scheduler-issue updates to web interface clients.
func (s *Server) startBroadcasters(ctx context.Context, wg *waitgroup.WaitGroup) {
	wgk4 := wg.Add(1)

	go func() {
		defer internal.LogPanic(ctx, "jobqueue web server casting", true)
		defer wg.Done(wgk4)

		s.badServerCaster.Broadcasting(0)
	}()

	wgk5 := wg.Add(1)

	go func() {
		defer internal.LogPanic(ctx, "jobqueue web server scheduler casting", true)
		defer wg.Done(wgk5)

		s.schedCaster.Broadcasting(0)
	}()

	wgk6 := wg.Add(1)

	go func() {
		defer internal.LogPanic(ctx, "jobqueue web server status casting", true)
		defer wg.Done(wgk6)

		s.statusCaster.Broadcasting(0)
	}()
}

// serveClients launches numRPCReaders concurrent reader goroutines that admit
// client requests from the single command socket, and closes clientHandlingDone
// only once every reader has stopped, so waitForClientHandling still blocks
// until serving has fully stopped (spec B1). Concurrent RecvMsg() on this raw
// xrep socket is safe: mangos snapshots the shared recvQ channel under a brief
// lock and a Go channel receive delivers each message to exactly one reader, so
// distinct requests fan out to distinct readers without cross-talk, and each
// reply is routed by the message's own pipe-ID header (see reply/SendMsg).
func (s *Server) serveClients(ctx context.Context, sock mangos.Socket, wg *waitgroup.WaitGroup,
	wgk string, stopClientHandling <-chan bool, clientHandlingDone chan<- struct{}) {
	// log panics and die
	defer internal.LogPanic(ctx, "jobqueue serving", true)
	defer wg.Done(wgk)
	defer close(clientHandlingDone)

	n := numRPCReaders
	if n < 1 {
		n = 1
	}

	var readers sync.WaitGroup

	readers.Add(n)

	for range n {
		go s.serveClientsReader(ctx, sock, wg, &readers, stopClientHandling)
	}

	// block until every reader has exited (all observe the same closed
	// stopClientHandling), so clientHandlingDone (deferred above) closes only
	// after serving has fully stopped.
	readers.Wait()
}

// receiveClientMessage receives the next message from the command socket. It
// returns ok=false (logging unexpected errors) when no message was received.
func (s *Server) receiveClientMessage(ctx context.Context, sock mangos.Socket) (*mangos.Message, bool) {
	m, rerr := sock.RecvMsg()
	if rerr != nil {
		if !s.inShutdown() && !errors.Is(rerr, mangos.ErrRecvTimeout) {
			clog.Error(ctx, "Server socket Receive error", "err", rerr)
		}

		return nil, false
	}

	return m, true
}

// inShutdown reports whether the server is currently shutting down.
func (s *Server) inShutdown() bool {
	s.krmutex.RLock()
	defer s.krmutex.RUnlock()

	return s.killRunners
}

// dispatchClientRequest handles a single received client message, logging any
// error unless the server is shutting down.
func (s *Server) dispatchClientRequest(ctx context.Context, m *mangos.Message, wg *waitgroup.WaitGroup, wgk string) {
	// log panics and continue
	defer internal.LogPanic(ctx, "jobqueue server client handling", false)
	defer wg.Done(wgk)

	herr := s.handleRequest(ctx, m)
	if ServerLogClientErrors && herr != nil && !s.inShutdown() {
		clog.Error(ctx, "Server handle client request error", "err", herr)
	}
}

// handleSignals waits for an OS signal, certificate expiry, or s.Stop(), and
// shuts the server down with the appropriate reason. It returns once signal
// handling has been stopped.
func (s *Server) handleSignals(ctx context.Context, sigs chan os.Signal,
	certExpired <-chan time.Time, stopSigHandling <-chan bool) {
	// log panics and die
	defer internal.LogPanic(ctx, "jobqueue serving", true)

	for {
		select {
		case sig := <-sigs:
			var reason string

			switch sig {
			case os.Interrupt:
				reason = ErrClosedInt
			case syscall.SIGTERM:
				reason = ErrClosedTerm
			}

			signal.Stop(sigs)
			s.shutdown(ctx, reason, true, false)

			return
		case <-certExpired:
			signal.Stop(sigs)
			s.shutdown(ctx, ErrClosedCert, true, false)
		case <-stopSigHandling: // s.Stop() causes this to be sent during s.shutdown(), which it calls
			signal.Stop(sigs)

			return
		}
	}
}

// recordBadServerState records or forgets a server based on its current bad
// state, scheduling dead-confirmation for newly bad servers. It returns
// skip=true when the change should not be broadcast (an already-destroyed
// server). Must be called with s.bsmutex held.
func (s *Server) recordBadServerState(ctx context.Context, server *cloud.Server, autoConfirmDead time.Duration) bool {
	if !server.IsBad() {
		delete(s.badServers, server.ID)

		return false
	}

	// double check that due to timing issues this server hasn't been destroyed,
	// which is not something to warn anyone about
	if server.Destroyed() {
		return true
	}

	s.badServers[server.ID] = server

	// arrange to confirm this dead after the configured time
	if autoConfirmDead > 0 {
		go s.confirmServerDeadAfter(ctx, server.ID, autoConfirmDead)
	}

	return false
}

// handleBadServerUpdate processes a scheduler callback about a (possibly) bad
// cloud server: it records or forgets the server, optionally schedules its
// confirmation as dead after autoConfirmDead, and broadcasts the change to the
// web interface.
func (s *Server) handleBadServerUpdate(ctx context.Context, server *cloud.Server, autoConfirmDead time.Duration) {
	s.bsmutex.Lock()
	skip := s.recordBadServerState(ctx, server, autoConfirmDead)
	s.bsmutex.Unlock()

	if !skip {
		s.badServerCaster.Send(&BadServer{
			ID:      server.ID,
			Name:    server.Name,
			IP:      server.IP,
			Date:    time.Now().Unix(),
			IsBad:   server.IsBad(),
			Problem: server.PermanentProblem(),
		})
	}
}

// confirmServerDeadAfter waits autoConfirmDead and, if the identified server is
// still bad for at least that long, destroys it, kills its jobs and clears the
// web interface warning about it.
func (s *Server) confirmServerDeadAfter(ctx context.Context, serverID string, autoConfirmDead time.Duration) {
	<-time.After(autoConfirmDead)
	s.bsmutex.Lock()
	defer s.bsmutex.Unlock()

	badServer, exists := s.badServers[serverID]
	if !exists || badServer.BadDuration() < autoConfirmDead {
		return
	}

	delete(s.badServers, serverID)

	waited := badServer.BadDuration()
	errd := badServer.Destroy(ctx)
	clog.Warn(ctx, "server destroyed after remaining bad for some time",
		"server", serverID, "waited", waited, "err", errd)

	serverIDs := map[string]bool{serverID: true}
	s.killJobsOnServers(ctx, serverIDs)

	if errd == nil {
		// make the message in the web interface about this server go away
		s.badServerCaster.Send(&BadServer{
			ID:      serverID,
			Name:    badServer.Name,
			IP:      badServer.IP,
			Date:    time.Now().Unix(),
			IsBad:   false,
			Problem: badServer.PermanentProblem(),
		})
	}
}

// recordSchedulerMessage records a scheduler issue message (incrementing its
// count if already seen) and broadcasts it to the web interface.
func (s *Server) recordSchedulerMessage(msg string) {
	s.simutex.Lock()

	si, existed := s.schedIssues[msg]
	if existed {
		si.LastDate = time.Now().Unix()
		si.Count++
	} else {
		si = &schedulerIssue{
			Msg:       msg,
			FirstDate: time.Now().Unix(),
			LastDate:  time.Now().Unix(),
			Count:     1,
		}
		s.schedIssues[msg] = si
	}
	s.simutex.Unlock()
	s.schedCaster.Send(si)
}

// recoverPriorJobs builds queue item definitions for jobs that were incomplete
// when a previous server instance stopped, and enqueues them. It is a no-op if
// there were no prior jobs.
func (s *Server) recoverPriorJobs(ctx context.Context, config ServerConfig, priorJobs []*Job) error {
	if len(priorJobs) == 0 {
		return nil
	}

	var (
		loginUser string
		ttd       time.Duration
	)

	if cloudConfig, ok := config.SchedulerConfig.(scheduler.CloudConfig); ok {
		// *** for server recovery purposes, which involves ssh'ing to existing
		// servers and monitoring them, we need to know the login username, but
		// we don't. The best we can do is hope the configured default username
		// is the right one
		loginUser = cloudConfig.GetOSUser()
		ttd = cloudConfig.GetServerKeepTime()
	}

	itemdefs := make([]*queue.ItemDef, 0, len(priorJobs))

	for _, job := range priorJobs {
		// abort promptly if shutdown cancelled us, before doing further
		// per-job scheduler.Recover work or the enqueue below (which would
		// otherwise race scheduler cleanup and queue destroy during shutdown).
		if err := ctx.Err(); err != nil {
			return err
		}

		itemdef, err := s.recoveredItemDef(ctx, job, loginUser, ttd)
		if err != nil {
			return err
		}

		itemdefs = append(itemdefs, itemdef)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	_, _, err := s.enqueueItems(ctx, itemdefs)

	return err
}

// recoveredItemDef builds the queue item definition for a single recovered job,
// setting its start sub-queue based on the job's prior state and, for running
// jobs, re-incrementing limit groups and asking the scheduler to recover them.
func (s *Server) recoveredItemDef(ctx context.Context, job *Job, loginUser string,
	ttd time.Duration) (*queue.ItemDef, error) {
	deps, waitingForDepGroups, err := job.Dependencies.incompleteJobKeys(s.db)
	if err != nil {
		return nil, err
	}

	job.setWaitingForDepGroups(waitingForDepGroups)

	itemdef := &queue.ItemDef{
		Key: job.Key(), ReserveGroup: job.getSchedulerGroup(), Data: job,
		Priority: job.Priority, Delay: 0 * time.Second, TTR: s.itemTTRDuration(),
		Dependencies: deps,
	}

	switch job.State {
	case JobStateRunning:
		itemdef.StartQueue = queue.SubQueueRun

		s.recoverRunningJob(ctx, job, loginUser, ttd)
	case JobStateBuried:
		itemdef.StartQueue = queue.SubQueueBury
	case JobStateSuspended:
		itemdef.StartQueue = queue.SubQueueSuspended
	default:
		// any other recovered state keeps the default start queue.
	}

	return itemdef, nil
}

// recoverRunningJob re-increments a recovered running job's limit groups and
// asks the scheduler to recover the host it was running on.
func (s *Server) recoverRunningJob(ctx context.Context, job *Job, loginUser string, ttd time.Duration) {
	if len(job.LimitGroups) > 0 {
		if s.limiter.Increment(ctx, job.LimitGroups) {
			// (our note of incrementation done in the server that died is not
			//  stored in the db)
			job.noteIncrementedLimitGroups(job.LimitGroups)
		}
	}

	req := reqForScheduler(job.Requirements)

	scheduleCmd := s.groupToScheduleCmd(ctx, s.runnerCommand(), req.Stringify(), req)
	recoveredHost := &scheduler.RecoveredHostDetails{Host: job.Host, UserName: loginUser, TTD: ttd}

	errr := s.scheduler.Recover(ctx, scheduleCmd, req, recoveredHost)
	if errr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			clog.Debug(ctx, "recovery of an old cmd skipped during shutdown",
				"cmd", job.Cmd, "host", job.Host, "err", errr)
		} else {
			clog.Warn(ctx, "recovery of an old cmd failed", "cmd", job.Cmd, "host", job.Host, "err", errr)
		}
	}

	s.rrjMu.Lock()
	s.recoveredRunningJobs[job.Key()] = true
	s.rrjMu.Unlock()
}

// Block makes you block while the server does the job of serving clients. This
// will return with an error indicating why it stopped blocking, which will
// be due to receiving a signal or because you called Stop().
func (s *Server) Block() error {
	s.ssmutex.Lock()
	s.blocking = true
	s.ssmutex.Unlock()

	return <-s.done
}

// Stop will cause a graceful shut down of the server. Supplying an optional
// bool of true will cause Stop() to wait until all runners have exited and
// the server is truly down before returning.
func (s *Server) Stop(ctx context.Context, wait ...bool) {
	s.shutdown(ctx, ErrClosedStop, len(wait) == 1 && wait[0], true)
}

// Drain will stop the server spawning new runners and stop Reserve*() from
// returning any more Jobs. Once all current runners exit, we Stop().
func (s *Server) Drain(ctx context.Context) error {
	s.ssmutex.Lock()
	defer s.ssmutex.Unlock()

	if !s.up {
		return Error{"Drain", "", ErrNoServer}
	}

	if s.drain && s.ServerInfo.Mode == ServerModeDrain {
		return nil
	}

	s.drain = true
	s.ServerInfo.Mode = ServerModeDrain

	go s.waitForDrainComplete(ctx)

	return nil
}

// waitForDrainComplete polls until nothing is running (or the server is
// shutting down) and then Stop()s the server, waiting for runner clients to
// exit so the job scheduler is left clean.
func (s *Server) waitForDrainComplete(ctx context.Context) {
	defer internal.LogPanic(ctx, "jobqueue drain", true)

	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()

	for range ticker.C {
		// grab the queue under lock; shutdown() sets up false (and then nils q)
		// under the same lock, so if a shutdown has already begun we stop here
		// rather than racing its nil of s.q
		s.ssmutex.RLock()
		q := s.q
		up := s.up
		s.ssmutex.RUnlock()

		if !up || q == nil {
			return
		}

		// check our queue for things running, which is cheap
		if q.Stats().Running > 0 {
			continue
		}

		// now that we think nothing should be running, get Stop() to wait for
		// the runner clients to exit so the job scheduler will be nice and clean
		s.Stop(ctx, true)

		return
	}
}

// Pause is like Drain(), except that we don't Stop(). Returns true if we were
// not already paused.
func (s *Server) Pause() (bool, error) {
	s.ssmutex.Lock()
	defer s.ssmutex.Unlock()

	if !s.up {
		return false, Error{"Pause", "", ErrNoServer}
	}

	if s.drain {
		if s.ServerInfo.Mode == ServerModeDrain {
			return false, Error{"Pause", "", ErrBeingDrained}
		}
	}

	s.drain = true
	s.ServerInfo.Mode = ServerModePause
	s.pauseRequests++

	return s.pauseRequests == 1, nil
}

// Resume undoes Pause(). Does not return an error if we were not paused.
// If multiple pauses have been requested at once, actually does nothing until
// the number of resume requests matches the number of pauses.
// Returns true if actually resumed.
func (s *Server) Resume(ctx context.Context) (bool, error) {
	s.ssmutex.Lock()
	defer s.ssmutex.Unlock()

	if !s.up {
		return false, Error{"Resume", "", ErrNoServer}
	}

	if !s.drain {
		return false, nil
	}

	if s.ServerInfo.Mode == ServerModeDrain {
		return false, Error{"Resume", "", ErrBeingDrained}
	}

	s.pauseRequests--
	if s.pauseRequests > 0 {
		return false, nil
	} else if s.pauseRequests < 0 {
		s.pauseRequests = 0
	}

	s.drain = false
	s.ServerInfo.Mode = ServerModeNormal
	s.triggerReadyAddedCallback(ctx)

	return true, nil
}

// GetServerStats returns some simple live stats about what's happening in the
// server's queue.
func (s *Server) GetServerStats() *ServerStats {
	now := time.Now()

	stats := s.q.Stats()
	running, etc := s.runningCountAndETC(now)

	return &ServerStats{
		Delayed: stats.Delayed,
		Ready:   stats.Ready,
		Running: running,
		Buried:  stats.Buried,
		ETC:     etc.Truncate(time.Second).Sub(now.Truncate(time.Second)),
	}
}

// runningCountAndETC returns the number of running jobs and the latest time any
// of them is expected to finish (defaulting to now if none have a known end).
func (s *Server) runningCountAndETC(now time.Time) (int, time.Time) {
	running := 0
	etc := now

	for _, inter := range s.q.GetRunningData() {
		running++

		// work out when this Job is going to end, and update etc if later
		job, ok := inter.(*Job)
		if !ok {
			continue
		}

		job.RLock()

		if !job.StartTime.IsZero() && job.Requirements.Time.Seconds() > 0 {
			endTime := job.StartTime.Add(job.Requirements.Time)
			if endTime.After(etc) {
				etc = endTime
			}
		}

		job.RUnlock()
	}

	return running, etc
}

// BackupDB lets you do a manual live backup of the server's database to a given
// writer. Note that automatic backups occur to the configured location
// without calling this.
func (s *Server) BackupDB(w io.Writer) error {
	return s.db.backup(w)
}

// HasRunners tells you if there are currently runner clients in the job
// scheduler (either running or pending).
func (s *Server) HasRunners(ctx context.Context) bool {
	return s.scheduler.Busy(ctx)
}

// uploadFile uploads the given file data to the given path on the machine where
// the server process is running.
//
// If savePath is an empty string, the file is stored at a path based on the MD5
// checksum of the file data, rooted in the server's configured UploadDir. If it
// turns out such a file already exists, no error is generated. savePath can be
// prefixed with ~/ to have it saved relative to the server's home directory.
//
// Files stored will only be readable by the user that started the server.
//
// Note that this is only intended for a few small files, such as config files
// that need to be passed through to spawned cloud servers, when doing a cloud
// deployment.
//
// Returns the absolute path to the file that now contains the given file data.
func (s *Server) uploadFile(ctx context.Context, source io.Reader, savePath string) (string, error) {
	file, savePath, usedTempFile, err := s.openUploadDestination(ctx, savePath)
	if err != nil {
		return "", err
	}

	_, err = io.Copy(file, source)
	if err != nil {
		clog.Error(ctx, "uploadFile store file error", "err", err)

		return "", err
	}

	if errc := file.Close(); errc != nil {
		clog.Warn(ctx, "uploadFile close file error", "err", errc)
	}

	if usedTempFile {
		return s.finalizeTempUpload(ctx, savePath)
	}

	return savePath, nil
}

// openUploadDestination opens the file that uploaded data should be written to.
// If savePath is empty a temporary file is created under the server's upload
// directory (usedTempFile is then true and the returned path is its name);
// otherwise the chosen savePath (with ~/ expanded) is created.
func (s *Server) openUploadDestination(ctx context.Context, savePath string) (*os.File, string, bool, error) {
	if savePath == "" {
		file, tempPath, err := s.createUploadTempFile(ctx)

		return file, tempPath, true, err
	}

	savePath = internal.TildaToHome(savePath)

	//nolint:gosec // savePath is the destination an authenticated client deliberately chose to upload to
	if err := os.MkdirAll(filepath.Dir(savePath), os.ModePerm); err != nil {
		clog.Error(ctx, "uploadFile create directory error", "err", err)

		return nil, "", false, err
	}

	//nolint:gosec // savePath is the destination an authenticated client deliberately chose to upload to
	file, err := os.OpenFile(savePath, os.O_RDWR|os.O_CREATE, ownerReadWrite)
	if err != nil {
		clog.Error(ctx, "uploadFile create file error", "err", err)

		return nil, "", false, err
	}

	return file, savePath, false, nil
}

// createUploadTempFile creates a temporary file in the server's upload
// directory, creating the directory first if necessary.
func (s *Server) createUploadTempFile(ctx context.Context) (*os.File, string, error) {
	if _, err := os.Stat(s.uploadDir); err != nil && os.IsNotExist(err) {
		if err = os.MkdirAll(s.uploadDir, os.ModePerm); err != nil {
			clog.Error(ctx, "uploadFile create directory error", "err", err)

			return nil, "", err
		}
	}

	file, err := os.CreateTemp(s.uploadDir, "file_upload")
	if err != nil {
		clog.Error(ctx, "uploadFile temp file create error", "err", err)

		return nil, "", err
	}

	return file, file.Name(), nil
}

// finalizeTempUpload renames the just-written temp file to a path based on the
// md5 checksum of its contents, returning the final path. If a file already
// exists at that path the temp file is removed instead.
func (s *Server) finalizeTempUpload(ctx context.Context, tempPath string) (string, error) {
	md5, err := internal.FileMD5(ctx, tempPath)
	if err != nil {
		clog.Error(ctx, "uploadFile md5 calculation error", "err", err)

		return "", err
	}

	dir, leaf := calculateHashedDir(s.uploadDir, md5)

	//nolint:gosec // dir is rooted in the server's configured UploadDir, named after an md5 hash
	if err = os.MkdirAll(dir, os.ModePerm); err != nil {
		clog.Error(ctx, "uploadFile create directory error", "err", err)

		return "", err
	}

	finalPath := path.Join(dir, leaf)
	if err = placeUploadedFile(ctx, tempPath, finalPath); err != nil {
		return "", err
	}

	return finalPath, nil
}

// placeUploadedFile moves tempPath to finalPath, unless a file already exists
// at finalPath, in which case tempPath is removed instead.
func placeUploadedFile(ctx context.Context, tempPath, finalPath string) error {
	//nolint:gosec // finalPath is rooted in the server's configured UploadDir, named after an md5 hash
	_, err := os.Stat(finalPath)
	if err == nil {
		// already exists, delete the temp file
		//nolint:gosec // tempPath is a temp file created under the server's UploadDir
		if errr := os.Remove(tempPath); errr != nil {
			clog.Warn(ctx, "uploadFile file removal error", "err", errr)
		}

		return nil
	}

	if !os.IsNotExist(err) {
		clog.Error(ctx, "uploadFile stat file error", "err", err)

		return err
	}

	//nolint:gosec // both paths are rooted in the server's configured UploadDir
	if err = os.Rename(tempPath, finalPath); err != nil {
		clog.Error(ctx, "uploadFile rename file error", "err", err)

		return err
	}

	return nil
}

// createQueue creates and stores a queue.Queue on the Server and sets up its
// callbacks.
func (s *Server) createQueue(ctx context.Context) {
	q := queue.New(ctx, serverQueueName)
	s.q = q

	// we set a callback for things entering this queue's ready sub-queue.
	// This function will be called in a go routine and receives a slice of
	// all the ready jobs. Based on the requirements, we add to each job a
	// schedulerGroup, which the runners we spawn will be able to pass to
	// Reserve() so that they run the correct jobs for the machine and
	// resource reservations the job scheduler will run them under. queue
	// package will only call this once at a time, so we don't need to worry
	// about locking across the whole function.
	q.SetReadyAddedCallback(func(_ string, allitemdata []any) {
		s.readyAddedCallback(ctx, q, allitemdata)
	})

	// we set a callback for things changing in the queue, which lets us
	// update the status webpage with the minimal work and data transfer
	q.SetChangedCallback(func(fromQ, toQ queue.SubQueue, data []any) {
		s.emitChangeCallbackTransition(ctx, fromQ, toQ, data)
	})

	// we set a callback for running items that hit their ttr because the
	// runner died or because of networking issues: we keep them in the
	// running queue, but mark them up as having possibly failed, leaving it
	// up the user if they want to confirm the jobs are dead by killing
	// them or leaving them to spring back to life if not. If they already
	// killed it, however, we'll do normal releasing behaviour afterwards.
	q.SetTTRCallback(func(data any) queue.SubQueue {
		job := data.(*Job) //nolint:errcheck,forcetypeassert // queue only ever stores *Job

		return s.ttrCallback(ctx, job)
	})
}

// ttrCallback handles a running item hitting its TTR. A TTR-expired job is
// marked lost and kept in the run queue while its death is confirmed
// asynchronously; an on-time touch resets the TTR (via q.Touch), and a late
// touch clears the lost flag and recovers the job. Under socket saturation a
// touch RPC can be processed after the TTR deadline, so this callback can fire
// even for a still-alive, responsive runner. That is benign: a spuriously-lost
// job is parked in Run, is never re-reserved while its runner owns it, and its
// owner's successful archive is still accepted; a later touch clears the lost
// flag. A job that is already lost stays parked in the run queue (a touch will
// recover it, or the in-flight confirmation will kill it) without being
// re-marked or re-confirmed.
//
// Both a started-then-silent job and a reserved-but-never-started job whose TTR
// expires are handled the same way: marked lost and parked in Run, with death
// confirmed asynchronously via the runner's host+pid (recorded at reserve for
// the reserved-not-started case, spec C1/C2). We never requeue a
// reserved-not-started job on a StartTime.IsZero() proxy, so a live-but-
// backlogged runner's job is never re-reserved, while a genuinely dead runner's
// job is still reclaimed once confirmed dead; an old client (pid 0) is not
// confirmed dead and stays parked (confirmJobDead returns false), recovering
// only when its Started/Touch finally drains. Only a released/finished item
// (job.Exited) awaiting its delay proceeds to the delay sub-queue.
func (s *Server) ttrCallback(ctx context.Context, job *Job) queue.SubQueue {
	job.Lock()

	// a released/finished item awaiting its delay is not a live reservation; let
	// it proceed to the delay sub-queue as before.
	if job.Exited {
		job.Unlock()
		job.decrementLimitGroups(s.limiter)

		return queue.SubQueueDelay
	}

	// an already-lost job is left parked; its death is already being confirmed
	// and a touch will recover it, so we neither re-mark nor re-confirm it.
	if job.Lost {
		job.Unlock()

		return queue.SubQueueRun
	}

	job.Lost = true
	job.FailReason = FailReasonLost
	job.EndTime = time.Now()
	lostUpdate := jobUpdateFromLockedJob(job, JobStateLost)

	// we don't test recovered jobs are dead because they might have exited
	// while the server wasn't running, and we want the existing client to tell
	// us if it should be archived or buried
	defer s.markJobLost(ctx, job, false, lostUpdate)

	return queue.SubQueueRun
}

// markJobLost records a running->lost transition for job (which must be locked;
// it is unlocked here) and asynchronously confirms whether the job is dead,
// killing or releasing it as appropriate. It runs as a deferred call from
// ttrCallback while the queue mutex is still held.
func (s *Server) markJobLost(ctx context.Context, job *Job, wasLost bool, lostUpdate *JobUpdate) {
	killCalled := job.killCalled
	jobKey := job.Key()
	jobHost := job.Host
	jobPID := job.Pid
	repGroup := job.RepGroup
	serverLostJobCheckTimeout, serverLostJobCheckRetryTime := s.lostJobCheckDurations()
	job.Unlock()

	// since our changed callback won't be called, record this running -> lost
	// transition through the single chokepoint: the web-UI status-count delta
	// always (statusCaster derives the "+all+" aggregate from the contribution),
	// and the pre-built lost subscription update only if the job wasn't already
	// lost. Both run after job.Unlock while queue.mutex is still held; neither
	// statusCaster.Send nor the subscription locks are ever taken before the
	// queue lock.
	s.emitJobTransition(
		[]countContribution{{from: JobStateRunning, to: JobStateLost, repGroup: repGroup, n: 1}},
		func() {
			if !wasLost {
				s.enqueueSubscriptionUpdate(lostUpdate, false)
			}
		},
	)

	go s.confirmOrReleaseLostJob(ctx, job, lostJobDetails{
		key: jobKey, host: jobHost, pid: jobPID, killCalled: killCalled,
		checkTimeout: serverLostJobCheckTimeout, checkRetryTime: serverLostJobCheckRetryTime,
	})
}

// lostJobDetails captures the fields a lost job's asynchronous confirmation
// needs, snapshotted while the job was locked.
type lostJobDetails struct {
	key            string
	host           string
	pid            int
	killCalled     bool
	checkTimeout   time.Duration
	checkRetryTime time.Duration
}

// confirmOrReleaseLostJob confirms whether a lost job is really dead and kills
// it, or (if the user already called kill) releases it back to the run queue.
func (s *Server) confirmOrReleaseLostJob(ctx context.Context, job *Job, d lostJobDetails) {
	s.rrjMu.RLock()
	recovered := s.recoveredRunningJobs[d.key]
	s.rrjMu.RUnlock()

	confirmedDead := !d.killCalled && !recovered
	if confirmedDead {
		confirmedDead = s.confirmJobDeadAndKill(ctx, d.key, d.host, d.pid, d.checkTimeout, d.checkRetryTime)
	}

	switch {
	case confirmedDead:
		clog.Info(ctx, "killed a job after confirming it was dead", "key", d.key)
	case d.killCalled:
		defer internal.LogPanic(ctx, "jobqueue ttr callback releaseJob", true)

		// wait for the item to go back to run queue
		<-time.After(ttrReleaseWait)

		// now release it
		err := s.releaseJob(ctx, job, &JobEndState{Exitcode: -1, Exited: true}, FailReasonLost, false, false)
		if err != nil {
			clog.Warn(ctx, "failed to release job after TTR", "err", err)
		}
	}
}

// readyAddedCallback is the queue's ready-added callback: it groups the ready
// jobs by scheduler group and, if a runner command is configured, schedules
// runners for each group. The queue calls this one at a time.
func (s *Server) readyAddedCallback(ctx context.Context, q *queue.Queue, allitemdata []any) {
	defer internal.LogPanic(ctx, "jobqueue ready added callback", true)

	clog.Debug(ctx, "rac started")
	defer clog.Debug(ctx, "rac finished")

	s.ssmutex.RLock()

	if s.drain || !s.up {
		s.ssmutex.RUnlock()

		return
	}

	s.ssmutex.RUnlock()

	s.rpmutex.Lock()
	s.racRunning = true
	s.rpmutex.Unlock()

	defer s.finishRAC()

	s.racmutex.RLock()
	rc := s.rc
	s.racmutex.RUnlock()

	groups := s.buildSchedulerGroups(ctx, q, allitemdata, rc)

	// We build scheduler groups above regardless (so newly added jobs get their
	// reserve group and stay reservable), but we do NOT dispatch runners for new
	// work while background prior-state recovery is still running (spec B1):
	// recovered running jobs have not yet been fully re-accounted in the
	// scheduler, so scheduling now could overcommit resources those recovered
	// runners still occupy. When recovery finishes it re-triggers this callback
	// (see recoverInBackground), so these ready jobs are then scheduled against
	// capacity that includes the recovered running jobs.
	if rc != "" && !s.isRecovering() {
		s.scheduleGroupRunners(ctx, q, groups)
	}
}

// buildSchedulerGroups calculates, sets and counts the ready jobs by scheduler
// group, updating each job's requirements and (when rc is set) its scheduler
// group, while respecting limit-group capacities.
func (s *Server) buildSchedulerGroups(ctx context.Context, q *queue.Queue,
	allitemdata []any, rc string) map[string]*sgroup {
	groups := make(map[string]*sgroup)
	reqGroupToReqs := make(map[string]*scheduler.Requirements)
	groupLimits := make(map[string]int)

	for _, inter := range allitemdata {
		job, ok := inter.(*Job)
		if !ok {
			continue
		}

		s.processReadyJob(ctx, q, job, rc, groups, reqGroupToReqs, groupLimits)
	}

	return groups
}

// processReadyJob updates one ready job's requirements and scheduler group, and
// (when rc is set) counts it against its scheduler group.
func (s *Server) processReadyJob(ctx context.Context, q *queue.Queue, job *Job, rc string,
	groups map[string]*sgroup, reqGroupToReqs map[string]*scheduler.Requirements, groupLimits map[string]int) {
	job.RLock()
	jobOverride := job.Override
	reqGroup := job.ReqGroup
	failureUpdateNeeded := failureMayUpdateJobRequirements(job)
	job.RUnlock()

	// depending on job.Override, get memory, disk and time recommendations,
	// which are rounded to get fewer larger groups
	recommendedReq := s.recommendedReqForGroup(reqGroup, reqGroupToReqs)

	if recommendedReq != nil || failureUpdateNeeded {
		job.Lock()
		updateJobRequirementsForRetry(job, jobOverride, recommendedReq)
		job.Unlock()
	}

	snapshot := job.schedulerGroupSnapshot()

	if rc == "" {
		return
	}

	if snapshot.previousGroup != snapshot.group {
		job.setSchedulerGroup(snapshot.group)

		warnUnexpectedSetReserveGroupError(ctx, q.SetReserveGroup(snapshot.key, snapshot.group))
	}

	s.countJobInGroup(ctx, groups, groupLimits, snapshot)
}

// recommendedReqForGroup returns the recommended requirements for reqGroup,
// using and populating the cache. A nil entry (cached) means recommendations
// could not be determined.
func (s *Server) recommendedReqForGroup(reqGroup string,
	cache map[string]*scheduler.Requirements) *scheduler.Requirements {
	if rec, existed := cache[reqGroup]; existed {
		return rec
	}

	recm, errm := s.db.recommendedReqGroupMemory(reqGroup)
	recd, errd := s.db.recommendedReqGroupDisk(reqGroup)

	recs, errs := s.db.recommendedReqGroupTime(reqGroup)
	if errm != nil || errd != nil || errs != nil {
		cache[reqGroup] = nil

		return nil
	}

	recdGBs := 0
	if recd > 0 {
		recdGBs = int(math.Ceil(float64(recd) / float64(mbPerGB)))
	}

	recommendedReq := &scheduler.Requirements{
		RAM:     max(recm, 0),
		Disk:    recdGBs,
		DiskSet: true,
		Time:    time.Duration(max(recs, 0)) * time.Second,
	}
	cache[reqGroup] = recommendedReq

	return recommendedReq
}

// countJobInGroup records a single ready job against its scheduler group,
// creating the group if needed and skipping jobs that would exceed the group's
// limit-group capacity.
func (s *Server) countJobInGroup(ctx context.Context, groups map[string]*sgroup,
	groupLimits map[string]int, snapshot schedulerGroupSnapshot) {
	schedulerGroup := snapshot.group

	group, set := groups[schedulerGroup]
	if !set {
		group = &sgroup{
			name: schedulerGroup,
			req:  snapshot.requirements.Clone(),
		}
		groups[schedulerGroup] = group
	}

	// ignore jobs that would put us over the limit
	limit := s.groupRemainingCapacity(ctx, schedulerGroup, groupLimits)
	if limit >= 0 && group.count == limit {
		group.skipped++

		return
	}

	group.count++

	if snapshot.priority > group.priority {
		group.priority = snapshot.priority
	}
}

// groupRemainingCapacity returns the remaining limit-group capacity for a
// scheduler group (-1 if the group has no limit groups), caching the result in
// groupLimits.
func (s *Server) groupRemainingCapacity(ctx context.Context, schedulerGroup string, groupLimits map[string]int) int {
	if limit, set := groupLimits[schedulerGroup]; set {
		return limit
	}

	limit := -1

	limitGroups := s.schedGroupToLimitGroups(schedulerGroup)
	if len(limitGroups) > 0 {
		limit = s.limiter.GetRemainingCapacity(ctx, limitGroups)
	}

	groupLimits[schedulerGroup] = limit

	return limit
}

// scheduleGroupRunners adds running jobs into the group counts, unschedules
// groups no longer needed, schedules runners for the current groups, and
// arranges for the ready-added callback to fire again later. Only called when a
// runner command is configured.
func (s *Server) scheduleGroupRunners(ctx context.Context, q *queue.Queue, groups map[string]*sgroup) {
	for name, group := range groups {
		clog.Debug(ctx, "rac saw ready jobs", "group", name, "count", group.count, "limitskipped", group.skipped)
	}

	s.psgmutex.Lock()
	s.accountForRunningJobs(q, groups)
	s.unscheduleUnneededGroups(ctx, groups)

	// schedule runners for each group in the job scheduler
	for name, group := range groups {
		s.scheduleGroup(ctx, name, group)
	}
	s.psgmutex.Unlock()

	s.scheduleRACRecheck(ctx, q)
}

// scheduleGroup records group as previously scheduled and asynchronously
// schedules its runners against a snapshot of the group. It does NOT hold the
// sgroup lock across scheduleRunners: the external scheduler command (eg. bsub)
// can be slow, and holding the sgroup write lock across it would block
// concurrent count decrements and skip checks (which take the sgroup RLock while
// holding s.psgmutex), deadlocking the archive/scheduling paths. The real group
// stays in previouslyScheduledGroups so decrementGroupCount/hasSkips operate on
// it normally; scheduling correctness for the same cmd is preserved by the
// scheduler's own per-cmd coalescing. Must be called with s.psgmutex held.
func (s *Server) scheduleGroup(ctx context.Context, name string, group *sgroup) {
	if group.count <= 0 {
		clog.Debug(ctx, "rac scheduling no jobs", "group", name, "count", group.count, "limitskipped", group.skipped)
	} else {
		clog.Debug(ctx, "rac scheduling jobs", "group", name, "count", group.count, "limitskipped", group.skipped)
	}

	s.previouslyScheduledGroups[name] = group

	snapshot := group.snapshot()

	wgk := s.wg.Add(1)

	go func(group *sgroup) {
		defer internal.LogPanic(ctx, "jobqueue schedule runners", true)
		defer s.wg.Done(wgk)

		s.scheduleRunners(ctx, group)
	}(snapshot)
}

// accountForRunningJobs adds currently running jobs into the group counts so
// scheduling accounts for them. Must be called with s.psgmutex held.
func (s *Server) accountForRunningJobs(q *queue.Queue, groups map[string]*sgroup) {
	for _, inter := range q.GetRunningData() {
		job, ok := inter.(*Job)
		if !ok {
			continue
		}

		schedulerGroup := job.getSchedulerGroup()

		group, set := groups[schedulerGroup]
		if !set {
			group = s.groupForRunningJob(schedulerGroup, job)
			groups[schedulerGroup] = group
		}

		group.count++
	}
}

// groupForRunningJob returns the sgroup a running job belongs to, reusing a
// previously scheduled group's requirements if known, otherwise building a new
// group from the job's own requirements.
func (s *Server) groupForRunningJob(schedulerGroup string, job *Job) *sgroup {
	if prev, set := s.previouslyScheduledGroups[schedulerGroup]; set {
		return prev.clone(0)
	}

	// this can happen if a newly added job is reserved the moment it is added,
	// so it becomes running before being processed by this rac
	job.Lock()
	defer job.Unlock()

	return &sgroup{
		name: schedulerGroup,
		req:  job.Requirements.Clone(),
	}
}

// unscheduleUnneededGroups asynchronously unschedules any previously scheduled
// groups that are no longer in groups. Must be called with s.psgmutex held.
func (s *Server) unscheduleUnneededGroups(ctx context.Context, groups map[string]*sgroup) {
	for name, group := range s.previouslyScheduledGroups {
		if _, needed := groups[name]; needed {
			continue
		}

		wgk := s.wg.Add(1)

		go func(group *sgroup) {
			defer internal.LogPanic(ctx, "jobqueue unschedule runners", true)
			defer s.wg.Done(wgk)

			clog.Debug(ctx, "rac unscheduling uneeded group", "group", group.name)
			s.scheduleRunners(ctx, group.clone(0))
		}(group.clone(0))

		delete(s.previouslyScheduledGroups, name)
		clog.Debug(ctx, "rac deleted previous unneeded group", "group", name)
	}
}

// scheduleRACRecheck ensures the ready-added callback fires again after
// CheckRunnerTime, in case spawned runners die or exit without new jobs being
// added.
func (s *Server) scheduleRACRecheck(ctx context.Context, q *queue.Queue) {
	s.racmutex.Lock()
	defer s.racmutex.Unlock()

	if s.racChecking {
		if !s.racCheckTimer.Stop() {
			<-s.racCheckTimer.C
		}

		s.racCheckTimer.Reset(s.timings.CheckRunnerTime)

		return
	}

	s.racCheckTimer = time.NewTimer(s.timings.CheckRunnerTime)

	wgk := s.wg.Add(1)

	go s.waitThenRecheckRAC(ctx, q, wgk)

	s.racChecking = true
}

// waitThenRecheckRAC waits for the RAC re-check timer (or server shutdown) and,
// if there are still ready jobs, re-triggers the ready-added callback.
func (s *Server) waitThenRecheckRAC(ctx context.Context, q *queue.Queue, wgk string) {
	defer internal.LogPanic(ctx, "jobqueue rac checking", true)
	defer s.wg.Done(wgk)

	select {
	case <-s.racCheckTimer.C:
	case <-s.stopClientHandling:
		return
	}

	s.racmutex.Lock()
	s.racChecking = false
	stats := q.Stats()

	if stats.Ready > 0 {
		s.racmutex.Unlock()
		s.triggerReadyAddedCallback(ctx)
	} else {
		s.racmutex.Unlock()
	}
}

// enqueueItems adds new items to a queue, for when we have new jobs to handle.
func (s *Server) enqueueItems(ctx context.Context, itemdefs []*queue.ItemDef) (added, dups int, err error) {
	readyCallbackExpected := slices.ContainsFunc(itemdefs, itemDefTriggersReadyAdded)

	if readyCallbackExpected {
		s.setRACPending()
	}

	added, dups, err = s.q.AddMany(ctx, itemdefs)
	if err != nil {
		if readyCallbackExpected {
			s.clearRACPending()
		}

		return added, dups, err
	}

	if readyCallbackExpected && added == 0 {
		s.clearRACPending()
	}

	s.recordRepGroupKeys(itemdefs)

	return added, dups, err
}

// recordRepGroupKeys adds each item's RepGroup->key mapping to the lookup and
// then remembers each as a subscription key.
func (s *Server) recordRepGroupKeys(itemdefs []*queue.ItemDef) {
	type repGroupKey struct {
		repGroup string
		key      string
	}

	repGroupKeys := make([]repGroupKey, 0, len(itemdefs))

	s.rpl.Lock()
	for _, itemdef := range itemdefs {
		job, ok := itemdef.Data.(*Job)
		if !ok {
			continue
		}

		rp := job.RepGroup
		s.rpl.Add(rp, itemdef.Key)
		repGroupKeys = append(repGroupKeys, repGroupKey{repGroup: rp, key: itemdef.Key})
	}
	s.rpl.Unlock()

	for _, rgk := range repGroupKeys {
		s.rememberRepGroupSubscriptionKey(rgk.repGroup, rgk.key)
	}
}

// prepareInputJobs locks and initialises each input job (env key, retry count,
// scheduler group, bsub id and user-specified limit groups), returning the
// limit groups to store and the set of input job keys.
func (s *Server) prepareInputJobs(inputJobs []*Job, envkey string,
	rcSet bool) (map[string]*limiter.GroupData, map[string]bool) {
	limitGroups := make(map[string]*limiter.GroupData)
	inputJobKeys := make(map[string]bool, len(inputJobs))

	for _, job := range inputJobs {
		job.Lock()
		job.EnvKey = envkey

		job.UntilBuried = job.Retries + 1
		if rcSet {
			job.schedulerGroup = job.generateSchedulerGroup(job.Requirements)
		}

		if job.BsubMode != "" {
			job.BsubID = atomic.AddUint64(&BsubID, 1)
		}

		if len(job.LimitGroups) > 0 {
			s.handleUserSpecifiedJobLimitGroups(job, limitGroups)
		}

		inputJobKeys[job.Key()] = true

		job.Unlock()
	}

	return limitGroups, inputJobKeys
}

// createJobs creates new jobs, adding them to the database and the in-memory
// queue. It returns 2 errors; the first is one of our Err constant strings,
// the second is the actual error with more details.
func (s *Server) createJobs(
	ctx context.Context,
	inputJobs []*Job,
	envkey string,
	ignoreComplete bool,
) (added int, dups int, alreadyComplete int, warnings AddWarnings, srerr string, qerr error) {
	s.racmutex.RLock()
	rcSet := s.rc != ""
	s.racmutex.RUnlock()

	var queuedDups int

	inputJobs, queuedDups = s.jobsNotAlreadyQueued(inputJobs, ignoreComplete)

	// create itemdefs for the jobs
	limitGroups, inputJobKeys := s.prepareInputJobs(inputJobs, envkey, rcSet)

	err := s.storeLimitGroups(limitGroups)
	if err != nil {
		return added, dups, alreadyComplete, warnings, ErrDBError, err
	}

	// keep an on-disk record of these new jobs; we sacrifice a lot of speed by
	// waiting on this database write to persist to disk. The alternative would
	// be to return success to the client as soon as the jobs were in the in-
	// memory queue, then lazily persist to disk in a goroutine, but we must
	// guarantee that jobs are never lost or a workflow could hopelessly break
	// if the server node goes down between returning success and the write to
	// disk succeeding. (If we don't return success to the client, it won't
	// Remove the job that created the new jobs from the queue and when we
	// recover, at worst the creating job will be run again - no jobs get lost.)
	jobsToQueue, jobsToUpdate, alreadyComplete, err := s.db.storeNewJobs(ctx, inputJobs, ignoreComplete)
	if err != nil {
		return added, dups, alreadyComplete, warnings, ErrDBError, err
	}

	itemdefs := s.itemDefsForNewJobs(jobsToQueue, inputJobKeys, &warnings)

	added, dups, srerr, qerr = s.queueNewJobItems(ctx, jobsToUpdate, itemdefs, ignoreComplete, queuedDups)

	return added, dups, alreadyComplete, warnings, srerr, qerr
}

// itemDefsForNewJobs builds the queue item definitions for the jobs returned by
// storeNewJobs, recording in warnings any never-seen dependency groups
// referenced by the originally input jobs.
func (s *Server) itemDefsForNewJobs(jobsToQueue []*Job,
	inputJobKeys map[string]bool, warnings *AddWarnings) []*queue.ItemDef {
	// now that jobs are in the db we can get dependencies fully, so now we can
	// build our itemdefs *** we really need to test for cycles, because if the
	// user creates one, we won't let them delete the bad jobs! storeNewJobs()
	// returns jobsToQueue, which is all of cr.Jobs plus any previously
	// Archive()d jobs that were resurrected because of one of their DepGroup
	// dependencies being in cr.Jobs
	var itemdefs []*queue.ItemDef

	warningDepGroups := make(map[string]bool)

	for _, job := range jobsToQueue {
		deps, waitingForDepGroups, err := job.Dependencies.incompleteJobKeys(s.db)
		if err != nil {
			// srerr/qerr are unconditionally overwritten by
			// updateJobDependencies below, so there is nothing to record here
			// beyond stopping the loop.
			break
		}

		job.setWaitingForDepGroups(waitingForDepGroups)

		if inputJobKeys[job.Key()] {
			collectStrings(waitingForDepGroups, warningDepGroups)
		}

		itemdefs = append(itemdefs, &queue.ItemDef{
			Key: job.Key(), ReserveGroup: job.getSchedulerGroup(), Data: job,
			Priority: job.Priority, Delay: 0 * time.Second, TTR: s.itemTTRDuration(),
			Dependencies: deps,
		})
	}

	warnings.NeverSeenDepGroups = sortedStringSet(warningDepGroups)

	return itemdefs
}

// queueNewJobItems updates dependencies of existing jobs, replaces any live
// rerun items, and enqueues the new item definitions, returning the counts of
// jobs added and duplicated plus any error.
func (s *Server) queueNewJobItems(ctx context.Context, jobsToUpdate []*Job, itemdefs []*queue.ItemDef,
	ignoreComplete bool, queuedDups int) (added, dups int, srerr string, qerr error) {
	srerr, qerr = s.updateJobDependencies(ctx, jobsToUpdate)

	var replaced int
	if qerr == nil {
		itemdefs, replaced, qerr = s.replaceLiveRerunItems(ctx, itemdefs, ignoreComplete)
	}

	// if anything has gone wrong up to here, the error is an internal one and
	// nothing more gets queued
	if qerr != nil {
		return added, dups, ErrInternalError, qerr
	}

	// add the jobs to the in-memory job queue
	added, dups, qerr = s.enqueueItems(ctx, itemdefs)
	dups += queuedDups
	added += replaced

	if qerr != nil {
		srerr = ErrInternalError
	}

	return added, dups, srerr, qerr
}

// handleUserSpecifiedJobLimitGroups takes limit groups on a job that may have
// been specified like name:limit, and fixes them to remove the limit suffix,
// dedup and sort the groups, and fill in your supplied limitGroups map with the
// latest limit on groups, if any were specified. You should hold the lock on
// the Job before calling this.
func (s *Server) handleUserSpecifiedJobLimitGroups(job *Job, limitGroups map[string]*limiter.GroupData) {
	displayByName := make(map[string]string, len(job.LimitGroups))

	// remove limit suffixes and remember the last limit per group specified
	for i, group := range job.LimitGroups {
		name, limit := s.splitSuffixedLimitGroup(group)
		displayByName[name] = group

		if limit != nil {
			job.LimitGroups[i] = name
			limitGroups[name] = limit
		}
	}

	// because these later become part of scheduler groups names, store
	// them in sorted order, with no duplicates
	if len(job.LimitGroups) > 1 {
		job.LimitGroups = internal.DedupSortStrings(job.LimitGroups)
	}

	job.LimitGroupsForDisplay = make([]string, 0, len(job.LimitGroups))
	for _, group := range job.LimitGroups {
		job.LimitGroupsForDisplay = append(job.LimitGroupsForDisplay, displayByName[group])
	}
}

// storeLimitGroups calls db.storeLimitGroups() and handles updating the
// in-memory representation of the groups.
func (s *Server) storeLimitGroups(limitGroups map[string]*limiter.GroupData) error {
	changed, removed, err := s.db.storeLimitGroups(limitGroups)
	if err != nil {
		return err
	}

	for _, group := range changed {
		s.limiter.SetLimit(group, *limitGroups[group])
	}

	for _, group := range removed {
		s.limiter.RemoveLimit(group)
	}

	return nil
}

// updateJobDependencies is used to handle the jobsToUpdate from storeNewJobs()
// and db.modifyLiveJobs(). These are those jobs currently in the queue that
// need their dependencies updated because they just changed when we stored the
// jobs.
func (s *Server) updateJobDependencies(ctx context.Context, jobs []*Job) (srerr string, qerr error) {
	updates, readyCallbackExpected, qerr := s.gatherDependencyUpdates(jobs)
	if qerr != nil {
		return ErrDBError, qerr
	}

	if readyCallbackExpected {
		s.setRACPending()
	}

	return "", s.applyDependencyUpdates(ctx, updates, readyCallbackExpected)
}

// jobDependencyUpdate holds a job's freshly computed dependencies, ready to be
// applied to the queue.
type jobDependencyUpdate struct {
	job                 *Job
	deps                []string
	waitingForDepGroups []string
}

// gatherDependencyUpdates recomputes each job's incomplete dependencies,
// reporting whether applying them is expected to make a job ready (so the
// ready-added callback should be armed).
func (s *Server) gatherDependencyUpdates(jobs []*Job) ([]jobDependencyUpdate, bool, error) {
	updates := make([]jobDependencyUpdate, 0, len(jobs))
	readyCallbackExpected := false

	for _, job := range jobs {
		deps, waitingForDepGroups, err := job.Dependencies.incompleteJobKeys(s.db)
		if err != nil {
			return updates, readyCallbackExpected, err
		}

		updates = append(updates, jobDependencyUpdate{job: job, deps: deps, waitingForDepGroups: waitingForDepGroups})

		if len(deps) == 0 && !readyCallbackExpected {
			item, errq := s.q.Get(job.Key())
			readyCallbackExpected = itemWillBecomeReadyAfterDependencyUpdate(item, errq)
		}
	}

	return updates, readyCallbackExpected, nil
}

// applyDependencyUpdates writes each gathered dependency update to the queue,
// clearing the armed ready-added callback if an update fails.
func (s *Server) applyDependencyUpdates(ctx context.Context, updates []jobDependencyUpdate,
	readyCallbackExpected bool) error {
	for _, update := range updates {
		job := update.job
		job.setWaitingForDepGroups(update.waitingForDepGroups)

		err := s.q.Update(
			ctx, job.Key(), job.getSchedulerGroup(), job, job.Priority, 0*time.Second, s.itemTTRDuration(), update.deps,
		)
		if err != nil {
			if readyCallbackExpected {
				s.clearRACPending()
			}

			return err
		}
	}

	return nil
}

// confirmJobDeadAndKill calls and returns the value of confirmJobDead(). If
// true, kills the job and triggers behaviours in a goroutine. If false,
// arranges to re-call this after the configured retry time. This is so that if
// we can't currently confirm the job is dead due to an ssh issue, but later on
// the job really does die because the server it was running on gets rebooted,
// we eventually auto-kill the job.
func (s *Server) confirmJobDeadAndKill(ctx context.Context, jobKey, jobHost string,
	jobPID int, serverLostJobCheckTimeout, serverLostJobCheckRetryTime time.Duration) bool {
	if !s.confirmJobDead(ctx, jobPID, jobHost, serverLostJobCheckTimeout) {
		go s.confirmJobDeadAndKillAfterRetryTime(ctx, jobKey, serverLostJobCheckRetryTime)

		return false
	}

	go s.killLostJobAndTriggerBehaviours(ctx, jobKey)

	return true
}

// killLostJobAndTriggerBehaviours kills the lost job and, on success, runs its
// behaviours, logging any problems.
func (s *Server) killLostJobAndTriggerBehaviours(ctx context.Context, jobKey string) {
	if _, errk := s.killJob(ctx, jobKey); errk != nil {
		clog.Warn(ctx, "failed to kill a job after TTR", "err", errk)

		return
	}

	q := s.queueIfPresent()
	if q == nil {
		clog.Warn(ctx, "failed to get a killed lost job", "err", queueClosedError("Get", jobKey))

		return
	}

	item, errg := q.Get(jobKey)
	if errg != nil {
		clog.Warn(ctx, "failed to get a killed lost job", "err", errg)

		return
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return
	}

	//nolint:contextcheck // behaviours run detached from the cancellable job context
	if errt := job.TriggerBehaviours(false); errt != nil {
		clog.Warn(ctx, "failed to run behaviours for a killed lost job", "err", errt)
	}
}

// confirmJobDead() checks if the actual PID isn't running on the job's host.
func (s *Server) confirmJobDead(ctx context.Context, jobPID int, jobHost string,
	serverLostJobCheckTimeout time.Duration) bool {
	if jobPID == 0 {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, serverLostJobCheckTimeout)
	defer cancel()

	return s.scheduler.ProcessNotRunningOnHost(ctx, jobPID, jobHost)
}

func (s *Server) confirmJobDeadAndKillAfterRetryTime(ctx context.Context, jobKey string,
	serverLostJobCheckRetryTime time.Duration) {
	timer := time.NewTimer(serverLostJobCheckRetryTime)
	defer timer.Stop()

	select {
	case <-timer.C:
		retry, ok := s.lostJobRetryCheck(jobKey)
		if !ok {
			return
		}

		s.confirmJobDeadAndKill(ctx, retry.jobKey, retry.jobHost, retry.jobPID, retry.checkTimeout,
			serverLostJobCheckRetryTime)
	case <-s.stopClientHandling:
		return
	}
}

// releaseJob either releases or buries a job as per its retries, and updates
// our scheduling counts as appropriate.
func (s *Server) releaseJob(ctx context.Context, job *Job, endState *JobEndState, failReason string,
	forceStorage, forceBury bool) error {
	// first check the job hasn't already been released/buried, only attempt
	// queue changes if not
	bury, key, currentState := releaseJobSnapshot(job, forceBury)

	q := s.queueIfPresent()
	if q == nil {
		return queueClosedError("Get", key)
	}

	item, err := q.Get(key)
	if err != nil {
		return err
	}

	alreadyDone, errq := s.applyReleaseQueueChange(ctx, q, item, key, bury, currentState, job)
	if errq != nil {
		return errq
	}

	if alreadyDone {
		return nil
	}

	s.finalizeReleasedJob(ctx, job, endState, failReason, forceStorage, forceBury)

	return nil
}

// releaseJobSnapshot reads, under the job's read lock, the values releaseJob
// needs: whether the job should be buried, its key, and its current state.
func releaseJobSnapshot(job *Job, forceBury bool) (bool, string, JobState) {
	job.RLock()
	defer job.RUnlock()

	bury := forceBury
	if !bury && !job.StartTime.IsZero() {
		bury = job.UntilBuried == 1
	}

	return bury, job.Key(), job.State
}

// applyReleaseQueueChange moves the queue item for a job being released to its
// bury or delay sub-queue as appropriate. It reports alreadyDone=true when the
// item is already in the target state (so there is nothing more to do).
func (s *Server) applyReleaseQueueChange(ctx context.Context, q *queue.Queue, item *queue.Item,
	key string, bury bool, currentState JobState, job *Job) (bool, error) {
	switch {
	case bury:
		if item.Stats().State == queue.ItemStateBury {
			return currentState == JobStateBuried, nil
		}

		if errq := q.Bury(key); errq != nil {
			return false, errq
		}

		s.deleteJobIfRequested(ctx, job)
	case item.Stats().State == queue.ItemStateDelay:
		return currentState == JobStateDelayed, nil
	default:
		if errq := q.Release(ctx, key); errq != nil {
			return false, errq
		}
	}

	return false, nil
}

// finalizeReleasedJob updates a released job's state (to buried or delayed,
// obeying its Retries count), persists it, and decrements its scheduler group
// count.
func (s *Server) finalizeReleasedJob(ctx context.Context, job *Job, endState *JobEndState,
	failReason string, forceStorage, forceBury bool) {
	job.updateAfterExit(endState, s.limiter)

	job.Lock()
	if forceBury {
		job.UntilBuried = 0
	} else if !job.StartTime.IsZero() {
		// obey jobs's Retries count by adjusting UntilBuried if a client
		// reserved this job and started to run the job's cmd
		job.UntilBuried--
	}

	sgroup := job.schedulerGroup

	var msg string

	if job.UntilBuried <= 0 {
		job.State = JobStateBuried
		msg = "buried job"
	} else {
		job.State = JobStateDelayed
		msg = "released job"
	}

	job.FailReason = failReason
	job.Unlock()

	s.decrementGroupCount(ctx, sgroup)
	s.db.updateJobAfterExit(ctx, job, endState.Stdout, endState.Stderr, forceStorage)
	clog.Debug(ctx, msg, "cmd", job.Cmd, "schedGrp", sgroup)
}

// inputToQueuedJobs shows you which of the inputJobs are now actually in the
// queue.
func (s *Server) inputToQueuedJobs(ctx context.Context, inputJobs []*Job) []*Job {
	// *** queue.AddMany doesn't currently return which jobs were added and
	// which were dups, and server.createJobs doesn't know which were ignored
	// due to being incomplete, so we do this loop even though it's probably
	// slow and wasteful?...
	var jobs []*Job

	for _, job := range inputJobs {
		item, qerr := s.q.Get(job.Key())
		if qerr == nil && item != nil {
			// append the q's version of the job, not the input job, since the
			// job may have been a duplicate and we want to return its current
			// state
			jobs = append(jobs, s.itemToJob(ctx, item, false, false))
		}
	}

	return jobs
}

// killJob sets the killCalled property on a job, to change the subsequent
// behaviour of touching, which should result in an executing job killing
// itself.
//
// If we have lost contact with the job, calling killJob is also the way to
// confirm it is definitely dead and won't spring back to life in the future:
// we release or bury it as appropriate.
//
// If the job wasn't running, returned bool will be false and nothing will have
// been done.
func (s *Server) killJob(ctx context.Context, jobkey string) (bool, error) {
	q := s.queueIfPresent()
	if q == nil {
		return false, queueClosedError("Get", jobkey)
	}

	item, err := q.Get(jobkey)
	if err != nil || item.Stats().State != queue.ItemStateRun {
		return false, err
	}

	job := item.Data().(*Job) //nolint:errcheck,forcetypeassert // queue only ever stores *Job
	job.Lock()
	job.killCalled = true

	if job.Lost {
		job.Unlock()
		err = s.releaseJob(ctx, job, &JobEndState{Exitcode: -1, Exited: true}, FailReasonLost, false, false)

		return true, err
	}

	job.Unlock()

	return true, err
}

// deleteJobs deletes the given jobs from the bury/delay/dependent/ready queue
// and the live bucket. Does not delete jobs that have jobs dependant upon them,
// unless all those dependants were also supplied to this method at the same
// time (in any order). Returns the keys of jobs actually deleted.
func (s *Server) deleteJobs(ctx context.Context, jobs []*Job) []string {
	var deleted []string

	for {
		pass := s.removeDeletableJobs(ctx, jobs)
		deleted = append(deleted, pass.toDelete...)

		if len(pass.toDelete) == 0 {
			break
		}

		s.finalizeDeletedJobs(ctx, pass)

		// if we skipped any due to deps, repeat and see if we can remove
		// everything desired by going down the dependency tree
		if len(pass.skippedDeps) == 0 {
			break
		}

		jobs = pass.skippedDeps
	}

	return deleted
}

// deletePass records what a single pass of deleteJobs removed, plus the jobs it
// had to skip because they still have dependents.
type deletePass struct {
	toDelete    []string
	schedGroups map[string]int
	repGroups   []string
	skippedDeps []*Job
}

// removeDeletableJobs removes from the queue every job that has no dependents,
// collecting what was removed (and what was skipped) for finalizeDeletedJobs.
func (s *Server) removeDeletableJobs(ctx context.Context, jobs []*Job) deletePass {
	pass := deletePass{schedGroups: make(map[string]int)}

	for _, job := range jobs {
		jobkey := job.Key()

		// we can't allow the removal of jobs that have dependencies, as *queue
		// would regard that as satisfying the dependency and downstream jobs
		// would start
		hasDeps, err := s.q.HasDependents(jobkey)
		if err != nil || hasDeps {
			if hasDeps {
				pass.skippedDeps = append(pass.skippedDeps, job)
			}

			continue
		}

		// mark the job deleted BEFORE removing it, so the queue change callback
		// (which reads each removed job's own State to decide complete vs deleted,
		// see emitChangeCallbackTransition) observes JobStateDeleted and broadcasts
		// a deleted update for this genuinely-removed incomplete job. Capture the
		// previous state so we can revert if the removal fails and the job remains
		// in the queue (otherwise it would be left visibly Deleted).
		job.Lock()
		prevState := job.State
		job.State = JobStateDeleted
		// capture the mutable fields we need after the lock is released, so
		// they are not read unsynchronised (they can be modified by web modify
		// paths). schedulerGroup is the field getSchedulerGroup() reads under
		// its own lock; capture it directly here to avoid re-locking.
		repGroup := job.RepGroup
		cmd := job.Cmd
		schedGroup := job.schedulerGroup
		job.Unlock()

		if err = s.q.Remove(ctx, jobkey); err == nil {
			pass.toDelete = append(pass.toDelete, jobkey)
			pass.schedGroups[schedGroup]++
			pass.repGroups = append(pass.repGroups, repGroup)

			clog.Debug(ctx, "removed job", "cmd", cmd)
		} else {
			// removal failed, so the job is still in the queue; revert its state
			// so it is not left visibly Deleted.
			job.Lock()
			job.State = prevState
			job.Unlock()
		}
	}

	return pass
}

// finalizeDeletedJobs removes a pass's jobs from the database, decrements their
// scheduler group counts, and cleans up the rep-group lookups.
func (s *Server) finalizeDeletedJobs(ctx context.Context, pass deletePass) {
	// delete from db live bucket all in one go
	if errd := s.db.deleteLiveJobs(ctx, pass.toDelete); errd != nil {
		clog.Error(ctx, "job deletion from database failed", "err", errd)
	}

	// update scheduler now we have fewer jobs
	for sg, count := range pass.schedGroups {
		s.decrementGroupCount(ctx, sg, count)
	}

	// clean up rpl lookups
	s.rpl.Lock()
	for i, rg := range pass.repGroups {
		s.rpl.Delete(rg, pass.toDelete[i])
	}
	s.rpl.Unlock()
}

// deleteJobIfRequested checks the job's behaviours and deletes the job if
// requested.
func (s *Server) deleteJobIfRequested(ctx context.Context, job *Job) {
	if job.RemovalRequested() {
		go s.deleteJobs(ctx, []*Job{job})
	}
}

// killJobsOnServers kills running and confirms lost jobs that were running on
// hosts with the given IDs. Returns the affected jobs.
func (s *Server) killJobsOnServers(ctx context.Context, serverIDs map[string]bool) []*Job {
	if len(serverIDs) == 0 {
		return nil
	}

	running := s.getJobsCurrent(ctx, "", RepGroupMatchExact, 0,
		JobStateRunning, false, false, false)

	lost := s.getJobsCurrent(ctx, "", RepGroupMatchExact, 0,
		JobStateLost, false, false, false)

	var jobs []*Job

	for _, job := range append(running, lost...) {
		if !serverIDs[job.HostID] {
			continue
		}

		if s.killJobOnServer(ctx, job) {
			jobs = append(jobs, job)
		}
	}

	clog.Debug(ctx, "killed jobs on bad servers", "number", len(jobs))

	return jobs
}

// killJobOnServer kills a single job whose server is being destroyed and, on
// success, refreshes its state from the live queue. It reports whether the job
// was killed (and so should be returned to the client).
func (s *Server) killJobOnServer(ctx context.Context, job *Job) bool {
	k, err := s.killJob(ctx, job.Key())
	if err != nil {
		clog.Error(ctx, "failed to kill a job after destroying its server: %s", err)

		return false
	}

	if !k {
		return false
	}

	// try and grab the latest job state after having killed it, but still
	// return the client version of the job
	if item, errg := s.q.Get(job.Key()); errg == nil && item != nil {
		refreshJobFromLiveItem(job, item)
	}

	return true
}

// refreshJobFromLiveItem copies the latest state from the live queue item onto
// job (used after killing it), adjusting UntilBuried for a still-running job.
func refreshJobFromLiveItem(job *Job, item *queue.Item) {
	liveJob, ok := item.Data().(*Job)
	if !ok {
		return
	}

	job.State = liveJob.State
	job.UntilBuried = liveJob.UntilBuried

	if job.State == JobStateRunning && !liveJob.StartTime.IsZero() {
		// we're going to release the job as soon as it goes from running to lost
		job.UntilBuried--
	}
}

// kickJobs unburies the given jobs and returns the number affected.
func (s *Server) kickJobs(ctx context.Context, jobs []*Job) (kicked int) {
	for _, job := range jobs {
		readyCallbackExpected := false

		item, errg := s.q.Get(job.Key())
		if errg == nil && item != nil && len(item.UnresolvedDependencies()) == 0 {
			readyCallbackExpected = true

			s.setRACPending()
		}

		err := s.q.Kick(ctx, job.Key())
		if err == nil {
			job.Lock()
			job.UntilBuried = job.Retries + 1
			clog.Debug(ctx, "unburied job", "cmd", job.Cmd, "schedGrp", job.schedulerGroup)
			job.State = JobStateReady
			job.Unlock()

			kicked++

			s.db.updateJobAfterChange(ctx, job)
		} else if readyCallbackExpected {
			s.clearRACPending()
		}
	}

	return kicked
}

// getJobsByKeys gets jobs with the given keys (current and complete).
func (s *Server) getJobsByKeys(ctx context.Context, keys []string, getStd, getEnv bool) (
	jobs []*Job, srerr string, qerr string,
) {
	jobs, notfound := s.queuedJobsByKeys(ctx, keys, getStd)

	getStd = shouldPopulateStd(jobs, getStd)

	if getStd || getEnv {
		for _, job := range jobs {
			s.jobPopulateStdEnv(ctx, job, getStd, getEnv)
		}
	}

	if len(notfound) > 0 {
		// try and get the jobs from the permanent store
		found, fsrerr, fqerr := s.completeJobsByKeys(ctx, notfound, getEnv)
		jobs = append(jobs, found...)
		srerr = fsrerr
		qerr = fqerr
	}

	return jobs, srerr, qerr
}

// queuedJobsByKeys looks up the given keys in the in-memory queue, returning the
// jobs found and the keys that were not found.
func (s *Server) queuedJobsByKeys(ctx context.Context, keys []string, getStd bool) ([]*Job, []string) {
	var (
		jobs     []*Job
		notfound []string
	)

	for _, jobkey := range keys {
		// try and get the job from the in-memory queue
		item, err := s.q.Get(jobkey)
		if err != nil || item == nil {
			notfound = append(notfound, jobkey)

			continue
		}

		if job := s.itemToJob(ctx, item, getStd, false); job != nil {
			jobs = append(jobs, job)
		}
	}

	return jobs, notfound
}

// completeJobsByKeys retrieves complete (archived) jobs for the given keys from
// the permanent store, populating their env if requested.
func (s *Server) completeJobsByKeys(ctx context.Context, keys []string, getEnv bool) ([]*Job, string, string) {
	found, err := s.db.retrieveCompleteJobsByKeys(keys)
	if err != nil {
		return nil, ErrDBError, err.Error()
	}

	if getEnv { // complete jobs don't have any std
		for _, job := range found {
			s.jobPopulateStdEnv(ctx, job, false, getEnv)
		}
	}

	return found, "", ""
}

// checkJobByKey checks to see if the given key corresponds to a job currently
// in the queue, or complete in the database.
func (s *Server) checkJobByKey(key string) (bool, error) {
	item, err := s.q.Get(key)
	if err != nil {
		var qerr queue.Error
		if !errors.As(err, &qerr) || !errors.Is(qerr.Err, queue.ErrNotFound) {
			return false, err
		}
	}

	if item != nil {
		return true, nil
	}

	found, err := s.db.retrieveCompleteJobsByKeys([]string{key})

	return len(found) == 1, err
}

type repGroupOptions struct {
	RepGroup string // The RepGroup to get jobs for
	Match    RepGroupMatch
	limitJobsOptions
}

func (opts *repGroupOptions) toLimitOpts() limitJobsOptions {
	return limitJobsOptions{
		Limit:               opts.Limit,
		Offset:              opts.Offset,
		State:               opts.State,
		ExitCode:            opts.ExitCode,
		FailReason:          opts.FailReason,
		GetStd:              opts.GetStd,
		GetEnv:              opts.GetEnv,
		WaitingForDepGroups: opts.WaitingForDepGroups,
	}
}

// getJobsByRepGroup gets jobs in the given group (current and complete).
func (s *Server) getJobsByRepGroup(ctx context.Context, opts repGroupOptions) (jobs []*Job, srerr string, qerr string) {
	rgs, srerr, qerr := s.getRepGroupsList(opts.RepGroup, opts.Match)
	if srerr != "" {
		return nil, srerr, qerr
	}

	for i := range rgs {
		rg := rgs[i]
		queueJobs := s.getQueueJobsByRepGroup(ctx, rg, opts.GetStd)
		jobs = append(jobs, queueJobs...)

		complete := s.getDBJobsByRepGroup(rg, opts.State, &srerr, &qerr)
		jobs = append(jobs, complete...)
	}

	jobs = s.limitJobs(ctx, jobs, opts.toLimitOpts())

	return jobs, srerr, qerr
}

// getRepGroupsList gets the list of RepGroups based on matching criteria.
func (s *Server) getRepGroupsList(repGroup string, match RepGroupMatch) ([]string, string, string) {
	if match == RepGroupMatchExact {
		return []string{repGroup}, "", ""
	}

	rgs, err := s.db.retrieveRepGroups()
	if err != nil {
		return nil, ErrDBError, err.Error()
	}

	matches := make([]string, 0, len(rgs))

	for i := range rgs {
		rg := rgs[i]
		if RepGroupMatches(rg, repGroup, match) {
			matches = append(matches, rg)
		}
	}

	return matches, "", ""
}

// getQueueJobsByRepGroup gets jobs from the in-memory queue for a given
// RepGroup.
func (s *Server) getQueueJobsByRepGroup(ctx context.Context, repGroup string, getStd bool) []*Job {
	var jobs []*Job

	for _, key := range s.rpl.Values(repGroup) {
		item, _ := s.q.Get(key) //nolint:errcheck
		if item != nil {
			job := s.itemToJob(ctx, item, getStd, false)
			jobs = append(jobs, job)
		}
	}

	return jobs
}

// getDBJobsByRepGroup gets jobs from the permanent store for a given RepGroup.
func (s *Server) getDBJobsByRepGroup(rg string, state JobState, srerr *string, qerr *string) []*Job {
	if state != "" && state != JobStateComplete {
		return nil
	}

	var complete []*Job

	complete, *srerr, *qerr = s.getCompleteJobsByRepGroup(rg)

	for _, cj := range complete {
		cj.RepGroup = rg
	}

	sort.Slice(complete, func(i, j int) bool {
		if complete[i].StartTime.Equal(complete[j].StartTime) {
			return complete[i].EndTime.Before(complete[j].EndTime)
		}

		return complete[i].StartTime.Before(complete[j].StartTime)
	})

	return complete
}

// getCompleteJobsByRepGroup gets complete jobs in the given group.
func (s *Server) getCompleteJobsByRepGroup(repgroup string) (jobs []*Job, srerr string, qerr string) {
	jobs, err := s.db.retrieveCompleteJobsByRepGroup(repgroup)
	if err != nil {
		srerr = ErrDBError
		qerr = err.Error()
	}

	return jobs, srerr, qerr
}

// getLastCompletionTimeByRepGroup gets the latest completion time by RepGroup
// for matching groups.
func (s *Server) getLastCompletionTimeByRepGroup(repGroup string,
	match RepGroupMatch) (map[string]time.Time, string, string) {
	rgs, srerr, qerr := s.getRepGroupsList(repGroup, match)
	if srerr != "" {
		return nil, srerr, qerr
	}

	completionTimes, err := s.db.retrieveLastCompletionTimeByRepGroup(rgs)
	if err != nil {
		return nil, ErrDBError, err.Error()
	}

	return completionTimes, "", ""
}

// getJobsCurrent gets all current (incomplete) jobs. If repGroup is not
// blank, only jobs whose RepGroup matches repGroup according to match are
// returned.
func (s *Server) getJobsCurrent(ctx context.Context, repGroup string, match RepGroupMatch,
	limit int, state JobState, getStd bool, getEnv bool, waitingForDepGroups bool) []*Job {
	jobs := s.getQueueJobsCurrent(ctx, repGroup, match, getStd)

	jobs = s.limitJobs(ctx, jobs, limitJobsOptions{
		Limit:               limit,
		State:               state,
		GetStd:              getStd,
		GetEnv:              getEnv,
		WaitingForDepGroups: waitingForDepGroups,
	})

	return jobs
}

func (s *Server) getQueueJobsCurrent(ctx context.Context, repGroup string, match RepGroupMatch, getStd bool) []*Job {
	if repGroup == "" {
		return s.getAllQueueJobs(ctx, getStd)
	}

	if match == RepGroupMatchExact {
		return s.getQueueJobsByRepGroup(ctx, repGroup, getStd)
	}

	return s.getQueueJobsByRepGroupMatch(ctx, repGroup, match, getStd)
}

func (s *Server) getAllQueueJobs(ctx context.Context, getStd bool) []*Job {
	allItems := s.q.AllItems()
	jobs := make([]*Job, 0, len(allItems))

	for _, item := range allItems {
		jobs = append(jobs, s.itemToJob(ctx, item, getStd, false))
	}

	return jobs
}

func (s *Server) getQueueJobsByRepGroupMatch(ctx context.Context, repGroup string,
	match RepGroupMatch, getStd bool) []*Job {
	allItems := s.q.AllItems()
	jobs := make([]*Job, 0, len(allItems))

	for _, item := range allItems {
		job := s.itemToJob(ctx, item, getStd, false)
		if job == nil || !RepGroupMatches(job.RepGroup, repGroup, match) {
			continue
		}

		jobs = append(jobs, job)
	}

	return jobs
}

func increaseJobDiskAfterFailure(job *Job) {
	const diskIncreaseRoundGB = 100

	updatedGB := float64(job.PeakDisk) / float64(mbPerGB)
	updatedGB *= RAMIncreaseMultHigh
	newDisk := int(math.Ceil(updatedGB/diskIncreaseRoundGB) * diskIncreaseRoundGB)

	if newDisk > job.Requirements.Disk {
		job.Requirements.Disk = newDisk
	}
}

func increaseJobTimeAfterFailure(job *Job) {
	newTime := job.EndTime.Sub(job.StartTime) + (1 * time.Hour)
	if newTime > job.Requirements.Time {
		job.Requirements.Time = newTime
	}
}

func normalizedStatusFilter(filter JobState) JobState {
	if filter == JobStateReserved {
		return JobStateRunning
	}

	return filter
}

func jobUnixNano(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}

	i := t.UnixNano()

	return &i
}

func jobUpdateKind(state JobState) JobUpdateKind {
	if state == JobStateLost {
		return JobUpdateLost
	}

	if state != JobStateComplete && state != JobStateBuried {
		return JobUpdateStateChange
	}

	return JobUpdateTerminal
}

func normalizeRepGroupMatch(match RepGroupMatch, search bool) RepGroupMatch {
	switch match {
	case RepGroupMatchExact, RepGroupMatchSubStr, RepGroupMatchPrefix, RepGroupMatchSuffix:
		return match
	}

	if search {
		return RepGroupMatchSubStr
	}

	return RepGroupMatchExact
}

type limitJobsOptions struct {
	Limit               int      // Maximum number of jobs to return (<1 = no limit)
	Offset              int      // Starting offset for pagination
	FailReason          string   // Fail reason to filter jobs by
	ExitCode            int      // Exit code to filter jobs by (if FailReason is set)
	State               JobState // Filter jobs by this state
	GetStd              bool     // If true, populate StdOut and StdErr of jobs
	GetEnv              bool     // If true, populate Env of jobs
	WaitingForDepGroups bool     // If true, return jobs waiting on never-seen dep groups
}

// limitJobs handles the limiting of jobs for getJobsByRepGroup() and
// getJobsCurrent(). States 'reserved' and 'running' are treated as the same
// state.
func (s *Server) limitJobs(ctx context.Context, jobs []*Job, opts limitJobsOptions) []*Job {
	if opts.Limit <= 0 && opts.State == "" && !opts.GetStd && !opts.GetEnv && !opts.WaitingForDepGroups {
		return jobs
	}

	opts = s.normalizeOptions(opts)
	limited := s.filterAndGroupJobs(jobs, opts)
	getStd := shouldPopulateStd(limited, opts.GetStd)
	s.populateJobData(ctx, limited, getStd, opts.GetEnv)

	return limited
}

// normalizeOptions ensures the options have valid values.
func (s *Server) normalizeOptions(opts limitJobsOptions) limitJobsOptions {
	if opts.Limit < 0 {
		opts.Limit = 0
	}

	if opts.Offset < 0 {
		opts.Offset = 0
	}

	if opts.State == JobStateRunning {
		opts.State = JobStateReserved
	}

	return opts
}

// filterAndGroupJobs filters jobs by state and groups them by characteristics.
func (s *Server) filterAndGroupJobs(jobs []*Job, opts limitJobsOptions) []*Job {
	if opts.Limit == 0 {
		return s.filterJobsByState(jobs, opts)
	}

	return s.groupAndLimitJobs(jobs, opts)
}

// filterJobsByState returns only jobs matching the state filters.
func (s *Server) filterJobsByState(jobs []*Job, opts limitJobsOptions) []*Job {
	var limited []*Job

	for _, job := range jobs {
		if s.jobMatchesFilters(job, opts) {
			limited = append(limited, job)
		}
	}

	return limited
}

// jobMatchesFilters checks if a job matches the filtering criteria.
func (s *Server) jobMatchesFilters(job *Job, opts limitJobsOptions) bool {
	jState, jExitCode, jFailReason, jLost := getJobProps(job)

	jState = s.normalizeJobState(jState, jLost)

	return s.matchesStateFilter(jState, opts.State) &&
		s.matchesFailureFilter(jFailReason, jExitCode, opts.FailReason, opts.ExitCode) &&
		matchesWaitingForDepGroupsFilter(job, opts.WaitingForDepGroups)
}

func getJobProps(job *Job) (JobState, int, string, bool) {
	job.RLock()
	defer job.RUnlock()

	return job.State, job.Exitcode, job.FailReason, job.Lost
}

// normalizeJobState converts running jobs to either lost or reserved state.
func (s *Server) normalizeJobState(state JobState, lost bool) JobState {
	if state == JobStateRunning {
		if lost {
			return JobStateLost
		}

		return JobStateReserved
	}

	return state
}

// matchesStateFilter checks if a job's state matches the filter criteria.
func (s *Server) matchesStateFilter(jobState JobState, filterState JobState) bool {
	if filterState == "" {
		return true
	}

	if filterState == JobStateDeletable {
		return jobState != JobStateReserved && jobState != JobStateRunning && jobState != JobStateComplete
	}

	return jobState == filterState
}

// matchesFailureFilter checks if a job's failure reason and exit code match the
// filter criteria.
func (s *Server) matchesFailureFilter(jobFailReason string, jobExitCode int,
	filterFailReason string, filterExitCode int) bool {
	if filterFailReason == "" {
		return true
	}

	return jobFailReason == filterFailReason && jobExitCode == filterExitCode
}

// groupAndLimitJobs groups jobs by characteristics and applies limits.
func (s *Server) groupAndLimitJobs(jobs []*Job, opts limitJobsOptions) []*Job {
	groups := s.groupJobsByCharacteristics(jobs, opts)
	groups = s.applyOffsetToGroups(groups, opts.Offset)

	return s.collectJobsFromGroups(groups)
}

// groupJobsByCharacteristics groups jobs by state, exit code, and failure
// reason.
func (s *Server) groupJobsByCharacteristics(jobs []*Job, opts limitJobsOptions) map[string][]*Job {
	groups := make(map[string][]*Job)

	for _, job := range jobs {
		if !s.jobMatchesFilters(job, opts) {
			continue
		}

		jState, jExitCode, jFailReason, jLost := getJobProps(job)
		jState = s.normalizeJobState(jState, jLost)

		group := fmt.Sprintf("%s.%d.%s", jState, jExitCode, jFailReason)
		s.addJobToGroup(job, group, groups, opts)
	}

	return groups
}

// applyOffsetToGroups applies pagination offset to each group of jobs.
func (s *Server) applyOffsetToGroups(groups map[string][]*Job, offset int) map[string][]*Job {
	if offset <= 0 {
		return groups
	}

	for group, groupJobs := range groups {
		if offset < len(groupJobs) {
			groups[group] = groupJobs[offset:]
		} else {
			delete(groups, group)
		}
	}

	return groups
}

// collectJobsFromGroups flattens all job groups into a single slice.
func (s *Server) collectJobsFromGroups(groups map[string][]*Job) []*Job {
	var allJobs []*Job
	for _, groupJobs := range groups {
		allJobs = append(allJobs, groupJobs...)
	}

	return allJobs
}

// addJobToGroup adds a job to a group, managing counts and similarity.
func (s *Server) addJobToGroup(job *Job, group string, groups map[string][]*Job, opts limitJobsOptions) {
	jobs, existed := groups[group]
	if !existed {
		jobs = []*Job{job}
		groups[group] = jobs

		return
	}

	lenj := len(jobs)
	if lenj == opts.Offset+opts.Limit {
		jobs[lenj-1].Similar++
	} else {
		jobs = append(jobs, job)
		groups[group] = jobs
	}
}

// populateJobData fills in standard output/error and environment data.
func (s *Server) populateJobData(ctx context.Context, jobs []*Job, getStd, getEnv bool) {
	if !getEnv && !getStd {
		return
	}

	for _, job := range jobs {
		s.jobPopulateStdEnv(ctx, job, getStd, getEnv)
	}
}

// shouldPopulateStd only returns true if the given getStd is true and if the
// number of jobs that could potentially have std is less than or equal to the
// maxJobsForStd.
func shouldPopulateStd(jobs []*Job, getStd bool) bool {
	if !getStd {
		return false
	}

	hasStd := 0

	for _, job := range jobs {
		if jobCouldHaveStd(job) {
			hasStd++
		}
	}

	return hasStd <= maxJobsForStd
}

// schedulerGroupDetails is used for debugging purposes to see how many jobs are
// associated with which scheduler groups.
func (s *Server) schedulerGroupDetails() []string {
	s.psgmutex.RLock()
	defer s.psgmutex.RUnlock()

	result := make([]string, len(s.previouslyScheduledGroups))

	i := 0
	for name, group := range s.previouslyScheduledGroups {
		result[i] = fmt.Sprintf("%s (%d jobs)", name, group.getCount())
		i++
	}

	return result
}

func (s *Server) groupToScheduleCmd(ctx context.Context, rc, group string, req *scheduler.Requirements) string {
	return fmt.Sprintf(
		rc, group, s.ServerInfo.Deployment, s.ServerInfo.Addr, s.ServerInfo.Host,
		s.scheduler.ReserveTimeout(ctx, req), int(s.scheduler.MaxQueueTime(req).Minutes()),
	)
}

// runnerCommand returns the configured runner command template under lock.
func (s *Server) runnerCommand() string {
	s.racmutex.RLock()
	defer s.racmutex.RUnlock()

	return s.rc
}

func (s *Server) scheduleRunners(ctx context.Context, group *sgroup) {
	rc := s.runnerCommand()
	if rc == "" {
		return
	}

	scheduleCmd := s.groupToScheduleCmd(ctx, rc, group.name, group.req)

	err := s.scheduler.Schedule(ctx, scheduleCmd, group.req, group.priority, group.count)
	if err == nil {
		group.resetRetryState()

		return
	}

	problem := true

	var serr scheduler.Error
	if errors.As(err, &serr) && serr.Err == scheduler.ErrImpossible {
		// the requirements are impossible, so bury all jobs in this group
		problem = s.buryImpossibleGroupJobs(ctx, group)
		s.triggerReadyAddedCallback(ctx)
	}

	if problem {
		group.failures++

		// log the error, escalating to Error once the failure is persistent so a
		// permanently-failing submit is visible (not swallowed by an endless
		// stream of identical warnings) *** and inform (by email) the user about
		// this problem if it's persistent, once per hour (day?)
		if group.failures >= persistentScheduleFailures {
			clog.Error(ctx, "Server scheduling runners persistently failing",
				"err", err, "group", group.name, "consecutiveFailures", group.failures)
		} else {
			clog.Warn(ctx, "Server scheduling runners error",
				"err", err, "group", group.name, "consecutiveFailures", group.failures)
		}

		s.retryScheduleRunnersLater(ctx, group)
	}
}

// buryImpossibleGroupJobs buries every ready job in the given scheduler group
// (because its requirements cannot be satisfied), returning true if a problem
// occurred while reserving items.
func (s *Server) buryImpossibleGroupJobs(ctx context.Context, group *sgroup) bool {
	for {
		item, errr := s.q.Reserve(group.name, 0)
		if errr != nil {
			if isNothingReadyError(errr) {
				return false
			}

			clog.Warn(ctx, "scheduleRunners failed to reserve an item", "group", group, "err", errr)

			return true
		}

		if item == nil {
			return false
		}

		s.buryImpossibleItem(ctx, item)
	}
}

// isNothingReadyError reports whether err is the queue's "nothing ready" error.
func isNothingReadyError(err error) bool {
	var qerr queue.Error

	return errors.As(err, &qerr) && errors.Is(qerr.Err, queue.ErrNothingReady)
}

// buryImpossibleItem marks a reserved item's job as failed for resources, buries
// it, and deletes it if the job requested deletion on failure.
func (s *Server) buryImpossibleItem(ctx context.Context, item *queue.Item) {
	job, ok := item.Data().(*Job)
	if !ok {
		return
	}

	job.Lock()
	job.FailReason = FailReasonResource
	job.Unlock()

	if errb := s.q.Bury(item.Key); errb != nil {
		clog.Warn(ctx, "scheduleRunners failed to bury an item", "err", errb)
	} else {
		s.deleteJobIfRequested(ctx, job)
	}
}

// retryScheduleRunnersLater re-attempts scheduling runners for group after a
// delay drawn from the group's per-group backoff (a jittered exponential that
// starts at CheckRunnerTime and is capped at scheduleRetryBackoffMax), unless
// the server is shutting down. This stops a persistently-failing submit (eg. a
// queue that always rejects) from re-running with the same count forever at a
// fixed interval, while the jitter avoids many groups retrying in lockstep.
func (s *Server) retryScheduleRunnersLater(ctx context.Context, group *sgroup) {
	b := group.ensureRetryBackoff(s.timings.CheckRunnerTime)

	wgk := s.wg.Add(1)

	go func() {
		defer internal.LogPanic(ctx, "jobqueue schedule runners retry", true)
		defer s.wg.Done(wgk)

		// Bridge s.stopClientHandling (closed on shutdown) to a cancellable
		// context so the jittered backoff sleep aborts promptly: a pending sleep
		// (up to scheduleRetryBackoffMax) must not block s.wg and hang manager
		// shutdown. The bridge goroutine also exits when the sleep completes
		// normally (cancel via defer closes sleepCtx.Done()).
		sleepCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		go func() {
			select {
			case <-s.stopClientHandling:
				cancel()
			case <-sleepCtx.Done():
			}
		}()

		b.Sleep(sleepCtx)

		if sleepCtx.Err() != nil {
			return
		}

		group.Lock()
		s.scheduleRunners(ctx, group)
		group.Unlock()
	}()
}

// adjust our count of how many jobs with this schedulerGroup we need in the job
// scheduler. Optionally supply the number to decrement by (default 1).
func (s *Server) decrementGroupCount(ctx context.Context, schedulerGroup string, optionalDrop ...int) {
	drop := 1
	if len(optionalDrop) == 1 {
		drop = optionalDrop[0]
	}

	if s.runnerCommand() == "" {
		return
	}

	s.psgmutex.RLock()
	group, existed := s.previouslyScheduledGroups[schedulerGroup]
	hasSkippedGroups := s.hasSkippedScheduledGroups()
	s.psgmutex.RUnlock()

	if hasSkippedGroups {
		defer s.triggerReadyAddedCallback(ctx)
	}

	if !existed {
		return
	}

	count := group.decrement(drop)
	if count >= 0 {
		clone := group.clone(count)
		s.scheduleRunners(ctx, clone)
	}
}

// hasSkippedScheduledGroups reports whether any previously scheduled group had
// jobs skipped due to limits. Must be called with s.psgmutex held.
func (s *Server) hasSkippedScheduledGroups() bool {
	for _, scheduledGroup := range s.previouslyScheduledGroups {
		if scheduledGroup.hasSkips() {
			return true
		}
	}

	return false
}

// getBadServers converts the slice of cloud.Server objects we hold in to a
// slice of badServer structs.
func (s *Server) getBadServers() []*BadServer {
	s.bsmutex.RLock()

	bs := make([]*BadServer, 0, len(s.badServers))
	for _, server := range s.badServers {
		bs = append(bs, cloudServerToBadServer(server))
	}

	s.bsmutex.RUnlock()

	return bs
}

// cloudServerToBadServer converts a cloud.Server to a BadServer.
func cloudServerToBadServer(server *cloud.Server) *BadServer {
	return &BadServer{
		ID:      server.ID,
		Name:    server.Name,
		IP:      server.IP,
		Date:    time.Now().Unix(),
		IsBad:   server.IsBad(),
		Problem: server.PermanentProblem(),
	}
}

// killBadCloudServers confirms currently bad servers are dead. Supply it
// badservers from getBadServers(), and get back the ones actually killed, plus
// affected jobs. Optionally supply a non-blank server id to only work on that
// one, if it is amongst the bad servers.
func (s *Server) killBadCloudServers(ctx context.Context, servers []*BadServer, onlyid string) ([]*BadServer, []*Job) {
	// first destroy or confirm dead currently bad servers
	confirmed, serverIDs := s.confirmBadServersDead(ctx, servers, onlyid)

	clog.Debug(ctx, "confirmed bad servers as dead", "number", len(confirmed))

	// now kill running or lost jobs on those servers. Note that the delay
	// between destroying the servers and managing to eg. bury the affected jobs
	// with the killJob() call below can result in scheduler churn, where it
	// tries to bring up new servers for jobs we're seconds away from burying.
	// *** I don't think there's much to be done about that though; we must be
	// sure the servers are really dead before confirming jobs are dead.
	jobs := s.killJobsOnServers(ctx, serverIDs)

	return confirmed, jobs
}

// confirmBadServersDead destroys (or confirms already dead) the given bad
// servers, optionally restricted to onlyid, returning the confirmed servers and
// the set of their IDs.
func (s *Server) confirmBadServersDead(ctx context.Context, servers []*BadServer,
	onlyid string) ([]*BadServer, map[string]bool) {
	var confirmed []*BadServer

	serverIDs := make(map[string]bool)

	s.bsmutex.Lock()
	defer s.bsmutex.Unlock()

	for _, badServer := range servers {
		if !badServer.IsBad {
			continue
		}

		if onlyid != "" && onlyid != badServer.ID {
			continue
		}

		server := s.badServers[badServer.ID]
		delete(s.badServers, badServer.ID)

		if server != nil && server.IsBad() {
			s.destroyBadCloudServer(ctx, server)
		}

		confirmed = append(confirmed, badServer)

		serverIDs[badServer.ID] = true
	}

	return confirmed, serverIDs
}

// destroyBadCloudServer destroys the given server and removes info about this
// from the web interface. Only call while holding the bsmutex lock.
func (s *Server) destroyBadCloudServer(ctx context.Context, server *cloud.Server) {
	if err := server.Destroy(ctx); err != nil {
		clog.Warn(ctx, "server was bad but could not be destroyed", "server", server.ID, "err", err)

		return
	}

	bs := cloudServerToBadServer(server)
	bs.IsBad = false

	// make the message in the web interface about this server go away
	s.badServerCaster.Send(bs)
}

// killCloudServer is like killBadCloudServers(), but works only on the server
// with the given host name (returning it as a BadServer if found), and doesn't
// care if we currently consider it bad.
func (s *Server) killCloudServer(ctx context.Context, hostName string) (*BadServer, []*Job) {
	host := s.scheduler.GetHost(hostName)
	if host == nil {
		clog.Warn(ctx, "request to kill a non-existent host", "host", hostName)

		return nil, nil
	}

	server, ok := host.(*cloud.Server)
	if !ok {
		clog.Error(ctx, "killCloudServer host was not a cloud.Server", "host", host)

		return nil, nil
	}

	server.GoneBad("manually killed")

	s.bsmutex.Lock()
	delete(s.badServers, server.ID)
	s.destroyBadCloudServer(ctx, server)
	s.bsmutex.Unlock()

	return cloudServerToBadServer(server), s.killJobsOnServers(ctx, map[string]bool{server.ID: true})
}

// getSetLimitGroup does the server side of Client.GetOrSetLimitGroup(), taking
// the same argument. The string return value is one of our Err* constants.
func (s *Server) getSetLimitGroup(ctx context.Context, group string) (*limiter.GroupData, string, error) {
	name, limit := s.splitSuffixedLimitGroup(group)

	if limit == nil {
		return s.limiter.GetLimit(ctx, name), "", nil
	}

	if err := s.setLimitGroup(ctx, name, limit); err != nil {
		return limiter.NewCountGroupData(-1), ErrDBError, err
	}

	return limit, "", nil
}

// setLimitGroup persists a limit group, applies the new limit (if valid),
// removes any limits the store reported as removed, and re-triggers scheduling.
func (s *Server) setLimitGroup(ctx context.Context, name string, limit *limiter.GroupData) error {
	_, removed, err := s.db.storeLimitGroups(map[string]*limiter.GroupData{name: limit})
	if err != nil {
		return err
	}

	if limit.IsValid() {
		s.limiter.SetLimit(name, *limit)
	}

	for _, g := range removed {
		s.limiter.RemoveLimit(g)
	}

	s.triggerReadyAddedCallback(ctx)

	return nil
}

// splitSuffixedLimitGroup parses a limit group that might be suffixed with a
// colon and the limit of that group. Returns the group name, and if the final
// bool is true, the int will be the desired limit for that group.
func (s *Server) splitSuffixedLimitGroup(group string) (string, *limiter.GroupData) {
	return limiter.NameToGroupData(group)
}

// storeWebSocketConnection stores a connection and returns a unique identifier
// so that it can be later closed with closeWebSocketConnection(unique) or
// during Server shutdown.
func (s *Server) storeWebSocketConnection(conn *websocket.Conn) (string, bool) {
	s.ssmutex.RLock()
	defer s.ssmutex.RUnlock()

	if !s.up {
		return "", false
	}

	s.wsmutex.Lock()
	defer s.wsmutex.Unlock()

	s.wsHandlerWG.Add(statusWebSocketWorkerCount)

	unique := logext.RandId(webSocketIDLength)
	s.wsconns[unique] = conn
	s.wsWriteMutexes[unique] = &sync.Mutex{}

	return unique, true
}

// closeWebSocketConnection closes the connection that was stored with
// storeWebSocketConnection() and that returned the given unique string.
// Closing it this way means that during Server shutdown we won't try and close
// it again.
func (s *Server) closeWebSocketConnection(ctx context.Context, unique string) {
	s.wsmutex.Lock()

	conn, found := s.wsconns[unique]
	if found {
		delete(s.wsconns, unique)
		delete(s.wsWriteMutexes, unique)
	}

	s.wsmutex.Unlock()

	if found {
		err := conn.Close()
		if err != nil {
			clog.Warn(ctx, "websocket close failed", "err", err)
		}
	}
}

// shutdown stops listening to client connections, close all queues and
// persists them to disk.
//
// Does nothing if already shutdown.
//
// For now it also kills all currently running jobs so that their runners don't
// stay alive uselessly. *** This adds 15s to our shutdown time...
func (s *Server) shutdown(ctx context.Context, reason string, wait bool, stopSigHandling bool) {
	if !s.beginShutdown(ctx, stopSigHandling) {
		return
	}

	// stop the background startup goroutines (prior-state recovery and the
	// one-time complete-counter backfill) before touching the scheduler, DB or
	// queue below, so their late work can't race the teardown: no
	// scheduler.Recover after scheduler.Cleanup, no DB ops after db.close, and
	// no enqueue while s.q.Destroy runs.
	s.stopBackgroundStartupTasks()

	s.waitForRunnersToDie(ctx, wait)

	// stop the scheduler
	s.scheduler.Cleanup(ctx)

	s.closeWebSockets(ctx)
	s.wsHandlerWG.Wait()

	s.badServerCaster.Close()
	s.schedCaster.Close()
	s.statusCaster.Close()

	s.shutdownHTTPServer(ctx)

	shutdownPprofServer(ctx, s.pprofServer)

	s.closeServerCommsAndDB(ctx)

	// wait for our goroutines to finish
	s.wg.Wait(ServerShutdownWaitTime)

	s.waitForPortsClosed(ctx)

	// clean up our queues and empty everything out to be garbage collected,
	// in case the same process calls Serve() again after this
	if err := s.q.Destroy(); err != nil {
		clog.Warn(ctx, "server shutdown queue destruction failed", "err", err)
	}

	if wasBlocking := s.resetStateAfterShutdown(); wasBlocking {
		s.done <- Error{"Serve", "", reason}
	}
}

// closeServerCommsAndDB closes the command line interface, command socket and
// database, and frees any clients waiting on a reserve.
func (s *Server) closeServerCommsAndDB(ctx context.Context) {
	// close our command line interface
	s.closeClientSubscriptions()
	close(s.stopClientHandling)
	s.waitForClientHandling(ctx)
	time.Sleep(s.timings.ShutdownSocketWait)

	if err := s.sock.Close(); err != nil {
		clog.Warn(ctx, "server shutdown socket close failed", "err", err)
	}

	// close the database
	if err := s.db.close(ctx); err != nil {
		clog.Warn(ctx, "server shutdown database close failed", "err", err)
	}

	// free any waiting reserves
	s.rpmutex.Lock()
	s.racPending = false
	s.racRunning = false
	s.clearRACWaiters()
	s.rpmutex.Unlock()
}

// resetStateAfterShutdown clears the queue and shutdown flags so the same
// process can Serve() again, returning whether a caller was blocking in Block().
func (s *Server) resetStateAfterShutdown() bool {
	// nil under ssmutex, since the Drain() goroutine reads s.q under it
	s.ssmutex.Lock()
	s.q = nil
	s.ssmutex.Unlock()

	s.krmutex.Lock()
	s.killRunners = false
	s.krmutex.Unlock()

	s.ssmutex.Lock()
	defer s.ssmutex.Unlock()

	s.drain = false
	wasBlocking := s.blocking
	s.blocking = false

	return wasBlocking
}

// beginShutdown marks the server as down (so touches return a kill signal) and
// unschedules its groups, returning false if the server was already down.
func (s *Server) beginShutdown(ctx context.Context, stopSigHandling bool) bool {
	s.ssmutex.Lock()

	if !s.up {
		s.ssmutex.Unlock()

		return false
	}

	if stopSigHandling {
		close(s.stopSigHandling)
	}

	s.unscheduleAllGroups(ctx)

	// change touch to always return a kill signal
	s.up = false
	s.drain = true
	s.ServerInfo.Mode = ServerModeDrain
	s.ssmutex.Unlock()

	s.krmutex.Lock()
	s.killRunners = true
	s.krmutex.Unlock()

	return true
}

// unscheduleAllGroups unschedules every previously scheduled group. Must be
// called with s.ssmutex held.
func (s *Server) unscheduleAllGroups(ctx context.Context) {
	s.psgmutex.Lock()
	defer s.psgmutex.Unlock()

	for name, group := range s.previouslyScheduledGroups {
		s.scheduleRunners(ctx, group.clone(0))
		delete(s.previouslyScheduledGroups, name)
	}
}

// waitForRunnersToDie waits long enough for runners to have attempted a touch
// (and so learn they should die) and, if wait is set, polls until none remain.
func (s *Server) waitForRunnersToDie(ctx context.Context, wait bool) {
	if s.HasRunners(ctx) {
		// wait until everything must have attempted a touch
		<-time.After(s.timings.TouchInterval)
	}

	// wait for the runners to actually die
	if !wait {
		return
	}

	ticker := time.NewTicker(serverShutdownRunnerTickerTime)
	defer ticker.Stop()

	for range ticker.C {
		if !s.HasRunners(ctx) {
			return
		}
	}
}

// closeWebSockets closes and forgets every open websocket connection.
func (s *Server) closeWebSockets(ctx context.Context) {
	s.wsmutex.Lock()
	defer s.wsmutex.Unlock()

	for unique, conn := range s.wsconns {
		if errc := conn.Close(); errc != nil {
			clog.Warn(ctx, "server shutdown failed to close a websocket", "err", errc)
		}

		delete(s.wsconns, unique)
		delete(s.wsWriteMutexes, unique)
	}
}

// shutdownHTTPServer shuts the web interface down, forcing completion after
// httpServerShutdownTime because a graceful shutdown is slow due to a fixed
// 500ms poll.
func (s *Server) shutdownHTTPServer(ctx context.Context) {
	httpCtx, cancel := context.WithTimeout(ctx, ServerShutdownWaitTime)

	go func() {
		<-time.After(httpServerShutdownTime)
		cancel()
	}()

	err := s.httpServer.Shutdown(httpCtx)
	if err != nil && !errors.Is(err, context.Canceled) {
		clog.Warn(ctx, "server shutdown of web interface failed", "err", err)
	}
}

// waitForPortsClosed blocks until both the command and web ports are no longer
// being listened to (which is the best proxy we have for them being free).
// portStillListening closes any open connection as a side effect.
func (s *Server) waitForPortsClosed(ctx context.Context) {
	for {
		stillUp := s.portStillListening(ctx, s.ServerInfo.Port)
		if !stillUp {
			stillUp = s.portStillListening(ctx, s.ServerInfo.WebPort)
		}

		if !stillUp {
			return
		}
	}
}

// portStillListening reports whether something is still listening on the given
// port of this host. If a connection could be made it is immediately closed
// (any close error is just logged). A dial failure is taken to mean the port is
// no longer being listened to.
func (s *Server) portStillListening(ctx context.Context, port string) bool {
	dialCtx, cancel := context.WithTimeout(ctx, portCheckDialTimeout)
	defer cancel()

	var dialer net.Dialer

	conn, err := dialer.DialContext(dialCtx, "tcp", net.JoinHostPort("", port))
	if err != nil || conn == nil {
		return false
	}

	if errc := conn.Close(); errc != nil {
		clog.Warn(ctx, "server shutdown port close failed", "port", port, "err", errc)
	}

	return true
}

// subscribeToJobs adds the specified jobs to a status websocket subscription.
func (s *Server) subscribeToJobs(subscriptionID string, jobKeys []string) {
	if len(jobKeys) == 0 {
		return
	}

	sub, exists := s.clientSubscription(subscriptionID)
	if !exists {
		return
	}

	sub.addKeys(jobKeys)
}

// unsubscribeFromJob removes a specific job, or all jobs when jobKey is empty,
// from a status websocket subscription.
func (s *Server) unsubscribeFromJob(subscriptionID string, jobKey string) {
	sub, exists := s.clientSubscription(subscriptionID)
	if !exists {
		return
	}

	sub.removeKey(jobKey)
}
