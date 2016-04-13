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

import (
	"fmt"
	//"github.com/sb10/vrpipe/queue"
	// "github.com/sb10/vrpipe/beanstalk"
	//"io/ioutil"
	"log"
	// "net/http"
	// "net/url"
	// "bufio"
	// "net"
	"runtime"
	"testing"
	"time"
)

func TestJobqueue(t *testing.T) {
	if true {
		runtime.GOMAXPROCS(runtime.NumCPU())

		// bs, err := beanstalk.Connect("localhost:11300", "vrpipe.des", true)
		// if err != nil {
		// 	log.Fatal(err)
		// }

		// how many adds can we do in 10s? (Using the Benchmark functionality
		// doesn't behave the way we desire.)
		// stop := time.After(10 * time.Second)
		// stopped := make(chan bool)
		// k := 1
		// go func() {
		// 	for {
		// 		_, err := bs.Add(fmt.Sprintf("test job %d", k), 30)
		// 		if err != nil {
		// 			log.Fatal(err)
		// 		}
		// 		k++

		// 		select {
		// 		case <-stop:
		// 			close(stopped)
		// 			return
		// 		default:
		// 			continue
		// 		}
		// 	}
		// }()
		// <-stopped
		// log.Printf("Added %d beanstalk jobs\n", k)

		jq, err := Connect("vr-2-1-02:11301", "vrpipe.des", true)
		if err != nil {
			log.Fatal(err)
		}

		before := time.Now()
		n := 50000
		inserts := 0
		already := 0
		for i := 0; i < n; i++ {
			err := jq.Add(fmt.Sprintf("test job %d", i), "body", 1, 30)
			if err != nil {
				if qerr, ok := err.(Error); ok && qerr.Err == ErrAlreadyExists {
					already++
					continue
				}
				log.Fatal(err)
			}
			inserts++
		}
		e := time.Since(before)
		per := int64(e.Nanoseconds() / int64(n))
		jq.Disconnect()
		log.Printf("Added %d jobqueue jobs (%d inserts, %d dups) in %s == %d per\n", n, inserts, already, e, per)
		// jq.Shutdown()

		// stop = time.After(10 * time.Second)
		// stopped = make(chan bool)
		// k = 1
		// go func() {
		// 	for {
		// 		job, err := jobqueue.Reserve(5 * time.Second)
		// 		if err != nil {
		// 			log.Fatal(err)
		// 		}
		// 		if job == nil {
		// 			close(stopped)
		// 			return
		// 		}
		// 		k++

		// 		select {
		// 		case <-stop:
		// 			close(stopped)
		// 			return
		// 		default:
		// 			continue
		// 		}
		// 	}
		// }()
		// <-stopped
		// log.Printf("Reserved %d jobs\n", k)

		// q := queue.New("myqueue")
		// stop = time.After(10 * time.Second)
		// stopped = make(chan bool)
		// k = 1
		// go func() {
		// 	for {
		// 		_, err := q.Add(fmt.Sprintf("test job %d", k), "data", 0, 0*time.Second, 30*time.Second)
		// 		if err != nil {
		// 			log.Fatal(err)
		// 		}
		// 		k++

		// 		select {
		// 		case <-stop:
		// 			close(stopped)
		// 			return
		// 		default:
		// 			continue
		// 		}
		// 	}
		// }()
		// <-stopped
		// log.Printf("Added %d jobs to my own queue directly\n", k)

		// stop = time.After(10 * time.Second)
		// stopped = make(chan bool)
		// k = 1
		// go func() {
		// 	for {
		// 		_, err := q.Reserve()
		// 		if err != nil {
		// 			if qerr, ok := err.(queue.Error); ok && qerr.Err == queue.ErrNothingReady {
		// 				close(stopped)
		// 				return
		// 			}
		// 			log.Fatal(err)
		// 		}
		// 		k++

		// 		select {
		// 		case <-stop:
		// 			close(stopped)
		// 			return
		// 		default:
		// 			continue
		// 		}
		// 	}
		// }()
		// <-stopped
		// log.Printf("Reserved %d jobs from my own queue directly\n", k)

		// stop = time.After(10 * time.Second)
		// stopped = make(chan bool)
		// k = 1
		// go func() {
		// 	for {
		// 		// resp, err := http.PostForm("http://localhost:11301/enqueue", url.Values{"queue": {"httpqueue"}, "key": {fmt.Sprintf("test job %d", k)}, "job": {"job"}, "priority": {"1"}, "delay": {"0s"}, "ttr": {"30s"}})
		// 		// if err != nil {
		// 		// 	log.Fatal(err)
		// 		// }
		// 		// resp.Body.Close()
		// 		conn, _ := net.Dial("tcp", "127.0.0.1:11301")
		// 		fmt.Fprintf(conn, "foo %d\n", k)
		// 		message, _ := bufio.NewReader(conn).ReadString('\n')
		// 		message = message + ""
		// 		conn.Close()
		// 		k++

		// 		select {
		// 		case <-stop:
		// 			close(stopped)
		// 			return
		// 		default:
		// 			continue
		// 		}
		// 	}
		// }()
		// <-stopped
		// log.Printf("Added %d jobs to my own queue via http\n", k)

		// stop = time.After(10 * time.Second)
		// stopped = make(chan bool)
		// k = 1
		// go func() {
		// 	for {
		// 		resp, err := http.PostForm("http://localhost:11301/dequeue", url.Values{"queue": {"httpqueue"}})
		// 		if err != nil {
		// 			log.Fatal(err)
		// 		}
		// 		resp.Body.Close()
		// 		// body, err := ioutil.ReadAll(resp.Body)
		// 		// if err != nil {
		// 		// 	log.Fatal(err)
		// 		// }
		// 		// fmt.Println(body)
		// 		k++

		// 		select {
		// 		case <-stop:
		// 			close(stopped)
		// 			return
		// 		default:
		// 			continue
		// 		}
		// 	}
		// }()
		// <-stopped
		// log.Printf("Reserved %d jobs from my own queue via http\n", k)
	}
}
