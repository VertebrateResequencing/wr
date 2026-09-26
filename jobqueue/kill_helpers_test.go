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

import "time"

// killTestStartWait bounds how long a kill test waits for its command to be
// reported started; it is free on success.
const killTestStartWait = 20 * time.Second

// killTestPollInterval is how often a kill test polls the manager.
const killTestPollInterval = 10 * time.Millisecond

// killOnceStarted waits, for up to killTestStartWait, until the manager has
// the pid of job's command, then kills job, returning how many jobs were killed.
// Since Execute reports the pid only after starting the command, the kill can
// only reach the runner once there is a command to kill.
func killOnceStarted(jq *Client, job *Job) int {
	deadline := time.Now().Add(killTestStartWait)

	for time.Now().Before(deadline) {
		got, err := jq.GetByRepGroup(job.RepGroup, false, 0, JobStateRunning, false, false)
		if err == nil && len(got) == 1 && got[0].Pid != 0 {
			killed, errk := jq.Kill([]*JobEssence{{JobKey: job.Key()}})
			if errk != nil {
				return 0
			}

			return killed
		}

		time.Sleep(killTestPollInterval)
	}

	return 0
}
