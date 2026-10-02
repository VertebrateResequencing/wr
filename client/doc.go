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

A call made while the manager is down fails after SchedulerSettings.Timeout
with mangos.ErrSendTimeout, unless the manager is back within that time. A
request that was already sent waits for its reply for up to the larger of
Timeout and a minute. A submission made while the manager was down that failed
this way added nothing.

A wait already in progress rides out the outage: WaitForJobs, the wait in
SubmitJobsAndWait, and WaitForRunning after its first poll keep trying to reach
the manager, and return normally once the jobs reach the state waited for,
without the program calling again. They keep trying for up to the manager's
RetryTime (jobqueue.ServerTimings.RetryTime, 24h by default). If the manager
stays down for longer, WaitForJobs and SubmitJobsAndWait return an error
matching jobqueue.ErrSubscriptionClosed, and WaitForRunning returns its last
poll's error. Cancel the wait's ctx to give up sooner.

A submission in progress when the manager stops can be partly or wholly added.
The client resends it when it reconnects, so SubmitJobs can then return
ErrDuplicateJobs for jobs that only this one call added. If the outage outlasts
the wait for the reply, the call fails with mangos.ErrRecvTimeout, and the
program cannot tell how many of the jobs were added. To recover from either,
call SubmitJobsAndReturnIDs with the same jobs and the default
SubmitJobsOptions. It adds only the jobs that are neither queued nor complete,
and returns the keys of the jobs now queued. A job that has already completed
is neither added again nor in the result; Job.Key gives any job's key without
asking the manager. Do not set RerunCompleted for this: it adds completed jobs
again, so they run twice.
*/
package client
