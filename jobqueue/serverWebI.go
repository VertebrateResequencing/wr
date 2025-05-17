// Copyright © 2016-2021 Genome Research Limited
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

package jobqueue

// This file contains the web interface code of the server.

import (
	"context"
	"embed"
	"net/http"
	"strings"
	"sync"

	"github.com/wtsi-ssg/wr/clog"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gorilla/websocket"
)

//go:embed static
var staticFS embed.FS

// jstatusReq is what the status webpage sends us to ask for info about jobs.
type jstatusReq struct {
	// possible Requests are:
	// current = get count info for every job in every RepGroup in the cmds
	//           queue.
	// details = get example job details for jobs in the RepGroup, grouped by
	//           having the same Status, Exitcode and FailReason.
	// retry = retry buried jobs.
	// remove = remove non-running jobs.
	// kill = kill running jobs or confirm lost jobs are dead.
	// confirmBadServer = confirm that the server with ID ServerID is bad.
	// dismissMsg = dismiss the given Msg.
	// dismissMsgs = dismiss all scheduler messages.
	Request string

	// sending Key means "give me detailed info about this single job", and
	// modifies retry, remove and kill to only work on this job
	Key string

	// sending RepGroup means "send me limited info about the jobs with this
	// RepGroup", and modifies retry, remove and kill to work on all jobs with
	// the given RepGroup, ExitCode and FailReason
	RepGroup string

	State      JobState // A Job.State to limit RepGroup by in details mode
	Limit      int      // Limit the number of jobs returned in details mode (0 = no limit)
	Offset     int      // Offset the start of the returned jobs in details mode
	Exitcode   int
	FailReason string
	ServerID   string // required argument for confirmBadServer
	Msg        string // required argument for dismissMsg
}

// JStatus is the job info we send to the status webpage (only real difference
// to Job is that some of the values are converted to easy-to-display forms).
type JStatus struct {
	LimitGroups     []string
	DepGroups       []string
	Dependencies    []string
	OtherRequests   []string
	Env             []string
	Key             string
	RepGroup        string
	Cmd             string
	State           JobState
	Cwd             string
	CwdBase         string
	Behaviours      string
	Mounts          string
	MonitorDocker   string
	WithDocker      string
	WithSingularity string
	ContainerMounts string
	FailReason      string
	Host            string
	HostID          string
	HostIP          string
	StdErr          string
	StdOut          string
	ExpectedRAM     int     // ExpectedRAM is in Megabytes.
	ExpectedTime    float64 // ExpectedTime is in seconds.
	RequestedDisk   int     // RequestedDisk is in Gigabytes.
	Cores           float64
	PeakRAM         int
	PeakDisk        int64 // MBs
	Exitcode        int
	Pid             int
	Walltime        float64
	CPUtime         float64
	Started         *int64
	Ended           *int64
	Similar         int
	Attempts        uint32
	HomeChanged     bool
	Exited          bool
}

// webInterfaceStatic is a http handler for our static documents in the static
// folder of the source code repository, which are embedded at compile time.
func webInterfaceStatic(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// our home page is /status.html
		path := r.URL.Path
		path = strings.TrimPrefix(path, "/")
		if path == "" || path == "status" {
			path = "status.html"
		}

		if path == "status.html" {
			ok := s.httpAuthorized(w, r)
			if !ok {
				return
			}
		}

		path = "static/" + path
		doc, err := staticFS.ReadFile(path)
		if err != nil {
			clog.Warn(ctx, "not found", "err", err)
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", getContentTypeForPath(path))

		_, err = w.Write(doc)
		if err != nil {
			clog.Error(ctx, "web interface static document write failed", "err", err)
		}
	}
}

// getContentTypeForPath determines the appropriate Content-Type header based on
// the file path.
func getContentTypeForPath(path string) string { //nolint:gocyclo
	switch {
	case strings.HasPrefix(path, "static/js"):
		return "text/javascript; charset=utf-8"
	case strings.HasPrefix(path, "static/css"):
		return "text/css; charset=utf-8"
	case strings.HasPrefix(path, "static/fonts"):
		switch {
		case strings.HasSuffix(path, ".eot"):
			return "application/vnd.ms-fontobject"
		case strings.HasSuffix(path, ".svg"):
			return "image/svg+xml"
		case strings.HasSuffix(path, ".ttf"):
			return "application/x-font-truetype"
		case strings.HasSuffix(path, ".woff"):
			return "application/font-woff"
		case strings.HasSuffix(path, ".woff2"):
			return "application/font-woff2"
		}
	case strings.HasSuffix(path, "favicon.ico"):
		return "image/x-icon"
	}

	return "text/html; charset=utf-8"
}

// webSocket upgrades a http connection to a websocket
func webSocket(w http.ResponseWriter, r *http.Request) (*websocket.Conn, bool) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, "Could not open websocket connection", http.StatusBadRequest)
		return conn, false
	}
	return conn, true
}

// webInterfaceStatusWS reads from and writes to the websocket on the status
// webpage
func webInterfaceStatusWS(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok := s.httpAuthorized(w, r)
		if !ok {
			return
		}

		conn, ok := webSocket(w, r)
		if !ok {
			clog.Error(ctx, "Failed to set up websocket", "Host", r.Host)
			return
		}

		writeMutex := &sync.Mutex{}

		// when the server shuts down it will close our conn, ending the main
		// goroutine
		storedName := s.storeWebSocketConnection(conn)

		// when the main goroutine closes we will end all the others
		stopper := make(chan bool)

		// go routine to read client requests and respond to them
		go func(conn *websocket.Conn, connStorageName string, stop chan bool) {
			// log panics and die
			defer internal.LogPanic(ctx, "jobqueue websocket client handling", true)

			defer func() {
				s.closeWebSocketConnection(ctx, connStorageName)

				// stop the other goroutines
				close(stop)
			}()

			for {
				req := jstatusReq{}
				errr := conn.ReadJSON(&req)
				if errr != nil {
					// browser was refreshed or server shutdown
					break
				}

				switch {
				case req.Request != "":
					switch req.Request {
					case "current":
						// get all current jobs
						jobs := s.getJobsCurrent(ctx, 0, "", false, false)
						writeMutex.Lock()
						err := webInterfaceStatusSendGroupStateCount(conn, "+all+", jobs)
						if err != nil {
							writeMutex.Unlock()
							break
						}

						// for each different RepGroup amongst these jobs,
						// send the job state counts
						repGroups := make(map[string][]*Job)
						for _, job := range jobs {
							repGroups[job.RepGroup] = append(repGroups[job.RepGroup], job)
						}
						failed := false
						for repGroup, jobs := range repGroups {
							complete, _, qerr := s.getCompleteJobsByRepGroup(repGroup)
							if qerr != "" {
								failed = true
								break
							}
							jobs = append(jobs, complete...)
							err := webInterfaceStatusSendGroupStateCount(conn, repGroup, jobs)
							if err != nil {
								failed = true
								break
							}
						}

						// also send details of dead servers
						for _, bs := range s.getBadServers() {
							s.badServerCaster.Send(bs)
						}

						// and of scheduler messages
						s.simutex.RLock()
						for _, si := range s.schedIssues {
							s.schedCaster.Send(si)
						}
						s.simutex.RUnlock()

						writeMutex.Unlock()
						if failed {
							break
						}
					case "details":
						opts := repGroupOptions{
							RepGroup: req.RepGroup,
							Search:   false,
							limitJobsOptions: limitJobsOptions{
								Limit:      req.Limit,
								Offset:     req.Offset,
								State:      req.State,
								ExitCode:   req.Exitcode,
								FailReason: req.FailReason,
								GetStd:     true,
								GetEnv:     true,
							},
						}

						jobs, _, errstr := s.getJobsByRepGroup(ctx, opts)
						if errstr == "" && len(jobs) > 0 {
							writeMutex.Lock()
							failed := false

							for _, job := range jobs {
								status, err := job.ToStatus()
								if err != nil {
									failed = true
									break
								}
								status.RepGroup = req.RepGroup // since we want to return the group the user asked for, not the most recent group the job was made for
								err = conn.WriteJSON(status)
								if err != nil {
									failed = true
									break
								}
							}
							writeMutex.Unlock()
							if failed {
								break
							}
						}
					case "retry":
						jobs := s.reqToJobs(req, []queue.ItemState{queue.ItemStateBury})
						s.kickJobs(ctx, jobs)
					case "remove":
						jobs := s.reqToJobs(req, []queue.ItemState{queue.ItemStateBury, queue.ItemStateDelay, queue.ItemStateDependent, queue.ItemStateReady})
						deleted := s.deleteJobs(ctx, jobs)
						clog.Debug(ctx, "removed jobs", "count", len(deleted))
					case "kill":
						jobs := s.reqToJobs(req, []queue.ItemState{queue.ItemStateRun})
						for _, job := range jobs {
							_, err := s.killJob(ctx, job.Key())
							if err != nil {
								clog.Warn(ctx, "web interface kill job failed", "err", err)
							}
						}
					case "confirmBadServer":
						if req.ServerID != "" {
							s.bsmutex.Lock()
							server := s.badServers[req.ServerID]
							delete(s.badServers, req.ServerID)
							s.bsmutex.Unlock()
							if server != nil && server.IsBad() {
								err := server.Destroy(ctx)
								if err != nil {
									clog.Warn(ctx, "web interface confirm bad server destruction failed", "err", err)
								}
							}
						}
					case "dismissMsg":
						if req.Msg != "" {
							s.simutex.Lock()
							delete(s.schedIssues, req.Msg)
							s.simutex.Unlock()
						}
					case "dismissMsgs":
						s.simutex.Lock()
						s.schedIssues = make(map[string]*schedulerIssue)
						s.simutex.Unlock()
					default:
						continue
					}
				case req.Key != "":
					jobs, _, errstr := s.getJobsByKeys(ctx, []string{req.Key}, true, true)
					if errstr == "" && len(jobs) == 1 {
						status, err := jobs[0].ToStatus()
						if err != nil {
							break
						}
						writeMutex.Lock()
						err = conn.WriteJSON(status)
						writeMutex.Unlock()
						if err != nil {
							break
						}
					}
				}
			}
		}(conn, storedName, stopper)

		// go routines to push changes to the client
		go func(conn *websocket.Conn, stop chan bool) {
			// log panics and die
			defer internal.LogPanic(ctx, "jobqueue websocket status updating", true)

			statusReceiver := s.statusCaster.Join()
			defer statusReceiver.Close()

			for {
				select {
				case <-stop:
					return
				case status := <-statusReceiver.In:
					writeMutex.Lock()
					err := conn.WriteJSON(status)
					writeMutex.Unlock()
					if err != nil {
						clog.Warn(ctx, "status updater failed to send JSON to client", "err", err)
						return
					}
				}
			}
		}(conn, stopper)

		go func(conn *websocket.Conn, stop chan bool) {
			defer internal.LogPanic(ctx, "jobqueue websocket bad server updating", true)

			badserverReceiver := s.badServerCaster.Join()
			defer badserverReceiver.Close()

			for {
				select {
				case <-stop:
					return
				case server := <-badserverReceiver.In:
					writeMutex.Lock()
					err := conn.WriteJSON(server)
					writeMutex.Unlock()
					if err != nil {
						clog.Warn(ctx, "bad server caster failed to send JSON to client", "err", err)
						return
					}
				}
			}
		}(conn, stopper)

		go func(conn *websocket.Conn, stop chan bool) {
			defer internal.LogPanic(ctx, "jobqueue websocket scheduler issue updating", true)

			schedIssueReceiver := s.schedCaster.Join()
			defer schedIssueReceiver.Close()

			for {
				select {
				case <-stop:
					return
				case si := <-schedIssueReceiver.In:
					writeMutex.Lock()
					err := conn.WriteJSON(si)
					writeMutex.Unlock()
					if err != nil {
						clog.Warn(ctx, "scheduler issues caster failed to send JSON to client", "err", err)
						return
					}
				}
			}
		}(conn, stopper)
	}
}

// reqToJobs takes a request from the status webpage and returns the requested
// jobs.
func (s *Server) reqToJobs(req jstatusReq, allowedItemStates []queue.ItemState) []*Job {
	allowed := make(map[queue.ItemState]bool)
	for _, is := range allowedItemStates {
		allowed[is] = true
	}

	var jobs []*Job
	if req.RepGroup != "" {
		for _, key := range s.rpl.Values(req.RepGroup) {
			item, err := s.q.Get(key)
			if item == nil || err != nil {
				continue
			}
			stats := item.Stats()
			if allowed[stats.State] {
				job := item.Data().(*Job)
				job.Lock()
				job.State = s.itemStateToJobState(stats.State, job.Lost)
				if job.Exitcode == req.Exitcode && job.FailReason == req.FailReason {
					jobs = append(jobs, job)
				}
				job.Unlock()
			}
		}
	} else if req.Key != "" {
		item, err := s.q.Get(req.Key)
		if item == nil || err != nil {
			return nil
		}
		stats := item.Stats()
		if allowed[stats.State] {
			job := item.Data().(*Job)
			job.Lock()
			job.State = s.itemStateToJobState(stats.State, job.Lost)
			job.Unlock()
			jobs = append(jobs, job)
		}
	}
	return jobs
}

// webInterfaceStatusSendGroupStateCount sends the per-repgroup state counts
// to the status webpage websocket
func webInterfaceStatusSendGroupStateCount(conn *websocket.Conn, repGroup string, jobs []*Job) error {
	stateCounts := make(map[JobState]int)
	for _, job := range jobs {
		var state JobState

		// for display simplicity purposes, merge reserved in to running
		switch job.State {
		case JobStateReserved, JobStateRunning:
			state = JobStateRunning
		default:
			state = job.State
		}

		stateCounts[state]++
	}
	for to, count := range stateCounts {
		err := conn.WriteJSON(&jstateCount{repGroup, JobStateNew, to, count})
		if err != nil {
			return err
		}
	}
	return nil
}
