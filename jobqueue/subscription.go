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

package jobqueue

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/VertebrateResequencing/wr/internal/mangostlstcp" // register race-clean tls+tcp transport
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
	"go.nanomsg.org/mangos/v3/protocol/req"
)

const (
	serverSubscriptionQueueSize  = 1024
	serverSubscriptionHoldTime   = 25 * time.Second
	subscriptionSocketRecvMargin = 5 * time.Second
	subscriptionReconnectTimeout = time.Second
	subscriptionMinReconnectWait = 10 * time.Millisecond
)

// ErrSubscriptionClosed is returned by Subscription.Err after an unrecoverable
// subscription disconnect.
var ErrSubscriptionClosed = errors.New("jobqueue subscription closed: unrecoverable disconnect")

// errRetryBudgetSpent fails one reconnect attempt that had no retry budget left
// to bound its next step with. It does not itself end the retry loop: reconnect()
// still decides whether to attempt again solely by comparing the clock to
// retryEnd, so connection errors, ErrClosedStop and ErrRecovering keep retrying
// exactly as before, and a spent budget ends the loop there rather than here.
var errRetryBudgetSpent = errors.New("jobqueue subscription reconnect: retry budget spent")

// JobUpdateKind discriminates the events on a Subscription channel.
type JobUpdateKind int

const (
	// JobUpdateTerminal means a subscribed job reached complete or buried.
	JobUpdateTerminal JobUpdateKind = iota
	// JobUpdateLost means a subscribed job entered the provisional lost state.
	JobUpdateLost
	// JobUpdateRepGroupDone means all currently known jobs in a RepGroup are
	// terminal.
	JobUpdateRepGroupDone
	// JobUpdateResync means the client re-subscribed after reconnecting.
	JobUpdateResync
	// JobUpdateStateChange means a subscribed job changed to a non-terminal
	// state. Server-side status websocket detail subscriptions request all of
	// these updates; job-key subscriptions receive suspend/resume transitions.
	JobUpdateStateChange
	// JobUpdateLive means a subscribed running job has fresh live output or
	// resource details.
	JobUpdateLive
)

// JobUpdate is the single event type delivered on Subscription.
type JobUpdate struct {
	Started    *int64
	Ended      *int64
	Kind       JobUpdateKind
	Key        string
	RepGroup   string
	State      JobState
	FailReason string
	JobKeys    []string
	JobStates  []JobState
	Exitcode   int
	Complete   int
	Buried     int
	Lost       int
	Total      int
	PeakRAM    int
	PeakDisk   int64
	Pid        int
	CPUtime    time.Duration
	Host       string
	HostID     string
	HostIP     string
	CwdBase    string
	Cwd        string
	StdOut     string
	StdErr     string
	SSHCommand string
}

func receiveJobUpdate(ctx context.Context, updates <-chan *JobUpdate) (*JobUpdate, error) {
	select {
	case update, ok := <-updates:
		if !ok {
			return nil, closedSubscriptionError(ctx)
		}

		return update, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func recordTerminalKey(update *JobUpdate, wanted map[string]struct{}, seen map[string]JobState) {
	if !isTerminalUpdate(update) {
		return
	}

	if _, wantedKey := wanted[update.Key]; !wantedKey {
		return
	}

	if _, alreadySeen := seen[update.Key]; alreadySeen {
		return
	}

	seen[update.Key] = update.State
}

func isTerminalUpdate(update *JobUpdate) bool {
	if update == nil || update.Kind != JobUpdateTerminal {
		return false
	}

	return update.State == JobStateComplete || update.State == JobStateBuried
}

// Subscription is a client-side handle for job completion updates.
type Subscription struct {
	client    *Client
	sock      mangos.Socket
	sockMu    sync.RWMutex
	ch        codec.Handle
	updates   chan *JobUpdate
	stop      chan struct{}
	closed    chan struct{}
	id        string
	dialAddr  string
	keys      []string
	repGroup  string
	stopOnce  sync.Once
	unsubOnce sync.Once
	doneOnce  sync.Once
	errMu     sync.RWMutex
	err       error
}

func newSubscription(
	c *Client,
	sock mangos.Socket,
	id string,
	dialAddr string,
	keys []string,
	repGroup string,
) *Subscription {
	return &Subscription{
		client:   c,
		sock:     sock,
		ch:       new(codec.BincHandle),
		updates:  make(chan *JobUpdate, serverSubscriptionQueueSize),
		stop:     make(chan struct{}),
		closed:   make(chan struct{}),
		id:       id,
		dialAddr: dialAddr,
		keys:     append([]string(nil), keys...),
		repGroup: repGroup,
	}
}

// Updates returns the receive-only channel of events.
func (s *Subscription) Updates() <-chan *JobUpdate {
	return s.updates
}

// Err returns the terminal teardown cause, or nil while the subscription is
// live or after a clean Unsubscribe.
func (s *Subscription) Err() error {
	s.errMu.RLock()
	defer s.errMu.RUnlock()

	return s.err
}

// Unsubscribe tears down the subscription. It is idempotent.
func (s *Subscription) Unsubscribe() {
	s.requestStop(nil)

	if err := s.unsubscribeServer(); err != nil {
		s.finish(err)
	}

	s.waitUntilClosed()
}

func (s *Subscription) unsubscribeServer() error {
	var unsubErr error

	s.unsubOnce.Do(func() {
		_, unsubErr = s.client.request(&clientRequest{Method: requestMethodUnsubscribe, SubscriptionID: s.id})
	})

	return unsubErr
}

func (s *Subscription) waitUntilClosed() {
	select {
	case <-s.closed:
	case <-time.After(time.Second):
	}
}

func (s *Subscription) stopWhenContextDone(ctx context.Context) {
	select {
	case <-ctx.Done():
		ctxErr := ctx.Err()
		s.requestStop(ctxErr)

		if err := s.unsubscribeServer(); err != nil {
			s.setErr(errors.Join(ctxErr, err))
			s.finish(err)
		}
	case <-s.closed:
	}
}

func (s *Subscription) poll(ctx context.Context, initial []*JobUpdate) {
	if !s.publishClientUpdates(ctx, initial) {
		return
	}

	for {
		resp, err := s.requestUpdates()
		if err != nil {
			if !s.reconnectAfterPollError(ctx) {
				return
			}

			continue
		}

		if !s.publishClientUpdates(ctx, resp.JobUpdates) {
			return
		}
	}
}

func (s *Subscription) requestUpdates() (*serverResponse, error) {
	encoded, method, encodeErr := s.encodeUpdateRequest()
	if encodeErr != nil {
		return nil, encodeErr
	}

	s.sockMu.RLock()
	sock := s.sock
	s.sockMu.RUnlock()

	if err := sock.Send(encoded); err != nil {
		return nil, err
	}

	resp, err := sock.Recv()
	if err != nil {
		return nil, err
	}

	return s.decodeUpdateResponse(method, resp)
}

func (s *Subscription) encodeUpdateRequest() ([]byte, string, error) {
	var encoded []byte

	cr := s.updateRequest()
	enc := codec.NewEncoderBytes(&encoded, s.ch)

	if err := enc.Encode(cr); err != nil {
		return nil, "", err
	}

	return encoded, cr.Method, nil
}

func (s *Subscription) updateRequest() *clientRequest {
	return &clientRequest{
		Method:         requestMethodWaitForUpdates,
		SubscriptionID: s.id,
		Token:          s.client.token,
		ClientID:       s.client.clientid,
		Timeout:        serverSubscriptionHoldTime,
	}
}

func (s *Subscription) decodeUpdateResponse(method string, resp []byte) (*serverResponse, error) {
	sr := &serverResponse{}
	dec := codec.NewDecoderBytes(resp, s.ch)

	if err := dec.Decode(sr); err != nil {
		return nil, err
	}

	if sr.Err != "" {
		return nil, Error{method, "", sr.Err}
	}

	return sr, nil
}

func (s *Subscription) publishClientUpdates(ctx context.Context, updates []*JobUpdate) bool {
	for _, update := range updates {
		if !s.publishClientUpdate(ctx, update) {
			return false
		}
	}

	return true
}

func (s *Subscription) reconnectAfterPollError(ctx context.Context) bool {
	if s.isStopping() {
		s.finish(nil)

		return false
	}

	catchUp, ok := s.reconnect(ctx)
	if !ok {
		return false
	}

	if !s.publishClientUpdate(ctx, &JobUpdate{Kind: JobUpdateResync}) {
		return false
	}

	return s.publishClientUpdates(ctx, catchUp)
}

func (s *Subscription) reconnect(ctx context.Context) ([]*JobUpdate, bool) {
	retryEnd := time.Now().Add(s.client.retryTime)

	for {
		if s.isStopping() {
			s.finish(nil)

			return nil, false
		}

		catchUp, err := s.reconnectOnce(retryEnd)
		if err == nil {
			return catchUp, true
		}

		if time.Now().After(retryEnd) {
			s.finish(ErrSubscriptionClosed)

			return nil, false
		}

		if !s.waitBeforeReconnect(ctx) {
			return nil, false
		}
	}
}

func (s *Subscription) reconnectOnce(retryEnd time.Time) ([]*JobUpdate, error) {
	remaining, err := subscriptionBudgetRemaining(retryEnd)
	if err != nil {
		return nil, err
	}

	if err = s.client.reconnect(min(remaining, subscriptionReconnectTimeout)); err != nil {
		return nil, err
	}

	dialAddr := s.client.subscriptionDialAddr()

	sock, err := dialSubscriptionSocket(dialAddr, s.client.args[1], s.client.args[2],
		serverSubscriptionHoldTime+subscriptionSocketRecvMargin)
	if err != nil {
		return nil, err
	}

	resp, err := s.resubscribeWithinBudget(retryEnd)
	if err != nil {
		_ = sock.Close()

		return nil, err
	}

	if !s.replaceSock(sock, resp.SubscriptionID, dialAddr) {
		return nil, s.unsubscribeRejectedReplacement(resp.SubscriptionID, retryEnd)
	}

	return resp.JobUpdates, nil
}

// subscriptionBudgetRemaining returns how long is left of the reconnect retry
// budget ending at retryEnd, so a step of a reconnect attempt can be bounded by
// it and cannot outlive it. It returns errRetryBudgetSpent once nothing is
// left: no timeout value would bound the step then, since both requestWithin
// and a mangos socket read a non-positive one as "no bound at all", so the step
// must not be taken.
//
// It does not bound a whole attempt: dialling the replacement subscription
// socket carries only that socket's own deadline, and mangos's first Dial is
// synchronous with no timeout of its own.
func subscriptionBudgetRemaining(retryEnd time.Time) (time.Duration, error) {
	remaining := time.Until(retryEnd)
	if remaining <= 0 {
		return 0, errRetryBudgetSpent
	}

	return remaining, nil
}

// resubscribeWithinBudget sends the resubscribe request, bounded by whatever is
// left of the reconnect retry budget ending at retryEnd.
//
// The resubscribe must not outlive that budget: a manager part-way through
// shutdown can answer the connect-time Ping of the attempt this is part of and
// then stop answering anything. requestWithin only ever narrows, so the
// production 24h budget leaves the client's usual generous receive floor in
// place and a slow-but-alive manager is still not mistaken for a dead one.
//
// The budget is re-read here rather than passed in from the top of the attempt
// because the connect and dial steps before this one consume it: a budget that
// ran out in the meantime would ask requestWithin for no bound at all, handing
// this request the very ClientMinRequestTimeout floor the cap exists to avoid.
func (s *Subscription) resubscribeWithinBudget(retryEnd time.Time) (*serverResponse, error) {
	remaining, err := subscriptionBudgetRemaining(retryEnd)
	if err != nil {
		return nil, err
	}

	return s.client.requestWithin(s.subscribeRequest(), remaining)
}

func (s *Subscription) unsubscribeRejectedReplacement(subscriptionID string, retryEnd time.Time) error {
	if _, err := s.client.requestWithin(&clientRequest{
		Method:         requestMethodUnsubscribe,
		SubscriptionID: subscriptionID,
	}, rejectedReplacementUnsubscribeTimeout(retryEnd)); err != nil {
		return errors.Join(ErrSubscriptionClosed, err)
	}

	return ErrSubscriptionClosed
}

// rejectedReplacementUnsubscribeTimeout bounds the unsubscribe that removes a
// replacement subscription the client registered and then rejected. That is a
// step of a reconnect attempt, so whatever is left of the attempt's retry
// budget bounds it: on plain request() it waits on the socket's
// ClientMinRequestTimeout floor instead, holding the poll goroutine for a
// minute past a budget of milliseconds. requestWithin only ever narrows, so the
// production ClientRetryTime budget leaves the step exactly as it was.
//
// A spent budget still sends it, bounded by subscriptionReconnectTimeout,
// rather than skipping it. Nothing else can remove that replacement: its id is
// known only here (Unsubscribe sends the id the subscription is still holding),
// and the manager has no reaper for a registration. Skipping would strand a
// serverSubscription, its delivery goroutine and its queues for the manager's
// lifetime, and leave hasAnyClientSubscriptions permanently true, which costs
// every job transition the zero-subscriber early-out it depends on.
func rejectedReplacementUnsubscribeTimeout(retryEnd time.Time) time.Duration {
	remaining, err := subscriptionBudgetRemaining(retryEnd)
	if err != nil {
		return subscriptionReconnectTimeout
	}

	return remaining
}

func (s *Subscription) subscribeRequest() *clientRequest {
	req := &clientRequest{
		Method: requestMethodSubscribe,
		Keys:   append([]string(nil), s.keys...),
	}

	if s.repGroup != "" {
		req.Job = &Job{RepGroup: s.repGroup}
	}

	return req
}

func (s *Subscription) waitBeforeReconnect(ctx context.Context) bool {
	timer := time.NewTimer(subscriptionReconnectWait(s.client.retryWait))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		s.requestStop(ctx.Err())
		s.finish(ctx.Err())

		return false
	case <-s.stop:
		s.finish(nil)

		return false
	case <-timer.C:
		return true
	}
}

func subscriptionReconnectWait(retryWait time.Duration) time.Duration {
	if retryWait <= 0 {
		return subscriptionMinReconnectWait
	}

	return retryWait
}

func (s *Subscription) publishClientUpdate(ctx context.Context, update *JobUpdate) bool {
	select {
	case s.updates <- update:
		return true
	case <-ctx.Done():
		s.requestStop(ctx.Err())
		s.finish(ctx.Err())

		return false
	case <-s.stop:
		s.finish(nil)

		return false
	}
}

func (s *Subscription) requestStop(err error) {
	s.stopOnce.Do(func() {
		s.setErr(err)

		close(s.stop)
		s.closeSock()
	})
}

func (s *Subscription) setErr(err error) {
	if err == nil {
		return
	}

	s.errMu.Lock()
	s.err = err
	s.errMu.Unlock()
}

func (s *Subscription) isStopping() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *Subscription) finish(err error) {
	s.doneOnce.Do(func() {
		if err != nil {
			s.errMu.Lock()
			if s.err == nil {
				s.err = err
			}
			s.errMu.Unlock()
		}

		close(s.updates)
		close(s.closed)
	})
}

func (s *Subscription) closeSock() {
	s.sockMu.RLock()
	sock := s.sock
	s.sockMu.RUnlock()

	if sock != nil {
		_ = sock.Close()
	}
}

func (s *Subscription) replaceSock(sock mangos.Socket, id, dialAddr string) bool {
	s.sockMu.Lock()
	if s.isStopping() {
		s.sockMu.Unlock()

		_ = sock.Close()

		return false
	}

	oldSock := s.sock
	s.sock = sock
	s.id = id
	s.dialAddr = dialAddr
	s.sockMu.Unlock()

	if oldSock != nil {
		_ = oldSock.Close()
	}

	return true
}

// SubscribeToJobKeys subscribes to updates for the given job keys.
func (c *Client) SubscribeToJobKeys(ctx context.Context, keys []string) (*Subscription, error) {
	if len(keys) == 0 {
		return nil, Error{requestMethodSubscribe, "", ErrBadRequest}
	}

	cr := &clientRequest{Method: requestMethodSubscribe, Keys: append([]string(nil), keys...)}

	return c.subscribe(ctx, cr)
}

// SubscribeToRepGroup subscribes to updates for a single exact RepGroup.
func (c *Client) SubscribeToRepGroup(ctx context.Context, repGroup string) (*Subscription, error) {
	if repGroup == "" {
		return nil, Error{requestMethodSubscribe, "", ErrBadRequest}
	}

	cr := &clientRequest{Method: requestMethodSubscribe, Job: &Job{RepGroup: repGroup}}

	return c.subscribe(ctx, cr)
}

func (c *Client) subscribe(ctx context.Context, cr *clientRequest) (*Subscription, error) {
	resp, err := c.request(cr)
	if err != nil {
		return nil, err
	}

	dialAddr := c.subscriptionDialAddr()

	sock, err := dialSubscriptionSocket(dialAddr, c.args[1], c.args[2],
		serverSubscriptionHoldTime+subscriptionSocketRecvMargin)
	if err != nil {
		return nil, c.unsubscribeAfterDialFailure(resp.SubscriptionID, err)
	}

	keys, repGroup := subscriptionScope(cr)
	sub := newSubscription(c, sock, resp.SubscriptionID, dialAddr, keys, repGroup)

	go sub.poll(ctx, resp.JobUpdates)
	go sub.stopWhenContextDone(ctx)

	return sub, nil
}

//nolint:ireturn // req.NewSocket exposes subscription sockets only as the mangos.Socket interface.
func dialSubscriptionSocket(addr, caFile, certDomain string, timeout time.Duration) (mangos.Socket, error) {
	sock, err := req.NewSocket()
	if err != nil {
		return nil, err
	}

	if err = configureSubscriptionSocket(sock, timeout); err != nil {
		_ = sock.Close()

		return nil, err
	}

	dialOpts := subscriptionDialOptions(caFile, certDomain)

	if err = sock.DialOptions("tls+tcp://"+addr, dialOpts); err != nil {
		_ = sock.Close()

		return nil, err
	}

	return sock, nil
}

func subscriptionScope(cr *clientRequest) ([]string, string) {
	keys := append([]string(nil), cr.Keys...)

	if cr.Job == nil {
		return keys, ""
	}

	return keys, cr.Job.RepGroup
}

func addAndWaitError(
	ctx context.Context,
	waitErr error,
	keys []string,
	seen map[string]JobState,
	fetchErr error,
) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		waitErr = fmt.Errorf("%w; unfinished job keys: %s", ctxErr, strings.Join(unfinishedKeys(keys, seen), ", "))
	}

	return errors.Join(waitErr, fetchErr)
}

func unfinishedKeys(keys []string, seen map[string]JobState) []string {
	unfinished := make([]string, 0, len(keys)-len(seen))

	for _, key := range keys {
		if _, ok := seen[key]; !ok {
			unfinished = append(unfinished, key)
		}
	}

	return unfinished
}

func closedSubscriptionError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return ErrSubscriptionClosed
}

func configureSubscriptionSocket(sock mangos.Socket, timeout time.Duration) error {
	if err := sock.SetOption(mangos.OptionMaxRecvSize, 0); err != nil {
		return err
	}

	if err := sock.SetOption(mangos.OptionRecvDeadline, timeout); err != nil {
		return err
	}

	return nil
}

func subscriptionDialOptions(caFile, certDomain string) map[string]any {
	return map[string]any{
		mangos.OptionTLSConfig: subscriptionTLSConfig(caFile, certDomain),
	}
}

func subscriptionTLSConfig(caFile, certDomain string) *tls.Config {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: certDomain}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return tlsConfig
	}

	certPool := x509.NewCertPool()

	if certPool.AppendCertsFromPEM(caCert) {
		tlsConfig.RootCAs = certPool
	}

	return tlsConfig
}

func collectDistinctTerminalKeys(
	ctx context.Context,
	updates <-chan *JobUpdate,
	keys []string,
) (map[string]JobState, error) {
	wanted := terminalKeySet(keys)
	seen := make(map[string]JobState, len(wanted))

	for len(seen) < len(wanted) {
		update, err := receiveJobUpdate(ctx, updates)
		if err != nil {
			return seen, err
		}

		recordTerminalKey(update, wanted, seen)
	}

	return seen, nil
}

func terminalKeySet(keys []string) map[string]struct{} {
	wanted := make(map[string]struct{}, len(keys))

	for _, key := range keys {
		wanted[key] = struct{}{}
	}

	return wanted
}

func distinctKeysInOrder(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	distinct := make([]string, 0, len(keys))

	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}

		seen[key] = struct{}{}

		distinct = append(distinct, key)
	}

	return distinct
}

// AddAndWait adds jobs, then blocks until every just-added job reaches a
// terminal state. Returned jobs are re-fetched with stdout/stderr populated
// where wr stores them. Complete and buried jobs are both successful returns;
// ctx cancellation returns the terminal jobs gathered so far plus an error
// naming the unfinished keys.
func (c *Client) AddAndWait(ctx context.Context, jobs []*Job, envVars []string, ignoreComplete bool) ([]*Job, error) {
	jobsDone, _, err := c.AddAndWaitWithWarnings(ctx, jobs, envVars, ignoreComplete)

	return jobsDone, err
}

// AddAndWaitWithWarnings is like AddAndWait, and also returns non-fatal
// warnings from the add step.
func (c *Client) AddAndWaitWithWarnings(
	ctx context.Context,
	jobs []*Job,
	envVars []string,
	ignoreComplete bool,
) (jobsDone []*Job, warnings AddWarnings, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, AddWarnings{}, ctxErr
	}

	keys, warnings, err := c.AddAndReturnIDsWithWarnings(jobs, envVars, ignoreComplete)
	if err != nil {
		return nil, AddWarnings{}, err
	}

	jobsDone, err = c.waitForAddedJobKeys(ctx, keys)

	return jobsDone, warnings, err
}

func (c *Client) waitForAddedJobKeys(ctx context.Context, keys []string) ([]*Job, error) {
	keys = distinctKeysInOrder(keys)
	if len(keys) == 0 {
		return []*Job{}, nil
	}

	sub, err := c.SubscribeToJobKeys(ctx, keys)
	if err != nil {
		return nil, err
	}
	defer sub.Unsubscribe()

	seen, err := collectDistinctTerminalKeys(ctx, sub.Updates(), keys)
	if err != nil {
		terminalJobs, fetchErr := c.fetchSeenTerminalJobs(keys, seen)

		return terminalJobs, addAndWaitError(ctx, err, keys, seen, fetchErr)
	}

	return c.fetchSeenTerminalJobs(keys, seen)
}

func (c *Client) fetchSeenTerminalJobs(keys []string, seen map[string]JobState) ([]*Job, error) {
	jobs := make([]*Job, 0, len(seen))

	for _, key := range keys {
		if _, ok := seen[key]; !ok {
			continue
		}

		job, err := c.GetByEssence(&JobEssence{JobKey: key}, true, false)
		if err != nil {
			return jobs, err
		}

		if job != nil {
			jobs = append(jobs, job)
		}
	}

	return jobs, nil
}

func (c *Client) subscriptionDialAddr() string {
	dialAddr := c.args[0]

	if c.ServerInfo != nil && c.ServerInfo.Addr != "" {
		dialAddr = c.ServerInfo.Addr
	}

	return dialAddr
}

func (c *Client) unsubscribeAfterDialFailure(subscriptionID string, dialErr error) error {
	if _, unsubscribeErr := c.request(&clientRequest{
		Method:         requestMethodUnsubscribe,
		SubscriptionID: subscriptionID,
	}); unsubscribeErr != nil {
		return errors.Join(dialErr, unsubscribeErr)
	}

	return dialErr
}

func (c *Client) reconnect(timeout time.Duration) error {
	newClient, err := Connect(c.args[0], c.args[1], c.args[2], c.token, timeout)
	if err != nil {
		return err
	}

	c.Lock()
	oldSock := c.sock
	c.sock = newClient.sock
	c.ServerInfo = newClient.ServerInfo
	c.Unlock()

	if oldSock != nil {
		_ = oldSock.Close()
	}

	return nil
}
