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
	"os"
	"sync"
	"time"

	"github.com/ugorji/go/codec"
	"nanomsg.org/go-mangos"
	"nanomsg.org/go-mangos/protocol/req"
	"nanomsg.org/go-mangos/transport/tlstcp"
)

const (
	serverSubscriptionQueueSize  = 1024
	serverSubscriptionHoldTime   = 25 * time.Second
	subscriptionSocketRecvMargin = 5 * time.Second
)

// ErrSubscriptionClosed is returned by Subscription.Err after an unrecoverable
// subscription disconnect.
var ErrSubscriptionClosed = errors.New("subscription closed")

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
}

// Subscription is a client-side handle for job completion updates.
type Subscription struct {
	client    *Client
	sock      mangos.Socket
	ch        codec.Handle
	updates   chan *JobUpdate
	stop      chan struct{}
	closed    chan struct{}
	id        string
	dialAddr  string
	stopOnce  sync.Once
	unsubOnce sync.Once
	doneOnce  sync.Once
	errMu     sync.RWMutex
	err       error
}

func newSubscription(c *Client, sock mangos.Socket, id, dialAddr string) *Subscription {
	return &Subscription{
		client:   c,
		sock:     sock,
		ch:       new(codec.BincHandle),
		updates:  make(chan *JobUpdate, serverSubscriptionQueueSize),
		stop:     make(chan struct{}),
		closed:   make(chan struct{}),
		id:       id,
		dialAddr: dialAddr,
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
		_, unsubErr = s.client.request(&clientRequest{Method: "unsubscribe", SubscriptionID: s.id})
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
			s.finishAfterPollError()

			return
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

	if err := s.sock.Send(encoded); err != nil {
		return nil, err
	}

	resp, err := s.sock.Recv()
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
		Method:         "waitForUpdates",
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

func (s *Subscription) finishAfterPollError() {
	if s.isStopping() {
		s.finish(nil)

		return
	}

	s.finish(ErrSubscriptionClosed)
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
		_ = s.sock.Close()
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

// SubscribeToJobKeys subscribes to updates for the given job keys.
func (c *Client) SubscribeToJobKeys(ctx context.Context, keys []string) (*Subscription, error) {
	if len(keys) == 0 {
		return nil, Error{"subscribe", "", ErrBadRequest}
	}

	cr := &clientRequest{Method: "subscribe", Keys: append([]string(nil), keys...)}

	return c.subscribe(ctx, cr)
}

// SubscribeToRepGroup subscribes to updates for a single exact RepGroup.
func (c *Client) SubscribeToRepGroup(ctx context.Context, repGroup string) (*Subscription, error) {
	if repGroup == "" {
		return nil, Error{"subscribe", "", ErrBadRequest}
	}

	cr := &clientRequest{Method: "subscribe", Job: &Job{RepGroup: repGroup}}

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

	sub := newSubscription(c, sock, resp.SubscriptionID, dialAddr)

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

	sock.AddTransport(tlstcp.NewTransport())

	dialOpts := subscriptionDialOptions(caFile, certDomain)

	if err = sock.DialOptions("tls+tcp://"+addr, dialOpts); err != nil {
		_ = sock.Close()

		return nil, err
	}

	return sock, nil
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

func subscriptionDialOptions(caFile, certDomain string) map[string]interface{} {
	return map[string]interface{}{mangos.OptionTLSConfig: subscriptionTLSConfig(caFile, certDomain)}
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

func (c *Client) subscriptionDialAddr() string {
	dialAddr := c.args[0]

	if c.ServerInfo != nil && c.ServerInfo.Addr != "" {
		dialAddr = c.ServerInfo.Addr
	}

	return dialAddr
}

func (c *Client) unsubscribeAfterDialFailure(subscriptionID string, dialErr error) error {
	if _, unsubscribeErr := c.request(&clientRequest{
		Method:         "unsubscribe",
		SubscriptionID: subscriptionID,
	}); unsubscribeErr != nil {
		return errors.Join(dialErr, unsubscribeErr)
	}

	return dialErr
}
