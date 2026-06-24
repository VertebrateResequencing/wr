/*******************************************************************************
 * Copyright (c) 2017-2021, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: [Theo Barber-Bany] <theobarberbany@gmail.com>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Rosie Kern
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

// This file contains the REST API code of the server. It is not used
// internally, but provides 3rd party non-go clients the ability to interact
// with the job queue using JSON over HTTP.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/bytefmt"
	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/ugorji/go/codec"
)

const (
	restAPIVersion         = "1"
	restVersionEndpoint    = "/rest/version/"
	restJobsEndpoint       = "/rest/v" + restAPIVersion + "/jobs/"
	restWarningsEndpoint   = "/rest/v" + restAPIVersion + "/warnings/"
	restBadServersEndpoint = "/rest/v" + restAPIVersion + "/servers/"
	restFileUploadEndpoint = "/rest/v" + restAPIVersion + "/upload/"
	restInfoEndpoint       = "/rest/v" + restAPIVersion + "/info/"
	restFormTrue           = "true"
	bearerSchema           = "Bearer "
	restJobKeyLength       = 32
	restModifyOverrideMax  = 2
	restModifyUint8Max     = 255
)

var (
	errRESTModifyCmdEmpty        = errors.New("cmd cannot be empty")
	errRESTModifyCwdEmpty        = errors.New("cwd cannot be empty")
	errRESTModifyIdentifierEmpty = errors.New("job identifier is required")
	errRESTModifyNoEditable      = errors.New("no editable jobs matched")
	errRESTModifyCmdMultiJob     = errors.New("cmd can only be modified for one job")
	errRESTModifyNoneModified    = errors.New("no jobs were modified")
	errRESTModifyNotFound        = errors.New("job not found")
)

type restRangeError struct {
	name  string
	value int
	limit int
}

func (e restRangeError) Error() string {
	return fmt.Sprintf("%s value (%d) is not in the range 0..%d", e.name, e.value, e.limit)
}

func uint8ModificationValue(name string, value *int, limit int) (uint8, bool, error) {
	if value == nil {
		return 0, false, nil
	}

	if *value < 0 || *value > limit {
		return 0, false, restRangeError{name: name, value: *value, limit: limit}
	}

	parsed, err := strconv.ParseUint(strconv.Itoa(*value), 10, 8)
	if err != nil {
		return 0, false, err
	}

	return uint8(parsed), true, nil
}

func restJobsModify(ctx context.Context, r *http.Request, s *Server) (*JobModifyResponse, int, error) {
	ids, modifier, status, err := restJobModifierFromRequest(r)
	if err != nil {
		return nil, status, err
	}

	editableKeys, status, err := restEditableKeysForModification(ctx, s, ids, modifier)
	if err != nil {
		return nil, status, err
	}

	modified, err := s.modifyJobsByKeys(ctx, editableKeys, modifier)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	if len(modified) == 0 {
		return nil, http.StatusConflict, s.restModifyEmptyResultError(editableKeys)
	}

	statuses, err := s.modifiedJobStatuses(ctx, modified)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	return &JobModifyResponse{Modified: modified, Jobs: statuses}, http.StatusOK, nil
}

func restJobModifierFromRequest(r *http.Request) (string, *JobModifier, int, error) {
	ids := strings.TrimPrefix(r.URL.Path, restJobsEndpoint)
	if ids == "" {
		return "", nil, http.StatusBadRequest, errRESTModifyIdentifierEmpty
	}

	var modifyJSON JobModifyViaJSON
	if err := json.NewDecoder(r.Body).Decode(&modifyJSON); err != nil {
		return "", nil, http.StatusBadRequest, err
	}

	modifier, err := modifyJSON.Convert()
	if err != nil {
		return "", nil, http.StatusBadRequest, err
	}

	return ids, modifier, http.StatusOK, nil
}

func restEditableKeysForModification(ctx context.Context, s *Server, ids string,
	modifier *JobModifier,
) ([]string, int, error) {
	targets, status, err := restJobsModificationTargets(ctx, s, ids)
	if err != nil {
		return nil, status, err
	}

	editableKeys := restEditableJobKeys(targets)
	if len(editableKeys) == 0 {
		return nil, http.StatusConflict, errRESTModifyNoEditable
	}

	if modifier.Cmd != "" && len(editableKeys) > 1 {
		return nil, http.StatusBadRequest, errRESTModifyCmdMultiJob
	}

	return editableKeys, http.StatusOK, nil
}

func restJobsModificationTargets(ctx context.Context, s *Server, ids string) ([]*Job, int, error) {
	var targets []*Job

	for _, id := range strings.Split(ids, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}

		jobs, status, err := restJobsModificationTarget(ctx, s, id)
		if err != nil {
			return nil, status, err
		}

		targets = append(targets, jobs...)
	}

	if len(targets) == 0 {
		return nil, http.StatusNotFound, errRESTModifyNotFound
	}

	return targets, http.StatusOK, nil
}

func restJobsModificationTarget(ctx context.Context, s *Server, id string) ([]*Job, int, error) {
	if len(id) != restJobKeyLength {
		return restJobsModificationRepGroupTarget(ctx, s, id)
	}

	jobs, _, qerr := s.getJobsByKeys(ctx, []string{id}, false, false)
	if qerr != "" {
		return nil, http.StatusInternalServerError, Error{Err: qerr}
	}

	if len(jobs) > 0 {
		return jobs, http.StatusOK, nil
	}

	return restJobsModificationRepGroupTarget(ctx, s, id)
}

func restJobsModificationRepGroupTarget(ctx context.Context, s *Server, id string) ([]*Job, int, error) {
	opts := repGroupOptions{
		RepGroup: id,
		Match:    RepGroupMatchExact,
	}

	jobs, _, qerr := s.getJobsByRepGroup(ctx, opts)
	if qerr != "" {
		return nil, http.StatusInternalServerError, Error{Err: qerr}
	}

	return jobs, http.StatusOK, nil
}

func restEditableJobKeys(jobs []*Job) []string {
	keys := make(map[string]bool)

	for _, job := range jobs {
		if restEditableState(job.State) {
			keys[job.Key()] = true
		}
	}

	editable := make([]string, 0, len(keys))
	for key := range keys {
		editable = append(editable, key)
	}

	sort.Strings(editable)

	return editable
}

func restEditableState(state JobState) bool {
	switch state {
	case JobStateDelayed, JobStateReady, JobStateDependent, JobStateBuried:
		return true
	default:
		return false
	}
}

// JobViaJSON describes the properties of a JOB that a user wishes to add to the
// queue, convenient if they are supplying JSON.
type JobViaJSON struct {
	MountConfigs MountConfigs      `json:"mounts"`
	LimitGrps    []string          `json:"limit_grps"`
	Modules      []string          `json:"modules"`
	DepGrps      []string          `json:"dep_grps"`
	Deps         []string          `json:"deps"`
	CmdDeps      Dependencies      `json:"cmd_deps"`
	OnFailure    BehavioursViaJSON `json:"on_failure"`
	OnSuccess    BehavioursViaJSON `json:"on_success"`
	OnExit       BehavioursViaJSON `json:"on_exit"`
	Env          []string          `json:"env"`
	Cmd          string            `json:"cmd"`
	Cwd          string            `json:"cwd"`
	ReqGrp       string            `json:"req_grp"`
	Group        string            `json:"group"`
	// Memory is a number and unit suffix, eg. 1G for 1 Gigabyte.
	Memory string `json:"memory"`
	// Time is a duration with a unit suffix, eg. 1h for 1 hour.
	Time                 string   `json:"time"`
	RepGrp               string   `json:"rep_grp"`
	MonitorDocker        string   `json:"monitor_docker"`
	WithDocker           string   `json:"with_docker"`
	WithSingularity      string   `json:"with_singularity"`
	ContainerMounts      string   `json:"container_mounts"`
	CloudOS              string   `json:"cloud_os"`
	CloudUser            string   `json:"cloud_username"`
	CloudScript          string   `json:"cloud_script"`
	CloudConfigFiles     string   `json:"cloud_config_files"`
	CloudFlavor          string   `json:"cloud_flavor"`
	SchedulerQueue       string   `json:"queue"`
	SchedulerQueuesAvoid string   `json:"queues_avoid"`
	SchedulerMisc        string   `json:"misc"`
	BsubMode             string   `json:"bsub_mode"`
	CPUs                 *float64 `json:"cpus"`
	// Disk is the number of Gigabytes the cmd will use.
	Disk                  *int   `json:"disk"`
	Override              *int   `json:"override"`
	Priority              *int   `json:"priority"`
	Retries               *int   `json:"retries"`
	NoRetriesOverWalltime string `json:"no_retry_over_walltime"`
	CloudOSRam            *int   `json:"cloud_ram"`
	RTimeout              *int   `json:"reserve_timeout"`
	CwdMatters            bool   `json:"cwd_matters"`
	ChangeHome            bool   `json:"change_home"`
	CloudShared           bool   `json:"cloud_shared"`
}

// JobDefaults is supplied to JobViaJSON.Convert() to provide default values for
// the conversion.
type JobDefaults struct {
	LimitGroups   []string
	Modules       []string
	DepGroups     []string
	Deps          Dependencies
	OnFailure     Behaviours
	OnSuccess     Behaviours
	OnExit        Behaviours
	MountConfigs  MountConfigs
	compressedEnv []byte
	RepGrp        string
	Group         string
	// Cwd defaults to /tmp.
	Cwd    string
	ReqGrp string
	// Env is a comma separated list of key=val pairs.
	Env             string
	MonitorDocker   string
	WithDocker      string
	WithSingularity string
	ContainerMounts string
	CloudOS         string
	CloudUser       string
	CloudFlavor     string
	// CloudScript is the local path to a script.
	CloudScript string
	// CloudConfigFiles is the config files to copy in cloud.Server.CopyOver() format
	CloudConfigFiles     string
	SchedulerQueue       string
	SchedulerQueuesAvoid string
	SchedulerMisc        string
	BsubMode             string
	osRAM                string
	// CPUs is the number of CPU cores each cmd will use.
	CPUs   float64 // Memory is the number of Megabytes each cmd will use. Defaults to 1000.
	Memory int
	// Time is the amount of time each cmd will run for. Defaults to 1 hour.
	Time time.Duration
	// Disk is the number of Gigabytes cmds will use.
	Disk     int
	Override int
	Priority int
	Retries  int
	// NoRetriesOverWalltime is the amount of time that a cmd can run for and
	// then fail and still automatically retry.
	NoRetriesOverWalltime time.Duration
	// CloudOSRam is the number of Megabytes that CloudOS needs to run. Defaults
	// to 1000.
	CloudOSRam int
	RTimeout   int
	CwdMatters bool
	ChangeHome bool
	// DiskSet is used to distinguish between Disk not being provided, and
	// being provided with a value of 0 or more.
	DiskSet     bool
	CloudShared bool
}

// DefaultCwd returns the Cwd value, defaulting to /tmp.
func (jd *JobDefaults) DefaultCwd() string {
	if jd.Cwd == "" {
		return "/tmp"
	}
	return jd.Cwd
}

// DefaultCPUs returns the CPUs value, but a minimum of 0.
func (jd *JobDefaults) DefaultCPUs() float64 {
	if jd.CPUs < 0 {
		return 0
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
	var (
		cmd, cwd, rg, repg, monitorDocker, bsubMode  string
		withDocker, withSingularity, containerMounts string
		mb, disk, override, priority, retries        int
		diskSet                                      bool
		cpus                                         float64
		dur, noRetry                                 time.Duration
		envOverride                                  []byte
		limitGroups, modules, depGroups              []string
		deps                                         Dependencies
		behaviours                                   Behaviours
		mounts                                       MountConfigs
	)

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

	group := jd.Group
	if jvj.Group != "" {
		group = jvj.Group
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
			return nil, fmt.Errorf("memory value (%s) was not specified correctly: %w", jvj.Memory, err)
		}
		mb = int(thismb)
	}

	if jvj.Time == "" {
		dur = jd.DefaultTime()
	} else {
		var err error
		dur, err = time.ParseDuration(jvj.Time)
		if err != nil {
			return nil, fmt.Errorf("time value (%s) was not specified correctly: %w", jvj.Time, err)
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
		diskSet = jd.DiskSet
	} else {
		disk = *jvj.Disk
		diskSet = true
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

	if jvj.NoRetriesOverWalltime == "" {
		noRetry = jd.NoRetriesOverWalltime
	} else {
		var err error
		noRetry, err = time.ParseDuration(jvj.NoRetriesOverWalltime)
		if err != nil {
			return nil, fmt.Errorf("no_retry_over_walltime value (%s) was not specified correctly: %w",
				jvj.NoRetriesOverWalltime, err)
		}
	}

	if len(jvj.LimitGrps) == 0 {
		limitGroups = jd.LimitGroups
	} else {
		limitGroups = jvj.LimitGrps
	}

	if len(jvj.Modules) == 0 {
		modules = jd.Modules
	} else {
		modules = jvj.Modules
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
	if jvj.WithDocker == "" {
		withDocker = jd.WithDocker
	} else {
		withDocker = jvj.WithDocker
	}
	if jvj.WithSingularity == "" {
		withSingularity = jd.WithSingularity
	} else {
		withSingularity = jvj.WithSingularity
	}
	if jvj.ContainerMounts == "" {
		containerMounts = jd.ContainerMounts
	} else {
		containerMounts = jvj.ContainerMounts
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
		scriptContent, err := internal.PathToContent(cloudScriptPath)
		if err != nil {
			return nil, err
		}
		other["cloud_script"] = scriptContent
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

	if jvj.SchedulerQueue != "" {
		other["scheduler_queue"] = jvj.SchedulerQueue
	} else if jd.SchedulerQueue != "" {
		other["scheduler_queue"] = jd.SchedulerQueue
	}

	if jvj.SchedulerQueuesAvoid != "" {
		other["scheduler_queues_avoid"] = jvj.SchedulerQueuesAvoid
	} else if jd.SchedulerQueuesAvoid != "" {
		other["scheduler_queues_avoid"] = jd.SchedulerQueuesAvoid
	}

	if jvj.SchedulerMisc != "" {
		other["scheduler_misc"] = jvj.SchedulerMisc
	} else if jd.SchedulerMisc != "" {
		other["scheduler_misc"] = jd.SchedulerMisc
	}

	if jvj.RTimeout != nil {
		rtimeout := *jvj.RTimeout
		other["rtimeout"] = strconv.Itoa(rtimeout)
	} else if jd.RTimeout != 0 {
		other["rtimeout"] = strconv.Itoa(jd.RTimeout)
	}

	return &Job{
		RepGroup:              repg,
		Cmd:                   cmd,
		Cwd:                   cwd,
		CwdMatters:            cwdMatters,
		ChangeHome:            changeHome,
		ReqGroup:              rg,
		Group:                 group,
		Requirements:          &jqs.Requirements{RAM: mb, Time: dur, Cores: cpus, Disk: disk, DiskSet: diskSet, Other: other},
		Override:              uint8(override),
		Priority:              uint8(priority),
		Retries:               uint8(retries),
		NoRetriesOverWalltime: noRetry,
		LimitGroups:           limitGroups,
		Modules:               modules,
		DepGroups:             depGroups,
		Dependencies:          deps,
		EnvOverride:           envOverride,
		Behaviours:            behaviours,
		MountConfigs:          mounts,
		MonitorDocker:         monitorDocker,
		WithDocker:            withDocker,
		WithSingularity:       withSingularity,
		ContainerMounts:       containerMounts,
		BsubMode:              bsubMode,
	}, nil
}

// JobModifyViaJSON describes the properties of queued jobs that a REST client
// wishes to modify. Nil fields are left unchanged.
type JobModifyViaJSON struct {
	MountConfigs         *MountConfigs      `json:"mounts,omitempty"`
	LimitGrps            *[]string          `json:"limit_grps,omitempty"`
	Modules              *[]string          `json:"modules,omitempty"`
	Deps                 *[]string          `json:"deps,omitempty"`
	CmdDeps              *Dependencies      `json:"cmd_deps,omitempty"`
	OnFailure            *BehavioursViaJSON `json:"on_failure,omitempty"`
	OnSuccess            *BehavioursViaJSON `json:"on_success,omitempty"`
	OnExit               *BehavioursViaJSON `json:"on_exit,omitempty"`
	Env                  *[]string          `json:"env,omitempty"`
	Other                *map[string]string `json:"other,omitempty"`
	Cmd                  *string            `json:"cmd,omitempty"`
	Cwd                  *string            `json:"cwd,omitempty"`
	ReqGrp               *string            `json:"req_grp,omitempty"`
	Group                *string            `json:"group,omitempty"`
	Memory               *string            `json:"memory,omitempty"`
	Time                 *string            `json:"time,omitempty"`
	MonitorDocker        *string            `json:"monitor_docker,omitempty"`
	WithDocker           *string            `json:"with_docker,omitempty"`
	WithSingularity      *string            `json:"with_singularity,omitempty"`
	ContainerMounts      *string            `json:"container_mounts,omitempty"`
	SchedulerQueue       *string            `json:"queue,omitempty"`
	SchedulerQueuesAvoid *string            `json:"queues_avoid,omitempty"`
	SchedulerMisc        *string            `json:"misc,omitempty"`
	CloudOS              *string            `json:"cloud_os,omitempty"`
	CloudUser            *string            `json:"cloud_username,omitempty"`
	CloudRAM             *int               `json:"cloud_ram,omitempty"`
	CloudFlavor          *string            `json:"cloud_flavor,omitempty"`
	CloudScript          *string            `json:"cloud_script,omitempty"`
	CloudConfigFiles     *string            `json:"cloud_config_files,omitempty"`
	CloudShared          *bool              `json:"cloud_shared,omitempty"`
	CPUs                 *float64           `json:"cpus,omitempty"`
	Disk                 *int               `json:"disk,omitempty"`
	Override             *int               `json:"override,omitempty"`
	Priority             *int               `json:"priority,omitempty"`
	Retries              *int               `json:"retries,omitempty"`
	NoRetryOverWalltime  *string            `json:"no_retry_over_walltime,omitempty"`
	CwdMatters           *bool              `json:"cwd_matters,omitempty"`
	ChangeHome           *bool              `json:"change_home,omitempty"`
}

// Convert converts REST JSON modification fields to a JobModifier.
func (jvj *JobModifyViaJSON) Convert() (*JobModifier, error) {
	modifier := NewJobModifer()

	if err := jvj.setModifierIdentityFields(modifier); err != nil {
		return nil, err
	}

	if err := jvj.setModifierRequirements(modifier); err != nil {
		return nil, err
	}

	if err := jvj.setModifierRetryFields(modifier); err != nil {
		return nil, err
	}

	jvj.setModifierSliceFields(modifier)

	if err := jvj.setModifierEnv(modifier); err != nil {
		return nil, err
	}

	jvj.setModifierBehaviours(modifier)
	jvj.setModifierContainerFields(modifier)

	return modifier, nil
}

func (jvj *JobModifyViaJSON) setModifierIdentityFields(modifier *JobModifier) error {
	if err := jvj.setModifierCommandFields(modifier); err != nil {
		return err
	}

	jvj.setModifierIdentityFlags(modifier)

	return nil
}

func (jvj *JobModifyViaJSON) setModifierCommandFields(modifier *JobModifier) error {
	if jvj.Cmd != nil {
		if *jvj.Cmd == "" {
			return errRESTModifyCmdEmpty
		}

		modifier.SetCmd(*jvj.Cmd)
	}

	if jvj.Cwd != nil {
		if *jvj.Cwd == "" {
			return errRESTModifyCwdEmpty
		}

		modifier.SetCwd(*jvj.Cwd)
	}

	return nil
}

func (jvj *JobModifyViaJSON) setModifierIdentityFlags(modifier *JobModifier) {
	if jvj.CwdMatters != nil {
		modifier.SetCwdMatters(*jvj.CwdMatters)
	}

	if jvj.ChangeHome != nil {
		modifier.SetChangeHome(*jvj.ChangeHome)
	}

	if jvj.ReqGrp != nil {
		modifier.SetReqGroup(*jvj.ReqGrp)
	}

	if jvj.Group != nil {
		modifier.SetUnixGroup(*jvj.Group)
	}
}

func (jvj *JobModifyViaJSON) setModifierRequirements(modifier *JobModifier) error {
	req, set, err := jvj.requirements()
	if err != nil {
		return err
	}

	if set {
		modifier.SetRequirements(req)
	}

	return nil
}

func (jvj *JobModifyViaJSON) requirements() (*jqs.Requirements, bool, error) {
	req := &jqs.Requirements{}

	var set bool

	memorySet, err := jvj.setMemoryRequirement(req)
	if err != nil {
		return nil, false, err
	}

	timeSet, err := jvj.setTimeRequirement(req)
	if err != nil {
		return nil, false, err
	}

	set = anyTrue(memorySet, timeSet, jvj.setCPURequirement(req), jvj.setDiskRequirement(req))

	other, otherSet, err := jvj.otherRequirements()
	if err != nil {
		return nil, false, err
	}

	if otherSet {
		req.Other = other
		req.OtherSet = true
		set = true
	}

	return req, set, nil
}

func anyTrue(values ...bool) bool {
	for _, value := range values {
		if value {
			return true
		}
	}

	return false
}

func (jvj *JobModifyViaJSON) setMemoryRequirement(req *jqs.Requirements) (bool, error) {
	if jvj.Memory == nil {
		return false, nil
	}

	mb, err := bytefmt.ToMegabytes(*jvj.Memory)
	if err != nil {
		return false, fmt.Errorf("memory value (%s) was not specified correctly: %w", *jvj.Memory, err)
	}

	maxInt := int(^uint(0) >> 1)
	if mb > uint64(maxInt) {
		return false, fmt.Errorf("memory value (%s) was not specified correctly: %w", *jvj.Memory, strconv.ErrRange)
	}

	ram, err := strconv.Atoi(strconv.FormatUint(mb, 10))
	if err != nil {
		return false, fmt.Errorf("memory value (%s) was not specified correctly: %w", *jvj.Memory, err)
	}

	req.RAM = ram

	return true, nil
}

func (jvj *JobModifyViaJSON) setTimeRequirement(req *jqs.Requirements) (bool, error) {
	if jvj.Time == nil {
		return false, nil
	}

	dur, err := time.ParseDuration(*jvj.Time)
	if err != nil {
		return false, fmt.Errorf("time value (%s) was not specified correctly: %w", *jvj.Time, err)
	}

	req.Time = dur

	return true, nil
}

func (jvj *JobModifyViaJSON) setCPURequirement(req *jqs.Requirements) bool {
	if jvj.CPUs == nil {
		return false
	}

	req.Cores = *jvj.CPUs
	req.CoresSet = true

	return true
}

func (jvj *JobModifyViaJSON) setDiskRequirement(req *jqs.Requirements) bool {
	if jvj.Disk == nil {
		return false
	}

	req.Disk = *jvj.Disk
	req.DiskSet = true

	return true
}

func (jvj *JobModifyViaJSON) otherRequirements() (map[string]string, bool, error) {
	other := make(map[string]string)

	var set bool

	if jvj.Other != nil {
		for key, val := range *jvj.Other {
			other[key] = val
		}

		set = true
	}

	jvj.setStringOtherRequirements(other, &set)

	if err := jvj.setCloudOtherRequirements(other, &set); err != nil {
		return nil, false, err
	}

	return other, set, nil
}

func (jvj *JobModifyViaJSON) setStringOtherRequirements(other map[string]string, set *bool) {
	setStringOther(other, "cloud_os", jvj.CloudOS, set)
	setStringOther(other, "cloud_user", jvj.CloudUser, set)
	setStringOther(other, "cloud_flavor", jvj.CloudFlavor, set)
	setStringOther(other, "cloud_config_files", jvj.CloudConfigFiles, set)
	setStringOther(other, "scheduler_queue", jvj.SchedulerQueue, set)
	setStringOther(other, "scheduler_queues_avoid", jvj.SchedulerQueuesAvoid, set)
	setStringOther(other, "scheduler_misc", jvj.SchedulerMisc, set)
}

func setStringOther(other map[string]string, key string, value *string, set *bool) {
	if value == nil {
		return
	}

	other[key] = *value
	*set = true
}

func (jvj *JobModifyViaJSON) setCloudOtherRequirements(other map[string]string, set *bool) error {
	if jvj.CloudRAM != nil {
		other["cloud_os_ram"] = strconv.Itoa(*jvj.CloudRAM)
		*set = true
	}

	if jvj.CloudShared != nil {
		other["cloud_shared"] = strconv.FormatBool(*jvj.CloudShared)
		*set = true
	}

	if jvj.CloudScript == nil {
		return nil
	}

	content, err := internal.PathToContent(*jvj.CloudScript)
	if err != nil {
		return err
	}

	other["cloud_script"] = content
	*set = true

	return nil
}

func (jvj *JobModifyViaJSON) setModifierRetryFields(modifier *JobModifier) error {
	if err := setUint8ModificationField("override", jvj.Override, restModifyOverrideMax,
		modifier.SetOverride); err != nil {
		return err
	}

	if err := setUint8ModificationField("priority", jvj.Priority, restModifyUint8Max, modifier.SetPriority); err != nil {
		return err
	}

	if err := setUint8ModificationField("retries", jvj.Retries, restModifyUint8Max, modifier.SetRetries); err != nil {
		return err
	}

	if jvj.NoRetryOverWalltime != nil {
		dur, err := time.ParseDuration(*jvj.NoRetryOverWalltime)
		if err != nil {
			return fmt.Errorf("no_retry_over_walltime value (%s) was not specified correctly: %w",
				*jvj.NoRetryOverWalltime, err)
		}

		modifier.SetNoRetriesOverWalltime(dur)
	}

	return nil
}

func setUint8ModificationField(name string, value *int, limit int, set func(uint8)) error {
	converted, ok, err := uint8ModificationValue(name, value, limit)
	if err != nil || !ok {
		return err
	}

	set(converted)

	return nil
}

func (jvj *JobModifyViaJSON) setModifierSliceFields(modifier *JobModifier) {
	if jvj.LimitGrps != nil {
		modifier.SetLimitGroups(*jvj.LimitGrps)
	}

	if jvj.Modules != nil {
		modifier.SetModules(*jvj.Modules)
	}

	if jvj.MountConfigs != nil {
		modifier.SetMountConfigs(*jvj.MountConfigs)
	}

	if jvj.Deps != nil || jvj.CmdDeps != nil {
		modifier.SetDependencies(jvj.dependencies())
	}
}

func (jvj *JobModifyViaJSON) dependencies() Dependencies {
	var deps Dependencies
	if jvj.CmdDeps != nil {
		deps = append(deps, (*jvj.CmdDeps)...)
	}

	if jvj.Deps != nil {
		for _, depgroup := range *jvj.Deps {
			deps = append(deps, NewDepGroupDependency(depgroup))
		}
	}

	return deps
}

func (jvj *JobModifyViaJSON) setModifierEnv(modifier *JobModifier) error {
	if jvj.Env == nil {
		return nil
	}

	return modifier.setEnvOverrideValues(*jvj.Env)
}

func (jvj *JobModifyViaJSON) setModifierBehaviours(modifier *JobModifier) {
	var (
		behaviours Behaviours
		set        bool
	)

	if jvj.OnFailure != nil {
		behaviours = append(behaviours, modifyBehaviours(*jvj.OnFailure, OnFailure)...)
		set = true
	}

	if jvj.OnSuccess != nil {
		behaviours = append(behaviours, modifyBehaviours(*jvj.OnSuccess, OnSuccess)...)
		set = true
	}

	if jvj.OnExit != nil {
		behaviours = append(behaviours, modifyBehaviours(*jvj.OnExit, OnExit)...)
		set = true
	}

	if set {
		modifier.SetBehaviours(behaviours)
	}
}

func modifyBehaviours(bvj BehavioursViaJSON, when BehaviourTrigger) Behaviours {
	if len(bvj) == 0 {
		bvj = BehavioursViaJSON{{Nothing: true}}
	}

	return bvj.Behaviours(when)
}

func (jvj *JobModifyViaJSON) setModifierContainerFields(modifier *JobModifier) {
	if jvj.MonitorDocker != nil {
		modifier.SetMonitorDocker(*jvj.MonitorDocker)
	}

	if jvj.WithDocker != nil {
		modifier.SetWithDocker(*jvj.WithDocker)
	}

	if jvj.WithSingularity != nil {
		modifier.SetWithSingularity(*jvj.WithSingularity)
	}

	if jvj.ContainerMounts != nil {
		modifier.SetContainerMounts(*jvj.ContainerMounts)
	}
}

// JobModifyResponse describes the jobs changed by a REST modification request.
type JobModifyResponse struct {
	Modified map[string]string `json:"modified"`
	Jobs     []JStatus         `json:"jobs"`
}

func restEditableItemState(state queue.ItemState) bool {
	switch state {
	case queue.ItemStateDelay, queue.ItemStateReady, queue.ItemStateDependent, queue.ItemStateBury:
		return true
	default:
		return false
	}
}

func modifiedJobs(modified map[string]string, byOldKey map[string]*Job) []*Job {
	jobs := make([]*Job, 0, len(modified))
	for _, old := range modified {
		if job := byOldKey[old]; job != nil {
			jobs = append(jobs, job)
		}
	}

	return jobs
}

func modifiedOldKeys(modified map[string]string, jobs []*Job) []string {
	oldKeys := make([]string, len(jobs))
	for i, job := range jobs {
		oldKeys[i] = modified[job.Key()]
	}

	return oldKeys
}

func (s *Server) restModifyEmptyResultError(editableKeys []string) error {
	editableJobs, _ := s.editableQueueJobs(editableKeys)
	if len(editableJobs) == 0 {
		return errRESTModifyNoEditable
	}

	return errRESTModifyNoneModified
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
func restJobs(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(ctx, "jobqueue web server restJobs", false)

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
			jobs, status, err = restJobsStatus(ctx, r, s)
		case http.MethodPost:
			jobs, status, err = restJobsAdd(ctx, r, s)
		case http.MethodPatch:
			response, modifyStatus, modifyErr := restJobsModify(ctx, r, s)
			if modifyStatus >= 400 || modifyErr != nil {
				http.Error(w, modifyErr.Error(), modifyStatus)

				return
			}

			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
			w.WriteHeader(modifyStatus)
			encoder := json.NewEncoder(w)
			encoder.SetEscapeHTML(false)

			erre := encoder.Encode(response)
			if erre != nil {
				clog.Warn(ctx, "restJobs failed to encode modified jobs", "err", erre)
			}

			return
		case http.MethodDelete:
			jobs, status, err = restJobsCancel(ctx, r, s)
		default:
			http.Error(w, "So far only GET, POST, PATCH and DELETE are supported", http.StatusBadRequest)
			return
		}

		if status >= 400 || err != nil {
			http.Error(w, err.Error(), status)
			return
		}

		// convert jobs to jstatus
		jstati := make([]JStatus, len(jobs))
		for i, job := range jobs {
			jstati[i], err = job.ToStatus()
			if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
				http.Error(w, err.Error(), status)
				return
			}
		}

		// return job details as JSON
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(status)
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		erre := encoder.Encode(jstati)
		if erre != nil {
			clog.Warn(ctx, "restJobs failed to encode job statuses", "err", erre)
		}
	}
}

// restJobsStatus gets the status of the requested jobs in the queue. The
// request url can be suffixed with comma separated job keys or RepGroups.
// Possible query parameters are search, std, env (which can take a "true"
// value), limit (a number) and state (one of
// delayed|ready|reserved|running|lost|buried|dependent|complete|deletable),
// where deletable == !(running|complete). Returns the Jobs, a http.Status*
// value and error.
func restJobsStatus(ctx context.Context, r *http.Request, s *Server) ([]*Job, int, error) {
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
		case "deletable":
			state = JobStateDeletable
		}
	}

	if len(r.URL.Path) > len(restJobsEndpoint) {
		// get the requested jobs
		ids := r.URL.Path[len(restJobsEndpoint):]
		var jobs []*Job
		for _, id := range strings.Split(ids, ",") {
			if len(id) == 32 {
				// id might be a Job.key()
				theseJobs, _, qerr := s.getJobsByKeys(ctx, []string{id}, getStd, getEnv)
				if qerr == "" && len(theseJobs) > 0 {
					jobs = append(jobs, theseJobs...)
					continue
				}
			}

			// id might be a Job.RepGroup
			opts := repGroupOptions{
				RepGroup: id,
				Match:    normalizeRepGroupMatch("", search),
				limitJobsOptions: limitJobsOptions{
					Limit:  limit,
					State:  state,
					GetStd: getStd,
					GetEnv: getEnv,
				},
			}
			theseJobs, _, qerr := s.getJobsByRepGroup(ctx, opts)
			if qerr != "" {
				return nil, http.StatusInternalServerError, Error{Err: qerr}
			}
			if len(theseJobs) > 0 {
				jobs = append(jobs, theseJobs...)
			}
		}
		return jobs, http.StatusOK, err
	}

	// get all current jobs
	return s.getJobsCurrent(ctx, "", RepGroupMatchExact, limit, state, getStd,
		getEnv), http.StatusOK, err
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
func restJobsAdd(ctx context.Context, r *http.Request, s *Server) ([]*Job, int, error) {
	// handle possible ?query parameters
	_, diskSet := r.Form["disk"]
	jd := &JobDefaults{
		Cwd:             r.Form.Get("cwd"),
		RepGrp:          r.Form.Get("rep_grp"),
		LimitGroups:     urlStringToSlice(r.Form.Get("limit_grps")),
		ReqGrp:          r.Form.Get("req_grp"),
		CPUs:            urlStringToFloat(r.Form.Get("cpus")),
		Disk:            urlStringToInt(r.Form.Get("disk")),
		DiskSet:         diskSet,
		Override:        urlStringToInt(r.Form.Get("override")),
		Priority:        urlStringToInt(r.Form.Get("priority")),
		Retries:         urlStringToInt(r.Form.Get("retries")),
		DepGroups:       urlStringToSlice(r.Form.Get("dep_grps")),
		Env:             r.Form.Get("env"),
		MonitorDocker:   r.Form.Get("monitor_docker"),
		WithDocker:      r.Form.Get("with_docker"),
		WithSingularity: r.Form.Get("with_singularity"),
		ContainerMounts: r.Form.Get("container_mounts"),
		CloudOS:         r.Form.Get("cloud_os"),
		CloudUser:       r.Form.Get("cloud_username"),
		CloudScript:     r.Form.Get("cloud_script"),
		CloudFlavor:     r.Form.Get("cloud_flavor"),
		CloudOSRam:      urlStringToInt(r.Form.Get("cloud_ram")),
		BsubMode:        r.Form.Get("bsub_mode"),
	}
	if jd.RepGrp == "" {
		jd.RepGrp = "manually_added"
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
	if r.Form.Get("no_retry_over_walltime") != "" {
		var err error
		jd.NoRetriesOverWalltime, err = time.ParseDuration(r.Form.Get("no_retry_over_walltime"))
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
	}
	var rerun bool
	if r.Form.Get("rerun") == restFormTrue {
		rerun = true
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
	inputJobs := make([]*Job, 0, len(jvjs))
	for _, jvj := range jvjs {
		job, errf := jvj.Convert(jd)
		if errf != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("there was a problem interpreting your job: %w", errf)
		}
		inputJobs = append(inputJobs, job)
	}

	envkey, err := s.db.storeEnv([]byte{})
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	//nolint:dogsled // REST add only needs to know whether the shared add path failed.
	_, _, _, _, _, err = s.createJobs(ctx, inputJobs, envkey, !rerun)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	// see which of the inputJobs are now actually in the queue
	jobs := s.inputToQueuedJobs(ctx, inputJobs)

	return jobs, http.StatusCreated, err
}

// restJobsCancel kills running jobs, confirms lost jobs as dead, or deletes
// incomplete jobs. You identify the jobs to operate on in the same way as for
// restJobsStatus(). However state must be specified, and only one of:
// (running|lost|deletable) are allowed. Returns the affected Jobs, a
// http.Status* value and error.
func restJobsCancel(ctx context.Context, r *http.Request, s *Server) ([]*Job, int, error) {
	var state JobState
	if r.Form.Get("state") != "" {
		switch r.Form.Get("state") {
		case "running":
			state = JobStateRunning
		case "lost":
			state = JobStateLost
		case "deletable":
			state = JobStateDeletable
		}
	}
	if state == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("state must be supplied as one of running|lost|deletable")
	}

	jobs, status, err := restJobsStatus(ctx, r, s)
	if err != nil || status != http.StatusOK {
		return nil, status, err
	}

	var handled []*Job
	returnStatus := http.StatusAccepted
	if state == JobStateDeletable {
		returnStatus = http.StatusOK

		deleted := s.deleteJobs(ctx, jobs)
		d := make(map[string]bool, len(deleted))
		for _, key := range deleted {
			d[key] = true
		}
		for _, job := range jobs {
			if d[job.Key()] {
				job.State = JobStateDeleted
				handled = append(handled, job)
			}
		}
	} else {
		for _, job := range jobs {
			k, err := s.killJob(ctx, job.Key())
			if err != nil {
				return handled, http.StatusInternalServerError, err
			}
			if k {
				handled = append(handled, job)
			}
		}
	}
	return handled, returnStatus, nil
}

// restWarnings lets you read warnings from the scheduler, and auto-"dismisses"
// (deletes) them.
func restWarnings(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(ctx, "jobqueue web server restWarnings", false)

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
			clog.Warn(ctx, "restWarnings failed to encode scheduler issues", "err", erre)
		}
	}
}

// restBadServers lets you do CRUD on cloud servers that have gone bad. The
// DELETE verb has a required 'id' parameter, being the ID of a server you wish
// to confirm as bad and have terminated if it still exists.
func restBadServers(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(ctx, "jobqueue web server restBadServers", false)

		ok := s.httpAuthorized(w, r)
		if !ok {
			return
		}

		// carry out a different action based on the HTTP Verb
		switch r.Method {
		case http.MethodGet:
			servers := s.getBadServers()
			if len(servers) == 0 {
				servers = []*BadServer{}
			}
			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
			w.WriteHeader(http.StatusOK)
			encoder := json.NewEncoder(w)
			encoder.SetEscapeHTML(false)
			erre := encoder.Encode(servers)
			if erre != nil {
				clog.Warn(ctx, "restBadServers failed to encode servers", "err", erre)
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
				err := server.Destroy(ctx)
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
func restFileUpload(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(ctx, "jobqueue web server restFileUpload", false)

		ok := s.httpAuthorized(w, r)
		if !ok {
			return
		}

		if r.Method != http.MethodPut {
			http.Error(w, "Only PUT is supported", http.StatusBadRequest)
			return
		}

		savePath, err := s.uploadFile(ctx, r.Body, r.Form.Get("path"))
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
			clog.Warn(ctx, "restFileUpload failed to encode success msg", "err", err)
		}
	}
}

// restInfo lets you get info on self.
func restInfo(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(ctx, "jobqueue server status", false)

		ok := s.httpAuthorized(w, r)
		if !ok {
			return
		}

		if r.Method != http.MethodGet {
			http.Error(w, "Only GET is supported", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		err := encoder.Encode(s.ServerInfo)
		if err != nil {
			clog.Warn(ctx, "restInfo failed to encode ServerInfo", "err", err)
		}
	}
}

// restVersion lets you get info on the version of the server and the supported
// API version (we only support 1 API version at a time). This is the only
// end point that doesn't need authentication.
func restVersion(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(ctx, "jobqueue server version", false)

		if r.Method != http.MethodGet {
			http.Error(w, "Only GET is supported", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		err := encoder.Encode(s.ServerVersions)
		if err != nil {
			clog.Warn(ctx, "restVersion failed to encode ServerVersions", "err", err)
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

// urlStringToFloat takes a possible string from a url parameter value and
// converts it to a float64. If the value is "", or if the value isn't a number,
// returns 0.
func urlStringToFloat(value string) float64 {
	if value == "" {
		return 0
	}
	num, err := strconv.ParseFloat(value, 64)
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

func (s *Server) modifiedJobStatuses(ctx context.Context, modified map[string]string) ([]JStatus, error) {
	keys := make([]string, 0, len(modified))
	for key := range modified {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	jobs, _, qerr := s.getJobsByKeys(ctx, keys, false, false)
	if qerr != "" {
		return nil, Error{Err: qerr}
	}

	statuses := make([]JStatus, 0, len(jobs))
	for _, job := range jobs {
		status, err := job.ToStatus()
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, err
		}

		statuses = append(statuses, status)
	}

	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].Key < statuses[j].Key
	})

	return statuses, nil
}

func (s *Server) modifyJobsByKeys(ctx context.Context, keys []string,
	modifier *JobModifier,
) (modified map[string]string, err error) {
	paused, err := s.Pause()
	if err != nil {
		return nil, err
	}
	defer s.resumeAfterModify(ctx, &modified, &err)

	if paused {
		clog.Debug(ctx, "rest modify requested, paused server")
	}

	toModifyJobs, toModifyKeys := s.editableQueueJobs(keys)

	modified, err = modifier.Modify(toModifyJobs, s)
	if err != nil || len(modified) == 0 {
		return modified, err
	}

	return modified, s.storeModifiedJobs(ctx, modified, toModifyKeys, modifier)
}

func (s *Server) resumeAfterModify(ctx context.Context, modified *map[string]string, modifyErr *error) {
	resumed, resumeErr := s.Resume(ctx)
	if resumeErr != nil && *modifyErr == nil {
		*modifyErr = resumeErr

		return
	}

	if resumed {
		clog.Debug(ctx, "rest modify completed, resumed server", "count", len(*modified))
	}
}

func (s *Server) editableQueueJobs(keys []string) ([]*Job, map[string]*Job) {
	jobs := make([]*Job, 0, len(keys))
	byOldKey := make(map[string]*Job, len(keys))

	for _, key := range keys {
		item, err := s.q.Get(key)
		if err != nil || item == nil || !restEditableItemState(item.Stats().State) {
			continue
		}

		job, ok := item.Data().(*Job)
		if !ok {
			continue
		}

		jobs = append(jobs, job)
		byOldKey[key] = job
	}

	return jobs, byOldKey
}

func (s *Server) storeModifiedJobs(ctx context.Context, modified map[string]string, byOldKey map[string]*Job,
	modifier *JobModifier,
) error {
	jobs := modifiedJobs(modified, byOldKey)
	if len(jobs) == 0 {
		return nil
	}

	if err := s.changeModifiedQueueKeys(modified, jobs); err != nil {
		return err
	}

	s.storeModifiedLimitGroupsIfNeeded(ctx, jobs, modifier)

	if err := s.db.modifyLiveJobs(ctx, modifiedOldKeys(modified, jobs), jobs); err != nil {
		return err
	}

	if modifier.DependenciesSet || modifier.PrioritySet {
		return s.updateModifiedQueueJobs(ctx, jobs)
	}

	return nil
}

func (s *Server) storeModifiedLimitGroupsIfNeeded(ctx context.Context, jobs []*Job, modifier *JobModifier) {
	if modifier.LimitGroupsSet {
		s.storeModifiedLimitGroups(ctx, jobs)
	}
}

func (s *Server) storeModifiedLimitGroups(ctx context.Context, jobs []*Job) {
	limitGroups := make(map[string]*limiter.GroupData)
	for _, job := range jobs {
		s.handleUserSpecifiedJobLimitGroups(job, limitGroups)
	}

	if err := s.storeLimitGroups(limitGroups); err != nil {
		clog.Error(ctx, "failed to store limit groups", "err", err)
	}
}

func (s *Server) changeModifiedQueueKeys(modified map[string]string, jobs []*Job) error {
	keyToRP := make(map[string]string, len(jobs))
	for _, job := range jobs {
		keyToRP[job.Key()] = job.RepGroup
	}

	s.rpl.Lock()
	defer s.rpl.Unlock()

	for newKey, oldKey := range modified {
		if oldKey == newKey {
			continue
		}

		if err := s.q.ChangeKey(oldKey, newKey); err != nil {
			return err
		}

		rp := keyToRP[newKey]
		s.rpl.Delete(rp, oldKey)
		s.rpl.Add(rp, newKey)
	}

	return nil
}

func (s *Server) updateModifiedQueueJobs(ctx context.Context, jobs []*Job) error {
	for _, job := range jobs {
		deps, err := job.Dependencies.incompleteJobKeys(s.db)
		if err != nil {
			return err
		}

		item, err := s.q.Get(job.Key())
		if err != nil {
			return err
		}

		stats := item.Stats()

		err = s.q.Update(ctx, job.Key(), job.getSchedulerGroup(), job, job.Priority,
			stats.Delay, stats.TTR, deps)
		if err != nil {
			return err
		}
	}

	return nil
}
