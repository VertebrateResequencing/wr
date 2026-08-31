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

// This file contains the functions needed to implement a jobqueue client.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/container"
	"github.com/VertebrateResequencing/wr/container/docker"
	"github.com/VertebrateResequencing/wr/fs/local"
	"github.com/VertebrateResequencing/wr/internal"
	_ "github.com/VertebrateResequencing/wr/internal/mangostlstcp" // register race-clean tls+tcp transport
	"github.com/gofrs/uuid/v5"
	"github.com/kballard/go-shellquote"
	"github.com/moby/moby/client"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
	"go.nanomsg.org/mangos/v3/protocol/req"
)

// FailReason* are the reasons for cmd line failure stored on Jobs.
const (
	FailReasonEnv      = "failed to get environment variables"
	FailReasonCwd      = "working directory does not exist"
	FailReasonStart    = "command failed to start"
	FailReasonCPerm    = "command permission problem"
	FailReasonCFound   = "command not found"
	FailReasonCExit    = "command invalid exit code"
	FailReasonExit     = "command exited non-zero"
	FailReasonRAM      = "command used too much RAM"
	FailReasonDisk     = "ran out of disk space"
	FailReasonTime     = "command used too much time"
	FailReasonDocker   = "could not interact with docker"
	FailReasonAbnormal = "command failed to complete normally"
	FailReasonLost     = "lost contact with runner"
	FailReasonSignal   = "runner received a signal to stop"
	FailReasonResource = "resource requirements cannot be met"
	FailReasonMount    = "mounting of remote file system(s) failed"
	FailReasonUpload   = "failed to upload files to remote file system"
	FailReasonKilled   = "killed by user request"
)

// lsfEmulationDir is the name of the directory we store our LSF emulation
// symlinks in.
const lsfEmulationDir = ".wr_lsf_emulation"

// localhost is the name of host we're running on.
const localhost = "localhost"

// terminateGrace is how long we wait after terminating a child process before
// we follow up with a kill signal.
const terminateGrace = 500 * time.Millisecond

// these global variables are primarily exported for testing purposes; you
// probably shouldn't change them (*** and they should probably be re-factored
// as fields of a config struct...)
//
//nolint:gochecknoglobals // exported tunables, primarily for tests; see comment above.
var (
	ClientTouchInterval                  = 15 * time.Second
	ClientReleaseDelayMin                = 30 * time.Second
	ClientReleaseDelayMax                = 1800 * time.Second
	ClientReleaseDelayStepFactor float64 = 2
	ClientPercentMemoryKill              = 90
	ClientRetryWait                      = 15 * time.Second
	ClientRetryTime                      = 24 * time.Hour
	ClientShutdownTimeout                = 120 * time.Second
	ClientShutdownTestInterval           = 100 * time.Millisecond
	ClientSuggestedPingTimeout           = 10 * time.Millisecond
	ClientMinRequestTimeout              = 60 * time.Second
	RAMIncreaseMin               float64 = 1000
	RAMIncreaseMultLow                   = 2.0
	RAMIncreaseMultHigh                  = 1.3
	RAMIncreaseMultBreakpoint    float64 = 8192
)

const (
	requestMethodStart          = "jstart"
	requestMethodModify         = "jmod"
	requestMethodSubscribe      = "subscribe"
	requestMethodTouch          = "jtouch"
	requestMethodUnsubscribe    = "unsubscribe"
	requestMethodWaitForUpdates = "waitForUpdates"
)

const (
	exitCodeCommandPermission = 126
	exitCodeCommandNotFound   = 127
	exitCodeCommandInvalid    = 128
	exitCodeAbnormal          = 255
)

const (
	// clientOpExecute is the operation name used in Errors returned by Execute.
	clientOpExecute = "Execute"

	// stdSaverBytes is how many bytes of the head and tail of a command's
	// STDOUT/STDERR we keep.
	stdSaverBytes = 4096

	// executeStopChannelBuffer is the buffer size of the stopTouching and
	// stopChecking channels, which may receive from more than one place.
	executeStopChannelBuffer = 2

	// executeSignalChannelBuffer is the buffer size of the OS signal channel.
	executeSignalChannelBuffer = 5

	// fusermountRetryDelaySeconds is how long we wait before retrying a mount
	// that failed with "fusermount exited with code 256".
	fusermountRetryDelaySeconds = 5

	// kbPerMB converts between kilobytes and megabytes (bytesPerKB is shared
	// from utils.go).
	kbPerMB = 1024

	// percentDivisor turns a percentage into a fraction of a whole.
	percentDivisor = 100
)

// errGetRecentState is returned by GetRecent when given a non-empty state; it
// only returns complete jobs and does not support a state filter.
var errGetRecentState = errors.New(
	"GetRecent (--recent) only returns complete jobs and does not support a state filter",
)

// errRecvDeadlineType is returned if mangos ever stops reporting the socket's
// receive deadline as a time.Duration.
var errRecvDeadlineType = errors.New("socket receive deadline was not a duration")

const (
	RepGroupMatchExact  RepGroupMatch = "exact"
	RepGroupMatchSubStr RepGroupMatch = "substr"
	RepGroupMatchPrefix RepGroupMatch = "prefix"
	RepGroupMatchSuffix RepGroupMatch = "suffix"
)

// AddWarnings describes non-fatal add-time conditions callers may want to
// surface to users.
type AddWarnings struct {
	NeverSeenDepGroups []string
}

func touchEndState(job *Job) *JobEndState {
	return &JobEndState{
		PeakRAM:  job.PeakRAM,
		PeakDisk: job.PeakDisk,
		CPUtime:  job.CPUtime,
		Stdout:   slices.Clone(job.StdOutC),
		Stderr:   slices.Clone(job.StdErrC),
	}
}

func cloneJobEndState(endState *JobEndState) *JobEndState {
	if endState == nil {
		return nil
	}

	clone := *endState
	clone.Stdout = slices.Clone(endState.Stdout)
	clone.Stderr = slices.Clone(endState.Stderr)

	return &clone
}

// clientRequest is the struct that clients send to the server over the network
// to request it do something. (The properties are only exported so the
// encoder doesn't ignore them.)
type clientRequest struct {
	Env                     []byte // compressed binc encoding of []string
	Jobs                    []*Job
	Keys                    []string
	States                  []JobState
	File                    []byte // compressed bytes of file content
	Token                   []byte
	LimitGroup              string
	Method                  string
	SubscriptionID          string
	SchedulerGroup          string
	State                   JobState
	Path                    string // desired path File should be stored at, can be blank
	CloudServerID           string
	FailReason              string
	Host                    string // ADDITIVE wire-only: reserving runner's host (old client sends "")
	SchedulerID             string // ADDITIVE wire-only: reserving runner's scheduler element id (old/non-LSF sends "")
	Job                     *Job
	JobEndState             *JobEndState
	Modifier                *JobModifier
	Limit                   int
	Pid                     int // ADDITIVE wire-only: reserving runner's pid (old client sends 0)
	Timeout                 time.Duration
	Period                  time.Duration
	ClientID                uuid.UUID
	FirstReserve            bool
	GetEnv                  bool
	GetStd                  bool
	IgnoreComplete          bool
	IncludeComplete         bool
	IncludeStatusDetails    bool
	Search                  bool
	WaitingForDepGroups     bool
	RepGroupMatch           RepGroupMatch
	ConfirmDeadCloudServers bool
	DestroyCloudHost        string
	ReturnIDs               bool // when adding jobs, return the IDs of the added jobs
}

// RepGroupMatch controls how RepGroup filters are applied by repgroup-based
// job retrieval calls.
type RepGroupMatch string

// RepGroupMatches reports if jobRepGroup matches repgroup according to match.
func RepGroupMatches(jobRepGroup, repgroup string, match RepGroupMatch) bool {
	switch match {
	case RepGroupMatchSubStr:
		return strings.Contains(jobRepGroup, repgroup)
	case RepGroupMatchPrefix:
		return strings.HasPrefix(jobRepGroup, repgroup)
	case RepGroupMatchSuffix:
		return strings.HasSuffix(jobRepGroup, repgroup)
	default:
		return jobRepGroup == repgroup
	}
}

func (cr *clientRequest) key() string {
	if len(cr.Keys) > 0 {
		return cr.Keys[0]
	}

	if cr.Job != nil {
		return cr.Job.Key()
	}

	return ""
}

func (cr *clientRequest) failReason() string {
	if cr.FailReason != "" || cr.Job == nil {
		return cr.FailReason
	}

	return cr.Job.FailReason
}

type serverContactState struct {
	lost atomic.Bool
}

func (state *serverContactState) recordTouchResult(err error) {
	state.lost.Store(err != nil)
}

func (state *serverContactState) schedulerMemoryFallbackAllowed() bool {
	return !state.lost.Load()
}

type executeLiveState struct {
	sync.Mutex
	stdout   *liveTailSaver
	stderr   *liveTailSaver
	cwd      string
	peakRAM  int
	peakDisk int64
	cpuTime  time.Duration
}

func newExecuteLiveState(cwd string, stdout, stderr *liveTailSaver) *executeLiveState {
	return &executeLiveState{
		stdout: stdout,
		stderr: stderr,
		cwd:    cwd,
	}
}

func (state *executeLiveState) updateResources(peakRAM int, peakDisk int64, cpuTime time.Duration) {
	state.Lock()
	defer state.Unlock()

	if peakRAM > state.peakRAM {
		state.peakRAM = peakRAM
	}

	if peakDisk > state.peakDisk {
		state.peakDisk = peakDisk
	}

	if cpuTime > state.cpuTime {
		state.cpuTime = cpuTime
	}
}

func (state *executeLiveState) snapshot() *JobEndState {
	stdout := state.stdout.FlushCompressed()
	stderr := state.stderr.FlushCompressed()

	state.Lock()
	defer state.Unlock()

	return &JobEndState{
		Cwd:      state.cwd,
		PeakRAM:  state.peakRAM,
		PeakDisk: state.peakDisk,
		CPUtime:  state.cpuTime,
		Stdout:   stdout,
		Stderr:   stderr,
	}
}

// GetRecent gets archived Jobs across all rep groups that finished running
// (were Archive()d) within the last period. Only exit-0 jobs are ever archived,
// so all returned jobs are complete; state must be "" (a non-"" state is a
// programming error - the CLI rejects state filters before calling). 'limit',
// 'getStd' and 'getEnv' behave as in GetByRepGroup.
func (c *Client) GetRecent(period time.Duration, limit int, state JobState, getStd, getEnv bool) ([]*Job, error) {
	if state != "" {
		return nil, fmt.Errorf("%w, but was given state %q", errGetRecentState, state)
	}

	resp, err := c.request(&clientRequest{
		Method: requestMethodGetRecent, Period: period, Limit: limit, GetStd: getStd, GetEnv: getEnv,
	})
	if err != nil {
		return nil, err
	}

	return resp.Jobs, err
}

// SetReserveSchedulerID records the scheduler element id (e.g. an LSF
// "jobid[index]") of the runner using this client, so subsequent Reserve /
// ReserveScheduled requests tell the server which scheduler element holds the
// reservation, ensuring it is never killed as excess. Pass "" for
// non-scheduler/non-LSF clients (the default).
func (c *Client) SetReserveSchedulerID(schedulerID string) {
	c.reserveSchedulerID = schedulerID
}

// requestWithin is request(), but with this one request's receive deadline
// narrowed to timeout, restoring the socket's own deadline afterwards. A failed
// restore is joined on to whatever the request itself returned rather than
// replacing it or being dropped: the socket is then stuck on the narrow
// deadline, so the caller needs to be told, and errors.Is still finds the
// request's own error for callers that discriminate on it.
//
// It can only ever narrow. The deadline it narrows from and restores to is read
// off the socket rather than recomputed from c.timeout, because c.timeout is
// the connect timeout the Client was made with and reconnect() does not update
// it: recomputing would widen a reconnected socket's deadline (60s to
// requestTimeout(c.timeout)) in the name of capping it. A timeout that is not
// positive asks for no bound at all, so it narrows nothing; any other timeout
// narrows whenever the socket's deadline is wider, which includes the socket's
// deadline being non-positive, since mangos reads that as "wait forever".
//
// The subscription reconnect path uses it because it has a retry budget to
// honour: a manager part-way through shutdown can still accept a connection and
// answer the connect-time Ping moments before it stops answering anything, and
// the unanswered request that follows would otherwise block for the socket's
// ClientMinRequestTimeout (60s) floor, overrunning a budget of milliseconds.
// Because only this request is narrowed, every other request keeps that floor
// and a slow-but-alive server is still not mistaken for a dead one.
func (c *Client) requestWithin(cr *clientRequest, timeout time.Duration) (sr *serverResponse, err error) {
	c.Lock()
	defer c.Unlock()

	socketTimeout, err := c.recvDeadline()
	if err != nil {
		return nil, err
	}

	if timeout <= 0 || (socketTimeout > 0 && timeout >= socketTimeout) {
		return c.requestLocked(cr)
	}

	if err = c.sock.SetOption(mangos.OptionRecvDeadline, timeout); err != nil {
		return nil, err
	}

	defer func() {
		if restoreErr := c.sock.SetOption(mangos.OptionRecvDeadline, socketTimeout); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
	}()

	return c.requestLocked(cr)
}

// recvDeadline returns the receive deadline the client's socket currently has.
func (c *Client) recvDeadline() (time.Duration, error) {
	val, err := c.sock.GetOption(mangos.OptionRecvDeadline)
	if err != nil {
		return 0, err
	}

	timeout, ok := val.(time.Duration)
	if !ok {
		return 0, errRecvDeadlineType
	}

	return timeout, nil
}

// requestLocked does the work of request(), and must be called with the
// client's lock held.
func (c *Client) requestLocked(cr *clientRequest) (*serverResponse, error) {
	if err := c.encodeAndSend(cr); err != nil {
		return nil, err
	}

	sr, err := c.recvAndDecode()
	if err != nil {
		return nil, err
	}

	// pull the error out of sr
	if sr.Err != "" {
		return sr, Error{cr.Method, cr.key(), sr.Err}
	}

	return sr, nil
}

// reserveHostAndPid returns this runner's hostname (falling back to localhost if
// it can't be determined) and its own pid, to stamp on a reserve request so the
// server can record which runner holds the reservation before the command's own
// pid is reported at Started.
func reserveHostAndPid() (string, int) {
	host, err := os.Hostname()
	if err != nil {
		host = localhost
	}

	return host, os.Getpid()
}

func currentProcessTreeCPUtime(pid int) time.Duration {
	pid32, ok := processPID(pid)
	if !ok {
		return 0
	}

	root, err := process.NewProcess(pid32)
	if err != nil {
		return 0
	}

	total := currentProcessCPUtime(root)

	children, err := getChildProcesses(pid32)
	if err != nil {
		return total
	}

	for _, child := range children {
		total += currentProcessCPUtime(child)
	}

	return total
}

// Client represents the client side of the socket that the jobqueue server is
// Serve()ing, specific to a particular queue.
type Client struct {
	ch          codec.Handle
	clientid    uuid.UUID
	hasReserved bool
	sock        mangos.Socket
	sync.Mutex
	teMutex    sync.Mutex // to protect Touch() from other methods during Execute()
	token      []byte
	timeout    time.Duration
	restClient *http.Client
	ServerInfo *ServerInfo
	host       string
	port       string
	args       []string // allowing internal reconnects

	// timing parameters this client uses; defaulted from the server's
	// ServerInfo at Connect() (falling back to the Client* package defaults for
	// older servers), but may be overridden by in-package code or tests before use.
	touchInterval time.Duration
	retryWait     time.Duration
	retryTime     time.Duration

	// percentMemoryKill is the percentage of physical machine memory a running
	// command may use before we kill it. Defaults to ClientPercentMemoryKill,
	// but may be overridden locally before Execute() (used by tests).
	percentMemoryKill int

	// liveTouchHook is used by in-package tests to inspect the live touch state
	// assembled during Execute().
	liveTouchHook func(*JobEndState)

	// reserveSchedulerID is the scheduler element id (e.g. an LSF "jobid[index]")
	// of the runner using this client, set by the runner via
	// SetReserveSchedulerID and sent on reserve requests so the server can tell
	// the scheduler which element holds the reservation (it must not be killed as
	// excess). Empty for non-scheduler/non-LSF clients.
	reserveSchedulerID string
}

// envStr holds the []string from os.Environ(), for codec compatibility.
type envStr struct {
	Environ []string
}

// Connect creates a connection to the jobqueue server.
//
// addr is the host or IP of the machine running the server, suffixed with a
// colon and the port it is listening on, eg localhost:1234
//
// caFile is a path to the PEM encoded CA certificate that was used to sign the
// server's certificate. If set as a blank string, or if the file doesn't exist,
// the server's certificate will be trusted based on the CAs installed in the
// normal location on the system.
//
// certDomain is a domain that the server's certificate is supposed to be valid
// for.
//
// token is the authentication token that Serve() returned when the server was
// started.
//
// Timeout determines how long to wait for a response from the server, not only
// while connecting, but for all subsequent interactions with it using the
// returned Client.
func Connect(addr, caFile, certDomain string, token []byte, timeout time.Duration) (*Client, error) {
	expiry, err := internal.CertExpiry(caFile)
	if err != nil {
		return nil, err
	}

	if time.Now().After(expiry) {
		return nil, internal.CertError{Type: internal.ErrExpiredCert, Path: caFile}
	}

	sock, err := dialClientSocket(addr, caFile, certDomain, timeout)
	if err != nil {
		return nil, err
	}

	c, err := newClientForSocket(sock, addr, caFile, certDomain, token, timeout)
	if err != nil {
		return nil, err
	}

	if clientOnErr, errp := c.establishServerInfo(timeout); errp != nil {
		return clientOnErr, errp
	}

	// now that connect-readiness has been confirmed, decouple the per-request
	// RECEIVE deadline from the (possibly short) connect timeout, giving it a
	// generous floor so that a slow-but-alive server reply is not mistaken for a
	// timeout (the cause of the spurious 'receive time out' flake). We do NOT
	// widen the SEND deadline: the req socket blocks Send until it has a live
	// pipe to write to, so a short send deadline is what makes a NEW request to a
	// gone-away server fail fast (with 'send time out'), which is how unreachable
	// servers are detected promptly.
	if err = sock.SetOption(mangos.OptionRecvDeadline, requestTimeout(timeout)); err != nil {
		return nil, err
	}

	return c, nil
}

// newClientForSocket builds a Client around an already-dialled socket, giving it
// a fresh client UUID.
func newClientForSocket(sock mangos.Socket, addr, caFile, certDomain string,
	token []byte, timeout time.Duration,
) (*Client, error) {
	// clients identify themselves (only for the purpose of calling methods that
	// require the client has previously used Reserve()) with a UUID; v4 is used
	// since speed doesn't matter: a typical client executable will only
	// Connect() once; on the other hand, we avoid any possible problem with
	// running on machines with low time resolution
	u, err := uuid.NewV4()
	if err != nil {
		return nil, err
	}

	addrParts := strings.Split(addr, ":")

	return &Client{
		sock:     sock,
		ch:       new(codec.BincHandle),
		token:    token,
		timeout:  timeout,
		clientid: u,
		host:     addrParts[0],
		port:     addrParts[1],
		args:     []string{addr, caFile, certDomain},
	}, nil
}

// dialClientSocket creates a req socket configured with TLS for the given
// server and dials it, returning ErrNoServer if the dial fails.
func dialClientSocket(addr, caFile, certDomain string, timeout time.Duration) (mangos.Socket, error) {
	sock, err := req.NewSocket()
	if err != nil {
		return nil, err
	}

	if err = setConnectSocketOptions(sock, timeout); err != nil {
		return nil, err
	}

	dialOpts := map[string]any{mangos.OptionTLSConfig: clientTLSConfig(caFile, certDomain)}
	if err = sock.DialOptions("tls+tcp://"+addr, dialOpts); err != nil {
		if errc := sock.Close(); errc != nil && !isClosedSocketError(errc) {
			return nil, errc
		}

		return nil, Error{"Connect", "", ErrNoServer}
	}

	return sock, nil
}

// setConnectSocketOptions applies the message size and connect-time send/recv
// deadlines used while establishing a connection.
func setConnectSocketOptions(sock mangos.Socket, timeout time.Duration) error {
	if err := sock.SetOption(mangos.OptionMaxRecvSize, 0); err != nil {
		return err
	}

	// while connecting, bound send/recv by the supplied timeout so that
	// connect-readiness (the initial Ping below) fails fast if the server can be
	// dialled but does not respond. Once connected, the receive deadline is
	// widened to a generous floor (see requestTimeout / ClientMinRequestTimeout)
	// so individual requests are not subject to spurious 'receive time out's
	// under load; the send deadline stays short so requests still fail fast when
	// the server has gone away.
	if err := sock.SetOption(mangos.OptionRecvDeadline, timeout); err != nil {
		return err
	}

	return sock.SetOption(mangos.OptionSendDeadline, timeout)
}

// clientTLSConfig builds the TLS config for dialling the server, trusting the
// CA in caFile if it can be read.
func clientTLSConfig(caFile, certDomain string) *tls.Config {
	tlsConfig := &tls.Config{ServerName: certDomain}

	if caCert, err := os.ReadFile(filepath.Clean(caFile)); err == nil {
		certPool := x509.NewCertPool()
		certPool.AppendCertsFromPEM(caCert)
		tlsConfig.RootCAs = certPool
	}

	return tlsConfig
}

// establishServerInfo pings the server to confirm the application-level
// connection, populating ServerInfo and the client's derived timing fields, and
// reporting authentication failures consistently. On error it returns the
// client to hand back to the caller (non-nil only if closing the socket also
// failed, matching the original Connect behaviour).
func (c *Client) establishServerInfo(timeout time.Duration) (*Client, error) {
	si, err := c.Ping(timeout)
	if err != nil {
		return c.handlePingFailure(err)
	}

	c.ServerInfo = si
	c.touchInterval = dfltDuration(si.TouchInterval, ClientTouchInterval)
	c.retryWait = dfltDuration(si.RetryWait, ClientRetryWait)
	c.retryTime = dfltDuration(si.RetryTime, ClientRetryTime)
	c.percentMemoryKill = ClientPercentMemoryKill

	return nil, nil //nolint:nilnil // success: no client-to-return-on-error and no error.
}

// handlePingFailure closes the socket after a failed connect-time Ping and
// returns the error to report. It returns a non-nil client only if closing the
// socket also failed, matching the original Connect behaviour.
func (c *Client) handlePingFailure(pingErr error) (*Client, error) {
	if errc := c.sock.Close(); errc != nil {
		return c, errc
	}

	msg := ErrNoServer

	var jqerr Error
	if errors.As(pingErr, &jqerr) && jqerr.Err == ErrPermissionDenied {
		msg = ErrPermissionDenied
	}

	return nil, Error{"Connect", "", msg}
}

// ConnectUsingConfig calls Connect(), supplying values from user configuration
// available in the environment (config files and environment variables). To
// load the correct config, a deployment must be provided ('production' or
// 'development', whichever was used when starting the server).
func ConnectUsingConfig(ctx context.Context, deployment string, timeout time.Duration) (*Client, error) {
	config := internal.ConfigLoadFromCurrentDir(ctx, deployment)

	token, err := os.ReadFile(filepath.Clean(config.ManagerTokenFile))
	if err != nil {
		return nil, fmt.Errorf("could not read token file; has the manager been started? [%w]", err)
	}

	return Connect(config.ManagerHost+":"+config.ManagerPort, config.ManagerCAFile,
		config.ManagerCertDomain, token, timeout)
}

// Disconnect closes the connection to the jobqueue server. It is CRITICAL that
// you call Disconnect() before calling Connect() again in the same process.
func (c *Client) Disconnect() error {
	c.Lock()
	defer c.Unlock()

	if c.restClient != nil {
		c.restClient.CloseIdleConnections()
	}

	return c.sock.Close()
}

// Ping tells you if your connection to the server is working, returning static
// information about the server. If err is nil, it works. This is the only
// command that interacts with the server that works if a blank or invalid
// token had been supplied to Connect().
//
// timeout bounds how long we wait for the server's reply, so that a ping into a
// manager that still listens but no longer reads (the window during shutdown
// between its RPC readers stopping and its command socket closing) fails within
// the caller's own budget instead of on the socket's ClientMinRequestTimeout
// floor. requestWithin can only narrow, so a ping on a socket whose deadline is
// already shorter (Connect's readiness ping) is unaffected.
func (c *Client) Ping(timeout time.Duration) (*ServerInfo, error) {
	resp, err := c.requestWithin(&clientRequest{Method: "ping", Timeout: timeout}, timeout)
	if err != nil {
		return nil, err
	}

	return resp.SInfo, err
}

// DrainServer tells the server to stop spawning new runners, stop letting
// existing runners reserve new jobs, and exit once existing runners stop
// running. You get back a count of existing runners and an estimated time
// until completion for the last of those runners.
func (c *Client) DrainServer() (running int, etc time.Duration, err error) {
	return c.drainOrPauseServer("drain")
}

// drainOrPauseServer handles the response from drain or pause.
func (c *Client) drainOrPauseServer(method string) (running int, etc time.Duration, err error) {
	resp, err := c.request(&clientRequest{Method: method})
	if err != nil {
		return running, etc, err
	}

	s := resp.SStats
	running = s.Running
	etc = s.ETC

	return running, etc, err
}

// PauseServer tells the server to stop spawning new runners and stop letting
// existing runners reserve new jobs. (It is like DrainServer(), without
// stopping the server). You get back a count of existing runners and an
// estimated time until completion for the last of those runners.
func (c *Client) PauseServer() (running int, etc time.Duration, err error) {
	return c.drainOrPauseServer("pause")
}

// ResumeServer tells the server to start spawning new runners and start letting
// existing runners reserve new jobs. Use this after a PauseServer() call to
// resume normal operation.
func (c *Client) ResumeServer() error {
	_, err := c.request(&clientRequest{Method: "resume"})

	return err
}

// ShutdownServer tells the server to immediately cease all operations. Its last
// act will be to backup its internal database. Any existing runners will fail.
// Because the server gets shut down it can't respond with success/failure, so
// we indirectly report if the server was shut down successfully.
func (c *Client) ShutdownServer() bool {
	_, err := c.request(&clientRequest{Method: "shutdown"})
	if err != nil {
		return false
	}

	// wait a while for the server to stop responding to Pings. The deadline is
	// re-checked every pass rather than raced against in a select, so that a
	// ping's own duration cannot carry us past ClientShutdownTimeout.
	deadline := time.Now().Add(ClientShutdownTimeout)
	ticker := time.NewTicker(ClientShutdownTestInterval)

	defer ticker.Stop()

	for time.Now().Before(deadline) {
		<-ticker.C

		if _, err = c.Ping(ClientSuggestedPingTimeout); err != nil {
			return true
		}
	}

	return false
}

// BackupDB backs up the server's database to the given path. Note that
// automatic backups occur to the configured location without calling this.
func (c *Client) BackupDB(path string) error {
	resp, err := c.request(&clientRequest{Method: "backup"})
	if err != nil {
		return err
	}

	tmpPath := path + ".tmp"

	err = os.WriteFile(tmpPath, resp.DB, dbFilePermission)
	if err != nil {
		rerr := os.Remove(tmpPath)
		if rerr != nil {
			err = fmt.Errorf("%w\n%w", err, rerr)
		}

		return err
	}

	return os.Rename(tmpPath, path)
}

// Add adds new jobs to the job queue, but only if those jobs aren't already in
// there.
//
// If any were already there, you will not get an error, but the returned
// 'existed' count will be > 0. Note that no cross-queue checking is done, so
// you need to be careful not to add the same job to different queues.
//
// Note that if you add jobs to the queue that were previously added, Execute()d
// and were successfully Archive()d, the existed count will be 0 and the jobs
// will be treated like new ones, though when Archive()d again, the new Job will
// replace the old one in the database. To have such jobs skipped as "existed"
// instead, supply ignoreComplete as true.
//
// The envVars argument is a slice of ("key=value") strings with the environment
// variables you want to be set when the job's Cmd actually runs. Typically you
// would pass in os.Environ().
func (c *Client) Add(jobs []*Job, envVars []string, ignoreComplete bool) (added, existed int, err error) {
	added, existed, _, err = c.AddWithWarnings(jobs, envVars, ignoreComplete)

	return added, existed, err
}

// AddWithWarnings is like Add, and also returns non-fatal warnings about the
// added jobs.
func (c *Client) AddWithWarnings(
	jobs []*Job,
	envVars []string,
	ignoreComplete bool,
) (added int, existed int, warnings AddWarnings, err error) {
	compressed, err := c.CompressEnv(envVars)
	if err != nil {
		return 0, 0, AddWarnings{}, err
	}

	resp, err := c.request(&clientRequest{Method: "add", Jobs: jobs, Env: compressed, IgnoreComplete: ignoreComplete})
	if err != nil {
		return 0, 0, AddWarnings{}, err
	}

	return resp.Added, resp.Existed, resp.AddWarnings, err
}

// AddAndReturnIDs is like Add(), except that the internal IDs of jobs that are
// now in the queue are returned (including dups, excluding complete jobs). This
// is potentially expensive, so use Add() if you don't need these.
func (c *Client) AddAndReturnIDs(jobs []*Job, envVars []string, ignoreComplete bool) ([]string, error) {
	ids, _, err := c.AddAndReturnIDsWithWarnings(jobs, envVars, ignoreComplete)

	return ids, err
}

// AddAndReturnIDsWithWarnings is like AddAndReturnIDs, and also returns
// non-fatal warnings about the added jobs.
func (c *Client) AddAndReturnIDsWithWarnings(
	jobs []*Job,
	envVars []string,
	ignoreComplete bool,
) (ids []string, warnings AddWarnings, err error) {
	compressed, err := c.CompressEnv(envVars)
	if err != nil {
		return nil, AddWarnings{}, err
	}

	resp, err := c.request(&clientRequest{
		Method: "add", Jobs: jobs, Env: compressed, IgnoreComplete: ignoreComplete, ReturnIDs: true,
	})
	if err != nil {
		return nil, AddWarnings{}, err
	}

	return resp.AddedIDs, resp.AddWarnings, err
}

// Modify modifies previously Add()ed jobs that are incomplete and not currently
// running.
//
// The first argument lets you choose which jobs to modify. The second argument
// lets you define what you want to change in them all. If you want to change
// the actual command line of a job, you can only modify 1 job (and you can't
// change it to match another job in the queue or that has completed; those
// requests will be silently ignored).
//
// For each modified job, returns a mapping of new internal job id to the old
// internal job id (which will typically be the same, unless something critical
// like the command line was changed).
func (c *Client) Modify(jes []*JobEssence, modifier *JobModifier) (modified map[string]string, err error) {
	if validationErr, invalid := modifier.validationError(); invalid {
		return nil, validationErr
	}

	keys := c.jesToKeys(jes)

	resp, err := c.request(&clientRequest{Method: requestMethodModify, Keys: keys, Modifier: modifier})
	if err != nil {
		return nil, err
	}

	return resp.Modified, err
}

// Reserve takes a job off the jobqueue. If you process the job successfully you
// should Archive() it. If you can't deal with it right now you should Release()
// it. If you think it can never be dealt with you should Bury() it. If you die
// unexpectedly, the job will automatically be released back to the queue after
// some time.
//
// If no job was available in the queue for as long as the timeout argument, nil
// is returned for both job and error. If your timeout is 0, you will wait
// indefinitely for a job.
//
// NB: if your jobs have schedulerGroups (and they will if you added them to a
// server configured with a RunnerCmd), this will most likely not return any
// jobs; use ReserveScheduled() instead.
func (c *Client) Reserve(timeout time.Duration) (*Job, error) {
	fr := false
	if !c.hasReserved {
		fr = true
		c.hasReserved = true
	}

	host, pid := reserveHostAndPid()

	resp, err := c.request(&clientRequest{
		Method: requestMethodReserve, Timeout: timeout, FirstReserve: fr, Host: host, Pid: pid,
		SchedulerID: c.reserveSchedulerID,
	})
	if err != nil {
		return nil, err
	}

	return resp.Job, err
}

// ReserveScheduled is like Reserve(), except that it will only return jobs from
// the specified schedulerGroup.
//
// Based on the scheduler the server was configured with, it will group jobs
// based on their resource requirements and then submit runners to handle them
// to your system's job scheduler (such as LSF), possibly in different scheduler
// queues. These runners are told the group they are a part of, and that same
// group name is applied internally to the Jobs as the "schedulerGroup", so that
// the runners can reserve only Jobs that they're supposed to. Therefore, it
// does not make sense for you to call this yourself; it is only for use by
// runners spawned by the server.
func (c *Client) ReserveScheduled(timeout time.Duration, schedulerGroup string) (*Job, error) {
	fr := false
	if !c.hasReserved {
		fr = true
		c.hasReserved = true
	}

	host, pid := reserveHostAndPid()

	resp, err := c.request(&clientRequest{
		Method: requestMethodReserve, Timeout: timeout, SchedulerGroup: schedulerGroup, FirstReserve: fr,
		Host: host, Pid: pid, SchedulerID: c.reserveSchedulerID,
	})
	if err != nil {
		return nil, err
	}

	return resp.Job, err
}

// executeCmd bundles the command to run with the plumbing used to capture and
// live-tail its STDOUT and STDERR.
type executeCmd struct {
	cmd                  *exec.Cmd
	errReader, outReader io.ReadCloser
	stderr, stdout       *prefixSuffixSaver
	liveStderr           *liveTailSaver
	liveStdout           *liveTailSaver
	stderrWait           <-chan error
	stdoutWait           <-chan error
}

// buildExecCmd builds the exec.Cmd that will run the job's command line jc,
// prefixing module loading and pipefail handling as needed and running under
// newgrp if the job has a Group.
func buildExecCmd(ctx context.Context, job *Job, shell, jc string) *exec.Cmd {
	if len(job.Modules) > 0 {
		jc = "module load --force " + shellquote.Join(job.Modules...) + "; " + jc
	}

	// we support arbitrary shell commands that may include semi-colons,
	// quoted stuff and pipes, so it's best if we just pass it to bash
	if strings.Contains(jc, " | ") {
		jc = "set -o pipefail; " + jc
	}

	if job.Group != "" {
		//nolint:gosec // our whole purpose is to run user-supplied commands.
		cmd := exec.CommandContext(ctx, "newgrp", job.Group)
		cmd.Stdin = strings.NewReader(jc)

		return cmd
	}

	return exec.CommandContext(ctx, shell, "-c", jc)
}

// prepareCommand builds the exec.Cmd for the job's (possibly module/pipefail
// adjusted) command line jc, wiring up STDOUT/STDERR capture and live-tailing.
func prepareCommand(ctx context.Context, job *Job, shell, jc string) (*executeCmd, error) {
	cmd := buildExecCmd(ctx, job, shell, jc)

	// we'll filter STDERR/OUT of the cmd to keep only the first and last line
	// of any contiguous block of \r terminated lines (to mostly eliminate
	// progress bars), and we'll store only up to stdSaverBytes of head and tail
	ec := &executeCmd{
		cmd:        cmd,
		stderr:     &prefixSuffixSaver{N: stdSaverBytes},
		stdout:     &prefixSuffixSaver{N: stdSaverBytes},
		liveStderr: &liveTailSaver{},
		liveStdout: &liveTailSaver{},
	}

	var err error

	ec.errReader, err = cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create a pipe for STDERR from cmd [%s]: %w", jc, err)
	}

	ec.outReader, err = cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create a pipe for STDOUT from cmd [%s]: %w", jc, err)
	}

	ec.stderrWait = stdFilter(ec.errReader, io.MultiWriter(ec.stderr, ec.liveStderr))
	ec.stdoutWait = stdFilter(ec.outReader, io.MultiWriter(ec.stdout, ec.liveStdout))

	return ec, nil
}

// dockerMonitor holds the docker client plumbing used to monitor a job's
// container, and the id of the container once it has been identified.
type dockerMonitor struct {
	operator        *container.Operator
	interactor      *docker.Interactor
	monitorDocker   string
	getFirstAppears bool
	containerID     string
}

// setupDockerMonitor creates the docker client used to monitor the job's
// container if the job requests docker monitoring, returning nil if it does not.
// On failure the job is buried and an error returned.
func (c *Client) setupDockerMonitor(ctx context.Context, job *Job) (*dockerMonitor, error) {
	if job.MonitorDocker == "" {
		return nil, nil //nolint:nilnil // no docker monitoring requested is a valid, non-error result.
	}

	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, c.buryWithCause(job, FailReasonDocker, err, "failed to create docker client")
	}

	interactor := docker.NewInteractor(cli)
	dm := &dockerMonitor{
		operator:      container.NewOperator(interactor),
		interactor:    interactor,
		monitorDocker: job.MonitorDocker,
	}

	// if we've been asked to monitor the first container that appears, remember
	// existing containers
	if job.MonitorDocker == "?" {
		dm.getFirstAppears = true

		if errc := dm.operator.RememberCurrentContainers(ctx); errc != nil {
			return nil, c.buryWithCause(job, FailReasonDocker, errc, "failed to get docker containers")
		}
	}

	return dm, nil
}

// resolveContainerMem looks up (and caches) the monitored container's id and, if
// found, returns the larger of mem and the container's memory plus its CPU
// seconds. Any error finding the container is returned for accumulation.
func (dm *dockerMonitor) resolveContainerMem(ctx context.Context, cmdDir string, mem int) (int, int, error) {
	if dm == nil {
		return mem, 0, nil
	}

	var findErr error

	if dm.containerID == "" {
		findErr = dm.findContainerID(ctx, cmdDir)
	}

	if dm.containerID == "" {
		return mem, 0, findErr
	}

	dockerStats, errs := dm.interactor.ContainerStats(ctx, dm.containerID)
	if errs != nil {
		return mem, 0, findErr
	}

	if dockerStats.MemoryMB > mem {
		mem = dockerStats.MemoryMB
	}

	return mem, dockerStats.CPUSec, findErr
}

// findContainerID tries to identify the container to monitor, caching its id on
// success.
func (dm *dockerMonitor) findContainerID(ctx context.Context, cmdDir string) error {
	if dm.getFirstAppears {
		containers, err := dm.operator.GetNewContainers(ctx)
		if len(containers) > 0 {
			dm.containerID = containers[0].ID
		}

		return err
	}

	// monitorDocker might be the name of a new container
	dockerContainer, err := dm.operator.GetNewContainerByName(ctx, dm.monitorDocker)
	if dockerContainer != nil {
		dm.containerID = dockerContainer.ID

		return err
	}

	// monitorDocker might be a file path containing the id of a container
	dockerContainer, err = dm.operator.GetContainerByPath(ctx, dm.monitorDocker, cmdDir)
	if dockerContainer != nil {
		dm.containerID = dockerContainer.ID
	}

	return err
}

// addBsubEnv augments env with the PATH override and LSF emulation variables a
// bsub-mode job needs, so child jobs created via the bsub symlink can find the
// manager and inherit cloud/mount options.
func (c *Client) addBsubEnv(env []string, job *Job, prependPath, host, shell string) ([]string, error) {
	env = envOverride(env, []string{prependedPath(env, prependPath)})

	jobJSON, err := c.bsubConfigJSON(job, host)
	if err != nil {
		return nil, err
	}

	return envOverride(env, []string{
		"WR_BSUB_CONFIG=" + string(jobJSON),
		"WR_MANAGER_HOST=" + c.host,
		"WR_MANAGER_PORT=" + c.port,
		"LSF_SERVERDIR=/dev/null",
		"LSF_LIBDIR=/dev/null",
		"LSF_ENVDIR=/dev/null",
		"LSF_BINDIR=" + prependPath,
		"SHELL=" + shell,
	}), nil
}

// prependedPath returns a "PATH=" override that puts prependPath before the
// existing PATH value found in env (if any).
func prependedPath(env []string, prependPath string) string {
	for _, envvar := range env {
		pair := strings.Split(envvar, "=")
		if pair[0] == "PATH" {
			return "PATH=" + prependPath + ":" + pair[1]
		}
	}

	return "PATH=" + prependPath
}

// bsubConfigJSON marshals a simplified copy of job (carrying the details child
// jobs need) to JSON, burying the job and returning an error on failure.
func (c *Client) bsubConfigJSON(job *Job, host string) ([]byte, error) {
	// child jobs created via our bsub symlink need our requirements, deployment
	// (BsubMode) and host (so they know any mounts are expected to fail if they
	// run on the same host as us)
	simplified := &Job{
		Requirements: job.Requirements,
		BsubMode:     job.BsubMode,
		Host:         host,
	}
	if _, exists := job.Requirements.Other["cloud_shared"]; !exists {
		simplified.MountConfigs = job.MountConfigs
	}

	jobJSON, errm := json.Marshal(simplified)
	if errm != nil {
		errb := c.Bury(job, nil, fmt.Sprintf("could not convert job to JSON: %s", errm))

		extra := ""
		if errb != nil {
			extra = fmt.Sprintf(" (and burying the job failed: %s)", errb)
		}

		return nil, fmt.Errorf("could not convert job to JSON: %w%s", errm, extra)
	}

	return jobJSON, nil
}

// resolveWorkingDir sets cmd.Dir to the directory the command should run in. If
// the job's Cwd matters it is used directly and ("", "", nil) is returned;
// otherwise a unique hashed working directory is created and its cwd and tmp dir
// are returned (the job is buried and an error returned on failure).
func (c *Client) resolveWorkingDir(job *Job, cmd *exec.Cmd) (actualCwd, tmpDir string, err error) {
	if job.CwdMatters {
		cmd.Dir = job.Cwd

		return "", "", nil
	}

	// we'll create a unique location to work in
	actualCwd, tmpDir, err = mkHashedDir(job.Cwd, job.Key())
	if err != nil {
		buryErr := fmt.Errorf("could not create working directory: %w", err)

		errb := c.Bury(job, nil, FailReasonCwd, buryErr)
		if errb != nil {
			buryErr = fmt.Errorf("%w (and burying the job failed: %w)", buryErr, errb)
		}

		return "", "", buryErr
	}

	cmd.Dir = actualCwd

	job.Lock()
	job.setActualCwd(actualCwd)
	job.Unlock()

	return actualCwd, tmpDir, nil
}

// ensureMachineRAM returns machineRAM if it is already known (non-zero),
// otherwise it tries to read the machine's total RAM in MB, falling back to the
// supplied (zero) value if that fails.
func ensureMachineRAM(machineRAM int) int {
	if machineRAM != 0 {
		return machineRAM
	}

	if ram, err := internal.ProcMeminfoMBs(); err == nil {
		return ram
	}

	return machineRAM
}

// peakMemNeedsKill reports whether peakmem has reached the configured kill
// fraction of the machine's RAM, meaning the command should be killed to protect
// the machine.
func (c *Client) peakMemNeedsKill(peakmem, machineRAM int) bool {
	return machineRAM > 0 && peakmem >= ((machineRAM/percentDivisor)*c.percentMemoryKill)
}

// recoveryExtra returns a " (and <action> failed: <err>)" suffix when err is
// non-nil, used to annotate a primary error with a failed recovery step.
func recoveryExtra(action string, err error) string {
	if err == nil {
		return ""
	}

	return fmt.Sprintf(" (and %s failed: %s)", action, err)
}

// unmountExtra attempts to unmount the job and returns a suffix describing any
// failure to do so.
func unmountExtra(job *Job) string {
	_, err := job.Unmount(true)

	return recoveryExtra("unmounting the job", err)
}

// joinExecErr accumulates errors encountered during Execute's cleanup: if newErr
// is nil it returns existing unchanged; if existing is nil it returns newErr;
// otherwise it wraps newErr onto existing using context.
func joinExecErr(existing, newErr error, context string) error {
	if newErr == nil {
		return existing
	}

	if existing == nil {
		return newErr
	}

	return fmt.Errorf("%w (and %s: %w)", existing, context, newErr)
}

// appendExecErr is like joinExecErr but joins with a semicolon, matching the
// post-execution error messages ("<existing>; <phrase>: <newErr>").
func appendExecErr(existing, newErr error, phrase string) error {
	if newErr == nil {
		return existing
	}

	if existing == nil {
		return newErr
	}

	return fmt.Errorf("%w; %s: %w", existing, phrase, newErr)
}

// mountedDirsToSkip decides, for a job with a unique working directory
// (actualCwd) and a set of mounted directories, which directories should be
// skipped when measuring disk usage and whether actualCwd itself should be
// checked. It returns any error from closing the directories it inspected.
func mountedDirsToSkip(uniqueMountedDirs []string, actualCwd, cmdDir string) (map[string]bool, bool, error) {
	dontCheckDirs := make(map[string]bool)

	if actualCwd == "" {
		return dontCheckDirs, false, nil
	}

	if len(uniqueMountedDirs) == 0 {
		return dontCheckDirs, true, nil
	}

	var closeErr error

	for _, dir := range uniqueMountedDirs {
		if dirIsEmpty(dir, &closeErr) {
			continue
		}

		if dir == cmdDir {
			// a mounted dir is the working dir itself, so don't check it.
			return dontCheckDirs, false, closeErr
		}

		if strings.HasPrefix(dir, actualCwd) {
			dontCheckDirs[dir] = true
		}
	}

	return dontCheckDirs, true, closeErr
}

// dirIsEmpty reports whether dir could be opened and is empty, accumulating any
// error from closing it into closeErr.
func dirIsEmpty(dir string, closeErr *error) bool {
	d, erro := os.Open(filepath.Clean(dir))
	if erro != nil {
		return false
	}

	files, errr := d.Readdir(1)
	*closeErr = joinExecErr(*closeErr, d.Close(), "closing dir failed")

	return (errr == nil || errors.Is(errr, io.EOF)) && len(files) == 0
}

// ensureCwdExists makes sure the job's working directory exists, creating it if
// necessary, burying the job and returning an error if it cannot be created.
func (c *Client) ensureCwdExists(job *Job) error {
	if fi, errf := os.Stat(filepath.Clean(job.Cwd)); errf == nil && fi.Mode().IsDir() {
		return nil
	}

	errm := os.MkdirAll(filepath.Clean(job.Cwd), os.ModePerm)

	if _, errs := os.Stat(filepath.Clean(job.Cwd)); errs != nil {
		errb := c.Bury(job, nil, FailReasonCwd)

		extra := ""
		if errb != nil {
			extra = fmt.Sprintf(" (and burying the job failed: %s)", errb)
		}

		return fmt.Errorf("working directory [%s] does not exist%s: %w", job.Cwd, extra, errm)
	}

	return nil
}

// Execute runs the given Job's Cmd and blocks until it exits. Then any Job
// Behaviours get triggered as appropriate for the exit status.
//
// The Cmd is run using the environment variables set when the Job was Add()ed,
// or the current environment is used if none were set.
//
// The Cmd is also run within the Job's Cwd. If CwdMatters is false, a unique
// subdirectory is created within Cwd, and that is used as the actual working
// directory. When creating these unique subdirectories, directory hashing is
// used to allow the safe running of 100s of thousands of Jobs all using the
// same Cwd (that is, we will not break the directory listing of Cwd).
// Furthermore, a sister folder will be created in the unique location for this
// Job, the path to which will become the value of the TMPDIR environment
// variable. Once the Cmd exits, this temp directory will be deleted and the
// path to the actual working directory created will be in the Job's ActualCwd
// property. The unique folder structure itself can be wholly deleted through
// the Job behaviour "cleanup".
//
// If any environment modules were set when the Job was Add()ed, they are force
// loaded before execution of the Cmd.
//
// If any remote file system mounts have been configured for the Job, these are
// mounted prior to running the Cmd, and unmounted afterwards.
//
// If WithDocker or WithSingularity has been set, the Cmd is run within the
// corresponding container image, with any additional ContainerMounts mounted.
//
// Internally, Execute() calls Mount() and Started() and keeps track of peak RAM
// and disk used. It regularly calls Touch() on the Job so that the server knows
// we are still alive and handling the Job successfully. It also intercepts
// SIGTERM, SIGINT, SIGQUIT, SIGUSR1 and SIGUSR2, sending SIGKILL to the running
// Cmd and returning Error.Err(FailReasonSignal); you should check for this and
// exit your process. Finally it calls Unmount() and TriggerBehaviours().
//
// If Kill() is called while executing the Cmd, the next internal Touch() call
// will result in the Cmd being killed and the job being Bury()ied.
//
// If no error is returned, the Cmd will have run OK, exited with status 0, and
// been Archive()d from the queue while being placed in the permanent store.
// Otherwise, it will have been Release()d or Bury()ied as appropriate.
//
// The supplied shell is the shell to execute the Cmd under, ideally bash
// (something that understands the command "set -o pipefail").
//
// You have to have been the one to Reserve() the supplied Job, or this will
// immediately return an error. NB: the peak RAM tracking assumes we are running
// on a modern linux system with /proc/*/smaps.
//
// The bulk of the per-phase work (command setup, working dir, mounts, env,
// docker monitoring, outcome classification and the final state reporting) has
// been extracted into helpers. What remains here is the orchestration of two
// long-lived goroutines (the touch loop and the resource-monitor loop) that
// share mutable state under wkbsMutex/stateMutex and a set of channels, plus the
// deferred cleanups that must run in this scope. Splitting that choreography
// across methods would obscure it and risk a concurrency regression in this
// timing-sensitive path, so its residual gocognit/nestif are tolerated here.
//
//nolint:gocognit,nestif,gocyclo,cyclop,funlen,maintidx // see note above: irreducible goroutine/cleanup orchestration.
func (c *Client) Execute(ctx context.Context, job *Job, shell string) error {
	ctx = clog.ContextWithJobKey(ctx, job.Key())
	// quickly check upfront that we Reserve()d the job; this isn't required
	// for other methods since the server does this check and returns an error,
	// but in this case we want to avoid starting to execute the command before
	// finding out about this problem
	if c.clientid != job.ReservedBy {
		return Error{clientOpExecute, job.Key(), ErrMustReserve}
	}

	// we have a convienience feature that can run Cmd in a container, so get
	// possibly modified Cmd
	jc, cmdLineCleanup, err := job.CmdLine(ctx)
	if err != nil {
		return fmt.Errorf("failed to set up cmd file: %w", err)
	}
	defer cmdLineCleanup()

	ec, err := prepareCommand(ctx, job, shell, jc)
	if err != nil {
		return err
	}

	cmd := ec.cmd
	errReader, outReader := ec.errReader, ec.outReader
	stderr, stdout := ec.stderr, ec.stdout
	liveStderr, liveStdout := ec.liveStderr, ec.liveStdout
	stderrWait, stdoutWait := ec.stderrWait, ec.stdoutWait

	if err = c.ensureCwdExists(job); err != nil {
		return err
	}

	var dirsToCheckDiskSpace []string

	actualCwd, tmpDir, err := c.resolveWorkingDir(job, cmd)
	if err != nil {
		return err
	}

	if tmpDir != "" {
		dirsToCheckDiskSpace = append(dirsToCheckDiskSpace, tmpDir)
	}

	// before doing any other pre-start tasks, which might take time, start
	// touching the job, and keep doing so until after we've run the job and
	// carried out post-exit tasks
	liveState := newExecuteLiveState(actualCwd, liveStdout, liveStderr)
	touchTicker := time.NewTicker(c.touchInterval) // server-provided default (< its ItemTTR), overridable per client

	var wkbsMutex sync.RWMutex

	serverContact := &serverContactState{}
	killDoneCh := make(chan bool, 1)
	whenKilledByServer := func() {
		killDoneCh <- true
	}
	stopTouching := make(chan bool, executeStopChannelBuffer)
	stopChecking := make(chan bool, executeStopChannelBuffer)

	go func() {
		for {
			select {
			case <-touchTicker.C:
				kc, errf := c.touch(job, liveState.snapshot())
				if kc {
					wkbsMutex.RLock()
					defer wkbsMutex.RUnlock()

					whenKilledByServer()
					touchTicker.Stop()
					clog.Warn(ctx, "kill requested externally")

					stopChecking <- true

					return
				}

				if errf != nil {
					// we may have lost contact with the manager; this is OK. We
					// will keep trying to touch until it works
					serverContact.recordTouchResult(errf)
					clog.Warn(ctx, "could not touch", "err", errf)

					continue
				}

				serverContact.recordTouchResult(nil)
			case <-stopTouching:
				touchTicker.Stop()

				return
			}
		}
	}()

	defer func() {
		stopTouching <- true
	}()

	var myerr error

	var (
		onCwd       bool
		prependPath string
	)
	if job.BsubMode != "" {
		// create our bsub symlinks in a tmp dir
		prependPath, err = os.MkdirTemp("", lsfEmulationDir)
		if err != nil {
			stopTouching <- true

			return c.buryWithCause(job, FailReasonCwd, err, "could not create lsf emulation directory")
		}
		defer func() {
			myerr = joinExecErr(myerr, os.RemoveAll(prependPath), "removing the lsf emulation dir failed")
		}()

		err = c.createLSFSymlinks(prependPath, job)
		if err != nil {
			return err
		}

		onCwd = job.CwdMatters
	}

	// if we are a child job of another running on the same host, we expect
	// mounting to fail since we're running in the same directory as our
	// parent
	var mountCouldFail bool

	host, err := os.Hostname()
	if err != nil {
		host = localhost
	}

	if jsonStr := job.Getenv("WR_BSUB_CONFIG"); jsonStr != "" {
		configJob := &Job{}
		if erru := json.Unmarshal([]byte(jsonStr), configJob); erru == nil && configJob.Host == host {
			mountCouldFail = true
			// *** but the problem with this is, the parent job could finish
			// while we're still running, and unmount!...
		}
	}

	// we'll mount any configured remote file systems
	uniqueCacheDirs, uniqueMountedDirs, err := job.Mount(onCwd)
	if err != nil && !mountCouldFail {
		if strings.Contains(err.Error(), "fusermount exited with code 256") {
			// *** not sure what causes this, but perhaps trying again after a
			// few seconds will help?
			<-time.After(fusermountRetryDelaySeconds * time.Second)

			uniqueCacheDirs, uniqueMountedDirs, err = job.Mount()
		}

		if err != nil {
			stopTouching <- true

			buryErr := fmt.Errorf("failed to mount remote file system(s): %w (%s)", err, os.Environ())

			return c.buryWrapErr(job, FailReasonMount, buryErr)
		}
	}

	// later, check mount cache dirs for disk usage
	if len(uniqueCacheDirs) > 0 {
		dirsToCheckDiskSpace = append(dirsToCheckDiskSpace, uniqueCacheDirs...)
	}

	// later, check unmounted parts of unique cwd for disk usage, or mounted
	// parts that start off empty
	dontCheckDirs, addCwd, dirCloseErr := mountedDirsToSkip(uniqueMountedDirs, actualCwd, cmd.Dir)
	myerr = joinExecErr(myerr, dirCloseErr, "closing dir failed")

	if addCwd {
		dirsToCheckDiskSpace = append(dirsToCheckDiskSpace, actualCwd)
	}

	// and we'll run it with the environment variables that were present when
	// the command was first added to the queue (or if none, current env vars,
	// and in either case, including any overrides) *** we need a way for users
	// to update a job with new env vars
	env, err := job.Env()
	if err != nil {
		stopTouching <- true

		extra := recoveryExtra("burying the job", c.Bury(job, nil, FailReasonEnv))
		extra += unmountExtra(job)

		return fmt.Errorf("failed to extract environment variables for job [%s]: %w%s", job.Key(), err, extra)
	}

	if tmpDir != "" {
		// (this works fine even if tmpDir has a space in one of the dir names)
		env = envOverride(env, []string{"TMPDIR=" + tmpDir})
		defer func() {
			myerr = joinExecErr(myerr, os.RemoveAll(filepath.Clean(tmpDir)), "removing the tmpdir failed")
		}()

		if job.ChangeHome {
			env = envOverride(env, []string{"HOME=" + actualCwd})
		}
	}

	if prependPath != "" {
		env, err = c.addBsubEnv(env, job, prependPath, host, shell)
		if err != nil {
			return err
		}
	}

	cmd.Env = env

	// if docker monitoring has been requested, try and get the docker client
	// now and fail early if we can't
	dm, err := c.setupDockerMonitor(ctx, job)
	if err != nil {
		stopTouching <- true

		return err
	}

	// intercept certain signals (under LSF and SGE, SIGUSR2 may mean out-of-
	// time, but there's no reliable way of knowing out-of-memory, so we will
	// just treat them all the same)
	sigs := make(chan os.Signal, executeSignalChannelBuffer)

	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2)
	defer signal.Stop(sigs)

	// start running the command
	endT := time.Now().Add(job.Requirements.Time)

	err = cmd.Start()
	if err != nil {
		// some obscure internal error about setting things up
		stopTouching <- true

		extra := recoveryExtra("releasing the job", c.Release(job, nil, FailReasonStart))
		extra += unmountExtra(job)

		return fmt.Errorf("could not start command [%s]: %w%s", jc, err, extra)
	}

	clog.Info(ctx, "started executing", "cmd", job.Cmd, "pid", cmd.Process.Pid)

	var oomMonitor *cgroupOOMMonitor

	monitor, errm := newCgroupOOMMonitor(cmd.Process.Pid, procRoot, cgroupRoot)
	if errm == nil {
		oomMonitor = monitor
	}

	// update the server that we've started the job
	//nolint:contextcheck // transitively calls internal.CurrentIP, a self-contained local-IP lookup with its own context
	err = c.Started(job, cmd.Process.Pid)
	if err != nil {
		// if we can't access the server, may as well bail out now - kill the
		// command (and don't bother trying to Release(); it will auto-Release)
		extra := recoveryExtra("killing the cmd", cmd.Process.Kill())
		//nolint:contextcheck // behaviours run detached from the cancellable job context
		extra += recoveryExtra("triggering behaviours", job.TriggerBehaviours(false))
		extra += unmountExtra(job)

		return fmt.Errorf("command [%s] started running, but I killed it due to a jobqueue server error: %w%s",
			job.Cmd, err, extra)
	}

	// update peak mem and disk used by command, and check if we use too much
	// resources, every second. Also check for signals
	peakmem := 0

	var peakdisk int64

	dockerCPU := 0
	resourceTicker := time.NewTicker(1 * time.Second)
	machineRAM := 0
	exceededMemEstimate := false
	killedForMem := false
	ranoutTime := false
	ranoutDisk := false
	signalled := false
	killCalled := false

	var (
		killErr    error
		closeErr   error
		stateMutex sync.Mutex
	)

	diskUsageCheck := func() (int64, error) {
		var used int64

		for _, dir := range dirsToCheckDiskSpace {
			var (
				thisUsed int64
				thisErr  error
			)
			if dir == actualCwd {
				thisUsed, thisErr = currentDisk(dir, dontCheckDirs)
			} else {
				thisUsed, thisErr = currentDisk(dir)
			}

			if thisErr != nil {
				return 0, thisErr
			}

			used += thisUsed
		}

		return used, nil
	}
	finishedChecking := make(chan bool)

	go func() {
		killCmd := func() error {
			// get children first
			children, errc := getChildProcesses(int32(cmd.Process.Pid)) //nolint:gosec // an OS pid always fits in an int32.

			// then kill *** race condition if cmd spawns more children...
			errk := cmd.Process.Kill()

			if errc != nil {
				if errk == nil {
					clog.Info(ctx, "killed cmd", "cmd", job.Cmd, "pid", cmd.Process.Pid)

					errk = errc
				} else {
					clog.Warn(ctx, "failed to kill cmd", "cmd", job.Cmd, "pid", cmd.Process.Pid, "err", errk)
					errk = fmt.Errorf("%w, and getting child processes failed: %w", errk, errc)
				}
			}

			if dm != nil && dm.containerID != "" {
				// kill the docker container as well
				errd := dm.operator.KillContainer(ctx, dm.containerID)
				if errk == nil {
					errk = errd
				} else {
					errk = fmt.Errorf("%w, and killing the docker container failed: %w", errk, errd)
				}
			}

			var wg sync.WaitGroup

			wg.Add(len(children))

			for _, child := range children {
				// try and kill any children in case the above didn't already
				// result in their death
				errc = child.Terminate()
				if errk == nil {
					clog.Info(ctx, "killed child of cmd", "cmd", job.Cmd, "pid", child.Pid)

					errk = errc
				} else {
					clog.Warn(ctx, "failed to kill child of cmd", "cmd", job.Cmd, "pid", child.Pid)

					errk = fmt.Errorf("%w, and killing its child process failed: %w", errk, errc)
				}

				go func(child *process.Process) {
					time.Sleep(terminateGrace)
					child.Kill() //nolint:errcheck
					wg.Done()
				}(child)
			}

			wg.Wait()

			return errk
		}

		closeReaders := func() {
			errc := errReader.Close()
			if errc != nil {
				closeErr = errc
			}

			errc = outReader.Close()
			if errc != nil {
				closeErr = errc
			}
		}

		wkbsMutex.Lock()
		whenKilledByServer = func() {
			stateMutex.Lock()
			killCalled = true
			stateMutex.Unlock()

			killErr = killCmd()

			killDoneCh <- true
		}
		wkbsMutex.Unlock()

		volume := local.NewVolume(job.Cwd)

	CHECKING:
		for {
			select {
			case signal := <-sigs:
				clog.Warn(ctx, "aborting due to signal", "sig", signal.String())

				killErr = killCmd()

				stateMutex.Lock()
				if time.Now().After(endT) {
					// we allow things to go over time, but if signalled, we now
					// know it may be because we used too much time
					ranoutTime = true
				}

				signalled = true
				stateMutex.Unlock()
				closeReaders()

				break CHECKING
			case <-resourceTicker.C:
				// always see if we've run out of disk space on the machine, in
				// which case abort
				if volume.NoSpaceLeft(ctx) {
					clog.Warn(ctx, "aborting due to lack of disk space")

					killErr = killCmd()

					stateMutex.Lock()
					ranoutDisk = true
					stateMutex.Unlock()
					closeReaders()

					break CHECKING
				}

				// get current memory usage
				mem, errf := currentMemory(job.Pid)

				// deal with docker monitoring
				mem, cpuS, findErr := dm.resolveContainerMem(ctx, cmd.Dir, mem)
				myerr = joinExecErr(myerr, findErr, "finding the docker container had issues")

				// get current disk usage
				disk, errd := diskUsageCheck()

				// now update peaks
				stateMutex.Lock()
				if errf == nil && mem > peakmem {
					peakmem = mem

					if commandExceededMemoryEstimate(peakmem, job.Requirements.RAM) {
						exceededMemEstimate = true

						machineRAM = ensureMachineRAM(machineRAM)

						if c.peakMemNeedsKill(peakmem, machineRAM) {
							killErr = killCmd()
							killedForMem = true
							stateMutex.Unlock()

							break CHECKING
						}
					}
				}

				if cpuS > dockerCPU {
					dockerCPU = cpuS
				}

				if errd == nil && disk > peakdisk {
					peakdisk = disk
				}

				cpuTime := currentProcessTreeCPUtime(cmd.Process.Pid) + time.Duration(dockerCPU)*time.Second
				liveState.updateResources(peakmem, peakdisk, cpuTime)
				stateMutex.Unlock()
			case <-stopChecking:
				closeReaders()

				break CHECKING
			}
		}

		finishedChecking <- true
	}()

	// wait for the command to exit
	errsew := <-stderrWait
	errsow := <-stdoutWait
	err = cmd.Wait()

	resourceTicker.Stop()

	stopChecking <- true

	<-finishedChecking
	stateMutex.Lock()
	defer stateMutex.Unlock()

	endTime := time.Now()

	if killCalled {
		<-killDoneCh
	}

	// though we have tried to track peak memory while the cmd ran (mainly to
	// know if we use too much memory and kill during a run), our method might
	// miss a peak that cmd.ProcessState can tell us about, so use that if
	// higher
	if rusage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		peakRSS := rusage.Maxrss

		var peakRSSMB int
		if runtime.GOOS == "darwin" {
			// Maxrss values are bytes
			peakRSSMB = int((peakRSS / bytesPerKB) / kbPerMB)
		} else {
			// Maxrss values are kb
			peakRSSMB = int(peakRSS / kbPerMB)
		}

		if peakRSSMB > peakmem {
			peakmem = peakRSSMB
		}
	}

	// include our OWN memory usage in the peakmem of the command, since the
	// peak memory is used to schedule us in the job scheduler, which may
	// kill us for using more memory than expected: we need to allow for our
	// own memory usage.
	//
	// We deliberately measure only our own Pss here, NOT our children: by this
	// point the job command has already exited (cmd.Wait() above) and its peak
	// RSS has already been folded into peakmem via cmd.ProcessState's rusage
	// Maxrss just above. Summing children would therefore add nothing useful
	// (the only child, the job command, is gone) while paying for a full
	// /proc walk (gopsutil Children()), which is expensive per job on a busy
	// host. ownMemoryMB avoids that scan.
	ourmem, cmerr := ownMemoryMB()
	if cmerr != nil {
		ourmem = 10
	}

	peakmem += ourmem

	// get a final read on disk usage, for jobs that produce output after the
	// last ticker fired
	finalDisk, errd := diskUsageCheck()
	if errd == nil && finalDisk > peakdisk {
		peakdisk = finalDisk
	}

	exceededMemEstimate = commandExceededMemoryEstimate(peakmem, job.Requirements.RAM)

	// get the exit code and figure out what to do with the Job; dobury,
	// dorelease and failreason are consulted by the unmount/behaviour reporting
	// below before being finalised by the outcome classification.
	var (
		dobury, dorelease bool
		failreason        string
	)

	var mayBeTemp string
	if job.UntilBuried > 1 {
		mayBeTemp = ", which may be a temporary issue, so it will be tried again"
	}

	finalStdErr := bytes.TrimSpace(stderr.Bytes())

	myerr = appendExecErr(myerr, killErr, "killing the cmd also failed")

	if closeErr != nil && !strings.Contains(closeErr.Error(), "file already closed") {
		myerr = appendExecErr(myerr, closeErr, "closing stderr/out of the cmd also failed")
	}

	// run behaviours
	//nolint:contextcheck // behaviours run detached from the cancellable job context
	berr := job.TriggerBehaviours(err == nil && myerr == nil)
	myerr = appendExecErr(myerr, berr, "behaviour(s) also had problem(s)")

	// try and unmount now, because if we fail to upload files, we'll have to
	// start over
	addMountLogs := dobury || dorelease

	logs, unmountErr := job.Unmount()
	if unmountErr != nil {
		if strings.Contains(unmountErr.Error(), "failed to upload") && !dobury {
			// dorelease feeds the behaviour-problem reporting below; the fail
			// reason and exit code are then set by the outcome classification.
			dorelease = true
		}

		myerr = appendExecErr(myerr, unmountErr, "unmounting also caused problem(s)")
	}

	if addMountLogs && logs != "" {
		finalStdErr = append(finalStdErr, "\n\nMount logs:\n"...)
		finalStdErr = append(finalStdErr, logs...)
	}

	if (dobury || dorelease) && berr != nil {
		finalStdErr = append(finalStdErr, "\n\nBehaviour problems:\n"...)
		finalStdErr = append(finalStdErr, berr.Error()...)
	}

	if errsew != nil {
		finalStdErr = append(finalStdErr, "\n\nSTDERR handling problems:\n"...)
		finalStdErr = append(finalStdErr, errsew.Error()...)
	}

	// *** following is useful when debugging; need a better way to see these
	// errors from runner clients...
	// if myerr != nil {
	// 	finalStdErr = append(finalStdErr, "\n\nExecution errors:\n"...)
	// 	finalStdErr = append(finalStdErr, myerr.Error()...)
	// }

	finalStdOut := bytes.TrimSpace(stdout.Bytes())
	if errsow != nil {
		finalStdOut = append(finalStdOut, "\n\nSTDOUT handling problems:\n"...)
		finalStdOut = append(finalStdOut, errsow.Error()...)
	}

	outcome := c.classifyExecOutcome(execOutcomeInput{
		err:                 err,
		cmd:                 cmd,
		job:                 job,
		finalStdOut:         finalStdOut,
		finalStdErr:         finalStdErr,
		mayBeTemp:           mayBeTemp,
		oomMonitor:          oomMonitor,
		serverContact:       serverContact,
		peakmem:             peakmem,
		exceededMemEstimate: exceededMemEstimate,
		flags: execRunFlags{
			ranoutDisk: ranoutDisk, signalled: signalled, ranoutTime: ranoutTime,
			killCalled: killCalled, killedForMem: killedForMem,
		},
	})

	dobury = outcome.dobury
	dorelease = outcome.dorelease
	failreason = outcome.failreason
	myerr = outcome.myerr

	exitcode := outcome.exitcode
	doarchive := outcome.doarchive

	// now we've done everything time-consuming so can stop touching the job
	stopTouching <- true

	jes := &JobEndState{
		Cwd:      actualCwd,
		Exitcode: exitcode,
		PeakRAM:  peakmem,
		PeakDisk: peakdisk,
		CPUtime:  cmd.ProcessState.SystemTime() + cmd.ProcessState.UserTime() + time.Duration(dockerCPU)*time.Second,
		EndTime:  endTime,
		Stdout:   compressStd(finalStdOut),
		Stderr:   compressStd(finalStdErr),
		Exited:   true,
	}

	worked, hadProblems := c.reportFinalState(ctx, job, jes, execAction{
		bury: dobury, release: dorelease, archive: doarchive, failreason: failreason,
	})
	if !worked {
		//nolint:contextcheck // behaviours run detached from the cancellable job context
		errt := job.TriggerBehaviours(false)

		extra := ""
		if errt != nil {
			extra = fmt.Sprintf(" (and triggering behaviours failed: %s)", errt)
		}

		return fmt.Errorf("command [%s] finished running, but will need to be rerun due to a jobqueue server error: %w%s",
			job.Cmd, err, extra)
	}

	if hadProblems {
		if myerr != nil {
			myerr = fmt.Errorf("%w; %s", myerr, ErrStopReserving)
		} else {
			myerr = Error{clientOpExecute, job.Key(), ErrStopReserving}
		}
	}

	return myerr
}

// execAction describes the final action to take on a job and the fail reason to
// record with it.
type execAction struct {
	failreason string
	bury       bool
	release    bool
	archive    bool
}

// reportFinalState repeatedly tries to update the server with the job's final
// state, reconnecting if the connection was lost, until it succeeds or
// c.retryTime elapses. It returns whether it succeeded and whether it hit any
// problems along the way (which the caller turns into ErrStopReserving).
func (c *Client) reportFinalState(ctx context.Context, job *Job, jes *JobEndState, action execAction) (bool, bool) {
	retryEnd := time.Now().Add(c.retryTime)
	disconnected := false
	hadProblems := false

	for !time.Now().After(retryEnd) {
		if disconnected && !c.quickReconnect(ctx) {
			continue
		}

		err := c.applyFinalState(job, jes, action)
		if err == nil {
			return true, hadProblems
		}

		hadProblems = true

		var giveUp bool

		disconnected, giveUp = c.handleFinalStateError(ctx, err)
		if giveUp {
			return false, hadProblems
		}
	}

	clog.Warn(ctx, "giving up trying to connect to server")

	return false, hadProblems
}

// handleFinalStateError reacts to a failed state update: it logs the error,
// disconnects, and reports whether the client is now disconnected and whether
// the failure is permanent (giveUp). For transient failures it sleeps the retry
// delay before returning.
func (c *Client) handleFinalStateError(ctx context.Context, err error) (disconnected, giveUp bool) {
	clog.Error(ctx, "failed to update server with cmd's final state", "err", err)

	disconnected = c.disconnectAfterFailure(ctx)

	if strings.Contains(err.Error(), ErrBadJob) || strings.Contains(err.Error(), ErrBadRequest) {
		// this is a permanent error, give up
		return disconnected, true
	}

	<-time.After(c.retryWait)

	return disconnected, false
}

// quickReconnect performs a quick reconnect attempt, updating the client's
// socket on success. On failure it logs, sleeps a jittered retry delay and
// returns false.
func (c *Client) quickReconnect(ctx context.Context) bool {
	newC, errc := Connect(c.args[0], c.args[1], c.args[2], c.token, 1*time.Second)
	if errc != nil {
		clog.Warn(ctx, "tried to reconnect to server but failed", "err", errc)

		// keep retrying after a jittered sleep (weak random is fine here)
		wait := c.retryWait + time.Duration(rand.Float64()*0.5*float64(c.retryWait)) //nolint:gosec
		<-time.After(wait)

		return false
	}

	// server is back, update ourselves and continue (we keep the quick timeout,
	// but that should be good enough just to get through this)
	clog.Info(ctx, "reconnected to server")

	c.Lock()
	c.sock = newC.sock
	c.Unlock()

	return true
}

// applyFinalState updates the server with the job's end state according to the
// chosen action.
func (c *Client) applyFinalState(job *Job, jes *JobEndState, action execAction) error {
	switch {
	case action.bury:
		return c.Bury(job, jes, action.failreason)
	case action.release:
		return c.Release(job, jes, action.failreason) // which buries after job.Retries fails in a row
	case action.archive:
		return c.Archive(job, jes)
	default:
		return nil
	}
}

// disconnectAfterFailure disconnects from the server after a failed state
// update, returning whether the client is now considered disconnected.
func (c *Client) disconnectAfterFailure(ctx context.Context) bool {
	errd := c.Disconnect()
	if errd == nil || isClosedSocketError(errd) {
		return true
	}

	clog.Warn(ctx, "failed to disconnect", "err", errd)

	return false
}

// execRunFlags captures the boolean conditions observed while a command ran that
// influence how its non-zero exit is classified.
type execRunFlags struct {
	ranoutDisk   bool
	signalled    bool
	ranoutTime   bool
	killCalled   bool
	killedForMem bool
}

// execOutcomeInput bundles everything classifyExecOutcome needs to decide a
// finished command's fate.
type execOutcomeInput struct {
	err                 error
	cmd                 *exec.Cmd
	job                 *Job
	finalStdOut         []byte
	finalStdErr         []byte
	mayBeTemp           string
	oomMonitor          *cgroupOOMMonitor
	serverContact       *serverContactState
	peakmem             int
	exceededMemEstimate bool
	flags               execRunFlags
}

// execOutcome is the decision about what to do with a finished command.
type execOutcome struct {
	myerr      error
	failreason string
	exitcode   int
	dobury     bool
	dorelease  bool
	doarchive  bool
}

// classifyExecOutcome decides, from how a command finished, whether to bury,
// release or archive its job, the exit code to record and the error (if any) to
// return.
func (c *Client) classifyExecOutcome(in execOutcomeInput) execOutcome {
	if in.err == nil {
		// the command worked fine
		out := execOutcome{doarchive: true}
		if waitStatus, ok := in.cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
			out.exitcode = waitStatus.ExitStatus()
		}

		return out
	}

	cmdOut := execCmdOutput(in.finalStdOut, in.finalStdErr)

	var exitError *exec.ExitError
	if !errors.As(in.err, &exitError) {
		// some obscure internal error unrelated to the exit code
		return abnormalOutcome(in, cmdOut)
	}

	waitStatus, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok {
		return abnormalOutcome(in, cmdOut)
	}

	return c.classifyExitStatus(in, waitStatus, cmdOut)
}

// abnormalOutcome is the outcome for a command that failed to complete normally
// (no usable wait status), releasing the job for a retry.
func abnormalOutcome(in execOutcomeInput, cmdOut string) execOutcome {
	return execOutcome{
		exitcode:   exitCodeAbnormal,
		dorelease:  true,
		failreason: FailReasonAbnormal,
		myerr: fmt.Errorf("command [%s] failed to complete normally (%w)%s%s",
			in.job.Cmd, in.err, in.mayBeTemp, cmdOut),
	}
}

// execCmdOutput renders the captured stdout/stderr for inclusion in an error,
// preferring stderr when both are present (matching the original behaviour).
func execCmdOutput(finalStdOut, finalStdErr []byte) string {
	cmdOut := ""
	if len(finalStdOut) > 0 {
		cmdOut = fmt.Sprintf(" [stdout: %s]", string(finalStdOut))
	}

	if len(finalStdErr) > 0 {
		cmdOut = fmt.Sprintf(" [sterr: %s]", string(finalStdErr))
	}

	return cmdOut
}

// classifyExitStatus classifies a command that exited with a wait status,
// handling the permanent (bury) exit codes directly and delegating other codes
// to classifyReleasedExit.
func (c *Client) classifyExitStatus(in execOutcomeInput, waitStatus syscall.WaitStatus, cmdOut string) execOutcome {
	exitcode := waitStatus.ExitStatus()

	out := execOutcome{exitcode: exitcode, dobury: true}

	switch exitcode {
	case exitCodeCommandPermission:
		out.failreason = FailReasonCPerm
		//nolint:err113
		out.myerr = fmt.Errorf(
			"command [%s] exited with code %d (permission problem, or command is not executable), "+
				"which seems permanent, so it has been buried%s",
			in.job.Cmd, exitcode, cmdOut,
		)
	case exitCodeCommandNotFound:
		out.failreason = FailReasonCFound
		//nolint:err113
		out.myerr = fmt.Errorf(
			"command [%s] exited with code %d (command not found), which seems permanent, so it has been buried%s",
			in.job.Cmd, exitcode, cmdOut,
		)
	case exitCodeCommandInvalid:
		out.failreason = FailReasonCExit
		//nolint:err113
		out.myerr = fmt.Errorf(
			"command [%s] exited with code %d (invalid exit code), which seems permanent, so it has been buried%s",
			in.job.Cmd, exitcode, cmdOut,
		)
	default:
		out = c.classifyReleasedExit(in, waitStatus, exitcode, cmdOut)
	}

	return out
}

// classifyReleasedExit classifies a command whose exit code is not one of the
// permanent-failure codes, deciding the fail reason and whether to (still) bury.
func (c *Client) classifyReleasedExit(in execOutcomeInput, waitStatus syscall.WaitStatus,
	exitcode int, cmdOut string,
) execOutcome {
	out := execOutcome{exitcode: exitcode, dorelease: true}
	job := in.job

	switch {
	case in.flags.ranoutDisk:
		out.failreason, out.myerr = job.simpleFail(FailReasonDisk)
	case in.flags.signalled:
		out.failreason, out.myerr = c.signalledOutcome(job, in.flags.ranoutTime)
	case in.flags.killCalled:
		out.dobury = true
		out.failreason, out.myerr = job.simpleFail(FailReasonKilled)
	case c.memoryDeath(in, waitStatus):
		out.failreason, out.myerr = job.simpleFail(FailReasonRAM)
	case waitStatus.Signaled(): //nolint:misspell // Signaled is syscall's method name.
		out.failreason = FailReasonSignal
		out.myerr = signalExitError(job, waitStatus, in.mayBeTemp, cmdOut)
	default:
		out.failreason = FailReasonExit
		out.dobury, out.myerr = plainExitOutcome(job, exitcode, in.mayBeTemp, cmdOut)
	}

	out.myerr = maybeAppendHighMemoryNote(out.myerr, out.failreason, in)

	return out
}

// simpleFail returns reason and an Execute Error carrying reason for job.
func (job *Job) simpleFail(reason string) (string, error) {
	return reason, Error{clientOpExecute, job.Key(), reason}
}

// signalExitError builds the error for a command terminated by a signal.
func signalExitError(job *Job, waitStatus syscall.WaitStatus, mayBeTemp, cmdOut string) error {
	shellExitCode := shellSignalExitCodeOffset + int(waitStatus.Signal())

	//nolint:err113
	return fmt.Errorf(
		"command [%s] terminated by signal %s (shell exit code %d)%s%s",
		job.Cmd, waitStatus.Signal(), shellExitCode, mayBeTemp, cmdOut,
	)
}

// maybeAppendHighMemoryNote annotates err with a high-memory note when the
// failure was not already a RAM failure but the command exceeded its estimate.
func maybeAppendHighMemoryNote(err error, failreason string, in execOutcomeInput) error {
	if failreason != FailReasonRAM && in.exceededMemEstimate {
		return appendHighMemoryNote(err, in.peakmem, in.job.Requirements.RAM)
	}

	return err
}

// plainExitOutcome classifies a non-zero exit that isn't otherwise special: if
// the job has run past its no-retries-over-walltime it is buried, otherwise it
// is released for a retry. The fail reason is always FailReasonExit.
func plainExitOutcome(job *Job, exitcode int, mayBeTemp, cmdOut string) (bury bool, err error) {
	if noRetriesTimeExceeded(job) {
		//nolint:err113
		return true, fmt.Errorf(
			"command [%s] exited with code %d%s%s",
			job.Cmd, exitcode, ", after the noretries time, so will not be tried again", cmdOut,
		)
	}

	//nolint:err113
	return false, fmt.Errorf(
		"command [%s] exited with code %d%s%s", job.Cmd, exitcode, mayBeTemp, cmdOut)
}

// signalledOutcome returns the fail reason and error for a job killed by a
// signal, distinguishing an out-of-time kill.
func (c *Client) signalledOutcome(job *Job, ranoutTime bool) (string, error) {
	if ranoutTime {
		return job.simpleFail(FailReasonTime)
	}

	return job.simpleFail(FailReasonSignal)
}

// memoryDeath reports whether the command's death is attributable to running out
// of memory.
func (c *Client) memoryDeath(in execOutcomeInput, waitStatus syscall.WaitStatus) bool {
	return attributedMemoryDeath(in.flags.killedForMem, in.oomMonitor.oomKillIncreased(), waitStatus,
		in.peakmem, in.job.Requirements.RAM, c.schedulerName(), in.serverContact.schedulerMemoryFallbackAllowed())
}

// noRetriesTimeExceeded reports whether the job has run for longer than its
// no-retries-over-walltime threshold (and so should not be retried).
func noRetriesTimeExceeded(job *Job) bool {
	return job.UntilBuried > 1 && job.NoRetriesOverWalltime > 0 && job.WallTime() > job.NoRetriesOverWalltime
}

// schedulerName returns the configured scheduler name, or "" if unknown.
func (c *Client) schedulerName() string {
	if c.ServerInfo != nil {
		return c.ServerInfo.Scheduler
	}

	return ""
}

func isClosedSocketError(err error) bool {
	if err == nil {
		return false
	}

	return errors.Is(err, mangos.ErrClosed) || strings.Contains(err.Error(), "connection closed")
}

// createLSFSymlinks creates symlinks of bsub, bjobs and bkill to own exe,
// inside the given dir.
func (c *Client) createLSFSymlinks(prependPath string, job *Job) error {
	wr, erre := os.Executable()
	if erre != nil {
		return c.buryAndWrap(job, FailReasonCwd, erre, "could not get path to wr")
	}

	for _, name := range []string{"bsub", "bjobs", "bkill"} {
		if err := os.Symlink(wr, filepath.Join(prependPath, name)); err != nil {
			return c.buryAndWrap(job, FailReasonCwd, err, "could not create "+name+" symlink")
		}
	}

	return nil
}

// buryAndWrap buries job with the given failReason and returns an error wrapping
// cause with message, additionally noting any failure to bury the job.
func (c *Client) buryAndWrap(job *Job, failReason string, cause error, message string) error {
	if errb := c.Bury(job, nil, failReason); errb != nil {
		return fmt.Errorf("%s: %w (and burying the job failed: %w)", message, cause, errb)
	}

	return fmt.Errorf("%s: %w", message, cause)
}

// buryWithCause buries job (recording the "message: cause" error as the job's
// stderr) and returns that error, noting any failure to bury.
func (c *Client) buryWithCause(job *Job, failReason string, cause error, message string) error {
	return c.buryWrapErr(job, failReason, fmt.Errorf("%s: %w", message, cause))
}

// buryWrapErr buries job recording buryErr as its stderr, and returns buryErr,
// noting any failure to bury.
func (c *Client) buryWrapErr(job *Job, failReason string, buryErr error) error {
	if errb := c.Bury(job, nil, failReason, buryErr); errb != nil {
		return fmt.Errorf("%w (and burying the job failed: %w)", buryErr, errb)
	}

	return buryErr
}

func compressStd(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}

	compressed, err := compress(data)
	if err != nil {
		return nil
	}

	return compressed
}

// Started updates a Job on the server with information that you've started
// running the Job's Cmd. Started also figures out some host name, ip and
// possibly id (in cloud situations) to associate with the job, so that if
// something goes wrong the user can go to the host and investigate. Note that
// HostID will not be set on job after this call; only the server will know
// about it (use one of the Get methods afterwards to get a new object with the
// HostID set if necessary).
func (c *Client) Started(job *Job, pid int) error {
	// host details
	host, err := os.Hostname()
	if err != nil {
		host = localhost
	}

	hostIP, err := internal.CurrentIP("")
	if err != nil {
		return err
	}

	job.Lock()
	job.Host = host
	job.HostIP = hostIP
	job.Pid = pid
	job.Attempts++             // not considered by server, which does this itself - just for benefit of this process
	job.StartTime = time.Now() // ditto
	requestJob := keyOnlyJob(job)
	requestJob.Host = job.Host
	requestJob.HostIP = job.HostIP
	requestJob.Pid = job.Pid

	// the working directory this run is going to use, which resolveWorkingDir
	// has already created. Reporting it HERE is what lets the manager clean up
	// after a run that dies without ever touching, and after every run at all on
	// a manager with no web port, which never asks for a live snapshot.
	requestJob.ActualCwd = job.ActualCwd
	job.Unlock()

	_, err = c.request(&clientRequest{Method: requestMethodStart, Job: requestJob})

	return err
}

func keyOnlyJob(job *Job) *Job {
	return &Job{
		Cmd:             job.Cmd,
		Cwd:             job.Cwd,
		CwdMatters:      job.CwdMatters,
		MountConfigs:    cloneMountConfigs(job.MountConfigs),
		WithDocker:      job.WithDocker,
		WithSingularity: job.WithSingularity,
		ContainerMounts: job.ContainerMounts,
	}
}

// Touch adds to a job's ttr, allowing you more time to work on it. Note that
// you must have reserved the job before you can touch it. If the returned bool
// is true, you stop doing what you're doing and bury the job, since this means
// that Kill() has been called for this job.
func (c *Client) Touch(job *Job) (bool, error) {
	job.Lock()
	endState := touchEndState(job)
	job.Unlock()

	return c.touch(job, endState)
}

// JobEndState is used to describe the state of a job after it has (tried to)
// execute it's Cmd. You supply these to Client.Bury(), Release() and Archive().
// The cwd you supply should be the actual working directory used, which may be
// different to the Job's Cwd property; if not, supply empty string. Always set
// exited to true, and populate all other fields, unless you never actually
// tried to execute the Cmd, in which case you would just provide a nil
// JobEndState to the methods that need one.
type JobEndState struct {
	Cwd      string
	Exitcode int
	PeakRAM  int
	PeakDisk int64
	CPUtime  time.Duration
	EndTime  time.Time
	Stdout   []byte
	Stderr   []byte
	Exited   bool
}

func (c *Client) touch(job *Job, endState *JobEndState) (bool, error) {
	c.teMutex.Lock()
	defer c.teMutex.Unlock()

	job.Lock()
	key := job.Key()
	job.Unlock()

	c.inspectLiveTouch(endState)

	resp, err := c.request(&clientRequest{
		Method:      requestMethodTouch,
		Keys:        []string{key},
		JobEndState: endState,
	})
	if err != nil {
		return false, err
	}

	return resp.KillCalled, err
}

func (c *Client) inspectLiveTouch(endState *JobEndState) {
	if c.liveTouchHook == nil {
		return
	}

	c.liveTouchHook(cloneJobEndState(endState))
}

// ended updates a Job for the benefit of the client only: this has no effect on
// the server's knowledge of the Job.
//
// teMutex and job must be locked before calling this function.
func (c *Client) ended(job *Job, jes *JobEndState) {
	if jes == nil || !jes.Exited {
		return
	}

	job.Exited = true
	job.Exitcode = jes.Exitcode
	job.PeakRAM = jes.PeakRAM
	job.PeakDisk = jes.PeakDisk
	job.CPUtime = jes.CPUtime
	job.EndTime = jes.EndTime
	job.setActualCwd(jes.Cwd)
	job.StdOutC = jes.Stdout
	job.StdErrC = jes.Stderr
}

// Archive removes a job from the jobqueue and adds it to the database of
// complete jobs, for use after you have run the job successfully. You have to
// have been the one to Reserve() the supplied Job, and the Job must be marked
// as having successfully run, or you will get an error.
func (c *Client) Archive(job *Job, jes *JobEndState) error {
	c.teMutex.Lock()
	defer c.teMutex.Unlock()

	job.Lock()
	key := job.Key()
	job.Unlock()

	_, err := c.request(&clientRequest{Method: "jarchive", Keys: []string{key}, JobEndState: jes})
	if err != nil {
		return err
	}

	job.Lock()
	defer job.Unlock()

	c.ended(job, jes)
	job.State = JobStateComplete

	return nil
}

// Release places a job back on the jobqueue, for use when you can't handle the
// job right now (eg. there was a suspected transient error) but maybe someone
// else can later. Note that you must reserve a job before you can release it.
// You can only Release() the same job as many times as its Retries value if it
// has been run and failed; a subsequent call to Release() will instead result
// in a Bury(). (If the job's Cmd was not run, you can Release() an unlimited
// number of times.)
func (c *Client) Release(job *Job, jes *JobEndState, failreason string) error {
	c.teMutex.Lock()
	defer c.teMutex.Unlock()

	key := setJobFailReason(job, failreason)

	_, err := c.request(&clientRequest{
		Method:      "jrelease",
		Keys:        []string{key},
		JobEndState: jes,
		FailReason:  failreason,
	})
	if err != nil {
		return err
	}

	c.finishRelease(job, jes)

	return nil
}

// finishRelease updates our local copy of job with the state the server would
// have applied after a release.
func (c *Client) finishRelease(job *Job, jes *JobEndState) {
	job.Lock()
	defer job.Unlock()

	c.ended(job, jes)

	// update our process with what the server would have done
	if job.Exited && job.Exitcode != 0 {
		job.UntilBuried--
	}

	if job.UntilBuried <= 0 {
		job.State = JobStateBuried
	} else {
		job.State = JobStateDelayed
	}
}

// setJobFailReason records failreason on job under its lock and returns its key.
func setJobFailReason(job *Job, failreason string) string {
	job.Lock()
	defer job.Unlock()

	job.FailReason = failreason

	return job.Key()
}

// Bury marks a job as unrunnable, so it will be ignored (until the user does
// something to perhaps make it runnable and kicks the job). Note that you must
// reserve a job before you can bury it. Optionally supply an error that will
// be displayed as the Job's stderr.
func (c *Client) Bury(job *Job, jes *JobEndState, failreason string, stderr ...error) error {
	c.teMutex.Lock()
	defer c.teMutex.Unlock()

	if len(stderr) == 1 && stderr[0] != nil {
		if jes == nil {
			jes = &JobEndState{}
		}

		jes.Stderr = compressStd([]byte(stderr[0].Error()))
	}

	key := setJobFailReason(job, failreason)

	_, err := c.request(&clientRequest{
		Method:      "jbury",
		Keys:        []string{key},
		JobEndState: jes,
		FailReason:  failreason,
	})
	if err != nil {
		return err
	}

	job.Lock()
	defer job.Unlock()

	c.ended(job, jes)
	job.State = JobStateBuried

	return nil
}

// Kick makes previously Bury()'d jobs runnable again (it can be Reserve()d in
// the future). It returns a count of jobs that it actually kicked. Errors will
// only be related to not being able to contact the server.
func (c *Client) Kick(jes []*JobEssence) (int, error) {
	keys := c.jesToKeys(jes)

	resp, err := c.request(&clientRequest{Method: "jkick", Keys: keys})
	if err != nil {
		return 0, err
	}

	return resp.Existed, err
}

// Suspend moves delayed, ready, and dependent jobs out of reservation until
// they are resumed. It returns a count of jobs that were actually suspended.
// Errors will only be related to not being able to contact the server.
func (c *Client) Suspend(jes []*JobEssence) (int, error) {
	keys := c.jesToKeys(jes)

	resp, err := c.request(&clientRequest{Method: "jsuspend", Keys: keys})
	if err != nil {
		return 0, err
	}

	return resp.Existed, err
}

// Resume moves suspended jobs back to ready or dependent according to their
// current dependencies. It returns a count of jobs that were actually resumed.
// Errors will only be related to not being able to contact the server.
func (c *Client) Resume(jes []*JobEssence) (int, error) {
	keys := c.jesToKeys(jes)

	resp, err := c.request(&clientRequest{Method: "jresume", Keys: keys})
	if err != nil {
		return 0, err
	}

	return resp.Existed, err
}

// Delete removes incomplete, not currently running jobs from the queue
// completely. For use when jobs were created incorrectly/ by accident, or they
// can never be fixed. It returns a count of jobs that it actually removed.
// Errors will only be related to not being able to contact the server.
func (c *Client) Delete(jes []*JobEssence) (int, error) {
	keys := c.jesToKeys(jes)

	resp, err := c.request(&clientRequest{Method: "jdel", Keys: keys})
	if err != nil {
		return 0, err
	}

	return resp.Existed, err
}

// Kill will cause the next Touch() call for the job(s) described by the input
// to return a kill signal. Touches happening as part of an Execute() will
// respond to this signal by terminating their execution and burying the job. As
// such you should note that there could be a delay between calling Kill() and
// execution ceasing; wait until the jobs actually get buried before retrying
// the jobs if desired.
//
// Kill returns a count of jobs that were eligible to be killed (those still in
// running state). Errors will only be related to not being able to contact the
// server.
func (c *Client) Kill(jes []*JobEssence) (int, error) {
	keys := c.jesToKeys(jes)

	resp, err := c.request(&clientRequest{Method: "jkill", Keys: keys})
	if err != nil {
		return 0, err
	}

	return resp.Existed, err
}

// GetByEssence gets a Job given a JobEssence to describe it. With the boolean
// args set to true, this is the only way to get a Job that StdOut() and
// StdErr() will work on, and one of 2 ways that Env() will work (the other
// being Reserve()).
func (c *Client) GetByEssence(je *JobEssence, getstd bool, getenv bool) (*Job, error) {
	resp, err := c.request(&clientRequest{Method: "getbc", Keys: []string{je.Key()}, GetStd: getstd, GetEnv: getenv})
	if err != nil {
		return nil, err
	}

	jobs := resp.Jobs
	if len(jobs) == 0 {
		return nil, err
	}

	return jobs[0], err
}

// GetByEssences gets multiple Jobs at once given JobEssences that describe
// them.
func (c *Client) GetByEssences(jes []*JobEssence) ([]*Job, error) {
	keys := c.jesToKeys(jes)

	resp, err := c.request(&clientRequest{Method: "getbc", Keys: keys})
	if err != nil {
		return nil, err
	}

	return resp.Jobs, err
}

// jesToKeys deals with the jes arg that GetByEccences(), Kick() and Delete()
// take.
func (c *Client) jesToKeys(jes []*JobEssence) []string {
	keys := make([]string, 0, len(jes))
	for _, je := range jes {
		keys = append(keys, je.Key())
	}

	return keys
}

// GetByRepGroup gets multiple Jobs at once given their RepGroup (an arbitrary
// user-supplied identifier for the purpose of grouping related jobs together
// for reporting purposes).
//
// If 'subStr' is true, gets Jobs in all RepGroups that the supplied repgroup is
// a substring of.
//
// 'limit', if greater than 0, limits the number of jobs returned that have the
// same State, FailReason and Exitcode, and on the last job of each
// State+FailReason group it populates 'Similar' with the number of other
// excluded jobs there were in that group.
//
// Providing 'state' only returns jobs in that State. 'getStd' and 'getEnv', if
// true, retrieve the stdout, stderr and environement variables for the Jobs.
func (c *Client) GetByRepGroup(repgroup string, subStr bool, limit int, state JobState,
	getStd bool, getEnv bool,
) ([]*Job, error) {
	match := RepGroupMatchExact
	if subStr {
		match = RepGroupMatchSubStr
	}

	return c.GetByRepGroupMatch(repgroup, match, limit, state, getStd, getEnv)
}

// GetByRepGroupMatch gets multiple Jobs at once given their RepGroup and the
// desired match mode.
func (c *Client) GetByRepGroupMatch(repgroup string, match RepGroupMatch, limit int,
	state JobState, getStd bool, getEnv bool) ([]*Job, error) {
	resp, err := c.request(&clientRequest{Method: "getbr", Job: &Job{RepGroup: repgroup},
		Search: match != RepGroupMatchExact, RepGroupMatch: match, Limit: limit,
		State: state, GetStd: getStd, GetEnv: getEnv})
	if err != nil {
		return nil, err
	}

	return resp.Jobs, err
}

// GetStatusByRepGroupMatch gets compact per-state job counts, optionally with
// summary details, for report groups that match repgroup.
func (c *Client) GetStatusByRepGroupMatch(repgroup string, match RepGroupMatch,
	states []JobState, includeComplete bool, includeStatusDetails bool) (map[string]*RepGroupStatus, error) {
	resp, err := c.request(&clientRequest{
		Method:               "getrs",
		Job:                  &Job{RepGroup: repgroup},
		Search:               match != RepGroupMatchExact,
		RepGroupMatch:        match,
		States:               states,
		IncludeComplete:      includeComplete,
		IncludeStatusDetails: includeStatusDetails,
	})
	if err != nil {
		return nil, err
	}

	return resp.StatusSummaries, err
}

// GetIncomplete gets all Jobs that are currently in the jobqueue, ie. excluding
// those that are complete and have been Archive()d. The args are as in
// GetByRepGroup().
func (c *Client) GetIncomplete(limit int, state JobState, getStd bool, getEnv bool) ([]*Job, error) {
	resp, err := c.request(&clientRequest{
		Method: requestMethodGetIncomplete, Limit: limit, State: state, GetStd: getStd, GetEnv: getEnv,
	})
	if err != nil {
		return nil, err
	}

	return resp.Jobs, err
}

// GetIncompleteByRepGroupMatch gets all non-archived jobs currently in the
// queue whose RepGroup matches repgroup using the supplied match mode. The
// remaining args are as in GetByRepGroup().
func (c *Client) GetIncompleteByRepGroupMatch(repgroup string, match RepGroupMatch,
	limit int, state JobState, getStd bool, getEnv bool) ([]*Job, error) {
	resp, err := c.request(&clientRequest{Method: requestMethodGetIncomplete, Job: &Job{RepGroup: repgroup},
		Search: match != RepGroupMatchExact, RepGroupMatch: match, Limit: limit,
		State: state, GetStd: getStd, GetEnv: getEnv})
	if err != nil {
		return nil, err
	}

	return resp.Jobs, err
}

// GetIncompleteWaitingForDepGroups gets all non-archived jobs currently in the
// queue whose WaitingForDepGroups field is non-empty. If repgroup is supplied,
// only jobs whose RepGroup matches according to match are returned.
func (c *Client) GetIncompleteWaitingForDepGroups(repgroup string, match RepGroupMatch,
	limit int, getStd bool, getEnv bool) ([]*Job, error) {
	resp, err := c.request(&clientRequest{Method: requestMethodGetIncomplete, Job: &Job{RepGroup: repgroup},
		Search: match != RepGroupMatchExact, RepGroupMatch: match, Limit: limit,
		GetStd: getStd, GetEnv: getEnv, WaitingForDepGroups: true})
	if err != nil {
		return nil, err
	}

	return resp.Jobs, err
}

// GetLastCompletionTimeByRepGroup returns the most recent completion time for
// each matched RepGroup.
func (c *Client) GetLastCompletionTimeByRepGroup(repgroup string,
	match RepGroupMatch) (map[string]time.Time, error) {
	resp, err := c.request(&clientRequest{Method: "getlct", Job: &Job{RepGroup: repgroup},
		Search: match != RepGroupMatchExact, RepGroupMatch: match})
	if err != nil {
		return nil, err
	}

	return resp.CompletionTimes, nil
}

// GetOrSetLimitGroup takes the name of a limit group and returns the current
// limit for that group. If the group isn't known about, returns -1.
//
// If the name is suffixed with :n, where n is an integer, then the limit of
// the group is set to n, and then n is returned. Setting n to -1 makes the
// group forgotten about, effectively making it unlimited.
func (c *Client) GetOrSetLimitGroup(group string) (int, error) {
	resp, err := c.request(&clientRequest{Method: "getsetlg", LimitGroup: group})
	if err != nil {
		return -1, err
	}

	return resp.Limit, err
}

// GetLimitGroups returns all currently known about limit groups, and the limit
// they are set to.
func (c *Client) GetLimitGroups() (map[string]int, error) {
	resp, err := c.request(&clientRequest{Method: "getlgs"})
	if err != nil {
		return nil, err
	}

	return resp.LimitGroups, err
}

// UploadFile uploads a local file to the machine where the server is running,
// so you can add cloud jobs that need a script or config file on your local
// machine to be copied over to created cloud instances.
//
// If the remote path is supplied as a blank string, the remote path will be
// chosen for you based on the MD5 checksum of your file data, rooted in the
// server's configured UploadDir.
//
// The remote path can be supplied prefixed with ~/ to upload relative to the
// remote's home directory. Otherwise it should be an absolute path.
//
// Returns the absolute path of the uploaded file on the server's machine.
//
// NB: This is only suitable for transferring small files!
func (c *Client) UploadFile(local, remote string) (string, error) {
	compressed, err := compressFile(local)
	if err != nil {
		return "", err
	}

	resp, err := c.request(&clientRequest{Method: "upload", File: compressed, Path: remote})
	if err != nil {
		return "", err
	}

	return resp.Path, err
}

// GetBadCloudServers (if the server is running with a cloud scheduler) returns
// servers that are currently non-responsive and might be dead.
func (c *Client) GetBadCloudServers() ([]*BadServer, error) {
	resp, err := c.request(&clientRequest{Method: "getbcs"})
	if err != nil {
		return nil, err
	}

	return resp.BadServers, err
}

// ConfirmCloudServersDead will confirm that currently non-responsive cloud
// servers (that would be returned by GetBadCloudServers()) are dead, triggering
// their destruction. If id is an empty string, applies to all such servers. If
// it is the ID of a server returned by GetBadCloudServers(), applies to just
// that server. Returns the servers that were successfully confirmed dead.
//
// Additionally, any jobs that were running or lost on those servers will be
// killed or confirmed dead, meaning that they become buried or delayed, as per
// their retry count. Jobs that were successfully killed are returned. Note that
// if a job hadn't become lost before calling this method, it will be returned
// with a state of "running", but as soon as it would normally be marked as
// lost, it will be instead be treated as if you confirmed it dead. The job's
// UntilBuried is what it will be at that future time point, so if it is 0 you
// know this currently running job will be buried.
func (c *Client) ConfirmCloudServersDead(id string) ([]*BadServer, []*Job, error) {
	resp, err := c.request(&clientRequest{Method: "getbcs", ConfirmDeadCloudServers: true, CloudServerID: id})
	if err != nil {
		return nil, nil, err
	}

	return resp.BadServers, resp.Jobs, err
}

// DestroyCloudHost will destroy the cloud server with the given host name. If
// the server was found and destroyed, it will be returned as a slice of
// BadServer (length 1, the slice for consistency with
// ConfirmCloudServersDead()).
//
// Additionally, any jobs that were running or lost on that server will be
// killed or confirmed dead, as per ConfirmCloudServersDead().
func (c *Client) DestroyCloudHost(hostName string) ([]*BadServer, []*Job, error) {
	resp, err := c.request(&clientRequest{Method: "dch", DestroyCloudHost: hostName})
	if err != nil {
		return nil, nil, err
	}

	return resp.BadServers, resp.Jobs, err
}

// request the server do something and get back its response. We can only cope
// with one request at a time per client, or we'll get replies back in the
// wrong order, hence we lock.
func (c *Client) request(cr *clientRequest) (*serverResponse, error) {
	c.Lock()
	defer c.Unlock()

	return c.requestLocked(cr)
}

// encodeAndSend encodes cr (stamping it with this client's token and id) and
// sends it to the server.
func (c *Client) encodeAndSend(cr *clientRequest) error {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, c.ch)
	cr.Token = c.token
	cr.ClientID = c.clientid

	if err := enc.Encode(cr); err != nil {
		return err
	}

	return c.sock.Send(encoded)
}

// recvAndDecode receives the server's reply and decodes it into a
// serverResponse.
func (c *Client) recvAndDecode() (*serverResponse, error) {
	resp, err := c.sock.Recv()
	if err != nil {
		return nil, err
	}

	sr := &serverResponse{}
	dec := codec.NewDecoderBytes(resp, c.ch)

	if err := dec.Decode(sr); err != nil {
		return nil, err
	}

	return sr, nil
}

// CompressEnv encodes the given environment variables (slice of "key=value"
// strings) and then compresses that, so that for Add() the server can store it
// on disc without holding it in memory, and pass the compressed bytes back to
// us when we need to know the Env (during Execute()).
func (c *Client) CompressEnv(envars []string) ([]byte, error) {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, c.ch)

	err := enc.Encode(&envStr{envars})
	if err != nil {
		return nil, err
	}

	return compress(encoded)
}

// requestTimeout returns the receive deadline to apply to the socket while
// waiting for the server's reply to a request. It is the larger of the supplied
// connect timeout and ClientMinRequestTimeout, so that callers who pass a short
// connect timeout (to fail fast when the server is unreachable) still tolerate a
// slow reply from a reachable-but-busy server: e.g. a large bulk Add, or any
// request on a heavily contended or CPU-starved machine, or under the race
// detector, must not be mistaken for a dead server via a spurious 'receive time
// out'. This is safe because an unreachable server is still detected promptly:
// a new request to a server with no live pipe fails fast on the short send
// deadline, well before this receive deadline is ever reached.
func requestTimeout(connectTimeout time.Duration) time.Duration {
	if connectTimeout > ClientMinRequestTimeout {
		return connectTimeout
	}

	return ClientMinRequestTimeout
}

func processPID(pid int) (int32, bool) {
	const maxProcessPID = int(^uint32(0) >> 1)

	if pid <= 0 || pid > maxProcessPID {
		return 0, false
	}

	return int32(pid), true
}

func currentProcessCPUtime(proc *process.Process) time.Duration {
	times, err := proc.Times()
	if err != nil {
		return 0
	}

	return time.Duration((times.User + times.System) * float64(time.Second))
}

func cloneMountConfigs(mountConfigs MountConfigs) MountConfigs {
	clone := slices.Clone(mountConfigs)
	for i := range clone {
		clone[i].Targets = slices.Clone(clone[i].Targets)
	}

	return clone
}
