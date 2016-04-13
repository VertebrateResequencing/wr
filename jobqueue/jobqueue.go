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
server.go in this package has some helpful functions for coding a server
executable.
*/
package jobqueue

import (
	// "bufio"
	"errors"
	"fmt"
	"github.com/sb10/vrpipe/queue"
	// "github.com/sb10/vrpipe/ssh"
	// "github.com/ugorji/go/codec"
	// "io"
	// "net"
	"github.com/go-mangos/mangos"
	"github.com/go-mangos/mangos/protocol/rep"
	"github.com/go-mangos/mangos/protocol/req"
	"strconv"
	"strings"
	"sync"
	"time"
	// "github.com/go-mangos/mangos/transport/ipc"
	"github.com/go-mangos/mangos/transport/tcp"
)

const (
	minLenToBuf = 1500 // minimum data len to send using bufio
)

var (
	ErrOutOfMemory    = errors.New("out of memory")
	ErrInternalError  = errors.New("internal error")
	ErrBadFormat      = errors.New("bad format")
	ErrUnknownCommand = errors.New("unknown command")
	ErrBuried         = errors.New("buried")
	errExpectedCrlf   = errors.New("expected CRLF")
	errJobTooBig      = errors.New("job too big")
	errDraining       = errors.New("draining")
	errDeadlineSoon   = errors.New("deadline soon")
	ErrTimedOut       = errors.New("timed out")
	ErrNotFound       = errors.New("not found")
	errInvalidLen     = errors.New("invalid length")
	ErrUnknown        = errors.New("unknown error")
	ErrClose          = errors.New("closed")
	ErrAlreadyExists  = errors.New("already exists")

	queues = struct {
		sync.Mutex
		qs map[string]*queue.Queue
	}{qs: make(map[string]*queue.Queue)}
)

// Error records an error and the operation, item and queue that caused it.
type Error struct {
	Queue string // the queue's Name
	Op    string // name of the method
	Item  string // the item's key
	Err   error  // one of our Err vars
}

func (e Error) Error() string {
	return "jobqueue(" + e.Queue + ") " + e.Op + "(" + e.Item + "): " + e.Err.Error()
}

// ClientRequest is the struct that clients send to the server over the network
// to request it do something
type ClientRequest struct {
	Method   string
	Queue    string
	Key      string
	Data     interface{}
	Priority uint8
	Delay    time.Duration
	TTR      time.Duration
	Timeout  time.Duration
}

// Conn represents a connection to the jobqueue server, specific to a particular
// queue
type Conn struct {
	// conn      net.Conn
	// bufReader *bufio.Reader
	// bufWriter *bufio.Writer
	sock      mangos.Socket
	queue     *queue.Queue
	queuename string
	// ch        codec.Handle
}

// Makes a new Conn
func New(sock mangos.Socket) *Conn {
	return &Conn{
		sock: sock,
		// conn:      netConn,
		// bufReader: bufio.NewReader(netConn),
		// bufWriter: bufio.NewWriter(netConn),
		// ch:        new(codec.BincHandle),
	}
}

// Connect creates a connection to the jobqueue server, specific to a single
// queue. If the spawn argument is set to true, then we will attempt to start up
// the server if it isn't running already.
func Connect(addr string, queuename string, spawn bool) (conn *Conn, err error) {
	sock, err := req.NewSocket()
	// if err != nil {
	// 	// on connection refused we'll assume it simply isn't running and try
	// 	// to start it up
	// 	if spawn && strings.Contains(err.Error(), "connection refused") {
	// 		host, port, err2 := net.SplitHostPort(addr)
	// 		if err2 != nil {
	// 			err = fmt.Errorf("%sAlso failed to parse [%s] as a place to ssh to: %s\n", err.Error(), addr, err2.Error())
	// 			return
	// 		}

	// 		//_, err2 = ssh.RunCmd(host, 22, "vrpipe jobqueue -p "+port, true)
	// 		_, err2 = ssh.RunCmd(host, 22, "vrpipe queue", true)
	// 		if err2 != nil {
	// 			err = fmt.Errorf("%sAlso failed to start up [vrpipe jobqueue -p %d] on %s: %s\n", err.Error(), port, host, err2.Error())
	// 			return
	// 		}

	// 		time.Sleep(1 * time.Second)
	// 		netConn, err = net.Dial("tcp", addr)
	// 		if err != nil {
	// 			return
	// 		}
	// 	} else {
	// 		return
	// 	}
	// }
	if err != nil {
		return
	}

	// sock.AddTransport(ipc.NewTransport())
	sock.AddTransport(tcp.NewTransport())

	if err = sock.Dial(addr); err != nil {
		return
	}

	conn = New(sock)
	conn.queuename = queuename

	return
}

// Serve is for use by a server and makes it start listening for Connect()ions
// from clients, and then handles those clients
func Serve(addr string) (err error) {
	sock, err := rep.NewSocket()
	if err != nil {
		return
	}

	// sock.AddTransport(ipc.NewTransport())
	sock.AddTransport(tcp.NewTransport())

	if err = sock.Listen(addr); err != nil {
		return
	}
	defer sock.Close()

	for {
		msg, rerr := sock.Recv()
		if rerr != nil {
			err = rerr
			return
		}

		herr := HandleCmd(sock, string(msg))
		if herr != nil {
			if herr != ErrClose {
				err = herr
				return
			}
		}
	}
}

// Disconnect closes the connection to the jobqueue server
func (c *Conn) Disconnect() {
	request(c.sock, "close\r\n")
	c.sock.Close()
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

// DaemonStats returns stats of the jobqueue server itself.
// func (c *Conn) ServerStats() (s BeanstalkStats, err error) {
// 	data, err := c.beanstalk.Stats()
// 	if err != nil {
// 		err = fmt.Errorf("Failed to get stats for beanstalkd: %s\n", err.Error())
// 		return
// 	}
// 	s = BeanstalkStats{}
// 	err = yaml.Unmarshal(data, &s)
// 	if err != nil {
// 		err = fmt.Errorf("Failed to parse yaml for beanstalkd stats: %s\n", err.Error())
// 	}
// 	return
// }

// Add adds a new job the job queue, but only if the job isn't already in there.
// The key is your own unique identifier for this job; you can query the job in
// the future using this key. The jobBody is any arbitrary string. The priority
// determines the order that jobs will be taken off the jobqueue and processed -
// higher priorities (up to a max of 255) will be dealt with first, and for
// those with equal priority, they are dealt with in fifo order. The ttr is the
// "time to release", meaning that if this job is reserved by a process, but
// that process exits before releasing, burying or deleting the job, the job
// will be automatically released ttr seconds after it was reserved (or last
// touched).
func (c *Conn) Add(key string, data string, pri uint8, ttr time.Duration) error {
	// ce := codec.NewEncoder(c.bufWriter, c.ch)
	// cr := &ClientRequest{Method: "add", Key: key, Data: data, Priority: pri, TTR: ttr}
	// err := ce.Encode(cr)
	// if err != nil {
	// 	return err
	// }

	// cd := codec.NewDecoder(c.bufReader, c.ch)
	// var worked bool
	// err = cd.Decode(worked)
	// if err != nil {
	// 	return err
	// }
	// if !worked {
	// 	return errUnknown
	// }
	// return nil

	cmd := fmt.Sprintf("add %d 0 %d\r\n%s\r\n%s\r\n%s\r\n", pri, uint64(ttr.Seconds()), c.queuename, key, data)

	resp, err := c.sendGetResp(cmd)
	if err != nil {
		return err
	}

	// parse add response
	switch resp {
	case "INSERTED\r\n":
		return nil
	case "ALREADY_EXISTS\r\n":
		return Error{c.queuename, "Add", key, ErrAlreadyExists}
	default:
		return parseCommonError(c.queuename, "Add", key, resp)
	}
	return Error{c.queuename, "Add", key, ErrUnknown}
}

// Reserve takes a job off the jobqueue. If you process the job successfully you
// should Delete() it. If you can't deal with it right now you should Release()
// it. If you think it can never be dealt with you should Bury() it. If you die
// unexpectedly, the job will automatically be released back to the queue after
// the job's ttr runs down. If no job was available in the queue for as long as
// the timeout argument, nil is returned for both job and error.
// func (c *Conn) Reserve(timeout time.Duration) (j *Job, err error) {
// 	err = c.watch()
// 	if err != nil {
// 		return
// 	}
// 	job, err := c.beanstalk.Reserve(timeout)
// 	if err == gobeanstalk.ErrTimedOut {
// 		err = nil
// 		return
// 	}
// 	if err != nil {
// 		err = fmt.Errorf("Failed to reserve a job from beanstalk: %s\n", err.Error())
// 		return
// 	}
// 	j = &Job{job.ID, job.Body, c}
// 	return
// }

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

// receive a queue name from the client and return it to the server (for
// server use only).
// func (c *Conn) HandleQueue() error {
// 	cmd, err := c.bufReader.ReadString('\n')
// 	if err != nil {
// 		return err
// 	}
// 	if string(cmd[0]) != "q" {
// 		return ErrBadFormat
// 	}

// 	cmd = strings.TrimRight(cmd, "\r\n")
// 	queuename := strings.TrimLeft(cmd, "q ")

// 	// get the queue from the global pool of queues, or create if necessary
// 	queues.Lock()
// 	q, existed := queues.qs[queuename]
// 	if !existed {
// 		q = queue.New(queuename)
// 		queues.qs[queuename] = q
// 	}
// 	queues.Unlock()

// 	c.queue = q

// 	return nil
// }

// receive a command and deal with it (for server use only).
func HandleCmd(sock mangos.Socket, msg string) error {
	// cmd, err := c.bufReader.ReadString('\n')
	// cd := codec.NewDecoder(c.bufReader, c.ch)
	// cr := &jobqueue.ClientRequest{}
	// err := cd.Decode(cr)
	// ce := codec.NewEncoder(c.bufWriter, c.ch)
	// result := true
	// err := ce.Encode(result)
	// if err != nil {
	// 	c.sendFull([]byte("INTERNAL_ERROR\r\n"))
	// 	return err
	// }

	lines := strings.Split(msg, "\r\n")
	cmd := lines[0]
	s := strings.Split(cmd, " ")

	switch string(cmd[0]) {
	case "a": // add
		priority, err := strconv.ParseUint(s[1], 10, 8)
		if err != nil {
			reply(sock, "BAD_FORMAT\r\n")
			return err
		}

		delay, err := strconv.ParseUint(s[2], 10, 64)
		if err != nil {
			reply(sock, "BAD_FORMAT\r\n")
			return err
		}

		ttr, err := strconv.ParseUint(s[3], 10, 64)
		if err != nil {
			reply(sock, "BAD_FORMAT\r\n")
			return err
		}

		queuename := lines[1]
		key := lines[2]
		data := lines[3]

		queues.Lock()
		q, existed := queues.qs[queuename]
		if !existed {
			q = queue.New(queuename)
			queues.qs[queuename] = q
		}
		queues.Unlock()

		_, err = q.Add(key, data, uint8(priority), time.Duration(delay)*time.Second, time.Duration(ttr)*time.Second)
		if err != nil {
			if qerr, ok := err.(queue.Error); ok && qerr.Err == queue.ErrAlreadyExists {
				err = reply(sock, "ALREADY_EXISTS\r\n")
				if err != nil {
					return err
				}
				return nil
			} else {
				reply(sock, "INTERNAL_ERROR\r\n")
				return err
			}
		}

		err = reply(sock, "INSERTED\r\n")
		if err != nil {
			return err
		}
		return nil
	case "c": // close
		return ErrClose
	}
	return ErrUnknownCommand
}

// send command and expect some exact response
// func (c *Conn) sendExpectExact(cmd string, expected string) error {
// 	resp, err := c.sendGetResp(cmd)
// 	if err != nil {
// 		return err
// 	}

// 	if resp != expected {
// 		return parseCommonError(c.queuename, "", "", resp)
// 	}
// 	return nil
// }

// send command and read response
func (c *Conn) sendGetResp(cmd string) (string, error) {
	err := request(c.sock, cmd)
	if err != nil {
		return "", err
	}

	// wait for response
	resp, err := receive(c.sock)
	if err != nil {
		return "", err
	}
	return resp, nil
}

// request a cmd be handled by the server
func request(sock mangos.Socket, cmd string) (err error) {
	err = sock.Send([]byte(cmd))
	return
}

// receive a response to a request() from the server
func receive(sock mangos.Socket) (msg string, err error) {
	bytes, err := sock.Recv()
	if err == nil {
		msg = string(bytes)
	}
	return
}

// reply to a client request() (server use only)
func reply(sock mangos.Socket, msg string) (err error) {
	err = sock.Send([]byte(msg))
	return
}

// try to send all of data
// if data len < 1500, it use TCPConn.Write
// if data len >= 1500, it use bufio.Write
// func (c *Conn) sendFull(data []byte) (int, error) {
// 	toWrite := data
// 	totWritten := 0
// 	var n int
// 	var err error
// 	for totWritten < len(data) {
// 		if len(toWrite) >= minLenToBuf {
// 			n, err = c.bufWriter.Write(toWrite)
// 			if err != nil && !isNetTempErr(err) {
// 				return totWritten, err
// 			}
// 			err = c.bufWriter.Flush()
// 			if err != nil && !isNetTempErr(err) {
// 				return totWritten, err
// 			}
// 		} else {
// 			n, err = c.conn.Write(toWrite)
// 			if err != nil && !isNetTempErr(err) {
// 				return totWritten, err
// 			}
// 		}
// 		totWritten += n
// 		toWrite = toWrite[n:]
// 	}
// 	return totWritten, nil
// }

// parse common errors
func parseCommonError(queuename string, command string, key string, msg string) error {
	switch msg {
	case "BURIED\r\n":
		return Error{queuename, command, key, ErrBuried}
	case "NOT_FOUND\r\n":
		return Error{queuename, command, key, ErrNotFound}
	case "OUT_OF_MEMORY\r\n":
		return Error{queuename, command, key, ErrOutOfMemory}
	case "INTERNAL_ERROR\r\n":
		return Error{queuename, command, key, ErrInternalError}
	case "BAD_FORMAT\r\n":
		return Error{queuename, command, key, ErrBadFormat}
	case "UNKNOWN_COMMAND\r\n":
		return Error{queuename, command, key, ErrUnknownCommand}
	}
	return Error{queuename, command, key, ErrUnknown}
}

// check if it is temporary network error
// func isNetTempErr(err error) bool {
// 	if nerr, ok := err.(net.Error); ok && nerr.Temporary() {
// 		return true
// 	}
// 	return false
// }
