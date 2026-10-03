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
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
	"go.nanomsg.org/mangos/v3/protocol/rep"
)

// cliConnectTimeout is the connect timeout wr's commands give a Client by
// default (cmd/root.go defaultManagerConnectTimeout): over the 60s
// ClientMinRequestTimeout floor, so it is also the receive deadline.
const cliConnectTimeout = 120 * time.Second

// longConnectTimeout is a connect timeout far beyond any resend time a fix
// might pick as merely generous, so only one that never comes round passes.
const longConnectTimeout = 10 * time.Hour

// resendTestSendWait is the send deadline of a Client whose next connection is
// held up for longer than it.
const resendTestSendWait = 200 * time.Millisecond

// resendTestReconnectDelay is how long a test holds up a Client's new
// connection after its first is lost.
const resendTestReconnectDelay = 1500 * time.Millisecond

// heldAddWait is how long a copyCountingManager holds its reply to an add in
// the scaled-down tests below: within the receive deadline they give the
// socket, and past the connect timeout.
const heldAddWait = 2 * time.Second

// copyCountingManagerWorkers is how many requests a copyCountingManager
// handles at once: more than one, so that a copy of a request arriving while
// the manager still holds the first is received and counted, as the real
// manager would receive and act on it.
const copyCountingManagerWorkers = 4

// copyCountingManager stands in for a manager on a real TLS rep socket. It
// answers pings at once, and counts every add it receives, holding its reply to
// each for hold. If dropFirst is set it closes the connection the first add
// arrived on without answering it, as a manager that goes away mid-request
// does. It counts every other request too, answering at once with the error in
// answers for its method if there is one, and otherwise holding its reply for
// hold as well. A manager made by startRememberingManager also acts on adds as
// the real manager does; with dropUnactioned it does not act on the first add
// it drops, as when a connection breaks before the add arrives.
type copyCountingManager struct {
	addr           string
	caFile         string
	hold           time.Duration
	dropFirst      bool
	dropUnactioned bool
	holdOnlyFirst  bool
	noBreakdown    bool
	adds           atomic.Int32
	stop           chan struct{}
	answers        map[string]string

	mu              sync.Mutex
	received        map[string]int
	known           map[string]bool
	complete        map[string]bool
	ignoreCompletes []bool
	pipes           []mangos.Pipe
}

// startCopyCountingManager starts a copyCountingManager, stopping it when the
// test ends.
func startCopyCountingManager(t *testing.T, hold time.Duration, dropFirst bool) *copyCountingManager {
	t.Helper()

	return startAnsweringManager(t, hold, dropFirst, nil)
}

// startAnsweringManager is startCopyCountingManager, also giving it the answers
// to give at once, by method.
func startAnsweringManager(t *testing.T, hold time.Duration, dropFirst bool,
	answers map[string]string,
) *copyCountingManager {
	t.Helper()

	return startManager(t, &copyCountingManager{hold: hold, dropFirst: dropFirst, answers: answers})
}

// startRememberingManager is startCopyCountingManager for a manager that acts on
// each add as it arrives, before holding or dropping its reply, remembering
// the keys of the jobs it added and answering as the real manager does: jobs it
// already has are reported as existing and queued. If holdOnlyFirst, only the
// first add's reply is held.
func startRememberingManager(t *testing.T, hold time.Duration, dropFirst, holdOnlyFirst bool) *copyCountingManager {
	t.Helper()

	return startManager(t, &copyCountingManager{
		hold: hold, dropFirst: dropFirst, holdOnlyFirst: holdOnlyFirst, known: make(map[string]bool),
	})
}

// startManager starts m, which has its behaviour set, stopping it when the
// test ends.
func startManager(t *testing.T, m *copyCountingManager) *copyCountingManager {
	t.Helper()

	caFile := generateTestCerts(t)
	dir := filepath.Dir(caFile)

	tlsConfig, err := serverTLSConfig(caFile, filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	So(err, ShouldBeNil)

	sock, err := rep.NewSocket()
	So(err, ShouldBeNil)
	So(sock.SetOption(mangos.OptionMaxRecvSize, 0), ShouldBeNil)

	sock.SetPipeEventHook(m.trackPipe)

	port, err := freeEphemeralTestPort()
	So(err, ShouldBeNil)
	So(listenTLS(sock, tlsConfig, strconv.Itoa(port)), ShouldBeNil)

	m.addr = "localhost:" + strconv.Itoa(port)
	m.caFile = caFile
	m.stop = make(chan struct{})
	m.received = make(map[string]int)

	var wg sync.WaitGroup

	for range copyCountingManagerWorkers {
		sctx, errc := sock.OpenContext()
		So(errc, ShouldBeNil)

		wg.Go(func() { m.serve(sctx) })
	}

	t.Cleanup(func() {
		close(m.stop)

		_ = sock.Close()

		wg.Wait()
	})

	return m
}

// serve answers requests arriving on sctx until the socket closes.
func (m *copyCountingManager) serve(sctx mangos.Context) {
	for {
		msg, err := sctx.RecvMsg()
		if err != nil {
			return
		}

		cr := &clientRequest{}
		if err = codec.NewDecoderBytes(msg.Body, new(codec.BincHandle)).Decode(cr); err != nil {
			msg.Free()

			continue
		}

		sr := &serverResponse{SInfo: &ServerInfo{}}
		dropping := cr.Method == requestMethodAdd && m.adds.Add(1) == 1 && m.dropFirst

		m.recordAdd(cr)

		if !dropping || !m.dropUnactioned {
			m.actOnAdd(cr, sr)
		}

		if dropping {
			_ = msg.Pipe.Close()

			msg.Free()

			continue
		}

		if !m.answer(cr, sr) {
			return
		}

		var encoded []byte
		if err = codec.NewEncoderBytes(&encoded, new(codec.BincHandle)).Encode(sr); err != nil {
			return
		}

		msg.Body = encoded
		if err = sctx.SendMsg(msg); err != nil {
			msg.Free()
		}
	}
}

// answer fills in sr, the reply to cr, after holding it if cr is not a ping or a
// request with an answer in m.answers. It reports false if m was stopped
// first.
func (m *copyCountingManager) answer(cr *clientRequest, sr *serverResponse) bool {
	if cr.Method == requestMethodPing {
		return true
	}

	m.mu.Lock()
	m.received[cr.Method]++
	m.mu.Unlock()

	if answer, ok := m.answers[cr.Method]; ok {
		sr.Err = answer

		return true
	}

	if !m.holdOnlyFirst || cr.Method != requestMethodAdd || m.adds.Load() == 1 {
		select {
		case <-time.After(m.hold):
		case <-m.stop:
			return false
		}
	}

	if m.known == nil {
		sr.Added = len(cr.Jobs)
	}

	return true
}

// recordAdd records cr's IgnoreComplete if it is an add.
func (m *copyCountingManager) recordAdd(cr *clientRequest) {
	if cr.Method != requestMethodAdd {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.ignoreCompletes = append(m.ignoreCompletes, cr.IgnoreComplete)
}

// actOnAdd fills in sr for cr if it is an add and m remembers jobs: jobs m has
// queued are reported as existing and queued, jobs in m.complete as existing
// and complete if cr skips complete jobs, and the rest are added. A manager
// with noBreakdown reports only how many existed, as one predating the
// breakdown does.
func (m *copyCountingManager) actOnAdd(cr *clientRequest, sr *serverResponse) {
	if cr.Method != requestMethodAdd || m.known == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, job := range cr.Jobs {
		switch {
		case m.known[job.Key()]:
			sr.Duplicates.Queued++
			sr.AddedIDs = append(sr.AddedIDs, job.Key())
		case cr.IgnoreComplete && m.complete[job.Key()]:
			sr.Duplicates.Complete++
		default:
			m.known[job.Key()] = true
			sr.Added++
			sr.AddedIDs = append(sr.AddedIDs, job.Key())
		}
	}

	sr.Existed = sr.Duplicates.Total()

	if m.noBreakdown {
		sr.Duplicates = DuplicateBreakdown{}
	}
}

// addIgnoreCompletes returns the IgnoreComplete of each add m has received, in
// order.
func (m *copyCountingManager) addIgnoreCompletes() []bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.ignoreCompletes)
}

// trackPipe records each connection to m as it attaches.
func (m *copyCountingManager) trackPipe(event mangos.PipeEvent, pipe mangos.Pipe) {
	if event != mangos.PipeEventAttached {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.pipes = append(m.pipes, pipe)
}

// closeConnections closes every connection to m, as a manager whose connection
// drops does, with nothing in progress.
func (m *copyCountingManager) closeConnections() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, pipe := range m.pipes {
		_ = pipe.Close()
	}

	m.pipes = nil
}

// receivedCopies returns how many requests of method m has received.
func (m *copyCountingManager) receivedCopies(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.received[method]
}

// connect connects a Client to the manager with the given connect timeout.
func (m *copyCountingManager) connect(timeout time.Duration) *Client {
	jq, err := Connect(m.addr, m.caFile, "localhost", make([]byte, tokenLength), timeout)
	So(err, ShouldBeNil)

	return jq
}

// TestClientDoesNotResendSlowRequests proves that a request a live manager is
// slow to answer reaches it once. mangos's req socket resends any request still
// unanswered after its resend time (one minute unless set), on the same live
// connection, while waiting for the reply up to the receive deadline: with a
// connect timeout over 60s, such as wr's commands' 120s default, the manager
// was handed a second copy of an add, reserve, release or bury it was still
// working on, and acted on both (.docs/bugfixes/261002-client-restart-2ca6d42f.md).
func TestClientDoesNotResendSlowRequests(t *testing.T) {
	Convey("Given a manager that holds its reply to an add", t, func() {
		Convey("a Client connected with the CLI's default timeout never resends a request it is still waiting on", func() {
			m := startCopyCountingManager(t, heldAddWait, false)

			jq := m.connect(cliConnectTimeout)
			defer disconnect(jq)

			// a behavioural run of this at full scale takes over a minute,
			// so here the socket's own resend time is compared with the
			// longest wait for a reply it can be given, which is what decides
			// whether mangos resends before the reply is given up on
			So(resendTime(jq), ShouldBeGreaterThan, recvDeadline(jq))

			added, err := addOne(jq)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 1)
			So(m.adds.Load(), ShouldEqual, 1)
			So(resendTime(jq), ShouldBeGreaterThan, recvDeadline(jq))

			_, finished, err := requestWithinHeldAdd(jq, heldAddWait+time.Second)
			So(finished, ShouldBeTrue)
			So(err, ShouldBeNil)
			So(m.adds.Load(), ShouldEqual, 2)
			So(resendTime(jq), ShouldBeGreaterThan, recvDeadline(jq))
		})

		Convey("a Client connected with a timeout of many hours still never resends", func() {
			m := startCopyCountingManager(t, 0, false)

			jq := m.connect(longConnectTimeout)
			defer disconnect(jq)

			So(recvDeadline(jq), ShouldEqual, longConnectTimeout)
			So(resendTime(jq), ShouldBeGreaterThan, recvDeadline(jq))
		})

		Convey("a reply slower than the connect timeout is waited for without a resend", func() {
			defer setClientMinRequestTimeout(heldAddWait + 2*time.Second)()

			m := startCopyCountingManager(t, heldAddWait, false)

			jq := m.connect(time.Second)
			defer disconnect(jq)

			added, err := addOne(jq)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 1)
			So(m.adds.Load(), ShouldEqual, 1)
		})

		Convey("an add whose connection drops is still resent once the Client redials", func() {
			m := startCopyCountingManager(t, 0, true)

			jq := m.connect(cliConnectTimeout)
			defer disconnect(jq)

			added, err := addOne(jq)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 1)
			So(m.adds.Load(), ShouldEqual, 2)
		})
	})
}

// resendTime returns jq's socket's resend time.
func resendTime(jq *Client) time.Duration {
	return socketDuration(jq, mangos.OptionRetryTime)
}

// socketDuration returns the duration option of jq's socket.
func socketDuration(jq *Client, option string) time.Duration {
	jq.Lock()
	defer jq.Unlock()

	d, err := jq.deadline(option)
	So(err, ShouldBeNil)

	return d
}

// recvDeadline returns jq's socket's receive deadline.
func recvDeadline(jq *Client) time.Duration {
	return socketDuration(jq, mangos.OptionRecvDeadline)
}

// addOne adds a single job with jq, returning what Add returns.
func addOne(jq *Client) (int, error) {
	return addOneJob(jq, true)
}

// addOneJob adds a single job with jq, skipping it if complete only if
// ignoreComplete, returning what Add returns.
func addOneJob(jq *Client, ignoreComplete bool) (int, error) {
	added, _, err := jq.Add(resendTestJobs(), nil, ignoreComplete)

	return added, err
}

// resendTestJobs returns the single job the tests here add.
func resendTestJobs() []*Job {
	return []*Job{{Cmd: "echo resend", Cwd: "/tmp", RepGroup: "resend"}}
}

// requestWithinHeldAdd sends an add with requestWithin, asking for budget, and
// reports what requestWithinLimited does, with a limit past budget.
func requestWithinHeldAdd(jq *Client, budget time.Duration) (time.Duration, bool, error) {
	return requestWithinLimited(jq, &clientRequest{Method: requestMethodAdd}, budget, budget+time.Second)
}

// TestClientReportsResentAdds checks that an Add tells its caller when it may
// have reached the manager more than once, so that jobs an earlier copy of it
// added, which the manager then reports as existing, can be told apart from
// jobs reported as existing by an Add sent once
// (.docs/bugfixes/261002-client-restart-2ca6d42f.md).
func TestClientReportsResentAdds(t *testing.T) {
	Convey("Given a manager that acts on each add as it arrives", t, func() {
		Convey("an Add whose connection drops after the manager acted on it is reported resent", func() {
			m := startRememberingManager(t, 0, true, false)

			jq := m.connect(cliConnectTimeout)
			defer disconnect(jq)

			added, dups, _, err := addResendJob(jq, false)
			So(err, ShouldBeNil)
			So(m.adds.Load(), ShouldEqual, 2)
			So(m.addIgnoreCompletes(), ShouldResemble, []bool{false, true})
			So(added, ShouldEqual, 0)
			So(dups.Total(), ShouldEqual, 1)
			So(dups.Resent(), ShouldBeTrue)

			Convey("while adding the same job again, sent once, is not", func() {
				added, dups, _, err = addResendJob(jq, false)
				So(err, ShouldBeNil)
				So(m.adds.Load(), ShouldEqual, 3)
				So(added, ShouldEqual, 0)
				So(dups.Total(), ShouldEqual, 1)
				So(dups.Resent(), ShouldBeFalse)
			})
		})

		Convey("an Add sent once is not reported resent, whether or not its job existed", func() {
			m := startRememberingManager(t, 0, false, false)

			jq := m.connect(cliConnectTimeout)
			defer disconnect(jq)

			added, dups, _, err := addResendJob(jq, false)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 1)
			So(dups.Resent(), ShouldBeFalse)

			added, dups, _, err = addResendJob(jq, false)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 0)
			So(dups.Total(), ShouldEqual, 1)
			So(dups.Resent(), ShouldBeFalse)
			So(m.adds.Load(), ShouldEqual, 2)
		})

		Convey("an Add sent while the Client reconnects, of a job that already existed, is not reported resent", func() {
			m := startRememberingManager(t, 0, false, false)

			jq := m.connect(cliConnectTimeout)
			defer disconnect(jq)

			_, _, _, err := addResendJob(jq, false)
			So(err, ShouldBeNil)

			detached := awaitDetach(jq)

			m.closeConnections()

			select {
			case <-detached:
			case <-time.After(goneManagerLimit):
				So("connection did not drop", ShouldBeEmpty)
			}

			// the Client redials a tenth of a second after the drop, so this
			// add waits in Send for the new connection
			added, dups, _, err := addResendJob(jq, false)
			So(err, ShouldBeNil)
			So(m.adds.Load(), ShouldEqual, 2)
			So(added, ShouldEqual, 0)
			So(dups.Total(), ShouldEqual, 1)
			So(dups.Resent(), ShouldBeFalse)
		})

		Convey("an Add a Client riding out outages sent again after it could not be sent is not reported resent", func() {
			m := startRememberingManager(t, 0, false, false)

			jq := m.connect(cliConnectTimeout)
			defer disconnect(jq)

			_, _, _, err := addResendJob(jq, false)
			So(err, ShouldBeNil)

			setOutageTimings(jq, outageTestRetryWait, cliConnectTimeout)
			jq.RetryWhileManagerUnreachable(context.Background())

			jq.Lock()
			So(jq.sock.SetOption(mangos.OptionSendDeadline, resendTestSendWait), ShouldBeNil)
			jq.Unlock()

			detached := awaitDetachDelayingAttach(jq, 2*resendTestSendWait)

			m.closeConnections()

			select {
			case <-detached:
			case <-time.After(goneManagerLimit):
				So("connection did not drop", ShouldBeEmpty)
			}

			// the new connection attaches during the first attempt, but is
			// held up until that attempt has timed out sending, so only a later
			// attempt is sent, once
			added, dups, _, err := addResendJob(jq, false)
			So(err, ShouldBeNil)
			So(m.adds.Load(), ShouldEqual, 2)
			So(added, ShouldEqual, 0)
			So(dups.Total(), ShouldEqual, 1)
			So(dups.Resent(), ShouldBeFalse)
		})

		Convey("an Add a Client riding out outages sent again after its reply timed out is reported resent", func() {
			m := startRememberingManager(t, outageTestHold, false, true)

			jq := m.connect(10 * time.Second)
			defer disconnect(jq)

			jq.Lock()
			So(jq.sock.SetOption(mangos.OptionRecvDeadline, outageTestReplyWait), ShouldBeNil)
			jq.Unlock()

			setOutageTimings(jq, outageTestRetryWait, outageTestRetryTime)
			jq.RetryWhileManagerUnreachable(context.Background())

			added, dups, _, err := addResendJob(jq, true)
			So(err, ShouldBeNil)
			So(m.adds.Load(), ShouldEqual, 2)
			So(added, ShouldEqual, 0)
			So(dups.Total(), ShouldEqual, 1)
			So(dups.Resent(), ShouldBeTrue)
		})
	})
}

// addResendJob adds a single job with jq's AddWithDuplicates, returning what
// that returns.
func addResendJob(jq *Client, ignoreComplete bool) (int, AddDuplicates, AddWarnings, error) {
	return jq.AddWithDuplicates(resendTestJobs(), nil, ignoreComplete)
}

// awaitDetach returns a channel closed when jq's connection next drops.
func awaitDetach(jq *Client) <-chan struct{} {
	return awaitDetachDelayingAttach(jq, 0)
}

// awaitDetachDelayingAttach is awaitDetach, also holding up the next connection
// jq makes for delay before mangos can send anything on it.
func awaitDetachDelayingAttach(jq *Client, delay time.Duration) <-chan struct{} {
	detached := make(chan struct{})

	var detachOnce, attachOnce sync.Once

	jq.Lock()
	defer jq.Unlock()

	jq.sock.SetPipeEventHook(func(event mangos.PipeEvent, _ mangos.Pipe) {
		switch event {
		case mangos.PipeEventDetached:
			detachOnce.Do(func() { close(detached) })
		case mangos.PipeEventAttaching:
			attachOnce.Do(func() { time.Sleep(delay) })
		case mangos.PipeEventAttached:
		}
	})

	return detached
}

// TestClientResendsInterruptedRerunAddSkippingComplete checks that an add asked
// to re-add complete jobs, whose connection is lost before its reply arrives,
// is not sent again as it was, since a job its first copy added may have
// completed since and would run again; it is sent again skipping complete
// jobs, and its caller is told if any were complete
// (.docs/bugfixes/261002-client-restart-2ca6d42f.md).
func TestClientResendsInterruptedRerunAddSkippingComplete(t *testing.T) {
	Convey("Given a manager that has a job complete and drops the first add before acting on it", t, func() {
		m := startManager(t, &copyCountingManager{
			dropFirst: true, dropUnactioned: true,
			known: make(map[string]bool), complete: map[string]bool{resendTestJobs()[0].Key(): true},
		})

		jq := m.connect(cliConnectTimeout)
		defer disconnect(jq)

		Convey("an Add re-adding it is sent again skipping it, and says the job may not have been rerun", func() {
			added, dups, _, err := addResendJob(jq, false)
			So(errors.Is(err, ErrResentAddSkippedComplete), ShouldBeTrue)
			So(m.addIgnoreCompletes(), ShouldResemble, []bool{false, true})
			So(added, ShouldEqual, 0)
			So(dups.Resent(), ShouldBeTrue)

			breakdown, ok := dups.Breakdown()
			So(ok, ShouldBeTrue)
			So(breakdown.Complete, ShouldEqual, 1)
		})

		Convey("AddAndReturnIDs re-adding it says the same", func() {
			ids, err := jq.AddAndReturnIDs(resendTestJobs(), nil, false)
			So(errors.Is(err, ErrResentAddSkippedComplete), ShouldBeTrue)
			So(ids, ShouldBeEmpty)
			So(m.addIgnoreCompletes(), ShouldResemble, []bool{false, true})
		})

		Convey("an Add that skips complete jobs is resent by the socket as it was, and succeeds", func() {
			added, existed, err := jq.Add(resendTestJobs(), nil, true)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 0)
			So(existed, ShouldEqual, 1)
			So(m.addIgnoreCompletes(), ShouldResemble, []bool{true, true})
		})
	})

	Convey("Given a manager that does not break duplicates down and drops the first add before acting on it", t, func() {
		Convey("an Add re-adding a job it has complete is reported as maybe not rerun", func() {
			m := startManager(t, &copyCountingManager{
				dropFirst: true, dropUnactioned: true, noBreakdown: true,
				known: make(map[string]bool), complete: map[string]bool{resendTestJobs()[0].Key(): true},
			})

			jq := m.connect(cliConnectTimeout)
			defer disconnect(jq)

			_, existed, err := jq.Add(resendTestJobs(), nil, false)
			So(errors.Is(err, ErrResentAddSkippedComplete), ShouldBeTrue)
			So(existed, ShouldEqual, 1)
		})

		Convey("an Add re-adding a job it does not have is not, as nothing existed", func() {
			m := startManager(t, &copyCountingManager{
				dropFirst: true, dropUnactioned: true, noBreakdown: true, known: make(map[string]bool),
			})

			jq := m.connect(cliConnectTimeout)
			defer disconnect(jq)

			added, dups, _, err := addResendJob(jq, false)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 1)
			So(dups.Resent(), ShouldBeTrue)
			So(m.addIgnoreCompletes(), ShouldResemble, []bool{false, true})
		})
	})
}

// TestClientResendsRerunAddLostBeforeItsReplyWasAwaited checks the two less
// common ways a re-adding add's connection can be lost: before the client has
// started waiting for the reply, and for longer than the connect timeout
// (.docs/bugfixes/261002-client-restart-2ca6d42f.md).
func TestClientResendsRerunAddLostBeforeItsReplyWasAwaited(t *testing.T) {
	Convey("Given a manager that drops the first add before acting on it", t, func() {
		m := startManager(t, &copyCountingManager{
			dropFirst: true, dropUnactioned: true, known: make(map[string]bool),
		})

		Convey("an Add re-adding jobs whose connection is lost before it awaits the reply is sent again", func() {
			jq := m.connect(cliConnectTimeout)
			defer disconnect(jq)

			detached := awaitDetach(jq)

			droppingAddSentHook = func() {
				select {
				case <-detached:
				case <-time.After(goneManagerLimit):
				}
			}

			defer func() { droppingAddSentHook = nil }()

			added, dups, _, err := addResendJob(jq, false)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 1)
			So(dups.Resent(), ShouldBeTrue)
			So(m.addIgnoreCompletes(), ShouldResemble, []bool{false, true})
		})

		Convey("an Add re-adding jobs waits past a short connect timeout for the new connection to send it again", func() {
			defer setClientMinRequestTimeout(3 * resendTestReconnectDelay)()

			jq := m.connect(resendTestReconnectDelay / 3)
			defer disconnect(jq)

			awaitDetachDelayingAttach(jq, resendTestReconnectDelay)

			added, _, _, err := addResendJob(jq, false)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 1)
			So(m.addIgnoreCompletes(), ShouldResemble, []bool{false, true})
			So(socketDuration(jq, mangos.OptionSendDeadline), ShouldEqual, resendTestReconnectDelay/3)
		})
	})
}
