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

/*
Package client provides Scheduler, which a Go program uses to add commands to
the queue of a running wr manager, wait for them, and find them again.

# Manager restarts

A long-running program can keep one Scheduler across a restart of the manager
(a clean stop, then a start), including one that gives the manager a new token.
New connects with jobqueue.ConnectUsingConfig, which reads the manager's token
file. When the manager rejects a request because its token has changed, the
Scheduler re-reads that file and sends the request again, so the program does
not need to call New again.

New fails if it cannot reach the manager within SchedulerSettings.Timeout. A
Timeout that is not positive means jobqueue.ClientDefaultConnectTimeout (2
minutes) rather than no limit.

Once connected, every call rides out the manager being down, as runners and
waits do, and returns normally once the manager is back, without the program
calling again. A request that cannot be sent within Timeout, or that the
manager refuses because it is stopping or still recovering, is sent again after
a wait that grows to the manager's RetryWait, logging warnings to
SchedulerSettings.Logger while the manager stays unreachable. A request whose
reply does not arrive within the larger of Timeout and a minute may have been
acted on, so it is sent again only if that is safe: lookups, KillJobs,
RemoveJobs, and submissions that skip complete jobs (SubmitJobsAndReturnIDs and
SubmitJobsAndWait without RerunCompleted). Others, including SubmitJobs, which
re-adds complete jobs, and the subscription WaitForJobs and SubmitJobsAndWait
start their wait with, fail with mangos.ErrRecvTimeout. A call keeps trying
for up to the manager's RetryTime (jobqueue.ServerTimings.RetryTime, 24h by
default), so it can now take that long, and then returns the last error, such
as mangos.ErrSendTimeout. An error that is the manager's answer, such as a bad
request, is returned at once. WaitForJobs and the wait in SubmitJobsAndWait end
with an error matching jobqueue.ErrSubscriptionClosed if they cannot reconnect
within the manager's RetryTime.

GetSchedulerAlerts reads the manager's web interface instead, and reading the
alerts dismisses them, so it is sent again only if none of it reached the
manager, as when it could not connect, or if the manager rejected it while
starting again, before writing its new token file. Any other failure, such as a
dropped connection or a reply that does not arrive within the smaller of
Timeout and 30 seconds, is returned at once.

To give up sooner, use a call that takes a context: WaitForRunning,
WaitForJobs, SubmitJobsAndWait, or the Context variant of any other call that
talks to the manager, such as SubmitJobsContext for SubmitJobs. The call a
Context variant is named after is that variant with context.Background().
Cancelling ctx, or its deadline passing, ends the ride-out with an error
matching ctx's (errors.Is(err, context.Canceled), for example). An attempt to
reach the manager in progress is not interrupted, so the call can return up to
Timeout after ctx is done, or up to the reply deadline (the larger of Timeout
and a minute) if the request had been sent. A Context variant does not
otherwise check ctx: a call made with a ctx already done still makes one
attempt.

A submission in progress when the manager stops can be partly or wholly added,
and is sent again once the manager is back. The manager then reports the jobs
the first copy added as already existing. SubmitJobs detects when its
submission may have been sent more than once, and then returns nil rather than
ErrDuplicateJobs, since every job is queued. It cannot tell those jobs apart
from identical jobs queued before it was called, so in that case it returns nil
for them too. The same happens if the connection drops while the manager is
up. A submission that re-adds complete jobs (SubmitJobs, or RerunCompleted) is
sent again skipping complete jobs, since a job its first copy added may have
completed since and must not run again. If any of its jobs was then complete,
the call returns an error matching jobqueue.ErrResentAddSkippedComplete: each
such job either completed after this call added it, or had completed before and
was not run again, and the two cannot be told apart. GetJobByKey shows which
jobs are complete; submit any you meant to rerun again.

A submission that failed because the manager stayed down may also have been
partly added. To recover from that, call SubmitJobsAndReturnIDs with the same
jobs and the default SubmitJobsOptions. It adds only the jobs that are neither
queued nor complete, and returns the keys of the jobs now queued. A job that
has already completed is neither added again nor in the result; Job.Key gives
any job's key without asking the manager. Do not set RerunCompleted for this:
it adds completed jobs again, so they run twice.
*/
package client
