// Copyright © 2017, 2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

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
    server, msg, err := jobqueue.Serve(jobqueue.ServerConfig{
        Port:            "12345",
        WebPort:         "12346",
        SchedulerName:   "local",
        SchedulerConfig: &jqs.ConfigLocal{Shell: "bash"},
        RunnerCmd:       selfExe + " runner -s '%s' --deployment %s --server '%s' -r %d -m %d",
        DBFile:          "/home/username/.wr_production/boltdb",
        DBFileBackup:    "/home/username/.wr_production/boltdb.backup",
        CertFile:        "/home/username/.wr_production/cert.pem",
        KeyFile:         "/home/username/.wr_production/key.pem",
        Deployment:      "production",
        CIDR:            "",
    })
    err = server.Block()

Client

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

    jq, err := jobqueue.Connect("localhost:12345", 30 * time.Second)
    inserts, dups, err := jq.Add(jobs, os.Environ())
*/
package jobqueue
