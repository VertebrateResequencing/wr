// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of VRPipe.
//
//  VRPipe is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  VRPipe is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with VRPipe. If not, see <http://www.gnu.org/licenses/>.

/*
Package jobqueue provides server/client functions to interact with the queue
structure provided by the queue package over a network.

This file contains all the functions for clients to interact with the server.
See server.go for the functions needed to implement a server executable.
*/
package jobqueue

import (
	"github.com/go-mangos/mangos"
	"github.com/go-mangos/mangos/protocol/req"
	"github.com/go-mangos/mangos/transport/tcp"
	"github.com/ugorji/go/codec"
	"time"
)

// clientRequest is the struct that clients send to the server over the network
// to request it do something. (The properties are only exported so the
// encoder doesn't ignore them.)
type clientRequest struct {
	Method         string
	Queue          string
	Key            string
	Jobs           []*Job
	Timeout        time.Duration
	SchedulerGroup string
}

// Job is a struct that represents a command that needs to be run and some
// associated metadata. ReqGroup is a string that you supply to group together
// all commands that you expect to have similar memory and time requirements.
// Memory and Time are added by the system based on past experience of running
// jobs with the same ReqGroup. If you supply these yourself, your memory and
// time will be used if there is insufficient past experience, or if you also
// supply Override, which can be 0 to not override, 1 to override past
// experience if your supplied values are higher, or 2 to always override.
// Priority is a number between 0 and 255 inclusive - higher numbered jobs will
// run before lower numbered ones (the default is 0). If you get a Job back
// from the server (via Reserve() or Get()), you should treat the properties as
// read-only: changing them will have no effect.
type Job struct {
	RepGroup       string // a name associated with related Jobs to help group them together when reporting on their status etc.
	ReqGroup       string
	Cmd            string
	Cwd            string        // the working directory to cd to before running Cmd
	Memory         int           // the expected peak memory in MB Cmd will use while running
	Time           time.Duration // the expected time Cmd will take to run
	CPUs           int           // how many processor cores the Cmd will use
	Override       uint8
	Priority       uint8
	Peakmem        int           // the actual peak memory is recorded here (MB)
	Exited         bool          // true if the Cmd was run and exited
	Exitcode       int           // if the job ran and exited, its exit code is recorded here, but check Exited because when this is not set it could like like exit code 0
	Pid            int           // the pid of the running or ran process is recorded here
	Host           string        // the host the process is running or did run on is recorded here
	Walltime       time.Duration // if the job ran or is running right now, the walltime for the run is recorded here
	starttime      time.Time     // the time the cmd starts running is recorded here
	endtime        time.Time     // the time the cmd stops running is recorded here
	schedulerGroup string        // we add this internally to match up runners we spawn via the scheduler to the Jobs they're allowed to ReserveFiltered()
}

// NewJob makes it a little easier to make a new Job, for use with Add()
func NewJob(cmd string, cwd string, group string, memory int, time time.Duration, cpus int, override uint8, priority uint8, repgroup string) *Job {
	return &Job{
		RepGroup: repgroup,
		ReqGroup: group,
		Cmd:      cmd,
		Cwd:      cwd,
		Memory:   memory,
		Time:     time,
		CPUs:     cpus,
		Override: override,
		Priority: priority,
	}
}

// Client represents the client side of the socket that the jobqueue server is
// Serve()ing, specific to a particular queue
type Client struct {
	sock  mangos.Socket
	queue string
	ch    codec.Handle
}

// Connect creates a connection to the jobqueue server, specific to a single
// queue. Timeout determines how long to wait for a response from the server,
// not only while connecting, but for all subsequent interactions with it using
// the returned Client.
func Connect(addr string, queue string, timeout time.Duration) (c *Client, err error) {
	sock, err := req.NewSocket()
	if err != nil {
		return
	}

	err = sock.SetOption(mangos.OptionRecvDeadline, timeout)
	if err != nil {
		return
	}

	sock.AddTransport(tcp.NewTransport())

	err = sock.Dial("tcp://" + addr)
	if err != nil {
		return
	}

	c = &Client{sock: sock, queue: queue, ch: new(codec.BincHandle)}

	// Dial succeeds even when there's no server up, so we test the connection
	// works with a Ping()
	ok := c.Ping(timeout)
	if !ok {
		sock.Close()
		c = nil
		err = Error{queue, "Connect", "", ErrNoServer}
	}

	return
}

// Disconnect closes the connection to the jobqueue server
func (c *Client) Disconnect() {
	c.sock.Close()
}

// Ping tells you if your connection to the server is working
func (c *Client) Ping(timeout time.Duration) bool {
	_, err := c.request(&clientRequest{Method: "ping", Queue: c.queue, Timeout: timeout})
	if err != nil {
		return false
	}
	return true
}

// Stats returns stats of the jobqueue server queue you connected to.
// func (c *Conn) Stats() (s TubeStats, err error) {
// 	data, err := c.beanstalk.StatsTube(c.tube)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to get stats for beanstalk tube %s: %s\n", c.tube, err.Error())
// 		return
// 	}
// 	s = TubeStats{}
// 	err = yaml.Unmarshal(data, &s)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to parse yaml for beanstalk tube %s stats: %s", c.tube, err.Error())
// 	}
// 	return
// }

// ServerStats returns stats of the jobqueue server itself.
func (c *Client) ServerStats() (s *ServerStats, err error) {
	resp, err := c.request(&clientRequest{Method: "sstats", Queue: c.queue})
	if err != nil {
		return
	}
	s = resp.SStats
	return
}

// Add adds new jobs to the job queue, but only if those jobs aren't already in
// there. If any where already there, you will not get an error, but the
// returned 'existed' count will be > 0. Note that no cross-queue checking is
// done, so you need to be careful not to add the same job to different queues.
func (c *Client) Add(jobs []*Job) (added int, existed int, err error) {
	resp, err := c.request(&clientRequest{Method: "add", Queue: c.queue, Jobs: jobs})
	if err != nil {
		return
	}
	added = resp.Added
	existed = resp.Existed
	return
}

// Reserve takes a job off the jobqueue. If you process the job successfully you
// should Delete() it. If you can't deal with it right now you should Release()
// it. If you think it can never be dealt with you should Bury() it. If you die
// unexpectedly, the job will automatically be released back to the queue after
// some time. If no job was available in the queue for as long as the timeout
// argument, nil is returned for both job and error. If your timeout is 0, you
// will wait indefinitely for a job.
func (c *Client) Reserve(timeout time.Duration) (j *Job, err error) {
	resp, err := c.request(&clientRequest{Method: "reserve", Queue: c.queue, Timeout: timeout})
	if err != nil {
		return
	}
	j = resp.Job
	return
}

// ReserveScheduled is like Reserve(), except that it will only return jobs from
// the specified schedulerGroup. Based on the scheduler the server was
// configured with, it will group jobs based on their resource requirements and
// then submit runners to handle them to your system's job scheduler (such as
// LSF), possible in different scheduler queues. These runners are told the
// group they are a part of, and that same group name is applied internally to
// the Jobs as the "schedulerGroup", so that the runners can reserve only Jobs
// that they're supposed to. Therefore, it does not make sense for you to call
// this yourself; it is only for use by runners spawned by the server.
func (c *Client) ReserveScheduled(timeout time.Duration, schedulerGroup string) (j *Job, err error) {
	resp, err := c.request(&clientRequest{Method: "reserve", Queue: c.queue, Timeout: timeout, SchedulerGroup: schedulerGroup})
	if err != nil {
		return
	}
	j = resp.Job
	return
}

// Stats returns stats of a jobqueue job.
// func (j *Job) Stats() (s JobStats, err error) {
// 	data, err := j.conn.beanstalk.StatsJob(j.ID)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to get stats for beanstalk job %d: %s\n", j.ID, err.Error())
// 		return
// 	}
// 	s = JobStats{}
// 	err = yaml.Unmarshal(data, &s)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to parse yaml for beanstalk job %d stats: %s\n", j.ID, err.Error())
// 	}
// 	return
// }

// Delete removes a job from the jobqueue, for use after you have run the job
// successfully.
// func (j *Job) Delete() (err error) {
// 	err = j.conn.beanstalk.Delete(j.ID)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to delete beanstalk job %d: %s\n", j.ID, err.Error())
// 	}
// 	return
// }

// Release places a job back on the jobqueue, for use when you can't handle the
// job right now (eg. there was a suspected transient error) but maybe someone
// else can later. Note that you must reserve a job before you can release it.
// func (j *Job) Release() (err error) {
// 	err = j.conn.beanstalk.Release(j.ID, 0, 60*time.Second)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to release beanstalk job %d: %s\n", j.ID, err.Error())
// 	}
// 	return
// }

// Touch resets a job's ttr, allowing you more time to work on it. Note that you
// must reserve a job before you can touch it.
// func (j *Job) Touch() (err error) {
// 	err = j.conn.beanstalk.Touch(j.ID)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to touch beanstalk job %d: %s\n", j.ID, err.Error())
// 	}
// 	return
// }

// Bury marks a job as unrunnable, so it will be ignored (until the user does
// something to perhaps make it runnable and kicks the job). Note that you must
// reserve a job before you can bury it.
// func (j *Job) Bury() (err error) {
// 	err = j.conn.beanstalk.Bury(j.ID, 0)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to bury beanstalk job %d: %s\n", j.ID, err.Error())
// 	}
// 	return
// }

// Kick makes a previously Bury()'d job runnable again (it can be reserved in
// the future).
// func (j *Job) Kick() (err error) {
// 	err = j.conn.beanstalk.KickJob(j.ID)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to kick beanstalk job %d: %s\n", j.ID, err.Error())
// 	}
// 	return
// }

// request the server do something and get back its response
func (c *Client) request(cr *clientRequest) (sr *serverResponse, err error) {
	// encode and send the request
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, c.ch)
	err = enc.Encode(cr)
	if err != nil {
		return
	}
	err = c.sock.Send(encoded)
	if err != nil {
		return
	}

	// get the response and decode it
	resp, err := c.sock.Recv()
	if err != nil {
		return
	}
	sr = &serverResponse{}
	dec := codec.NewDecoderBytes(resp, c.ch)
	err = dec.Decode(sr)
	if err != nil {
		return
	}

	// pull the error out of sr
	err = sr.Err
	return
}
