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

// This file contains how a Client that opted in rides out the manager being
// unreachable.

import (
	"context"
	"errors"
	"time"

	"github.com/VertebrateResequencing/wr/backoff"
	backofftime "github.com/VertebrateResequencing/wr/backoff/time"
	"github.com/VertebrateResequencing/wr/clog"
	"go.nanomsg.org/mangos/v3"
)

const (
	// outageRetryMinWait is the first wait before a request is sent again
	// during an outage; later waits double up to the client's retryWait.
	outageRetryMinWait = 250 * time.Millisecond
	outageRetryFactor  = 2

	// outageRetryFloor is the shortest wait between attempts, whatever the
	// client's retryWait, so that one of 0 cannot make a request spin.
	outageRetryFloor = 10 * time.Millisecond

	// outageWarnInterval is how often a request still retrying during an
	// outage logs another warning after its first.
	outageWarnInterval = time.Minute

	requestMethodShutdown = "shutdown"
)

// requestOutage is the spell during which a request could not reach the
// manager.
type requestOutage struct {
	start    time.Time
	method   string
	logCtx   context.Context //nolint:containedctx // only its log handler is used
	warned   time.Time
	attempts int
}

// warn records an attempt that failed with err, logging a warning if it is the
// first or the last warning was long enough ago.
func (o *requestOutage) warn(err error, retryTime time.Duration) {
	o.attempts++

	if !o.warned.IsZero() && time.Since(o.warned) < outageWarnInterval {
		return
	}

	o.warned = time.Now()

	clog.Warn(o.logCtx, "manager unreachable; retrying request", "method", o.method, "err", err,
		"attempts", o.attempts, "unreachable_for", time.Since(o.start), "retry_time", retryTime)
}

// recovered logs that the request reached the manager again, if an earlier
// attempt failed to and err, what the last attempt returned, is the manager's
// answer.
func (o *requestOutage) recovered(err error) {
	var jqErr Error
	if o.attempts == 0 || (err != nil && !errors.As(err, &jqErr)) {
		return
	}

	clog.Info(o.logCtx, "manager reachable again; retried request answered", "method", o.method, "err", err,
		"attempts", o.attempts+1, "unreachable_for", time.Since(o.start))
}

// outageRetry is what a Client that rides out outages needs to know.
type outageRetry struct {
	logCtx context.Context //nolint:containedctx // only its log handler is used, for warnings about outages
}

// rideOutOutage calls attempt, and while it reports that it failed only because
// the manager could not be reached, calls it again after a backoff, logging
// warnings about method as it goes. It returns the last attempt's error once
// an attempt reports otherwise, or once the manager has been unreachable for
// longer than its RetryTime. It gives up early, returning ctx's error joined to
// the last attempt's, if ctx is done; an attempt in progress is not
// interrupted.
func (c *Client) rideOutOutage(ctx context.Context, retry *outageRetry, method string,
	attempt func() (retryable bool, err error),
) error {
	outage := requestOutage{start: time.Now(), method: method, logCtx: retry.logCtx}
	wait := outageBackoff(c.currentRetryWait())

	for {
		retryable, err := attempt()
		if !retryable {
			outage.recovered(err)

			return err
		}

		retryTime := c.currentRetryTime()
		outage.warn(err, retryTime)

		if time.Since(outage.start) > retryTime {
			return err
		}

		wait.Sleep(ctx)

		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Join(ctxErr, err)
		}
	}
}

// requestRidingOutOutages is request() for a client that rides out outages
// (see RetryWhileManagerUnreachable). It holds the client's lock only for each
// attempt, not while waiting between them, so other requests are not held up
// for the whole outage. It gives up early, returning ctx's error joined to the
// last attempt's, if ctx is done.
func (c *Client) requestRidingOutOutages(ctx context.Context, retry *outageRetry,
	cr *clientRequest,
) (*serverResponse, error) {
	var sr *serverResponse

	err := c.rideOutOutage(ctx, retry, cr.Method, func() (bool, error) {
		var err error

		sr, err = c.requestOnce(cr)
		if !mayRetryDuringOutage(cr, err) {
			return false, err
		}

		// the manager may have acted on the copy whose reply did not arrive
		if errors.Is(err, mangos.ErrRecvTimeout) {
			cr.resent = true
		}

		return true, err
	})

	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		return nil, err
	}

	return sr, err
}

// outageBackoff returns the backoff between attempts at a request during an
// outage, growing to retryWait.
func outageBackoff(retryWait time.Duration) *backoff.Backoff {
	retryWait = max(retryWait, outageRetryFloor)

	return &backoff.Backoff{
		Min:     min(outageRetryMinWait, retryWait),
		Max:     retryWait,
		Factor:  outageRetryFactor,
		Sleeper: &backofftime.Sleeper{},
	}
}

// mayRetryDuringOutage reports whether cr, which failed with err, may be sent
// again because the manager could not be reached.
func mayRetryDuringOutage(cr *clientRequest, err error) bool {
	switch {
	case err == nil, cr.Method == requestMethodShutdown:
		return false
	case errors.Is(err, mangos.ErrRecvTimeout):
		return isResendSafe(cr)
	default:
		return managerDidNotTakeRequest(err)
	}
}

// isResendSafe reports whether cr has the same effect on the manager if it acts
// on it twice, so may be sent again when its first copy may have been acted on.
// An add is only if it skips complete jobs: otherwise a job the first copy
// added and that has since completed would be added, and run, again.
func isResendSafe(cr *clientRequest) bool {
	switch cr.Method {
	case requestMethodAdd:
		return cr.IgnoreComplete
	case requestMethodGetByCmd, requestMethodGetByRepGroup, requestMethodGetIncomplete,
		requestMethodGetRecent, requestMethodGetRepGroupStatus, requestMethodGetLastCompletion,
		requestMethodGetLimitGroups, requestMethodKill, requestMethodDelete:
		return true
	default:
		return false
	}
}

// managerDidNotTakeRequest reports whether err from a request means the manager
// could not be reached or refused it before acting on it, so it was not acted
// on. A request times out sending when there is no connection to send on. A
// manager that is stopping answers ErrClosedStop, and one recovering its prior
// state may answer ErrRecovering. mangos.ErrClosed is not included: the client
// holds its lock for a whole attempt, so it only means this client was
// disconnected.
func managerDidNotTakeRequest(err error) bool {
	if errors.Is(err, mangos.ErrSendTimeout) {
		return true
	}

	var jqErr Error
	if !errors.As(err, &jqErr) {
		return false
	}

	return jqErr.Err == ErrClosedStop || jqErr.Err == ErrRecovering
}

// RetryWhileManagerUnreachable makes this client's requests ride out the
// manager being unreachable, as across a restart, instead of failing after the
// connect timeout. Connecting is not affected: Connect and its relatives still
// fail if the manager cannot be reached within their timeout.
//
// A request that fails because it could not be sent, or because the manager
// refused it while stopping (ErrClosedStop) or while recovering its prior state
// (ErrRecovering), was not acted on, so it is sent again after a wait that
// starts at a fraction of a second and grows, with jitter, to the manager's
// RetryWait. That holds for every method except ShutdownServer, which is never
// sent again. A request whose reply did not arrive in time (mangos.ErrRecvTimeout)
// may have been acted on, so it is sent again only for requests that have the
// same effect if applied twice: adds that skip complete jobs (ignoreComplete
// true; their jobs are identified by key, and a resent add can report the jobs
// the first copy added as existing, when AddDuplicates.Resent reports true),
// the job getters, Kill and Delete (whose counts can then be lower than the
// jobs they acted on). Other requests, such
// as an add that re-adds complete jobs, Reserve, Archive or Modify, return the
// receive timeout at once. GetSchedulerAlerts, which uses the manager's web
// interface, is sent again only if it cannot have reached the manager, or the
// manager was not yet ready for it (see its doc).
//
// The request keeps being retried until the manager has been unreachable for
// longer than its RetryTime (ServerInfo.RetryTime, 24h by default), and then
// returns the last error, so a call can now take up to that long. An error that
// is the manager's answer is returned at once. A token reload (see
// ConnectWithTokenFile) still happens on each attempt.
//
// Warnings that the manager is unreachable are logged with the log handler in
// logCtx (see clog.ContextWithLogHandler) when a request first fails, then at
// most once a minute while it keeps failing, and an info message once a
// retried request reaches the manager again. logCtx's cancellation is ignored.
//
// Subscription polling (AddAndWait's wait), Ping and Unsubscribe are not
// affected: they have their own retries or bounds.
func (c *Client) RetryWhileManagerUnreachable(logCtx context.Context) {
	c.outageRetry.Store(&outageRetry{logCtx: context.WithoutCancel(logCtx)})
}

// requestOnce is one attempt at request(), taking the client's lock for it.
func (c *Client) requestOnce(cr *clientRequest) (*serverResponse, error) {
	c.Lock()
	defer c.Unlock()

	return c.requestLocked(cr)
}
