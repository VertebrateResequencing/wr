/*******************************************************************************
 * Copyright (c) 2017-2018, 2024 Genome Research Ltd.
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
Package jobqueue provides server/client functions to interact with the queue
structure provided by the queue package over a network.

It provides a job queue and running system which guarantees:

	# Created jobs are never lost accidentally.
	# The same job will not run more than once simultaneously:
	  - Duplicate jobs are not created
	  - Each job is handled by only a single client
	# Jobs are handled in the desired order (user priority and fifo, after
	  dependencies have been satisfied).
	# Jobs still get run despite crashing clients.
	# Completed jobs are kept forever for historical and "live" dependency
	  purposes.

You bring up the server, then use a client to add commands (jobs) to the queue.
The server then interacts with the configured scheduler to start running the
necessary number of runner clients on your compute cluster. The runner clients
ask the server for a command to run, and then they run the command. Once
complete, the server is updated and the runner client requests the next command,
or might exit if there are no more left.

As a user you can query the status of the system using client methods or by
viewing the real-time updated status web interface.

Server

	    import "github.com/VertebrateResequencing/wr/jobqueue"
	    server, msg, token, err := jobqueue.Serve(jobqueue.ServerConfig{
	        Port:            "12345",
	        WebPort:         "12346",
	        SchedulerName:   "local",
	        SchedulerConfig: &jqs.ConfigLocal{Shell: "bash"},
	        RunnerCmd:       selfExe + " runner -s '%s' --deployment %s --server '%s' --domain %s -r %d -m %d",
	        DBFile:          "/home/username/.wr_production/boltdb",
	        DBFileBackup:    "/home/username/.wr_production/boltdb.backup",
			TokenFile:       "/home/username/.wr_production/client.token",
			CAFile:          "/home/username/.wr_production/ca.pem",
	        CertFile:        "/home/username/.wr_production/cert.pem",
	        CertDomain:      "my.internal.domain.com",
	        KeyFile:         "/home/username/.wr_production/key.pem",
	        Deployment:      "production",
	        CIDR:            "",
	    })

	    // Serve() returns while prior-state recovery is still running, so the
	    // server is not reachable yet; wait for it to publish before using it.
	    <-server.Serving()

	    err = server.Block()

# Client

An example client, one for adding commands to the job queue:

	import {
	    "github.com/VertebrateResequencing/wr/jobqueue"
	    jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	}

	var jobs []*jobqueue.Job
	other := make(map[string]string)
	var deps []*jobqueue.Dependency
	deps = append(deps, jobqueue.NewDepGroupDependency("step1"))
	jobs = append(jobs, &jobqueue.Job{
	    RepGroup:     "friendly name",
	    Cmd:          "myexe -args",
	    Cwd:          "/tmp",
	    ReqGroup:     "myexeInArgsMode",
	    Requirements: &jqs.Requirements{RAM: 1024, Time: 10 * time.Minute, Cores: 1, Disk: 1, Other: other},
	    Override:     uint8(0),
	    Priority:     uint8(0),
	    Retries:      uint8(3),
	    DepGroups:    []string{"step2"},
	    Dependencies: deps,
	})

	jq, err := jobqueue.Connect(
	    "localhost:12345",
	    "/home/username/.wr_production/ca.pem",
	    "my.internal.domain.com",
	    token,
	    30 * time.Second
	)
	inserts, dups, err := jq.Add(jobs, os.Environ())
*/
package jobqueue
