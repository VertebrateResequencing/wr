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
	"maps"
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
	restDefaultMemoryMB    = 1000
	restDefaultCloudOSRam  = 1000
	restDefaultRepGroup    = "manually_added"
)

var (
	errRESTModifyCmdEmpty        = errors.New("cmd cannot be empty")
	errRESTModifyCwdEmpty        = errors.New("cwd cannot be empty")
	errRESTModifyIdentifierEmpty = errors.New("job identifier is required")
	errRESTModifyNoEditable      = errors.New("no editable jobs matched")
	errRESTModifyCmdMultiJob     = errors.New("cmd can only be modified for one job")
	errRESTModifyNoneModified    = errors.New("no jobs were modified")
	errRESTModifyNotFound        = errors.New("job not found")
	errRESTCmdNotSpecified       = errors.New("cmd was not specified")
	errRESTCancelStateRequired   = errors.New("state must be supplied as one of running|lost|deletable")
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

	for id := range strings.SplitSeq(ids, ",") {
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
		status, err := restJobsModificationEmptyStatus(s, ids)

		return nil, status, err
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
	// deliberately does NOT ask for complete jobs: only restEditableState (delayed,
	// ready, dependent, buried) jobs can be modified, so every archived job this
	// used to decode was discarded by restEditableJobKeys, at the cost of an
	// unbounded scan (reliable4 FINDING 1). The documented 409-vs-404 distinction for
	// a RepGroup with nothing but complete jobs is preserved without that scan by
	// restJobsModificationEmptyStatus, which asks the O(log n) end-time index whether
	// the RepGroup has any history at all.
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

// restJobsModificationEmptyStatus decides which refusal a modification whose ids
// matched no live job gets: 404 is reserved for ids that resolve to no queued or
// complete job and no RepGroup, so an id with archived history resolved to
// complete jobs, which are not an editable state, and is the 409 case.
//
// It does not fetch that history to find out (see
// restJobsModificationRepGroupTarget): every archive records its RepGroup's end
// time, so this is one O(log n) Get per id. A 32-char job key simply has no such
// entry, unless it happens to also name a RepGroup with history.
func restJobsModificationEmptyStatus(s *Server, ids string) (int, error) {
	for id := range strings.SplitSeq(ids, ",") {
		if s.db.repGroupHasHistory(strings.TrimSpace(id)) {
			return http.StatusConflict, errRESTModifyNoEditable
		}
	}

	return http.StatusNotFound, errRESTModifyNotFound
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
		return defaultUploadDir
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
		return restDefaultMemoryMB
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
			ram = restDefaultCloudOSRam
		}

		jd.osRAM = strconv.Itoa(ram)
	}

	return jd.osRAM
}

// Convert considers the supplied defaults and returns a *Job based on the
// properties of this JobViaJSON. The Job will not be in the queue until passed
// to a method that adds jobs to the queue.
func (jvj *JobViaJSON) Convert(jd *JobDefaults) (*Job, error) {
	cmd := jvj.Cmd
	if cmd == "" {
		return nil, errRESTCmdNotSpecified
	}

	fields, err := jvj.resolveErrorProneFields(jd)
	if err != nil {
		return nil, err
	}

	return jvj.buildJob(jd, cmd, fields), nil
}

// buildJob assembles the final Job from the JobViaJSON, its defaults and the
// already-resolved error-prone fields.
func (jvj *JobViaJSON) buildJob(jd *JobDefaults, cmd string, fields convertedFields) *Job {
	return &Job{
		RepGroup:              firstNonEmpty(jvj.RepGrp, jd.RepGrp),
		Cmd:                   cmd,
		Cwd:                   firstNonEmpty(jvj.Cwd, jd.DefaultCwd()),
		CwdMatters:            jvj.CwdMatters || jd.CwdMatters,
		ChangeHome:            jvj.ChangeHome || jd.ChangeHome,
		ReqGroup:              jvj.resolveReqGroup(jd, cmd),
		Group:                 firstNonEmpty(jvj.Group, jd.Group),
		Requirements:          fields.requirements,
		Override:              fields.override,
		Priority:              fields.priority,
		Retries:               fields.retries,
		NoRetriesOverWalltime: fields.noRetry,
		LimitGroups:           firstNonEmptySlice(jvj.LimitGrps, jd.LimitGroups),
		Modules:               firstNonEmptySlice(jvj.Modules, jd.Modules),
		DepGroups:             firstNonEmptySlice(jvj.DepGrps, jd.DepGroups),
		Dependencies:          jvj.resolveDependencies(jd),
		EnvOverride:           fields.envOverride,
		Behaviours:            jvj.resolveBehaviours(jd),
		MountConfigs:          jvj.resolveMountConfigs(jd),
		MonitorDocker:         firstNonEmpty(jvj.MonitorDocker, jd.MonitorDocker),
		WithDocker:            firstNonEmpty(jvj.WithDocker, jd.WithDocker),
		WithSingularity:       firstNonEmpty(jvj.WithSingularity, jd.WithSingularity),
		ContainerMounts:       firstNonEmpty(jvj.ContainerMounts, jd.ContainerMounts),
		BsubMode:              firstNonEmpty(jvj.BsubMode, jd.BsubMode),
	}
}

// convertedFields holds the JobViaJSON-derived values whose resolution can fail.
type convertedFields struct {
	requirements                *jqs.Requirements
	noRetry                     time.Duration
	envOverride                 []byte
	override, priority, retries uint8
}

// resolveErrorProneFields resolves the fields of a Job whose values are parsed
// or validated and may therefore return an error.
func (jvj *JobViaJSON) resolveErrorProneFields(jd *JobDefaults) (convertedFields, error) {
	var fields convertedFields

	var err error

	fields.requirements, err = jvj.resolveRequirements(jd)
	if err != nil {
		return fields, err
	}

	fields.override, fields.priority, fields.retries, err = jvj.resolveUint8Limits(jd)
	if err != nil {
		return fields, err
	}

	fields.noRetry, err = jvj.resolveNoRetriesOverWalltime(jd)
	if err != nil {
		return fields, err
	}

	fields.envOverride, err = jvj.resolveEnvOverride(jd)
	if err != nil {
		return fields, err
	}

	return fields, nil
}

// firstNonEmpty returns the first non-empty string from values, or "" if all
// are empty.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

// firstNonEmptySlice returns primary if it is non-empty, otherwise fallback.
func firstNonEmptySlice(primary, fallback []string) []string {
	if len(primary) > 0 {
		return primary
	}

	return fallback
}

// resolveReqGroup returns the ReqGroup to use, defaulting to the base name of
// the command's first word when neither jvj nor jd supply one.
func (jvj *JobViaJSON) resolveReqGroup(jd *JobDefaults, cmd string) string {
	if rg := firstNonEmpty(jvj.ReqGrp, jd.ReqGrp); rg != "" {
		return rg
	}

	parts := strings.Split(cmd, " ")

	return filepath.Base(parts[0])
}

// resolveRequirements builds the scheduler Requirements from jvj and its
// defaults.
func (jvj *JobViaJSON) resolveRequirements(jd *JobDefaults) (*jqs.Requirements, error) {
	cpus := jd.DefaultCPUs()
	if jvj.CPUs != nil {
		cpus = *jvj.CPUs
	}

	mb, err := jvj.resolveMemoryMB(jd)
	if err != nil {
		return nil, err
	}

	dur, err := jvj.resolveTime(jd)
	if err != nil {
		return nil, err
	}

	disk := jd.Disk
	diskSet := jd.DiskSet

	if jvj.Disk != nil {
		disk = *jvj.Disk
		diskSet = true
	}

	other, err := jvj.resolveSchedulerOther(jd)
	if err != nil {
		return nil, err
	}

	return &jqs.Requirements{RAM: mb, Time: dur, Cores: cpus, Disk: disk, DiskSet: diskSet, Other: other}, nil
}

// resolveMemoryMB resolves the requested memory in megabytes.
func (jvj *JobViaJSON) resolveMemoryMB(jd *JobDefaults) (int, error) {
	if jvj.Memory == "" {
		return jd.DefaultMemory(), nil
	}

	thismb, err := bytefmt.ToMegabytes(jvj.Memory)
	if err != nil {
		return 0, fmt.Errorf("memory value (%s) was not specified correctly: %w", jvj.Memory, err)
	}

	return int(thismb), nil //nolint:gosec // bytefmt megabytes for a job's RAM request always fits an int.
}

// resolveTime resolves the requested walltime.
func (jvj *JobViaJSON) resolveTime(jd *JobDefaults) (time.Duration, error) {
	if jvj.Time == "" {
		return jd.DefaultTime(), nil
	}

	dur, err := time.ParseDuration(jvj.Time)
	if err != nil {
		return 0, fmt.Errorf("time value (%s) was not specified correctly: %w", jvj.Time, err)
	}

	return dur, nil
}

// resolveUint8Limits resolves and range-validates the override, priority and
// retries values, all of which must fit in a uint8 within their documented
// ranges.
func (jvj *JobViaJSON) resolveUint8Limits(jd *JobDefaults) (override, priority, retries uint8, err error) {
	override, err = resolveUint8("override", jvj.Override, jd.Override, restModifyOverrideMax)
	if err != nil {
		return 0, 0, 0, err
	}

	priority, err = resolveUint8("priority", jvj.Priority, jd.Priority, restModifyUint8Max)
	if err != nil {
		return 0, 0, 0, err
	}

	retries, err = resolveUint8("retries", jvj.Retries, jd.Retries, restModifyUint8Max)
	if err != nil {
		return 0, 0, 0, err
	}

	return override, priority, retries, nil
}

// resolveUint8 returns value (if non-nil) or dflt, range-validated to 0..limit
// and converted to uint8.
func resolveUint8(name string, value *int, dflt, limit int) (uint8, error) {
	resolved := dflt
	if value != nil {
		resolved = *value
	}

	if resolved < 0 || resolved > limit {
		return 0, restRangeError{name: name, value: resolved, limit: limit}
	}

	return uint8(resolved), nil //nolint:gosec // resolved is range-checked to 0..limit (<= 255) just above.
}

// resolveNoRetriesOverWalltime resolves the no_retry_over_walltime duration.
func (jvj *JobViaJSON) resolveNoRetriesOverWalltime(jd *JobDefaults) (time.Duration, error) {
	if jvj.NoRetriesOverWalltime == "" {
		return jd.NoRetriesOverWalltime, nil
	}

	noRetry, err := time.ParseDuration(jvj.NoRetriesOverWalltime)
	if err != nil {
		return 0, fmt.Errorf("no_retry_over_walltime value (%s) was not specified correctly: %w",
			jvj.NoRetriesOverWalltime, err)
	}

	return noRetry, nil
}

// resolveDependencies resolves the job's Dependencies from jvj and its
// defaults.
func (jvj *JobViaJSON) resolveDependencies(jd *JobDefaults) Dependencies {
	if len(jvj.Deps) == 0 && len(jvj.CmdDeps) == 0 {
		return jd.Deps
	}

	var deps Dependencies
	if len(jvj.CmdDeps) > 0 {
		deps = jvj.CmdDeps
	}

	for _, depgroup := range jvj.Deps {
		deps = append(deps, NewDepGroupDependency(depgroup))
	}

	return deps
}

// resolveEnvOverride resolves the compressed environment variable override.
func (jvj *JobViaJSON) resolveEnvOverride(jd *JobDefaults) ([]byte, error) {
	if len(jvj.Env) > 0 {
		return compressEnv(jvj.Env)
	}

	if len(jd.Env) > 0 {
		return jd.DefaultEnv()
	}

	return nil, nil
}

// resolveBehaviours resolves the job's Behaviours, preferring jvj's values over
// the defaults for each behaviour type.
func (jvj *JobViaJSON) resolveBehaviours(jd *JobDefaults) Behaviours {
	var behaviours Behaviours

	behaviours = appendBehaviours(behaviours, jvj.OnFailure, OnFailure, jd.OnFailure)
	behaviours = appendBehaviours(behaviours, jvj.OnSuccess, OnSuccess, jd.OnSuccess)
	behaviours = appendBehaviours(behaviours, jvj.OnExit, OnExit, jd.OnExit)

	return behaviours
}

// appendBehaviours appends the jvj behaviours (converted using when) if present,
// otherwise the default behaviours.
func appendBehaviours(behaviours Behaviours, viaJSON BehavioursViaJSON, when BehaviourTrigger,
	defaults Behaviours,
) Behaviours {
	if len(viaJSON) > 0 {
		return append(behaviours, viaJSON.Behaviours(when)...)
	}

	return append(behaviours, defaults...)
}

// resolveMountConfigs resolves the job's MountConfigs from jvj and its defaults.
func (jvj *JobViaJSON) resolveMountConfigs(jd *JobDefaults) MountConfigs {
	if len(jvj.MountConfigs) > 0 {
		return jvj.MountConfigs
	}

	return jd.MountConfigs
}

// resolveSchedulerOther builds the scheduler-specific "other" options map.
func (jvj *JobViaJSON) resolveSchedulerOther(jd *JobDefaults) (map[string]string, error) {
	other := make(map[string]string)

	putIfNonEmpty(other, "cloud_os", firstNonEmpty(jvj.CloudOS, jd.CloudOS))
	putIfNonEmpty(other, "cloud_user", firstNonEmpty(jvj.CloudUser, jd.CloudUser))
	putIfNonEmpty(other, "cloud_flavor", firstNonEmpty(jvj.CloudFlavor, jd.CloudFlavor))
	putIfNonEmpty(other, "cloud_config_files", firstNonEmpty(jvj.CloudConfigFiles, jd.CloudConfigFiles))
	putIfNonEmpty(other, "scheduler_queue", firstNonEmpty(jvj.SchedulerQueue, jd.SchedulerQueue))
	putIfNonEmpty(other, "scheduler_queues_avoid", firstNonEmpty(jvj.SchedulerQueuesAvoid, jd.SchedulerQueuesAvoid))
	putIfNonEmpty(other, "scheduler_misc", firstNonEmpty(jvj.SchedulerMisc, jd.SchedulerMisc))

	if err := jvj.putCloudScript(other, jd); err != nil {
		return nil, err
	}

	putIfNonEmpty(other, "cloud_os_ram", jvj.cloudOSRam(jd))

	if jvj.CloudShared || jd.CloudShared {
		other["cloud_shared"] = restFormTrue
	}

	putIfNonEmpty(other, "rtimeout", intPointerOrDefault(jvj.RTimeout, jd.RTimeout))

	return other, nil
}

// putCloudScript reads the cloud_script file (if any) and stores its content.
func (jvj *JobViaJSON) putCloudScript(other map[string]string, jd *JobDefaults) error {
	cloudScriptPath := firstNonEmpty(jvj.CloudScript, jd.CloudScript)
	if cloudScriptPath == "" {
		return nil
	}

	scriptContent, err := internal.PathToContent(cloudScriptPath)
	if err != nil {
		return err
	}

	other["cloud_script"] = scriptContent

	return nil
}

// cloudOSRam returns the cloud_os_ram value as a string, or "" if neither jvj
// nor jd supply one.
func (jvj *JobViaJSON) cloudOSRam(jd *JobDefaults) string {
	if jvj.CloudOSRam != nil {
		return strconv.Itoa(*jvj.CloudOSRam)
	}

	if jd.CloudOSRam != 0 {
		return jd.DefaultCloudOSRam()
	}

	return ""
}

// intPointerOrDefault returns the string form of *value if value is non-nil, or
// of dflt if dflt is non-zero, otherwise "".
func intPointerOrDefault(value *int, dflt int) string {
	if value != nil {
		return strconv.Itoa(*value)
	}

	if dflt != 0 {
		return strconv.Itoa(dflt)
	}

	return ""
}

// putIfNonEmpty sets m[key] = value only when value is non-empty.
func putIfNonEmpty(m map[string]string, key, value string) {
	if value != "" {
		m[key] = value
	}
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
		maps.Copy(other, *jvj.Other)

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
		behaviours = append(behaviours, ModifyBehaviours(*jvj.OnFailure, OnFailure)...)
		set = true
	}

	if jvj.OnSuccess != nil {
		behaviours = append(behaviours, ModifyBehaviours(*jvj.OnSuccess, OnSuccess)...)
		set = true
	}

	if jvj.OnExit != nil {
		behaviours = append(behaviours, ModifyBehaviours(*jvj.OnExit, OnExit)...)
		set = true
	}

	if set {
		modifier.SetBehaviours(behaviours)
	}
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("form parsing error: %s", err), http.StatusBadRequest)

		return false
	}

	token, ok := requestToken(w, r)
	if !ok {
		return false
	}

	if !tokenMatches([]byte(token), s.token) {
		http.Error(w, "Invalid token", http.StatusUnauthorized)

		return false
	}

	return true
}

// requestToken extracts the auth token from the 'token' form parameter, or
// failing that the Authorization Bearer header. If neither is usable it writes
// the appropriate error to w and returns ok=false.
func requestToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	if token := r.Form.Get("token"); token != "" {
		return token, true
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		http.Error(w, "Authorization header required", http.StatusUnauthorized)

		return "", false
	}

	if !strings.HasPrefix(authHeader, bearerSchema) {
		http.Error(w, "Authorization requires Bearer scheme", http.StatusUnauthorized)

		return "", false
	}

	return authHeader[len(bearerSchema):], true
}

// writeJSON writes payload to w as JSON with the standard content type and the
// given status, logging (but not otherwise surfacing) any encoding error using
// errContext.
func writeJSON(ctx context.Context, w http.ResponseWriter, status int, payload any, errContext string) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)

	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(payload); err != nil {
		clog.Warn(ctx, errContext, "err", err)
	}
}

// restJobs lets you do CRUD on jobs in the queue.
func restJobs(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(ctx, "jobqueue web server restJobs", false)

		if !s.httpAuthorized(w, r) {
			return
		}

		if r.Method == http.MethodPatch {
			restJobsModifyResponse(ctx, w, r, s)

			return
		}

		jobs, status, err := restJobsAction(ctx, w, r, s)
		if status == 0 {
			// unsupported method; restJobsAction already wrote the error.
			return
		}

		writeJobsResponse(ctx, w, r, jobs, status, err)
	}
}

// writeJobsResponse writes the result of a job action as a JSON []JStatus, or an
// error if the action failed or a job's status could not be determined.
func writeJobsResponse(ctx context.Context, w http.ResponseWriter, r *http.Request,
	jobs []*Job, status int, err error,
) {
	if status >= 400 || err != nil {
		http.Error(w, err.Error(), status)

		return
	}

	jstati, ok := jobsToStatuses(w, r, jobs, status)
	if !ok {
		return
	}

	writeJSON(ctx, w, status, jstati, "restJobs failed to encode job statuses")
}

// restJobsAction dispatches a non-PATCH job request to the relevant handler. A
// status of 0 means the request was unsupported and an error has already been
// written to w.
func restJobsAction(ctx context.Context, w http.ResponseWriter, r *http.Request, s *Server) ([]*Job, int, error) {
	switch r.Method {
	case http.MethodGet:
		return restJobsStatus(ctx, r, s)
	case http.MethodPost:
		return restJobsAdd(ctx, r, s)
	case http.MethodDelete:
		return restJobsCancel(ctx, r, s)
	default:
		http.Error(w, "So far only GET, POST, PATCH and DELETE are supported", http.StatusBadRequest)

		return nil, 0, nil
	}
}

// restJobsModifyResponse handles a PATCH request and writes the modify response.
func restJobsModifyResponse(ctx context.Context, w http.ResponseWriter, r *http.Request, s *Server) {
	response, status, err := restJobsModify(ctx, r, s)
	if status >= 400 || err != nil {
		http.Error(w, err.Error(), status)

		return
	}

	writeJSON(ctx, w, status, response, "restJobs failed to encode modified jobs")
}

// jobsToStatuses converts jobs to their JStatus form, stripping std streams
// unless std=true was requested. It returns ok=false (after writing an error to
// w) if a job's status could not be determined.
func jobsToStatuses(w http.ResponseWriter, r *http.Request, jobs []*Job, status int) ([]JStatus, bool) {
	jstati := make([]JStatus, len(jobs))
	includeStd := r.URL.Query().Get("std") == restFormTrue

	for i, job := range jobs {
		var err error

		jstati[i], err = job.ToStatus()
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			http.Error(w, err.Error(), status)

			return nil, false
		}

		if !includeStd {
			jstati[i].StdErr = ""
			jstati[i].StdOut = ""
		}
	}

	return jstati, true
}

// restJobsStatus gets the status of the requested jobs in the queue. The
// request url can be suffixed with comma separated job keys or RepGroups.
// Possible query parameters are search, std, env and waiting_deps (boolean
// flags enabled by "true"), limit (a number) and state (one of
// delayed|ready|reserved|running|lost|buried|dependent|suspended|complete|deletable),
// where deletable excludes reserved, running and complete jobs. Returns the
// Jobs, a http.Status* value and error.
func restJobsStatus(ctx context.Context, r *http.Request, s *Server) ([]*Job, int, error) {
	q, err := parseRESTStatusQuery(r.URL.Query())
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	if len(r.URL.Path) > len(restJobsEndpoint) {
		ids := r.URL.Path[len(restJobsEndpoint):]

		return s.restJobsStatusByIDs(ctx, ids, q)
	}

	// get all current jobs
	return s.getJobsCurrent(ctx, "", RepGroupMatchExact, q.limit, q.state, q.getStd,
		q.getEnv, q.waitingForDepGroups), http.StatusOK, nil
}

// restStatusQuery holds the parsed query parameters of a job status request.
type restStatusQuery struct {
	state                                       JobState
	limit                                       int
	search, getStd, getEnv, waitingForDepGroups bool
}

// parseRESTStatusQuery parses the supported job status query parameters.
func parseRESTStatusQuery(query url.Values) (restStatusQuery, error) {
	q := restStatusQuery{
		search:              query.Get("search") == restFormTrue,
		getStd:              query.Get("std") == restFormTrue,
		getEnv:              query.Get("env") == restFormTrue,
		waitingForDepGroups: query.Get("waiting_deps") == restFormTrue,
		state:               parseRESTJobState(query.Get("state")),
	}

	if limit := query.Get("limit"); limit != "" {
		parsed, err := strconv.Atoi(limit)
		if err != nil {
			return q, err
		}

		q.limit = parsed
	}

	return q, nil
}

// parseRESTJobState returns the JobState for value, or "" if value is empty or
// not a recognised state.
func parseRESTJobState(value string) JobState {
	switch requested := JobState(value); requested {
	case JobStateDelayed, JobStateReady, JobStateReserved, JobStateRunning, JobStateLost,
		JobStateBuried, JobStateDependent, JobStateSuspended, JobStateComplete, JobStateDeletable:
		return requested
	default:
		return ""
	}
}

// restJobsStatusByIDs returns the jobs matching the comma-separated ids, each of
// which may be a job key or a RepGroup.
func (s *Server) restJobsStatusByIDs(ctx context.Context, ids string, q restStatusQuery) ([]*Job, int, error) {
	var jobs []*Job

	for id := range strings.SplitSeq(ids, ",") {
		theseJobs, status, err := s.restJobsStatusByID(ctx, id, q)
		if err != nil {
			return nil, status, err
		}

		jobs = append(jobs, theseJobs...)
	}

	return jobs, http.StatusOK, nil
}

// restJobsStatusByID returns the jobs matching a single id, treating it first as
// a job key (if it is key-length) and otherwise as a RepGroup.
func (s *Server) restJobsStatusByID(ctx context.Context, id string, q restStatusQuery) ([]*Job, int, error) {
	if len(id) == restJobKeyLength {
		// id might be a Job.key()
		theseJobs, _, qerr := s.getJobsByKeys(ctx, []string{id}, q.getStd, q.getEnv)
		if qerr == "" && len(theseJobs) > 0 {
			return theseJobs, http.StatusOK, nil
		}
	}

	// id might be a Job.RepGroup. This is the REST status endpoint, so it wants the
	// RepGroup's archived jobs as well as its live ones (a state filter of eg. ready
	// still avoids the history, as before).
	opts := repGroupOptions{
		RepGroup:        id,
		Match:           normalizeRepGroupMatch("", q.search),
		IncludeComplete: true,
		limitJobsOptions: limitJobsOptions{
			Limit:               q.limit,
			State:               q.state,
			GetStd:              q.getStd,
			GetEnv:              q.getEnv,
			WaitingForDepGroups: q.waitingForDepGroups,
		},
	}

	theseJobs, _, qerr := s.getJobsByRepGroup(ctx, opts)
	if qerr != "" {
		return nil, http.StatusInternalServerError, Error{Err: qerr}
	}

	return theseJobs, http.StatusOK, nil
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
	jd, err := jobDefaultsFromForm(r)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	inputJobs, status, err := decodeAndConvertJobs(r, jd)
	if err != nil {
		return nil, status, err
	}

	envkey, err := s.db.storeEnv([]byte{})
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	rerun := r.Form.Get("rerun") == restFormTrue

	//nolint:dogsled // REST add only needs to know whether the shared add path failed.
	_, _, _, _, _, err = s.createJobs(ctx, inputJobs, envkey, !rerun)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	// see which of the inputJobs are now actually in the queue
	return s.inputToQueuedJobs(ctx, inputJobs), http.StatusCreated, nil
}

// decodeAndConvertJobs decodes the POSTed []*JobViaJSON and converts each to a
// *Job using the supplied defaults. The returned int is a http.Status* value.
func decodeAndConvertJobs(r *http.Request, jd *JobDefaults) ([]*Job, int, error) {
	var jvjs []*JobViaJSON

	if err := json.NewDecoder(r.Body).Decode(&jvjs); err != nil {
		return nil, http.StatusBadRequest, err
	}

	inputJobs := make([]*Job, 0, len(jvjs))

	for _, jvj := range jvjs {
		job, err := jvj.Convert(jd)
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("there was a problem interpreting your job: %w", err)
		}

		inputJobs = append(inputJobs, job)
	}

	return inputJobs, http.StatusOK, nil
}

// jobDefaultsFromForm builds the JobDefaults for an add request from the request
// form parameters, parsing those that need it.
func jobDefaultsFromForm(r *http.Request) (*JobDefaults, error) {
	jd := newJobDefaultsFromForm(r)

	jd.CwdMatters = r.Form.Get("cwd_matters") == restFormTrue
	jd.ChangeHome = r.Form.Get("change_home") == restFormTrue
	jd.CloudShared = r.Form.Get("cloud_shared") == restFormTrue

	for _, depgroup := range urlStringToSlice(r.Form.Get("deps")) {
		jd.Deps = append(jd.Deps, NewDepGroupDependency(depgroup))
	}

	if err := jd.applyFormResources(r); err != nil {
		return nil, err
	}

	if err := jd.applyFormBehavioursAndMounts(r); err != nil {
		return nil, err
	}

	return jd, nil
}

// newJobDefaultsFromForm builds a JobDefaults from the plain (non-parsed) form
// parameters, defaulting the rep group when none was supplied.
//
//nolint:funlen // a flat field-by-field mapping of form parameters; splitting it would only obscure it.
func newJobDefaultsFromForm(r *http.Request) *JobDefaults {
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
		jd.RepGrp = restDefaultRepGroup
	}

	return jd
}

// applyFormResources parses and applies the memory, time and
// no_retry_over_walltime form parameters.
func (jd *JobDefaults) applyFormResources(r *http.Request) error {
	if memory := r.Form.Get("memory"); memory != "" {
		mb, err := bytefmt.ToMegabytes(memory)
		if err != nil {
			return err
		}

		jd.Memory = int(mb) //nolint:gosec // bytefmt megabytes for a job's RAM default always fits an int.
	}

	if t := r.Form.Get("time"); t != "" {
		dur, err := time.ParseDuration(t)
		if err != nil {
			return err
		}

		jd.Time = dur
	}

	if t := r.Form.Get("no_retry_over_walltime"); t != "" {
		dur, err := time.ParseDuration(t)
		if err != nil {
			return err
		}

		jd.NoRetriesOverWalltime = dur
	}

	return nil
}

// applyFormBehavioursAndMounts parses and applies the on_failure, on_success,
// on_exit and mounts form parameters.
func (jd *JobDefaults) applyFormBehavioursAndMounts(r *http.Request) error {
	onFailure, err := behavioursFromForm(r, "on_failure", OnFailure)
	if err != nil {
		return err
	}

	jd.OnFailure = onFailure

	onSuccess, err := behavioursFromForm(r, "on_success", OnSuccess)
	if err != nil {
		return err
	}

	jd.OnSuccess = onSuccess

	onExit, err := behavioursFromForm(r, "on_exit", OnExit)
	if err != nil {
		return err
	}

	jd.OnExit = onExit

	return jd.applyFormMounts(r)
}

// applyFormMounts parses and applies the mounts form parameter.
func (jd *JobDefaults) applyFormMounts(r *http.Request) error {
	mounts := r.Form.Get("mounts")
	if mounts == "" {
		return nil
	}

	var mcs MountConfigs

	if err := urlStringToStruct(mounts, &mcs); err != nil {
		return err
	}

	if mcs != nil {
		jd.MountConfigs = mcs
	}

	return nil
}

// behavioursFromForm parses the named behaviour form parameter (if present) and
// returns the corresponding Behaviours for the given trigger.
func behavioursFromForm(r *http.Request, param string, when BehaviourTrigger) (Behaviours, error) {
	value := r.Form.Get(param)
	if value == "" {
		return nil, nil
	}

	var bvj BehavioursViaJSON

	if err := urlStringToStruct(value, &bvj); err != nil {
		return nil, err
	}

	if bvj == nil {
		return nil, nil
	}

	return bvj.Behaviours(when), nil
}

// restJobsCancel kills running jobs, confirms lost jobs as dead, or deletes
// incomplete jobs. You identify the jobs to operate on in the same way as for
// restJobsStatus(). However state must be specified, and only one of:
// (running|lost|deletable) are allowed. Returns the affected Jobs, a
// http.Status* value and error.
func restJobsCancel(ctx context.Context, r *http.Request, s *Server) ([]*Job, int, error) {
	state := restCancelState(r.Form.Get("state"))
	if state == "" {
		return nil, http.StatusBadRequest, errRESTCancelStateRequired
	}

	jobs, status, err := restJobsStatus(ctx, r, s)
	if err != nil || status != http.StatusOK {
		return nil, status, err
	}

	if state == JobStateDeletable {
		return s.restDeleteJobs(ctx, jobs), http.StatusOK, nil
	}

	handled, err := s.restKillJobs(ctx, jobs)
	if err != nil {
		return handled, http.StatusInternalServerError, err
	}

	return handled, http.StatusAccepted, nil
}

// restCancelState maps a cancel 'state' parameter to a JobState, returning ""
// for any value other than the supported running|lost|deletable.
func restCancelState(value string) JobState {
	switch JobState(value) {
	case JobStateRunning, JobStateLost, JobStateDeletable:
		return JobState(value)
	default:
		return ""
	}
}

// restDeleteJobs deletes the deletable jobs and returns those actually deleted,
// with their State updated to reflect the deletion.
func (s *Server) restDeleteJobs(ctx context.Context, jobs []*Job) []*Job {
	deleted := s.deleteJobs(ctx, jobs)

	d := make(map[string]bool, len(deleted))
	for _, key := range deleted {
		d[key] = true
	}

	var handled []*Job

	for _, job := range jobs {
		if d[job.Key()] {
			job.State = JobStateDeleted
			handled = append(handled, job)
		}
	}

	return handled
}

// restKillJobs kills the supplied jobs and returns those actually killed.
func (s *Server) restKillJobs(ctx context.Context, jobs []*Job) ([]*Job, error) {
	var handled []*Job

	for _, job := range jobs {
		killed, err := s.killJob(ctx, job.Key())
		if err != nil {
			return handled, err
		}

		if killed {
			handled = append(handled, job)
		}
	}

	return handled, nil
}

// restWarnings lets you read warnings from the scheduler, and auto-"dismisses"
// (deletes) them.
func restWarnings(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(ctx, "jobqueue web server restWarnings", false)

		if !s.httpAuthorized(w, r) {
			return
		}

		if r.Method != http.MethodGet {
			http.Error(w, "Only GET is supported", http.StatusBadRequest)

			return
		}

		// carry out a different action based on the HTTP Verb
		sis := []*schedulerIssue{}

		s.simutex.Lock()
		for key, si := range s.schedIssues {
			sis = append(sis, si)

			delete(s.schedIssues, key)
		}
		s.simutex.Unlock()

		writeJSON(ctx, w, http.StatusOK, sis, "restWarnings failed to encode scheduler issues")
	}
}

// restBadServers lets you do CRUD on cloud servers that have gone bad. The
// DELETE verb has a required 'id' parameter, being the ID of a server you wish
// to confirm as bad and have terminated if it still exists.
func restBadServers(ctx context.Context, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer internal.LogPanic(ctx, "jobqueue web server restBadServers", false)

		if !s.httpAuthorized(w, r) {
			return
		}

		// carry out a different action based on the HTTP Verb
		switch r.Method {
		case http.MethodGet:
			s.restBadServersGet(ctx, w)
		case http.MethodDelete:
			s.restBadServersDelete(ctx, w, r)
		default:
			http.Error(w, "Only GET and DELETE are supported", http.StatusBadRequest)
		}
	}
}

// restBadServersGet writes the current bad servers as JSON.
func (s *Server) restBadServersGet(ctx context.Context, w http.ResponseWriter) {
	servers := s.getBadServers()
	if len(servers) == 0 {
		servers = []*BadServer{}
	}

	writeJSON(ctx, w, http.StatusOK, servers, "restBadServers failed to encode servers")
}

// restBadServersDelete confirms the server identified by the 'id' parameter as
// bad and destroys it if it still exists.
func (s *Server) restBadServersDelete(ctx context.Context, w http.ResponseWriter, r *http.Request) {
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
		if err := server.Destroy(ctx); err != nil {
			http.Error(w, fmt.Sprintf("Server was bad but could not be destroyed: %s", err), http.StatusNotModified)

			return
		}
	}

	w.WriteHeader(http.StatusOK)
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

		msg := map[string]string{"path": savePath}

		writeJSON(ctx, w, http.StatusOK, msg, "restFileUpload failed to encode success msg")
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

		writeJSON(ctx, w, http.StatusOK, s.ServerInfo, "restInfo failed to encode ServerInfo")
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

		writeJSON(ctx, w, http.StatusOK, s.ServerVersions, "restVersion failed to encode ServerVersions")
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
func urlStringToStruct(value string, v any) error {
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
// make a new codec each time.
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
	if validationErr, invalid := modifier.validationError(); invalid {
		return nil, validationErr
	}

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
		job.Lock()
		s.handleUserSpecifiedJobLimitGroups(job, limitGroups)
		job.Unlock()
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
		deps, waitingForDepGroups, err := job.Dependencies.incompleteJobKeys(s.db)
		if err != nil {
			return err
		}

		job.setWaitingForDepGroups(waitingForDepGroups)

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
