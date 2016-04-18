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

package jobqueue

// This file contains all the functions to implement a jobqueue server.

import (
	"errors"
	"fmt"
	"github.com/dgryski/go-farm"
	"github.com/go-mangos/mangos"
	"github.com/go-mangos/mangos/protocol/rep"
	"github.com/go-mangos/mangos/transport/tcp"
	"github.com/sb10/vrpipe/queue"
	"github.com/ugorji/go/codec"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var (
	ErrInternalError  = errors.New("internal error")
	ErrUnknownCommand = errors.New("unknown command")
	ErrUnknown        = errors.New("unknown error")
	ErrClosedInt      = errors.New("queues closed due to SIGINT")
	ErrClosedTerm     = errors.New("queues closed due to SIGTERM")
	ErrClosedStop     = errors.New("queues closed due to manual Stop()")
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

// jobErr is used internally to implement Reserve(), which needs to send job and
// err over a channel
type jobErr struct {
	job *Job
	err error
}

// serverResponse is the struct that the server sends to clients over the
// network in response to their clientRequest
type serverResponse struct {
	Err     error
	Added   int
	Existed int
	Job     *Job
}

// server represents the server side of the socket that clients Connect() to
type Server struct {
	sock mangos.Socket
	ch   codec.Handle
	done chan error
	quit chan bool
	sync.Mutex
	qs map[string]*queue.Queue
}

// Serve is for use by a server executable and makes it start listening for
// Connect()ions from clients, and then handles those clients. It returns a
// *Server that you will typically call Block() on to block until until your
// executable receives a SIGINT or SIGTERM, or you call Stop(), at which point
// the queues will be safely closed (you'd probably just exit at that point).
// The possible errors from Serve() will be related to not being able to start
// up at the supplied address; errors encountered while dealing with clients are
// logged but otherwise ignored.
func Serve(addr string) (s *Server, err error) {
	sock, err := rep.NewSocket()
	if err != nil {
		return
	}

	// we open ourselves up to possible denial-of-service attack if a client
	// sends us tons of data, but at least the client doesn't silently hang
	// forever when it legitimately wants to Add() a ton of jobs
	// unlimited Recv() length
	if err = sock.SetOption(mangos.OptionMaxRecvSize, 0); err != nil {
		return
	}

	// we use raw mode, allowing us to respond to multiple clients in
	// parallel
	if err = sock.SetOption(mangos.OptionRaw, true); err != nil {
		return
	}

	// we'll wait 5 seconds to recv from clients before trying again, allowing
	// us to check if signals have been passed
	if err = sock.SetOption(mangos.OptionRecvDeadline, 5*time.Second); err != nil {
		return
	}

	sock.AddTransport(tcp.NewTransport())

	if err = sock.Listen(addr); err != nil {
		return
	}

	// serving will happen in a goroutine that will stop on SIGINT or SIGTERM,
	// of if something is sent on the quit channel
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	quit := make(chan bool, 1)
	done := make(chan error, 1)

	s = &Server{sock: sock, ch: new(codec.BincHandle), qs: make(map[string]*queue.Queue), quit: quit, done: done}

	go func() {
		for {
			select {
			case sig := <-sigs:
				s.shutdown()
				var serr error
				switch sig {
				case os.Interrupt:
					serr = Error{"", "Serve", "", ErrClosedInt}
				case syscall.SIGTERM:
					serr = Error{"", "Serve", "", ErrClosedTerm}
				}
				done <- serr
				return
			case <-quit:
				s.shutdown()
				done <- Error{"", "Serve", "", ErrClosedStop}
				return
			default:
				// receive a clientRequest from a client
				m, rerr := sock.RecvMsg()
				if rerr != nil {
					if rerr != mangos.ErrRecvTimeout {
						log.Println(rerr)
					}
					continue
				}

				// parse the request, do the desired work and respond to the client
				go func() {
					herr := s.handleRequest(m)
					if herr != nil {
						log.Println(herr)
					}
				}()
			}
		}
	}()

	return
}

// handleRequest parses the bytes received from a connected client in to a
// clientRequest, does the requested work, then responds back to the client with
// a serverResponse
func (s *Server) handleRequest(m *mangos.Message) error {
	dec := codec.NewDecoderBytes(m.Body, s.ch)
	cr := &clientRequest{}
	err := dec.Decode(cr)
	if err != nil {
		return err
	}

	s.Lock()
	q, existed := s.qs[cr.Queue]
	if !existed {
		q = queue.New(cr.Queue)
		s.qs[cr.Queue] = q
	}
	s.Unlock()

	var sr *serverResponse

	switch cr.Method {
	case "add":
		var itemdefs []*queue.ItemDef
		for _, job := range cr.Jobs {
			l, h := farm.Hash128([]byte(fmt.Sprintf("%s.%s", job.Cwd, job.Cmd)))
			key := fmt.Sprintf("%016x%016x", l, h)
			itemdefs = append(itemdefs, &queue.ItemDef{key, job, job.Priority, 0 * time.Second, 1 * time.Minute})
		}

		added, dups, amerr := q.AddMany(itemdefs)
		if amerr != nil {
			s.reply(m, &serverResponse{Err: Error{cr.Queue, cr.Method, "", ErrInternalError}})
			return amerr
		}

		sr = &serverResponse{Added: added, Existed: dups}
	case "reserve":
		// first just try to Reserve normally
		item, err := q.Reserve()
		var job *Job
		if err != nil {
			if qerr, ok := err.(queue.Error); ok && qerr.Err == queue.ErrNothingReady {
				// there's nothing in the ready sub queue right now, so every
				// second try and Reserve() from the queue until either we get
				// an item, or we exceed the client's timeout
				var stop <-chan time.Time
				if cr.Timeout.Nanoseconds() > 0 {
					stop = time.After(cr.Timeout)
				} else {
					stop = make(chan time.Time)
				}

				joberrch := make(chan *jobErr, 1)
				ticker := time.NewTicker(1 * time.Second)
				go func() {
					for {
						select {
						case <-ticker.C:
							item, err := q.Reserve()
							if err != nil {
								if qerr, ok := err.(queue.Error); ok && qerr.Err == queue.ErrNothingReady {
									continue
								}
								ticker.Stop()
								joberrch <- &jobErr{err: Error{cr.Queue, cr.Method, "", err}}
								return
							}
							ticker.Stop()
							joberrch <- &jobErr{job: item.Data.(*Job)}
							return
						case <-stop:
							ticker.Stop()
							// if we time out, we'll return nil job and nil err
							joberrch <- &jobErr{}
							return
						}
					}
				}()
				joberr := <-joberrch
				close(joberrch)
				job = joberr.job
				err = joberr.err
			}
		} else {
			job = item.Data.(*Job)
		}

		sr = &serverResponse{Job: job, Err: err}
	}

	if sr == nil {
		err = Error{cr.Queue, cr.Method, cr.Key, ErrUnknownCommand}
		s.reply(m, &serverResponse{Err: err})
		return err
	}

	err = s.reply(m, sr)
	if err != nil {
		return err
	}
	return nil
}

// reply to a client
func (s *Server) reply(m *mangos.Message, sr *serverResponse) (err error) {
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, s.ch)
	err = enc.Encode(sr)
	if err != nil {
		return
	}
	m.Body = encoded
	err = s.sock.SendMsg(m)
	return
}

// shutdown stops listening to client connections, close all queues and
// persists them to disk
func (s *Server) shutdown() {
	s.sock.Close()

	//*** we want to persist production queues to disk

	// clean up our queues and empty everything out to be garbage collected,
	// in case the same process calls Serve() again after this
	for _, q := range s.qs {
		q.Destroy()
	}
	s.qs = nil
}

// Block makes you block while the server does the job of serving clients. This
// will return with an error indicating why it stopped blocking, which will
// be due to receiving a signal or because you called Stop()
func (s *Server) Block() (err error) {
	err = <-s.done
	return
}

// Stop will cause a graceful shut down of the server.
func (s *Server) Stop() {
	s.quit <- true
}
