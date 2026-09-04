/*******************************************************************************
 * Copyright (c) 2016-2021, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
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

// This file contains the web interface code of the server.

import (
	"context"
	"embed"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
)

//go:embed static
var staticFS embed.FS

const (
	jstatusRequestCurrent     = "current"
	jstatusRequestDetails     = "details"
	jstatusRequestRemove      = "remove"
	jstatusRequestResume      = "resume"
	jstatusRequestRerun       = "rerun"
	jstatusRequestUnsubscribe = requestMethodUnsubscribe
	statusAllRepGroups        = "+all+"

	// webSocketBufferSize is the read and write buffer size (bytes) for status
	// page websocket connections.
	webSocketBufferSize = 1024

	// statusWebSocketWorkerCount is the number of goroutines started for each
	// status page websocket connection.
	statusWebSocketWorkerCount = 5
)

// jstatusReq is what the status webpage sends us to ask for info about jobs.
type jstatusReq struct {
	// possible Requests are:
	// current = get count info for every job in every RepGroup in the cmds
	//           queue.
	// details = get example job details for jobs in the RepGroup, grouped by
	//           having the same Status, Exitcode and FailReason.
	// rerun = add completed jobs to the queue again, using Key or RepGroup.
	// resume = resume suspended jobs using Key or RepGroup.
	// retry = retry buried jobs.
	// remove = remove non-running jobs.
	// kill = kill running jobs or confirm lost jobs are dead.
	// confirmBadServer = confirm that the server with ID ServerID is bad.
	// dismissMsg = dismiss the given Msg.
	// dismissMsgs = dismiss all scheduler messages.
	// unsubscribe = unsubscribe from job updates for a specific key or all if key is empty.
	Request string

	// sending Key means "give me detailed info about this single job", and
	// modifies retry, remove and kill to only work on this job
	Key string

	// sending RepGroup means "send me limited info about the jobs with this
	// RepGroup", and modifies retry, remove and kill to work on all jobs with
	// the given RepGroup, ExitCode and FailReason
	RepGroup string

	Search     bool     // RepGroup is treated as a substring search term
	State      JobState // A Job.State to limit RepGroup by in details mode
	Limit      int      // Limit the number of jobs returned in details mode (0 = no limit)
	Offset     int      // Offset the start of the returned jobs in details mode
	Exitcode   int
	FailReason string
	ServerID   string // required argument for confirmBadServer
	Msg        string // required argument for dismissMsg
}

// webInterfaceStatusSendGroupStateCount sends the per-RepGroup state counts to
// the status webpage websocket as jstateCount deltas from the new state, so a
// fresh connection's counts are seeded the v0.36.5 way (reserved is merged into
// running for display).
func webInterfaceStatusSendGroupStateCount(conn *websocket.Conn, repGroup string, jobs []*Job) error {
	for to, count := range statusStateCounts(jobs) {
		msg := &jstateCount{RepGroup: repGroup, FromState: JobStateNew, ToState: to, Count: count}
		if err := conn.WriteJSON(msg); err != nil {
			return err
		}
	}

	return nil
}

func statusStateCounts(jobs []*Job) map[JobState]int {
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

	return stateCounts
}

// reqToCompletedJobs takes a rerun request from the status webpage and returns
// completed jobs that are not already live in the queue.
func (s *Server) reqToCompletedJobs(req jstatusReq) ([]*Job, string, string) {
	if req.Key != "" {
		return s.completedJobByKey(req)
	}

	return s.completedJobsByRepGroup(req)
}

func (s *Server) completedJobsByRepGroup(req jstatusReq) ([]*Job, string, string) {
	if req.RepGroup == "" || (req.State != "" && req.State != JobStateComplete) {
		return nil, "", ""
	}

	complete, srerr, qerr := s.getCompleteJobsByRepGroup(req.RepGroup)
	if srerr != "" {
		return nil, srerr, qerr
	}

	jobs := make([]*Job, 0, len(complete))
	for _, job := range complete {
		if !completedJobMatchesRerunRequest(job, req) {
			continue
		}

		job.Lock()
		job.RepGroup = req.RepGroup
		job.Unlock()
		jobs = append(jobs, job)
	}

	return jobs, "", ""
}

func completedJobMatchesRerunRequest(job *Job, req jstatusReq) bool {
	job.RLock()
	defer job.RUnlock()

	return job.Exitcode == req.Exitcode && job.FailReason == req.FailReason
}

// JStatus is the job info we send to the status webpage (only real difference
// to Job is that some of the values are converted to easy-to-display forms).
type JStatus struct {
	LimitGroups         []string
	DepGroups           []string
	Modules             []string
	Dependencies        []string
	WaitingForDepGroups []string
	OtherRequests       []string
	Env                 []string
	Key                 string
	RepGroup            string
	ReqGroup            string
	Cmd                 string
	State               JobState
	Cwd                 string
	CwdBase             string
	Behaviours          string
	Mounts              string
	MonitorDocker       string
	WithDocker          string
	WithSingularity     string
	ContainerMounts     string
	FailReason          string
	Host                string
	HostID              string
	HostIP              string
	SSHCommand          string
	StdErr              string
	StdOut              string
	ExpectedRAM         int     // ExpectedRAM is in Megabytes.
	ExpectedTime        float64 // ExpectedTime is in seconds.
	RequestedDisk       int     // RequestedDisk is in Gigabytes.
	EnvOverrides        []string
	Cores               float64
	NoRetryOverWalltime float64
	PeakRAM             int
	PeakDisk            int64 // MBs
	Exitcode            int
	Pid                 int
	Walltime            float64
	CPUtime             float64
	Started             *int64
	Ended               *int64
	Similar             int
	Attempts            uint32
	Override            uint8
	Priority            uint8
	Retries             uint8
	HomeChanged         bool
	CwdMatters          bool
	Exited              bool
	IsPushUpdate        bool
}

func statusFromJobUpdate(update *JobUpdate) *JStatus {
	return &JStatus{
		Key:          update.Key,
		RepGroup:     update.RepGroup,
		State:        update.State,
		Cwd:          update.Cwd,
		CwdBase:      update.CwdBase,
		FailReason:   update.FailReason,
		Host:         update.Host,
		HostID:       update.HostID,
		HostIP:       update.HostIP,
		SSHCommand:   update.SSHCommand,
		StdErr:       update.StdErr,
		StdOut:       update.StdOut,
		PeakRAM:      update.PeakRAM,
		PeakDisk:     update.PeakDisk,
		Exitcode:     update.Exitcode,
		Pid:          update.Pid,
		CPUtime:      update.CPUtime.Seconds(),
		Started:      statusTimeFromJobUpdateTime(update.Started),
		Ended:        statusTimeFromJobUpdateTime(update.Ended),
		IsPushUpdate: true,
	}
}

func (s *Server) statusFromSubscriptionUpdate(ctx context.Context, update *JobUpdate) *JStatus {
	if update == nil || update.Key == "" {
		return nil
	}

	jobs, _, errstr := s.getJobsByKeys(ctx, []string{update.Key}, true, true)
	if errstr != "" || len(jobs) != 1 {
		return statusFromJobUpdate(update)
	}

	status, err := jobs[0].ToStatus()
	if err != nil {
		return statusFromJobUpdate(update)
	}

	status.State = update.State
	status.Started = statusTimeFromJobUpdateTime(update.Started)
	status.Ended = statusTimeFromJobUpdateTime(update.Ended)
	status.IsPushUpdate = true

	return &status
}

func statusTimeFromJobUpdateTime(unixNano *int64) *int64 {
	if unixNano == nil {
		return nil
	}

	unixSeconds := *unixNano / int64(time.Second)

	return &unixSeconds
}

func (s *Server) writeStatusSubscriptionUpdate(ctx context.Context, conn *websocket.Conn,
	connName string, status *JStatus) bool {
	s.wsmutex.RLock()
	writeMutex := s.wsWriteMutexes[connName]
	s.wsmutex.RUnlock()

	if writeMutex == nil {
		return false
	}

	writeMutex.Lock()
	err := conn.WriteJSON(status)
	writeMutex.Unlock()

	if err != nil {
		clog.Warn(ctx, "status subscription updater failed to send JSON to client", "err", err)

		return false
	}

	return true
}

// webInterfaceStatic is a http handler for our static documents in the static
// folder of the source code repository, which are embedded at compile time.
func webInterfaceStatic(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.serveStaticDoc(ctx, w, r)
	}
}

// serveStaticDoc serves an embedded static document, requiring authorization for
// the status page.
func (s *Server) serveStaticDoc(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	// our home page is /status.html
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" || path == "status" {
		path = "status.html"
	}

	if path == "status.html" && !s.httpAuthorized(w, r) {
		return
	}

	path = "static/" + path

	doc, err := staticFS.ReadFile(path)
	if err != nil {
		clog.Warn(ctx, "not found", "err", err)
		http.NotFound(w, r)

		return
	}

	w.Header().Set("Content-Type", getContentTypeForPath(path))

	//nolint:gosec // doc is a compile-time-embedded static asset (embed.FS), not user content
	if _, err = w.Write(doc); err != nil {
		clog.Error(ctx, "web interface static document write failed", "err", err)
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

// webSocket upgrades a http connection to a websocket.
func webSocket(w http.ResponseWriter, r *http.Request) (*websocket.Conn, bool) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  webSocketBufferSize,
		WriteBufferSize: webSocketBufferSize,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, "Could not open websocket connection", http.StatusBadRequest)

		return conn, false
	}

	return conn, true
}

// webInterfaceStatusWS reads from and writes to the websocket on the status
// webpage.
func webInterfaceStatusWS(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.httpAuthorized(w, r) {
			return
		}

		conn, ok := webSocket(w, r)
		if !ok {
			clog.Error(ctx, "Failed to set up websocket", "Host", r.Host)

			return
		}

		// when the server shuts down it will close our conn, ending the main
		// goroutine
		storedName, stored := s.storeWebSocketConnection(conn)
		if !stored {
			if err := conn.Close(); err != nil {
				clog.Warn(ctx, "websocket close failed", "err", err)
			}

			return
		}

		statusSubscriptionID := s.registerStatusSubscription()

		// when the main goroutine closes we will end all the others
		stopper := make(chan bool)

		// go routine to read client requests and respond to them
		go s.runStatusWebSocketWorker(func() {
			s.readStatusWSRequests(ctx, conn, storedName, statusSubscriptionID, stopper)
		})

		// Set up goroutines to push changes to the client
		go s.runStatusWebSocketWorker(func() {
			s.setupUpdateListener(ctx, conn, stopper, storedName, s.statusCaster, "status updater")
		})
		go s.runStatusWebSocketWorker(func() {
			s.setupUpdateListener(ctx, conn, stopper, storedName, s.badServerCaster, "bad server caster")
		})
		go s.runStatusWebSocketWorker(func() {
			s.setupUpdateListener(ctx, conn, stopper, storedName, s.schedCaster, "scheduler issues caster")
		})
		go s.runStatusWebSocketWorker(func() {
			s.setupStatusSubscriptionUpdateListener(ctx, conn, stopper, storedName, statusSubscriptionID)
		})
	}
}

// readStatusWSRequests reads requests from a status page websocket and handles
// each until the connection closes, then stops the other per-connection
// goroutines.
func (s *Server) readStatusWSRequests(ctx context.Context, conn *websocket.Conn,
	connStorageName, subscriptionID string, stop chan bool) {
	// log panics and die
	defer internal.LogPanic(ctx, "jobqueue websocket client handling", true)

	defer func() {
		s.unregisterClientSubscription(subscriptionID)
		s.closeWebSocketConnection(ctx, connStorageName)

		// stop the other goroutines
		close(stop)
	}()

	for {
		req := jstatusReq{}

		if errr := conn.ReadJSON(&req); errr != nil {
			// browser was refreshed or server shutdown
			return
		}

		// Get the write mutex for this connection
		s.wsmutex.RLock()
		writeMutex := s.wsWriteMutexes[connStorageName]
		s.wsmutex.RUnlock()

		if writeMutex == nil {
			// Connection is being shut down
			return
		}

		s.handleStatusWSRequest(ctx, statusWSRequest{
			conn: conn, req: req, writeMutex: writeMutex, subscriptionID: subscriptionID,
		})
	}
}

// statusWSRequest bundles the context a single status-page websocket request
// handler needs.
type statusWSRequest struct {
	conn           *websocket.Conn
	req            jstatusReq
	writeMutex     *sync.Mutex
	subscriptionID string
}

// handleStatusWSRequest dispatches a single decoded status-page websocket
// request to the appropriate action.
func (s *Server) handleStatusWSRequest(ctx context.Context, r statusWSRequest) {
	switch {
	case r.req.Request != "":
		s.handleStatusWSCommand(ctx, r)
	case r.req.Key != "":
		s.handleStatusWSKeyRequest(ctx, r)
	}
}

// handleStatusWSCommand handles a status-page websocket request that carries a
// named Request command.
//
//nolint:cyclop,gocyclo,funlen // a flat command dispatch switch; each case delegates to a handler
func (s *Server) handleStatusWSCommand(ctx context.Context, r statusWSRequest) {
	switch r.req.Request {
	case jstatusRequestCurrent:
		s.sendCurrentStatus(ctx, r)
	case jstatusRequestDetails:
		s.sendJobDetails(ctx, r)
	case jstatusRequestUnsubscribe:
		s.unsubscribeFromJob(r.subscriptionID, r.req.Key)
	case "retry":
		s.kickJobs(ctx, s.reqToJobs(r.req, []queue.ItemState{queue.ItemStateBury}))
	case jstatusRequestRerun:
		s.rerunStatusJobs(ctx, r.req)
	case jstatusRequestRemove:
		jobs := s.reqToJobs(r.req, []queue.ItemState{
			queue.ItemStateBury, queue.ItemStateDelay,
			queue.ItemStateDependent, queue.ItemStateReady,
		})
		deleted := s.deleteJobs(ctx, jobs)
		clog.Debug(ctx, "removed jobs", "count", len(deleted))
	case jstatusRequestResume:
		s.resumeStatusJobs(ctx, r.req)
	case "kill":
		s.killStatusJobs(ctx, r.req)
	case "confirmBadServer":
		s.confirmBadServerFromStatus(ctx, r.req)
	case "dismissMsg":
		s.dismissSchedulerMessage(r.req)
	case "dismissMsgs":
		s.dismissAllSchedulerMessages()
	default:
	}
}

// sendCurrentStatus responds to a "current" request with v0.36.5's
// scan-on-connect: it sends the requesting client the current per-RepGroup
// status-count seed as jstateCount deltas (incomplete-only for the "+all+" live
// aggregate, plus each incomplete RepGroup's live-and-complete counts), then
// re-broadcasts the recoverable bad-server and scheduler-issue sets. The scan is
// incomplete-only so a completed-only RepGroup is naturally omitted from a fresh
// connection's seed (terminal-hiding on refresh); a RepGroup that COMPLETES
// while connected stays visible via the live running->complete delta.
func (s *Server) sendCurrentStatus(ctx context.Context, r statusWSRequest) {
	s.sendCurrentStatusCounts(ctx, r)

	for _, bs := range s.getBadServers() {
		s.badServerCaster.Send(bs)
	}

	s.simutex.RLock()
	defer s.simutex.RUnlock()

	for _, si := range s.schedIssues {
		s.schedCaster.Send(si)
	}
}

// sendCurrentStatusCounts sends the scan-on-connect status-count seed to the
// requesting client under its write mutex. It gets all current (incomplete)
// jobs, seeds the "+all+" live aggregate from them, then for each RepGroup among
// them seeds that RepGroup from its incomplete jobs plus its complete jobs.
func (s *Server) sendCurrentStatusCounts(ctx context.Context, r statusWSRequest) {
	jobs := s.getJobsCurrent(ctx, "", RepGroupMatchExact, 0, "", false, false, false)

	r.writeMutex.Lock()
	defer r.writeMutex.Unlock()

	if err := webInterfaceStatusSendGroupStateCount(r.conn, statusAllRepGroups, jobs); err != nil {
		return
	}

	repGroups := make(map[string][]*Job)
	for _, job := range jobs {
		repGroups[job.RepGroup] = append(repGroups[job.RepGroup], job)
	}

	for repGroup, rgJobs := range repGroups {
		complete, _, qerr := s.getCompleteJobsByRepGroup(repGroup)
		if qerr != "" {
			return
		}

		rgJobs = append(rgJobs, complete...)

		if err := webInterfaceStatusSendGroupStateCount(r.conn, repGroup, rgJobs); err != nil {
			return
		}
	}
}

// sendJobDetails sends the full status of every matching job in a rep group to
// the requesting client and subscribes the client to their future updates.
func (s *Server) sendJobDetails(ctx context.Context, r statusWSRequest) {
	req := r.req

	opts := repGroupOptions{
		RepGroup: req.RepGroup,
		Match:    normalizeRepGroupMatch("", req.Search),
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
	if errstr != "" || len(jobs) == 0 {
		return
	}

	jobKeys, statuses := jobStatuses(jobs, req)

	if len(jobKeys) > 0 {
		s.subscribeToJobs(r.subscriptionID, jobKeys)
	}

	s.writeJobStatuses(r, statuses)
}

// jobStatuses converts jobs to their JStatus and keys, overriding each status's
// rep group with the requested one unless this was a search. Conversion stops at
// the first job whose status cannot be built.
func jobStatuses(jobs []*Job, req jstatusReq) ([]string, []JStatus) {
	keys := make([]string, 0, len(jobs))
	statuses := make([]JStatus, 0, len(jobs))

	for _, job := range jobs {
		status, err := job.ToStatus()
		if err != nil {
			break
		}

		if !req.Search {
			// since we want to return the group the user asked for, not the most
			// recent group the job was made for
			status.RepGroup = req.RepGroup
		}

		statuses = append(statuses, status)
		keys = append(keys, job.Key())
	}

	return keys, statuses
}

// writeJobStatuses writes each job status to the client connection under its
// write mutex, invoking the optional details hook first. Writing stops at the
// first failure.
func (s *Server) writeJobStatuses(r statusWSRequest, statuses []JStatus) {
	r.writeMutex.Lock()
	defer r.writeMutex.Unlock()

	if s.statusWSDetailsHook != nil {
		s.statusWSDetailsHook()
	}

	for _, status := range statuses {
		if err := r.conn.WriteJSON(status); err != nil {
			return
		}
	}
}

// rerunStatusJobs reruns the completed jobs identified by a rerun request.
func (s *Server) rerunStatusJobs(ctx context.Context, req jstatusReq) {
	jobs, srerr, qerr := s.reqToCompletedJobs(req)
	if srerr != "" {
		clog.Warn(ctx, "web interface rerun lookup failed", "err", qerr)

		return
	}

	s.rerunCompletedJobs(ctx, jobs)
}

// resumeStatusJobs resumes the suspended jobs identified by a resume request.
func (s *Server) resumeStatusJobs(ctx context.Context, req jstatusReq) {
	jobs := s.reqToJobs(req, []queue.ItemState{queue.ItemStateSuspended})

	keys := make([]string, 0, len(jobs))
	for _, job := range jobs {
		keys = append(keys, job.Key())
	}

	resumed := s.resumeJobs(ctx, keys)
	clog.Debug(ctx, "resumed suspended jobs", "count", resumed)
}

// killStatusJobs kills the running jobs identified by a kill request.
func (s *Server) killStatusJobs(ctx context.Context, req jstatusReq) {
	for _, job := range s.reqToJobs(req, []queue.ItemState{queue.ItemStateRun}) {
		if _, err := s.killJob(ctx, job.Key()); err != nil {
			clog.Warn(ctx, "web interface kill job failed", "err", err)
		}
	}
}

// confirmBadServerFromStatus destroys a bad server confirmed dead from the
// status page.
func (s *Server) confirmBadServerFromStatus(ctx context.Context, req jstatusReq) {
	if req.ServerID == "" {
		return
	}

	s.bsmutex.Lock()
	server := s.badServers[req.ServerID]
	delete(s.badServers, req.ServerID)
	s.bsmutex.Unlock()

	if server != nil && server.IsBad() {
		if err := server.Destroy(ctx); err != nil {
			clog.Warn(ctx, "web interface confirm bad server destruction failed", "err", err)
		}
	}
}

// dismissSchedulerMessage forgets a single scheduler issue message.
func (s *Server) dismissSchedulerMessage(req jstatusReq) {
	if req.Msg == "" {
		return
	}

	s.simutex.Lock()
	delete(s.schedIssues, req.Msg)
	s.simutex.Unlock()
}

// dismissAllSchedulerMessages forgets all scheduler issue messages.
func (s *Server) dismissAllSchedulerMessages() {
	s.simutex.Lock()
	s.schedIssues = make(map[string]*schedulerIssue)
	s.simutex.Unlock()
}

// handleStatusWSKeyRequest sends the status of a single keyed job to the client
// and subscribes the client to its future updates.
func (s *Server) handleStatusWSKeyRequest(ctx context.Context, r statusWSRequest) {
	jobs, _, errstr := s.getJobsByKeys(ctx, []string{r.req.Key}, true, true)
	if errstr != "" || len(jobs) != 1 {
		return
	}

	status, err := jobs[0].ToStatus()
	if err != nil {
		return
	}

	s.subscribeToJobs(r.subscriptionID, []string{r.req.Key})

	r.writeMutex.Lock()
	defer r.writeMutex.Unlock()

	if err = r.conn.WriteJSON(status); err != nil {
		return
	}
}

func statusSubscriptionStopped(stop <-chan bool) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func resetCompletedJobForRerun(job *Job) {
	job.Lock()
	defer job.Unlock()

	resetJobExecutionFields(job)
	resetJobStatusFields(job)
}

func resetJobExecutionFields(job *Job) {
	job.clearActualCwd()
	job.PeakRAM = 0
	job.PeakDisk = 0
	job.Exited = false
	job.Exitcode = 0
	job.Lost = false
	job.FailReason = ""
	job.Pid = 0
	job.Host = ""
	job.HostID = ""
	job.HostIP = ""
	job.StartTime = time.Time{}
	job.EndTime = time.Time{}
	job.CPUtime = 0
	job.StdErrC = nil
	job.StdOutC = nil
}

func resetJobStatusFields(job *Job) {
	job.EnvC = nil
	job.EnvCRetrieved = false
	job.State = JobStateReady
	job.Attempts = 0
	job.UntilBuried = job.Retries + 1
	job.ReservedBy = uuid.UUID{}
	job.Similar = 0
	job.DelayTime = 0
	job.killCalled = false
	job.incrementedLimitGroups = nil
}

func (s *Server) completedJobByKey(req jstatusReq) ([]*Job, string, string) {
	live, err := s.db.checkIfLive(req.Key)
	if err != nil {
		return nil, ErrDBError, err.Error()
	}

	if live {
		return nil, "", ""
	}

	jobs, err := s.db.retrieveCompleteJobsByKeys([]string{req.Key})
	if err != nil {
		return nil, ErrDBError, err.Error()
	}

	if req.RepGroup != "" {
		for _, job := range jobs {
			job.Lock()
			job.RepGroup = req.RepGroup
			job.Unlock()
		}
	}

	return jobs, "", ""
}

// setupUpdateListener creates a goroutine that listens for updates from a
// broadcaster and forwards them to the WebSocket client.
func (s *Server) setupUpdateListener(ctx context.Context, conn *websocket.Conn, stop chan bool, //nolint:gocognit,funlen
	connName string, caster *caster, name string) {
	defer internal.LogPanic(ctx, "jobqueue websocket "+name, true)

	receiver := caster.Join()
	defer receiver.Close()

	for {
		select {
		case <-stop:
			return
		case msg, ok := <-receiver.In:
			if !ok {
				return
			}

			s.wsmutex.RLock()
			writeMutex := s.wsWriteMutexes[connName]
			s.wsmutex.RUnlock()

			if writeMutex == nil {
				return
			}

			writeMutex.Lock()
			err := conn.WriteJSON(msg)
			writeMutex.Unlock()

			if err != nil {
				clog.Warn(ctx, name+" failed to send JSON to client", "err", err)

				return
			}
		}
	}
}

// reqToJobs takes a request from the status webpage and returns the requested
// jobs.
func (s *Server) reqToJobs(req jstatusReq, allowedItemStates []queue.ItemState) []*Job {
	allowed := make(map[queue.ItemState]bool)
	for _, is := range allowedItemStates {
		allowed[is] = true
	}

	if req.RepGroup != "" {
		return s.reqToRepGroupJobs(req, allowed)
	}

	if req.Key != "" {
		return s.reqToKeyJobs(req, allowed)
	}

	return nil
}

// reqToRepGroupJobs returns the allowed-state jobs in a rep group that match the
// request's exit code and fail reason.
func (s *Server) reqToRepGroupJobs(req jstatusReq, allowed map[queue.ItemState]bool) []*Job {
	var jobs []*Job

	for _, key := range s.rpl.Values(req.RepGroup) {
		item, err := s.q.Get(key)
		if item == nil || err != nil {
			continue
		}

		job, stats, ok := allowedItemJob(item, allowed)
		if !ok {
			continue
		}

		job.Lock()
		job.State = s.itemStateToJobState(stats.State, job.Lost)

		if job.Exitcode == req.Exitcode && job.FailReason == req.FailReason {
			jobs = append(jobs, job)
		}
		job.Unlock()
	}

	return jobs
}

// reqToKeyJobs returns the single allowed-state job for the request's key, if
// any.
func (s *Server) reqToKeyJobs(req jstatusReq, allowed map[queue.ItemState]bool) []*Job {
	item, err := s.q.Get(req.Key)
	if item == nil || err != nil {
		return nil
	}

	job, stats, ok := allowedItemJob(item, allowed)
	if !ok {
		return nil
	}

	job.Lock()
	job.State = s.itemStateToJobState(stats.State, job.Lost)
	job.Unlock()

	return []*Job{job}
}

// allowedItemJob returns the item's job and stats if the item is currently in an
// allowed state.
func allowedItemJob(item *queue.Item, allowed map[queue.ItemState]bool) (*Job, *queue.ItemStats, bool) {
	stats := item.Stats()
	if !allowed[stats.State] {
		return nil, stats, false
	}

	job, ok := item.Data().(*Job)

	return job, stats, ok
}

func (s *Server) rerunCompletedJobs(ctx context.Context, jobs []*Job) {
	jobsByEnvKey := make(map[string][]*Job)

	for _, job := range jobs {
		job.RLock()
		envkey := job.EnvKey
		job.RUnlock()

		resetCompletedJobForRerun(job)
		jobsByEnvKey[envkey] = append(jobsByEnvKey[envkey], job)
	}

	for envkey, envJobs := range jobsByEnvKey {
		added, dups, _, _, srerr, err := s.createJobs(ctx, envJobs, envkey, false)
		if err != nil {
			clog.Warn(ctx, "web interface rerun failed", "err", err, "srerr", srerr)

			continue
		}

		clog.Debug(ctx, "reran completed jobs", "new", added, "dups", dups)
	}
}

func (s *Server) setupStatusSubscriptionUpdateListener(ctx context.Context, conn *websocket.Conn, stop chan bool,
	connName string, subscriptionID string) {
	defer internal.LogPanic(ctx, "jobqueue websocket status subscription updater", true)

	for {
		if statusSubscriptionStopped(stop) {
			return
		}

		updates, err := s.waitForSubscriptionUpdates(subscriptionID, serverSubscriptionHoldTime)
		if err != nil {
			return
		}

		if !s.writeStatusSubscriptionUpdates(ctx, conn, connName, updates) {
			return
		}
	}
}

func (s *Server) writeStatusSubscriptionUpdates(ctx context.Context, conn *websocket.Conn,
	connName string, updates []*JobUpdate) bool {
	for _, update := range updates {
		status := s.statusFromSubscriptionUpdate(ctx, update)
		if status == nil {
			continue
		}

		if !s.writeStatusSubscriptionUpdate(ctx, conn, connName, status) {
			return false
		}
	}

	return true
}
