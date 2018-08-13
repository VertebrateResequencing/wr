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

package jobqueue

// This file contains the REST API code of the server. It is not used
// internally, but provides 3rd party non-go clients the ability to interact
// with the job queue using JSON over HTTP.

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/bytefmt"
	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/ugorji/go/codec"
)

const (
	restJobsEndpoint       = "/rest/v1/jobs/"
	restWarningsEndpoint   = "/rest/v1/warnings/"
	restBadServersEndpoint = "/rest/v1/servers/"
	restFileUploadEndpoint = "/rest/v1/upload/"
	restFormTrue           = "true"
	bearerSchema           = "Bearer "
)

// JobViaJSON describes the properties of a JOB that a user wishes to add to the
// queue, convenient if they are supplying JSON.
type JobViaJSON struct {
	Cmd          string       `json:"cmd"`
	Cwd          string       `json:"cwd"`
	CwdMatters   bool         `json:"cwd_matters"`
	ChangeHome   bool         `json:"change_home"`
	MountConfigs MountConfigs `json:"mounts"`
	ReqGrp       string       `json:"req_grp"`
	// Memory is a number and unit suffix, eg. 1G for 1 Gigabyte.
	Memory string `json:"memory"`
	// Time is a duration with a unit suffix, eg. 1h for 1 hour.
	Time string `json:"time"`
	CPUs *int   `json:"cpus"`
	// Disk is the number of Gigabytes the cmd will use.
	Disk             *int              `json:"disk"`
	Override         *int              `json:"override"`
	Priority         *int              `json:"priority"`
	Retries          *int              `json:"retries"`
	RepGrp           string            `json:"rep_grp"`
	DepGrps          []string          `json:"dep_grps"`
	Deps             []string          `json:"deps"`
	CmdDeps          Dependencies      `json:"cmd_deps"`
	OnFailure        BehavioursViaJSON `json:"on_failure"`
	OnSuccess        BehavioursViaJSON `json:"on_success"`
	OnExit           BehavioursViaJSON `json:"on_exit"`
	Env              []string          `json:"env"`
	MonitorDocker    string            `json:"monitor_docker"`
	CloudOS          string            `json:"cloud_os"`
	CloudUser        string            `json:"cloud_username"`
	CloudScript      string            `json:"cloud_script"`
	CloudConfigFiles string            `json:"cloud_config_files"`
	CloudOSRam       *int              `json:"cloud_ram"`
	CloudFlavor      string            `json:"cloud_flavor"`
	CloudShared      bool              `json:"cloud_shared"`
	BsubMode         string            `json:"bsub_mode"`
	RTimeout         *int              `json:"rtimeout"`
}

// JobDefaults is supplied to JobViaJSON.Convert() to provide default values for
// the conversion.
type JobDefaults struct {
	RepGrp string
	// Cwd defaults to /tmp.
	Cwd        string
	CwdMatters bool
	ChangeHome bool
	ReqGrp     string
	// CPUs is the number of CPU cores each cmd will use. Defaults to 1.
	CPUs int
	// Memory is the number of Megabytes each cmd will use. Defaults to 1000.
	Memory int
	// Time is the amount of time each cmd will run for. Defaults to 1 hour.
	Time time.Duration
	// Disk is the number of Gigabytes cmds will use.
	Disk      int
	Override  int
	Priority  int
	Retries   int
	DepGroups []string
	Deps      Dependencies
	// Env is a comma separated list of key=val pairs.
	Env           string
	OnFailure     Behaviours
	OnSuccess     Behaviours
	OnExit        Behaviours
	MountConfigs  MountConfigs
	MonitorDocker string
	CloudOS       string
	CloudUser     string
	CloudFlavor   string
	// CloudScript is the local path to a script.
	CloudScript string
	// CloudConfigFiles is the config files to copy in cloud.Server.CopyOver() format
	CloudConfigFiles string
	// CloudOSRam is the number of Megabytes that CloudOS needs to run. Defaults
	// to 1000.
	CloudOSRam    int
	CloudShared   bool
	BsubMode      string
	compressedEnv []byte
	osRAM         string
	RTimeout      int
}

// DefaultCwd returns the Cwd value, defaulting to /tmp.
func (jd *JobDefaults) DefaultCwd() string {
	if jd.Cwd == "" {
		return "/tmp"
	}
	return jd.Cwd
}

// DefaultCPUs returns the CPUs value, but a minimum of 1.
func (jd *JobDefaults) DefaultCPUs() int {
	if jd.CPUs < 1 {
		return 1
	}
	return jd.CPUs
}

// DefaultMemory returns the Memory value, but if <1 returns 1000 instead.
func (jd *JobDefaults) DefaultMemory() int {
	if jd.Memory < 1 {
		return 1000
	}
	return jd.Memory
}

// DefaultTime returns the Time value, but if 0 returns 1 hour instead.
func (jd *JobDefaults) DefaultTime() time.Duration {
	if jd.Time == 0 {
		return 1 * time.Hour
	}
	return jd.Time
}

// DefaultEnv returns an encoded compressed version of the Env value.
func (jd *JobDefaults) DefaultEnv() ([]byte, error) {
	var err error
	if len(jd.compressedEnv) == 0 {
		jd.compressedEnv, err = compressEnv(strings.Split(jd.Env, ","))
	}
	return jd.compressedEnv, err
}

// DefaultCloudOSRam returns a string version of the CloudOSRam value, which is
// treated as 1000 if 0.
func (jd *JobDefaults) DefaultCloudOSRam() string {
	if jd.osRAM == "" {
		ram := jd.CloudOSRam
		if ram == 0 {
			ram = 1000
		}
		jd.osRAM = strconv.Itoa(ram)
	}
	return jd.osRAM
}

// Convert considers the supplied defaults and returns a *Job based on the
// properties of this JobViaJSON. The Job will not be in the queue until passed
// to a method that adds jobs to the queue.
func (jvj *JobViaJSON) Convert(jd *JobDefaults) (*Job, error) {
	var cmd, cwd, rg, repg, monitorDocker string
	var mb, cpus, disk, override, priority, retries int
	var dur time.Duration
	var envOverride []byte
	var depGroups []string
	var deps Dependencies
	var behaviours Behaviours
	var mounts MountConfigs
	var bsubMode string

	if jvj.RepGrp == "" {
		repg = jd.RepGrp
	} else {
		repg = jvj.RepGrp
	}

	cmd = jvj.Cmd
	if cmd == "" {
		return nil, fmt.Errorf("cmd was not specified")
	}

	if jvj.Cwd == "" {
		cwd = jd.DefaultCwd()
	} else {
		cwd = jvj.Cwd
	}

	cwdMatters := jd.CwdMatters
	if jvj.CwdMatters {
		cwdMatters = true
	}

	changeHome := jd.ChangeHome
	if jvj.ChangeHome {
		changeHome = true
	}

	if jvj.ReqGrp == "" {
		if jd.ReqGrp != "" {
			rg = jd.ReqGrp
		} else {
			parts := strings.Split(cmd, " ")
			rg = filepath.Base(parts[0])
		}
	} else {
		rg = jvj.ReqGrp
	}

	if jvj.CPUs == nil {
		cpus = jd.DefaultCPUs()
	} else {
		cpus = *jvj.CPUs
	}

	if jvj.Memory == "" {
		mb = jd.DefaultMemory()
	} else {
		thismb, err := bytefmt.ToMegabytes(jvj.Memory)
		if err != nil {
			return nil, fmt.Errorf("memory value (%s) was not specified correctly: %s", jvj.Memory, err)
		}
		mb = int(thismb)
	}

	if jvj.Time == "" {
		dur = jd.DefaultTime()
	} else {
		var err error
		dur, err = time.ParseDuration(jvj.Time)
		if err != nil {
			return nil, fmt.Errorf("time value (%s) was not specified correctly: %s", jvj.Time, err)
		}
	}

	if jvj.Override == nil {
		override = jd.Override
	} else {
		override = *jvj.Override
	}
	if override < 0 || override > 2 {
		return nil, fmt.Errorf("override value (%d) is not in the range 0..2", override)
	}

	if jvj.Disk == nil {
		disk = jd.Disk
	} else {
		disk = *jvj.Disk
	}

	if jvj.Priority == nil {
		priority = jd.Priority
	} else {
		priority = *jvj.Priority
	}
	if priority < 0 || priority > 255 {
		return nil, fmt.Errorf("priority value (%d) is not in the range 0..255", priority)
	}

	if jvj.Retries == nil {
		retries = jd.Retries
	} else {
		retries = *jvj.Retries
	}
	if retries < 0 || retries > 255 {
		return nil, fmt.Errorf("retries value (%d) is not in the range 0..255", retries)
	}

	if len(jvj.DepGrps) == 0 {
		depGroups = jd.DepGroups
	} else {
		depGroups = jvj.DepGrps
	}

	if len(jvj.Deps) == 0 && len(jvj.CmdDeps) == 0 {
		deps = jd.Deps
	} else {
		if len(jvj.CmdDeps) > 0 {
			deps = jvj.CmdDeps
		}
		if len(jvj.Deps) > 0 {
			for _, depgroup := range jvj.Deps {
				deps = append(deps, NewDepGroupDependency(depgroup))
			}
		}
	}

	if len(jvj.Env) > 0 {
		var err error
		envOverride, err = compressEnv(jvj.Env)
		if err != nil {
			return nil, err
		}
	} else if len(jd.Env) > 0 {
		var err error
		envOverride, err = jd.DefaultEnv()
		if err != nil {
			return nil, err
		}
	}

	if len(jvj.OnFailure) > 0 {
		behaviours = append(behaviours, jvj.OnFailure.Behaviours(OnFailure)...)
	} else if len(jd.OnFailure) > 0 {
		behaviours = append(behaviours, jd.OnFailure...)
	}
	if len(jvj.OnSuccess) > 0 {
		behaviours = append(behaviours, jvj.OnSuccess.Behaviours(OnSuccess)...)
	} else if len(jd.OnSuccess) > 0 {
		behaviours = append(behaviours, jd.OnSuccess...)
	}
	if len(jvj.OnExit) > 0 {
		behaviours = append(behaviours, jvj.OnExit.Behaviours(OnExit)...)
	} else if len(jd.OnExit) > 0 {
		behaviours = append(behaviours, jd.OnExit...)
	}

	if len(jvj.MountConfigs) > 0 {
		mounts = jvj.MountConfigs
	} else if len(jd.MountConfigs) > 0 {
		mounts = jd.MountConfigs
	}

	bsubMode = jvj.BsubMode
	if bsubMode == "" && jd.BsubMode != "" {
		bsubMode = jd.BsubMode
	}

	if jvj.MonitorDocker == "" {
		monitorDocker = jd.MonitorDocker
	} else {
		monitorDocker = jvj.MonitorDocker
	}

	// scheduler-specific options
	other := make(map[string]string)
	if jvj.CloudOS != "" {
		other["cloud_os"] = jvj.CloudOS
	} else if jd.CloudOS != "" {
		other["cloud_os"] = jd.CloudOS
	}

	if jvj.CloudUser != "" {
		other["cloud_user"] = jvj.CloudUser
	} else if jd.CloudUser != "" {
		other["cloud_user"] = jd.CloudUser
	}

	if jvj.CloudFlavor != "" {
		other["cloud_flavor"] = jvj.CloudFlavor
	} else if jd.CloudFlavor != "" {
		other["cloud_flavor"] = jd.CloudFlavor
	}

	var cloudScriptPath string
	if jvj.CloudScript != "" {
		cloudScriptPath = jvj.CloudScript
	} else if jd.CloudScript != "" {
		cloudScriptPath = jd.CloudScript
	}
	if cloudScriptPath != "" {
		cloudScriptPath = internal.TildaToHome(cloudScriptPath)
		postCreation, err := ioutil.ReadFile(cloudScriptPath)
		if err != nil {
			return nil, fmt.Errorf("cloud_script [%s] could not be read: %s", cloudScriptPath, err)
		}
		other["cloud_script"] = string(postCreation)
	}

	if jvj.CloudConfigFiles != "" {
		other["cloud_config_files"] = jvj.CloudConfigFiles
	} else if jd.CloudConfigFiles != "" {
		other["cloud_config_files"] = jd.CloudConfigFiles
	}

	if jvj.CloudOSRam != nil {
		ram := *jvj.CloudOSRam
		other["cloud_os_ram"] = strconv.Itoa(ram)
	} else if jd.CloudOSRam != 0 {
		other["cloud_os_ram"] = jd.DefaultCloudOSRam()
	}

	if jvj.CloudShared || jd.CloudShared {
		other["cloud_shared"] = "true"
	}

	if jvj.RTimeout != nil {
		rtimeout := *jvj.RTimeout
		other["rtimeout"] = strconv.Itoa(rtimeout)
	} else if jd.RTimeout != 0 {
		other["rtimeout"] = strconv.Itoa(jd.RTimeout)
	}

	return &Job{
		RepGroup:      repg,
		Cmd:           cmd,
		Cwd:           cwd,
		CwdMatters:    cwdMatters,
		ChangeHome:    changeHome,
		ReqGroup:      rg,
		Requirements:  &jqs.Requirements{RAM: mb, Time: dur, Cores: cpus, Disk: disk, Other: other},
		Override:      uint8(override),
		Priority:      uint8(priority),
		Retries:       uint8(retries),
		DepGroups:     depGroups,
		Dependencies:  deps,
		EnvOverride:   envOverride,
		Behaviours:    behaviours,
		MountConfigs:  mounts,
		MonitorDocker: monitorDocker,
		BsubMode:      bsubMode,
	}, nil
}

// httpAuthorized checks for parameter 'token' and for Authorization header for
// Bearer token; if not supplied, or the token is wrong, writes out an error to
// w, otherwise returns true.
func (s *Server) httpAuthorized(w http.ResponseWriter, r *http.Request) bool {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, fmt.Sprintf("form parsing error: %s", err), http.StatusBadRequest)
		return false
	}

	// try token parameter
	token := r.Form.Get("token")
	if token == "" {
		// try auth header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return false
		}

		if !strings.HasPrefix(authHeader, bearerSchema) {
			http.Error(w, "Authorization requires Bearer scheme", http.StatusUnauthorized)
			return false
		}

		token = authHeader[len(bearerSchema):]
	}

	if !tokenMatches([]byte(token), s.token) {
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return false
	}
	return true
}

// restJobs lets you do CRUD on jobs in the queue.
func restJobs(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(s.Logger, "jobqueue web server restJobs", false)

		ok := s.httpAuthorized(w, r)
		if !ok {
			return
		}

		// carry out a different action based on the HTTP Verb
		var jobs []*Job
		var status int
		var err error
		switch r.Method {
		case http.MethodGet:
			jobs, status, err = restJobsStatus(r, s)
		case http.MethodPost:
			jobs, status, err = restJobsAdd(r, s)
		default:
			http.Error(w, "So far only GET and POST are supported", http.StatusBadRequest)
			return
		}

		if status >= 400 || err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		// convert jobs to jstatus
		jstati := make([]JStatus, len(jobs))
		for i, job := range jobs {
			jstati[i] = job.ToStatus()
		}

		// return job details as JSON
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(status)
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		erre := encoder.Encode(jstati)
		if erre != nil {
			s.Warn("restJobs failed to encode job statuses", "err", erre)
		}
	}
}

// restJobsStatus gets the status of the requested jobs in the queue. The
// request url can be suffixed with comma separated job keys or RepGroups.
// Possible query parameters are search, std, env (which can take a "true"
// value), limit (a number) and state (one of
// delayed|ready|reserved|running|lost|buried| dependent|complete). Returns the
// Jobs, a http.Status* value and error.
func restJobsStatus(r *http.Request, s *Server) ([]*Job, int, error) {
	// handle possible ?query parameters
	var search, getStd, getEnv bool
	var limit int
	var state JobState
	var err error

	if r.Form.Get("search") == restFormTrue {
		search = true
	}
	if r.Form.Get("std") == restFormTrue {
		getStd = true
	}
	if r.Form.Get("env") == restFormTrue {
		getEnv = true
	}
	if r.Form.Get("limit") != "" {
		limit, err = strconv.Atoi(r.Form.Get("limit"))
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
	}
	if r.Form.Get("state") != "" {
		switch r.Form.Get("state") {
		case "delayed":
			state = JobStateDelayed
		case "ready":
			state = JobStateReady
		case "reserved":
			state = JobStateReserved
		case "running":
			state = JobStateRunning
		case "lost":
			state = JobStateLost
		case "buried":
			state = JobStateBuried
		case "dependent":
			state = JobStateDependent
		case "complete":
			state = JobStateComplete
		}
	}

	if len(r.URL.Path) > len(restJobsEndpoint) {
		// get the requested jobs
		ids := r.URL.Path[len(restJobsEndpoint):]
		var jobs []*Job
		for _, id := range strings.Split(ids, ",") {
			if len(id) == 32 {
				// id might be a Job.key()
				theseJobs, _, qerr := s.getJobsByKeys([]string{id}, getStd, getEnv)
				if qerr == "" && len(theseJobs) > 0 {
					jobs = append(jobs, theseJobs...)
					continue
				}
			}

			// id might be a Job.RepGroup
			theseJobs, _, qerr := s.getJobsByRepGroup(id, search, limit, state, getStd, getEnv)
			if qerr != "" {
				return nil, http.StatusInternalServerError, fmt.Errorf(qerr)
			}
			if len(theseJobs) > 0 {
				jobs = append(jobs, theseJobs...)
			}
		}
		return jobs, http.StatusOK, err
	}

	// get all current jobs
	return s.getJobsCurrent(limit, state, getStd, getEnv), http.StatusOK, err
}

// restJobsAdd creates and adds jobs to the queue and returns them on success.
// The request must have some POSTed JSON that is a []*JobViaJSON.
//
// It optionally takes parameters to use as defaults for the job properties,
// which correspond to the json properties of a JobViaJSON (except for cmd and
// cmd_deps). For dep_grps, deps and env, which normally take []string, provide
// a comma-separated list. mounts, on_failure, on_success and on_exit values
// should be supplied as url query escaped JSON strings.
//
// The returned int is a http.Status* variable.
func restJobsAdd(r *http.Request, s *Server) ([]*Job, int, error) {
	// handle possible ?query parameters
	jd := &JobDefaults{
		Cwd:           r.Form.Get("cwd"),
		RepGrp:        r.Form.Get("rep_grp"),
		ReqGrp:        r.Form.Get("req_grp"),
		CPUs:          urlStringToInt(r.Form.Get("cpus")),
		Disk:          urlStringToInt(r.Form.Get("disk")),
		Override:      urlStringToInt(r.Form.Get("override")),
		Priority:      urlStringToInt(r.Form.Get("priority")),
		Retries:       urlStringToInt(r.Form.Get("retries")),
		DepGroups:     urlStringToSlice(r.Form.Get("dep_grps")),
		Env:           r.Form.Get("env"),
		MonitorDocker: r.Form.Get("monitor_docker"),
		CloudOS:       r.Form.Get("cloud_os"),
		CloudUser:     r.Form.Get("cloud_username"),
		CloudScript:   r.Form.Get("cloud_script"),
		CloudFlavor:   r.Form.Get("cloud_flavor"),
		CloudOSRam:    urlStringToInt(r.Form.Get("cloud_ram")),
		BsubMode:      r.Form.Get("bsub_mode"),
	}
	if r.Form.Get("cwd_matters") == restFormTrue {
		jd.CwdMatters = true
	}
	if r.Form.Get("change_home") == restFormTrue {
		jd.ChangeHome = true
	}
	if r.Form.Get("cloud_shared") == restFormTrue {
		jd.CloudShared = true
	}
	if r.Form.Get("memory") != "" {
		mb, err := bytefmt.ToMegabytes(r.Form.Get("memory"))
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		jd.Memory = int(mb)
	}
	if r.Form.Get("time") != "" {
		var err error
		jd.Time, err = time.ParseDuration(r.Form.Get("time"))
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
	}
	defaultDeps := urlStringToSlice(r.Form.Get("deps"))
	if len(defaultDeps) > 0 {
		for _, depgroup := range defaultDeps {
			jd.Deps = append(jd.Deps, NewDepGroupDependency(depgroup))
		}
	}
	if r.Form.Get("on_failure") != "" {
		var bvj BehavioursViaJSON
		err := urlStringToStruct(r.Form.Get("on_failure"), &bvj)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		if bvj != nil {
			jd.OnFailure = bvj.Behaviours(OnFailure)
		}
	}
	if r.Form.Get("on_success") != "" {
		var bvj BehavioursViaJSON
		err := urlStringToStruct(r.Form.Get("on_success"), &bvj)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		if bvj != nil {
			jd.OnSuccess = bvj.Behaviours(OnSuccess)
		}
	}
	if r.Form.Get("on_exit") != "" {
		var bvj BehavioursViaJSON
		err := urlStringToStruct(r.Form.Get("on_exit"), &bvj)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		if bvj != nil {
			jd.OnExit = bvj.Behaviours(OnExit)
		}
	}
	if r.Form.Get("mounts") != "" {
		var mcs MountConfigs
		err := urlStringToStruct(r.Form.Get("mounts"), &mcs)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		if mcs != nil {
			jd.MountConfigs = mcs
		}
	}

	// decode the posted JSON
	var jvjs []*JobViaJSON
	err := json.NewDecoder(r.Body).Decode(&jvjs)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	// convert to real Job structs with default values filled in
	var inputJobs []*Job
	for _, jvj := range jvjs {
		job, errf := jvj.Convert(jd)
		if errf != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("There was a problem interpreting your job: %s", errf)
		}
		inputJobs = append(inputJobs, job)
	}

	envkey, err := s.db.storeEnv([]byte{})
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	_, _, _, _, err = s.createJobs(inputJobs, envkey, true)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	// see which of the inputJobs are now actually in the queue
	// *** queue.AddMany doesn't currently return which jobs were added and
	// which were dups, and server.createJobs doesn't know which were ignored
	// due to being incomplete, so we do this loop even though it's probably
	// slow and wasteful?...
	var jobs []*Job
	for _, job := range inputJobs {
		item, qerr := s.q.Get(job.Key())
		if qerr == nil && item != nil {
			// append the q's version of the job, not the input job, since the
			// job may have been a duplicate and we want to return its current
			// state
			jobs = append(jobs, s.itemToJob(item, false, false))
		}
	}

	return jobs, http.StatusCreated, err
}

// restWarnings lets you read warnings from the scheduler, and auto-"dismisses"
// (deletes) them.
func restWarnings(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(s.Logger, "jobqueue web server restWarnings", false)

		ok := s.httpAuthorized(w, r)
		if !ok {
			return
		}

		// carry out a different action based on the HTTP Verb
		sis := []*schedulerIssue{}
		switch r.Method {
		case http.MethodGet:
			s.simutex.Lock()
			for key, si := range s.schedIssues {
				sis = append(sis, si)
				delete(s.schedIssues, key)
			}
			s.simutex.Unlock()
		default:
			http.Error(w, "Only GET is supported", http.StatusBadRequest)
			return
		}

		// return schedulerIssues as JSON
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		erre := encoder.Encode(sis)
		if erre != nil {
			s.Warn("restWarnings failed to encode scheduler issues", "err", erre)
		}
	}
}

// restBadServers lets you do CRUD on cloud servers that have gone bad. The
// DELETE verb has a required 'id' parameter, being the ID of a server you wish
// to confirm as bad and have terminated if it still exists.
func restBadServers(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(s.Logger, "jobqueue web server restBadServers", false)

		ok := s.httpAuthorized(w, r)
		if !ok {
			return
		}

		// carry out a different action based on the HTTP Verb
		switch r.Method {
		case http.MethodGet:
			servers := s.getBadServers()
			if len(servers) == 0 {
				servers = []*badServer{}
			}
			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
			w.WriteHeader(http.StatusOK)
			encoder := json.NewEncoder(w)
			encoder.SetEscapeHTML(false)
			erre := encoder.Encode(servers)
			if erre != nil {
				s.Warn("restBadServers failed to encode servers", "err", erre)
			}
			return
		case http.MethodDelete:
			serverID := r.Form.Get("id")
			if serverID == "" {
				http.Error(w, "id parameter is required", http.StatusBadRequest)
				return
			}
			s.bsmutex.Lock()
			server := s.badServers[serverID]
			delete(s.badServers, serverID)
			s.bsmutex.Unlock()
			if server == nil {
				http.Error(w, "Server was not known to be bad", http.StatusNotFound)
				return
			}
			if server.IsBad() {
				err := server.Destroy()
				if err != nil {
					http.Error(w, fmt.Sprintf("Server was bad but could not be destroyed: %s", err), http.StatusNotModified)
					return
				}
			}
			w.WriteHeader(http.StatusOK)
			return
		default:
			http.Error(w, "Only GET and DELETE are supported", http.StatusBadRequest)
			return
		}
	}
}

// restFileUpload lets you upload files from a client to the server. The only
// method supported is PUT.
func restFileUpload(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(s.Logger, "jobqueue web server restFileUpload", false)

		ok := s.httpAuthorized(w, r)
		if !ok {
			return
		}

		if r.Method != http.MethodPut {
			http.Error(w, "Only PUT is supported", http.StatusBadRequest)
			return
		}

		savePath, err := s.uploadFile(r.Body, r.Form.Get("path"))
		if err != nil {
			http.Error(w, "file upload failed", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		msg := make(map[string]string)
		msg["path"] = savePath
		err = encoder.Encode(msg)
		if err != nil {
			s.Warn("restFileUpload failed to encode success msg", "err", err)
		}
	}
}

// urlStringToInt takes a possible string from a url parameter value and
// converts it to an int. If the value is "", or if the value isn't a number,
// returns 0.
func urlStringToInt(value string) int {
	if value == "" {
		return 0
	}
	num, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return num
}

// urlStringToSlice takes a possible comma-delimited string from a url parameter
// value and converts it to []string. If the value is "", returns an empty
// slice.
func urlStringToSlice(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

// urlStringToStruct takes a possible query escaped JSON string from a url
// parameter value and unmarshals it in to the pointed to struct. If the value
// is "", does nothing.
func urlStringToStruct(value string, v interface{}) error {
	if value == "" {
		return nil
	}
	jsonString, err := url.QueryUnescape(value)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(jsonString), v)
}

// compressEnv is a slower (?) version of Client.CompressEnv since we have to
// make a new codec each time
func compressEnv(envars []string) ([]byte, error) {
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, new(codec.BincHandle))
	err := enc.Encode(&envStr{envars})
	if err != nil {
		return nil, err
	}
	return compress(encoded)
}
