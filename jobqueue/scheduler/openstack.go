/*******************************************************************************
 * Copyright (c) 2016-2021, 2024, 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: David K Jackson <david.jackson@sanger.ac.uk>
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

package scheduler

// This file contains a scheduleri implementation for 'openstack': running jobs
// on servers spawned on demand.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/kballard/go-shellquote"
	"github.com/patrickmn/go-cache"
)

const (
	unquotadVal                  = 1000000 // a "large" number for use when we don't have quota
	serverNotNeededErrStr        = "server not needed"
	localhostName                = "localhost"
	flavorFailedCacheExpiry      = 15 * time.Minute
	flavorFailedCacheCleanup     = 30 * time.Minute
	flavorDeterminedCacheExpiry  = 5 * time.Minute
	flavorDeterminedCacheCleanup = 10 * time.Minute

	// defaultOSRAM is the default OSRAM (MB) when none is configured.
	defaultOSRAM = 2048

	// defaultStateUpdateFreq is the state update frequency used when none is
	// configured.
	defaultStateUpdateFreq = 1 * time.Minute

	// cleanupStatePollFreq is how often cleanup() polls for an in-progress
	// state update to finish.
	cleanupStatePollFreq = 10 * time.Millisecond

	// debugSlowSecondSpawnCount is the spawn number that the "slowSecondSpawn"
	// debugEffect delays.
	debugSlowSecondSpawnCount = 3

	// slowSecondSpawnDelay is how long the "slowSecondSpawn" debugEffect delays
	// a spawn for.
	slowSecondSpawnDelay = 10 * time.Second

	// errBadOpenStackConfig is the Error message used when initialize() is not
	// given a *ConfigOpenStack.
	errBadOpenStackConfig = "SchedulerConfig must be *ConfigOpenStack"

	remoteExePresent = "present"
	remoteExeMissing = "missing"
)

// op* are the Op names used in scheduler Errors raised by the named openstack
// methods.
const (
	opDetermineFlavor = "determineFlavor"
	opGetFlavorByName = "getFlavorByName"
)

// debugCounter and debugEffect are used by tests to prove some bugs
//
//nolint:gochecknoglobals // Existing debug hooks are package-level test controls.
var (
	debugCounter                 int
	debugEffect                  string
	errDebugFailBeforeUsingQuota = errors.New("forced fail before using quota")
	errDebugForcedFail           = errors.New("forced fail")
)

// sentinel errors returned by the openstack scheduler.
var (
	errServerNotNeeded    = errors.New(serverNotNeededErrStr)
	errNoAvailableServer  = errors.New("no available server")
	errCommandHasNoExe    = errors.New("command has no executable")
	errUnexpectedExeCheck = errors.New("remote executable check returned unexpected output")
)

type exeServer interface {
	RunCmd(ctx context.Context, cmd string, background bool) (stdout, stderr string, err error)
	UploadFile(ctx context.Context, source string, dest string) error
}

// opst is our implementer of scheduleri. It takes much of its implementation
// from the local scheduler.
type opst struct {
	local
	flavorSets        [][]string
	config            *ConfigOpenStack
	provider          *cloud.Provider
	quotaMaxInstances int
	quotaMaxCores     int
	quotaMaxRAM       int
	quotaMaxVolume    int
	reservedInstances int
	reservedCores     int
	reservedRAM       int
	reservedVolume    int
	spawningNow       map[string]int
	servers           map[string]*cloud.Server
	spawnedServers    map[string]*cloud.Server
	msgCB             MessageCallBack
	badServerCB       BadServerCallBack
	recoveredServers  map[string]bool
	stopRSMonitoring  chan struct{}
	ffCache           *cache.Cache
	dfCache           *cache.Cache
	serversMutex      sync.RWMutex
	cbmutex           sync.RWMutex
	scMutex           sync.Mutex
	stateMutex        sync.Mutex
	rsMutex           sync.Mutex
	spawnMutex        sync.Mutex
	spawnCanceller    map[string]map[string]chan struct{}
	updatingState     bool
}

// ConfigOpenStack represents the configuration options required by the
// OpenStack scheduler. All are required with no usable defaults, unless
// otherwise noted. This struct implements the CloudConfig interface.
type ConfigOpenStack struct {
	// ResourceName is the resource name prefix used to name any resources (such
	// as keys, security groups and servers) that need to be created.
	ResourceName string

	// OSPrefix is the prefix or full name of the Operating System image you
	// wish spawned servers to run by default (overridden during Schedule() by a
	// Requirements.Other["cloud_os"] value)
	OSPrefix string

	// OSUser is the login username of your chosen Operating System from
	// OSPrefix. (Overridden during Schedule() by a
	// Requirements.Other["cloud_user"] value.)
	OSUser string

	// OSRAM is the minimum RAM in MB needed to bring up a server instance that
	// runs your Operating System image. It defaults to 2048. (Overridden during
	// Schedule() by a Requirements.Other["cloud_os_ram"] value.)
	OSRAM int

	// OSDisk is the minimum disk in GB with which to bring up a server instance
	// that runs your Operating System image. It defaults to 1. (Overridden
	// during Schedule() by a Requirements.Disk value.)
	OSDisk int

	// FlavorRegex is a regular expression that you can use to limit what
	// flavors of server will be created to run commands on. The default of an
	// empty string means there is no limit, and any available flavor can be
	// used. (The flavor chosen for a command will be the flavor with the least
	// specifications (RAM, CPUs, Disk) capable of running the command, that
	// also satisfies this regex.)
	FlavorRegex string

	// FlavorSets is used to describe sets of flavors that will only run on
	// certain subsets of your available hardware. If a flavor in set 1 is
	// chosen, but OpenStack reports it isn't possible to create a server with
	// that flavor because there is no more available hardware to back it, then
	// the next best flavor in a different flavor set will be attempted. The
	// value here is a string in the form f1,f2;f3,f4 where f1 and f2 are in the
	// same set, and f3 and f4 are in a different set. The names of each flavor
	// are treates as regular expressions, so you may be able to describe all
	// the flavors in a set with a single entry.
	FlavorSets string

	// PostCreationScript is the []byte content of a script you want executed
	// after a server is Spawn()ed. (Overridden during Schedule() by a
	// Requirements.Other["cloud_script"] value.)
	PostCreationScript []byte

	// PostCreationForcedCommand is a command you want to always execute after
	// a server is Spawn(ed), regardless of any
	// Requirements.Other["cloud_script"] value. Unlike PostCreationScript, this
	// command will be run after the executable in the spawn cmd has been
	// uploaded to the server.
	PostCreationForcedCommand string

	// PreDestroyScript is the []byte content of a script you want executed
	// before it is destroyed.
	PreDestroyScript []byte

	// ConfigFiles is a comma separated list of paths to config files that
	// should be copied over to all spawned servers. Absolute paths are copied
	// over to the same absolute path on the new server. To handle a config file
	// that should remain relative to the home directory (and where the spawned
	// server may have a different username and thus home directory path
	// compared to the current server), use the prefix ~/ to signify the home
	// directory. It silently ignores files that don't exist locally.
	// (Appended to during Schedule() by a
	// Requirements.Other["cloud_config_files"] value.)
	ConfigFiles string

	// SavePath is an absolute path to a file on disk where details of any
	// created resources can be read from and written to.
	SavePath string

	// ServerKeepTime is the time to wait before an idle server is destroyed.
	// Zero duration means "never destroy due to being idle".
	ServerKeepTime time.Duration

	// StateUpdateFrequency is the frequency at which to check spawned servers
	// that are being used to run things, to see if they're still alive.
	// 0 (default) is treated as 1 minute.
	StateUpdateFrequency time.Duration

	// MaxInstances is the maximum number of instances we are allowed to spawn.
	// -1 means we will be limited by your quota, if any. 0 (the default) means
	// no additional instances will be spawned (commands will run locally on the
	// same instance the manager is running on).
	MaxInstances int

	// SimultaneousSpawns is the maximum number of instances we are allowed to
	// try and spawn simultaneously. 0 (the default) means unlimited. 1 would
	// mean all spawns occur sequentially, which may be more reliable, but would
	// result in very slow scale up.
	SimultaneousSpawns int

	// MaxLocalCores is the maximum number of cores that can be used to run
	// commands on the same instance the manager is running on. -1 (the default)
	// means all cores can be used. 0 will only allow 0 core cmds to run on it.
	// To distinguish "not defined" from 0, the value is a reference to an int.
	MaxLocalCores *int

	// MaxLocalRAM is the maximum number of MB of memory that can be used to run
	// commands on the same instance the manager is running on. -1 (the default)
	// means all memory can be used. 0 disables running commands on the
	// manager's instance. To distinguish "not defined" from 0, the value is a
	// reference to an int.
	MaxLocalRAM *int

	// Shell is the shell to use to run your commands with; 'bash' is
	// recommended.
	Shell string

	// ServerPorts are the TCP port numbers you need to be open for
	// communication with any spawned servers. At a minimum you will need to
	// specify []int{22}, unless the network you use has all ports open and does
	// not support applying security groups to servers, in which case you must
	// supply an empty slice.
	ServerPorts []int

	// UseConfigDrive, if set to true (default false), will cause all newly
	// spawned servers to mount a configuration drive, which is typically needed
	// for a network without DHCP.
	UseConfigDrive bool

	// CIDR describes the range of network ips that can be used to spawn
	// OpenStack servers on which to run our commands. The default is
	// "192.168.64.0/18", which allows for 16384 servers to be spawned. This
	// range ends at 192.168.127.255. If already in OpenStack, this chooses
	// which existing network (that the current host is attached to) to use.
	// Otherwise, this results in the creation of an appropriately configured
	// network and subnet.
	CIDR string

	// GatewayIP is the gateway ip address for the subnet that will be created
	// with the given CIDR. It defaults to 192.168.64.1.
	GatewayIP string

	// DNSNameServers is a slice of DNS IP addresses to use for lookups on the
	// created subnet. It defaults to Google's: []string{"8.8.4.4", "8.8.8.8"}.
	DNSNameServers []string

	// Umask is an optional umask to run remote commands under, to control the
	// permissions of files created on spawned OpenStack servers. If not
	// supplied (0), the umask used will be the default umask of the OSUser
	// user. Note that setting this will result in scheduled commands being
	// executed like `(umask Umask && cmd)`, which may present cross-platform
	// compatibility issues. (But should work on most linux-like systems.)
	Umask int
}

// AddConfigFile takes a value as per the ConfigFiles property, and appends it
// to the existing ConfigFiles value (or sets it if unset).
func (c *ConfigOpenStack) AddConfigFile(configFile string) {
	if c.ConfigFiles == "" {
		c.ConfigFiles = configFile
	} else {
		c.ConfigFiles += "," + configFile
	}
}

// GetOSUser returns OSUser, to meet the CloudConfig interface.
func (c *ConfigOpenStack) GetOSUser() string {
	return c.OSUser
}

// GetServerKeepTime returns ServerKeepTime, to meet the CloudConfig interface.
func (c *ConfigOpenStack) GetServerKeepTime() time.Duration {
	return c.ServerKeepTime
}

// initialize sets up an openstack scheduler.
func (s *opst) initialize(ctx context.Context, config any) error {
	conf, ok := config.(*ConfigOpenStack)
	if !ok {
		return Error{openstackScheduler, opInitialize, errBadOpenStackConfig}
	}

	s.config = conf
	if s.config.OSRAM == 0 {
		s.config.OSRAM = defaultOSRAM
	}

	if s.config.OSDisk == 0 {
		s.config.OSDisk = 1
	}

	if err := s.setupProvider(ctx); err != nil {
		return err
	}

	// setupLocalhostServer calls the cloud API's LocalhostServer, which takes no
	// context.
	//nolint:contextcheck // LocalhostServer is a cloud API call with no context
	if err := s.setupLocalhostServer(); err != nil {
		return err
	}

	s.setupTrackersAndFuncs(ctx)

	return nil
}

// setupProvider creates and deploys the openstack cloud provider, and records
// our quota maximums.
func (s *opst) setupProvider(ctx context.Context) error {
	// create a cloud provider for openstack, that we'll use to interact with
	// openstack
	provider, err := cloud.New(ctx, openstackScheduler, s.config.ResourceName, s.config.SavePath)
	if err != nil {
		return err
	}

	s.provider = provider

	err = provider.Deploy(ctx, &cloud.DeployConfig{
		RequiredPorts:  s.config.ServerPorts,
		UseConfigDrive: s.config.UseConfigDrive,
		GatewayIP:      s.config.GatewayIP,
		CIDR:           s.config.CIDR,
		DNSNameServers: s.config.DNSNameServers,
	})
	if err != nil {
		return err
	}

	// to debug spawned servers that don't work correctly:
	// keyFile := filepath.Join("/tmp", "key")
	// os.WriteFile(keyFile, []byte(provider.PrivateKey()), 0600)

	// query our quota maximums for cpu and memory and total number of
	// instances; 0 will mean unlimited
	quota, err := provider.GetQuota(ctx)
	if err != nil {
		return err
	}

	s.setQuotaMaxes(quota)

	return nil
}

// setQuotaMaxes records our quota maximums from the given quota, treating 0
// (unlimited) as a "large" number, and applying any configured MaxInstances.
func (s *opst) setQuotaMaxes(quota *cloud.Quota) {
	s.quotaMaxCores = quotaOrUnlimited(quota.MaxCores)
	s.quotaMaxRAM = quotaOrUnlimited(quota.MaxRAM)
	s.quotaMaxVolume = quotaOrUnlimited(quota.MaxVolume)
	s.quotaMaxInstances = quotaOrUnlimited(quota.MaxInstances)

	if s.config.MaxInstances > -1 && s.config.MaxInstances < s.quotaMaxInstances {
		s.quotaMaxInstances = s.config.MaxInstances
		if s.provider.InCloud() {
			s.quotaMaxInstances++
		}
	}
}

// quotaOrUnlimited returns unquotadVal if max is 0 (meaning unlimited), else
// max.
func quotaOrUnlimited(maxVal int) int {
	if maxVal == 0 {
		return unquotadVal
	}

	return maxVal
}

// setupLocalhostServer initialises our servers map with details of ourself,
// applying any configured local core/RAM limits.
func (s *opst) setupLocalhostServer() error {
	s.servers = make(map[string]*cloud.Server)

	localhost, err := s.provider.LocalhostServer(s.config.OSPrefix, s.config.PostCreationScript,
		s.config.ConfigFiles, s.config.CIDR)
	if err != nil {
		return err
	}

	localhost.Flavor.Cores = clampLocalLimit(s.config.MaxLocalCores, localhost.Flavor.Cores)
	localhost.Flavor.RAM = clampLocalLimit(s.config.MaxLocalRAM, localhost.Flavor.RAM)

	s.servers[localhostName] = localhost

	return nil
}

// clampLocalLimit returns limit (the configured max) if it is non-nil, >= 0 and
// less than current; otherwise it returns current unchanged.
func clampLocalLimit(limit *int, current int) int {
	if limit != nil && *limit >= 0 && *limit < current {
		return *limit
	}

	return current
}

// setupTrackersAndFuncs initialises our job queue and other trackers, and sets
// our functions for use in schedule() and processQueue().
func (s *opst) setupTrackersAndFuncs(ctx context.Context) {
	// initialize our job queue and other trackers
	s.queue = queue.New(ctx, localPlace)
	s.running = make(map[string]int)
	s.spawningNow = make(map[string]int)
	s.spawnedServers = make(map[string]*cloud.Server)
	s.recoveredServers = make(map[string]bool)
	s.stopRSMonitoring = make(chan struct{})
	s.spawnCanceller = make(map[string]map[string]chan struct{})

	s.setSchedulerFuncs()

	s.stateUpdateFreq = s.config.StateUpdateFrequency
	if s.stateUpdateFreq == 0 {
		s.stateUpdateFreq = defaultStateUpdateFreq
	}

	// pass through our shell config and logger to our local embed, as well as
	// creating its stopAuto channel
	s.local.config = &ConfigLocal{Shell: s.config.Shell}
	s.stopAuto = make(chan bool)

	if s.config.FlavorSets != "" {
		sets := strings.SplitSeq(s.config.FlavorSets, ";")
		for set := range sets {
			flavors := strings.Split(set, ",")
			s.flavorSets = append(s.flavorSets, flavors)
		}
	}

	s.ffCache = cache.New(flavorFailedCacheExpiry, flavorFailedCacheCleanup)
	s.dfCache = cache.New(flavorDeterminedCacheExpiry, flavorDeterminedCacheCleanup)
}

// setSchedulerFuncs sets our functions for use in schedule() and
// processQueue().
func (s *opst) setSchedulerFuncs() {
	s.reqCheckFunc = s.reqCheck
	s.maxMemFunc = s.maxMem
	s.maxCPUFunc = s.maxCPU
	s.canCountFunc = s.canCount
	s.cantFunc = s.spawnMultiple
	s.runCmdFunc = s.runCmd
	s.stateUpdateFunc = s.stateUpdate
	s.postProcessFunc = s.postProcess
	s.cmdNotNeededFunc = s.cmdNotNeeded
}

// reqCheck gives an ErrImpossible if the given Requirements can not be met,
// based on our quota and the available server flavours. Also based on the
// specific flavor the user has specified, if any.
func (s *opst) reqCheck(ctx context.Context, req *Requirements) error {
	reqForSpawn := s.reqForSpawn(req)

	if err := s.reqCheckQuota(ctx, reqForSpawn); err != nil {
		return err
	}

	name, defined := req.Other["cloud_flavor"]
	if !defined {
		// check if possible vs flavors
		_, err := s.determineFlavor(ctx, req, "")

		return err
	}

	return s.reqCheckFlavor(ctx, name, reqForSpawn)
}

// reqCheckQuota returns ErrImpossible if reqForSpawn exceeds our quota maximums.
func (s *opst) reqCheckQuota(ctx context.Context, reqForSpawn *Requirements) error {
	withinQuota := reqForSpawn.RAM <= s.quotaMaxRAM &&
		int(math.Ceil(reqForSpawn.Cores)) <= s.quotaMaxCores &&
		reqForSpawn.Disk <= s.quotaMaxVolume
	if withinQuota {
		return nil
	}

	clog.Warn(ctx, "Requested resources are greater than max quota", "quotaCores", s.quotaMaxCores, "requiredCores",
		reqForSpawn.Cores, "quotaRAM", s.quotaMaxRAM, "requiredRAM", reqForSpawn.RAM, "quotaDisk", s.quotaMaxVolume,
		"requiredDisk", reqForSpawn.Disk)
	s.notifyMessage(fmt.Sprintf("OpenStack: not enough quota for the job needing %f cores, %d RAM and %d Disk",
		reqForSpawn.Cores, reqForSpawn.RAM, reqForSpawn.Disk))

	return Error{openstackScheduler, opSchedule, ErrImpossible}
}

// reqCheckFlavor returns ErrImpossible if the user-requested flavor isn't big
// enough to run a job needing reqForSpawn.
func (s *opst) reqCheckFlavor(ctx context.Context, name string, reqForSpawn *Requirements) error {
	requestedFlavor, err := s.getFlavor(ctx, name)
	if err != nil {
		return err
	}

	// check that the user hasn't requested a flavor that isn't actually big
	// enough to run their job
	if requestedFlavor.Cores >= int(math.Ceil(reqForSpawn.Cores)) && requestedFlavor.RAM >= reqForSpawn.RAM {
		return nil
	}

	clog.Warn(ctx, "Requested flavor is too small for the job", "flavor", requestedFlavor.Name, "flavorCores",
		requestedFlavor.Cores, "requiredCores", reqForSpawn.Cores, "flavorRAM", requestedFlavor.RAM, "requiredRAM",
		reqForSpawn.RAM)
	s.notifyMessage(fmt.Sprintf("OpenStack: requested flavor %s is too small for the job needing %f cores and %d RAM",
		requestedFlavor.Name, reqForSpawn.Cores, reqForSpawn.RAM))

	return Error{openstackScheduler, opSchedule, ErrImpossible}
}

// maxMem returns the maximum memory available in quota.
func (s *opst) maxMem() int {
	return s.quotaMaxRAM
}

// maxCPU returns the maximum number of CPU cores available quota.
func (s *opst) maxCPU() int {
	return s.quotaMaxCores
}

// determineFlavor picks a server flavor, preferring the smallest (cheapest)
// amongst those that are capable of running it from the earliest possible
// flavor set.
//
// If the initial pick is for a flavor that has been marked as unusable (because
// the last time we tried to spawn a server of the flavor it failed due to lack
// of hardware), we return the best pick from the next possible flavor set. If
// all possible picks from all flavor sets have been marked unusable, we return
// the flavor from the first possible flavor set, to give it another try.
//
// Since this is called during our canCount and then during runCmd for each
// "can", we want the return value to be the same for that set of calls, so we
// cache based on the "call" argument that processQueue sent in to canCount and
// runCmd, which in turn pass through to here.
// cachedFlavor returns the previously determined flavor for the given non-empty
// call, if one is cached.
func (s *opst) cachedFlavor(call string) (*cloud.Flavor, bool) {
	if call == "" {
		return nil, false
	}

	cached, ok := s.dfCache.Get(call)
	if !ok {
		return nil, false
	}

	flavor, isFlavor := cached.(*cloud.Flavor)

	return flavor, isFlavor
}

func (s *opst) determineFlavor(ctx context.Context, req *Requirements, call string) (*cloud.Flavor, error) {
	ctx = clog.ContextWithCallValue(ctx, call)

	if flavor, cached := s.cachedFlavor(call); cached {
		return flavor, nil
	}

	flavors, err := s.provider.CheapestServerFlavors(ctx, int(math.Ceil(req.Cores)), req.RAM,
		s.config.FlavorRegex, s.flavorSets)
	if err != nil {
		return nil, err
	}

	if !hasUsableFlavor(flavors) {
		return nil, Error{openstackScheduler, opDetermineFlavor, ErrImpossible}
	}

	flavor := s.pickFlavor(ctx, flavors)

	if call != "" {
		s.dfCache.Set(call, flavor, cache.DefaultExpiration)
	}

	return flavor, nil
}

// hasUsableFlavor reports whether flavors contains at least one non-nil flavor.
func hasUsableFlavor(flavors []*cloud.Flavor) bool {
	for _, f := range flavors {
		if f != nil {
			return true
		}
	}

	return false
}

// pickFlavor picks the cheapest non-nil flavor that hasn't recently failed to
// spawn; if all have failed, it picks the one from the earliest flavor set.
func (s *opst) pickFlavor(ctx context.Context, flavors []*cloud.Flavor) *cloud.Flavor {
	flavor, pickedI, pickedFirst := s.selectFlavor(flavors)

	logFlavorPick(ctx, flavor, pickedI, pickedFirst)

	return flavor
}

// selectFlavor returns the first unfailed non-nil flavor (and its set index),
// falling back to the first non-nil flavor if all are failed. pickedFirst
// reports that fallback.
func (s *opst) selectFlavor(flavors []*cloud.Flavor) (flavor *cloud.Flavor, pickedI int, pickedFirst bool) {
	for i, f := range flavors {
		if f == nil {
			continue
		}

		if flavor == nil {
			flavor = f
			pickedI = i
			pickedFirst = true
		}

		if _, failed := s.ffCache.Get(f.ID); failed {
			continue
		}

		flavor = f
		pickedI = i
		pickedFirst = false

		break
	}

	return flavor, pickedI, pickedFirst
}

// logFlavorPick debug-logs when pickFlavor had to fall back to a failed flavor.
func logFlavorPick(ctx context.Context, flavor *cloud.Flavor, pickedI int, pickedFirst bool) {
	if pickedFirst {
		clog.Debug(ctx, "determineFlavor's picks were all failed, picking the one from the earliest flavor set",
			"set", pickedI, "flavor", flavor.Name)
	} else if pickedI != 0 {
		clog.Debug(ctx, "determineFlavor's first pick was failed, picking one that is unfailed",
			"set", pickedI, "flavor", flavor.Name)
	}
}

// getFlavor returns a flavor with the given name or id. Returns an error
// if no matching flavor exists.
func (s *opst) getFlavor(ctx context.Context, name string) (*cloud.Flavor, error) {
	flavor, err := s.provider.GetServerFlavor(ctx, name)
	if err != nil {
		var perr cloud.Error
		if errors.As(err, &perr) {
			err = Error{openstackScheduler, opGetFlavorByName, ErrBadFlavor}
		}
	}

	return flavor, err
}

// serverReqs checks the given req's Other details to see if a particular kind
// of server has been requested. If not specified, the returned os defaults to
// the configured OSPrefix, script defaults to PostCreationScript, config files
// defaults to ConfigFiles and flavor will be nil.
func (s *opst) serverReqs(ctx context.Context, req *Requirements) (osPrefix string, osScript []byte,
	osConfigFiles string, flavor *cloud.Flavor, sharedDisk bool, err error,
) {
	osPrefix = s.config.OSPrefix
	if val, defined := req.Other["cloud_os"]; defined {
		osPrefix = val
	}

	osScript = s.config.PostCreationScript
	if val, defined := req.Other["cloud_script"]; defined {
		osScript = []byte(val)
	}

	osConfigFiles = s.osConfigFilesForReq(req)

	if name, defined := req.Other["cloud_flavor"]; defined {
		flavor, err = s.getFlavor(ctx, name)
		if err != nil {
			return osPrefix, osScript, osConfigFiles, flavor, sharedDisk, err
		}
	}

	if val, defined := req.Other["cloud_shared"]; defined && val == "true" {
		sharedDisk = true

		// createSharedDisk calls the cloud API's CreateSharedDisk, which takes
		// no context.
		//nolint:contextcheck // CreateSharedDisk is a cloud API call with no context
		err = s.createSharedDisk()
	}

	return osPrefix, osScript, osConfigFiles, flavor, sharedDisk, err
}

// osConfigFilesForReq returns the config files to copy to a spawned server for
// req: the configured ConfigFiles, with any req-specific files appended.
func (s *opst) osConfigFilesForReq(req *Requirements) string {
	val, defined := req.Other["cloud_config_files"]
	if !defined {
		return s.config.ConfigFiles
	}

	if s.config.ConfigFiles != "" {
		return s.config.ConfigFiles + "," + val
	}

	return val
}

// createSharedDisk creates a shared disk on our "head" node (if not already
// done).
func (s *opst) createSharedDisk() error {
	s.serversMutex.RLock()
	defer s.serversMutex.RUnlock()

	return s.servers[localhostName].CreateSharedDisk()
}

// canCount tells you how many jobs with the given RAM and core requirements it
// is possible to run, given remaining resources in existing servers.
func (s *opst) canCount(ctx context.Context, _ string, req *Requirements, call string) int {
	ctx = clog.ContextWithCallValue(ctx, call)

	if s.cleanedUp() {
		return 0
	}

	requestedOS, requestedScript, requestedConfigFiles, requestedFlavor, needsSharedDisk, err := s.serverReqs(ctx, req)
	if err != nil {
		clog.Warn(ctx, "Failed to determine server requirements", "err", err)

		return 0
	}

	// we don't do any actual checking of current resources on the machines, but
	// instead rely on our simple tracking based on how many cores and RAM
	// prior cmds were /supposed/ to use. This could be bad for misbehaving cmds
	// that use too much memory, but we will end up killing cmds that do this,
	// so it shouldn't be too much of an issue.

	// see how many of these commands will run on existing servers
	return s.countSpaceOnServers(req, requestedOS, requestedScript, requestedConfigFiles, requestedFlavor,
		needsSharedDisk)
}

// countSpaceOnServers sums the space for jobs matching req across all existing
// non-bad servers that match the given server requirements.
func (s *opst) countSpaceOnServers(req *Requirements, requestedOS string, requestedScript []byte,
	requestedConfigFiles string, requestedFlavor *cloud.Flavor, needsSharedDisk bool,
) int {
	s.serversMutex.RLock()
	defer s.serversMutex.RUnlock()

	var canCount int

	for _, server := range s.servers {
		if server.IsBad() {
			continue
		}

		if server.Matches(requestedOS, requestedScript, requestedConfigFiles, requestedFlavor, needsSharedDisk) {
			canCount += server.HasSpaceFor(req.Cores, req.RAM, req.Disk)
		}
	}

	return canCount
}

// spawnMultiple is our cantFunc which is run when canCount() returns less than
// desired number of jobs.
//
// If there is enough quota to spawn new servers, and we are not already in the
// middle of spawning too many servers, we spawn instances in the background.
func (s *opst) spawnMultiple(ctx context.Context, desired int, cmd string, req *Requirements, call string) {
	ctx = clog.ContextWithCallValue(ctx, call)

	s.spawnMutex.Lock()
	defer s.spawnMutex.Unlock()

	spawningTotal, spawningCmd := s.countSpawning(cmd)

	if s.config.SimultaneousSpawns > 0 && spawningTotal >= s.config.SimultaneousSpawns {
		clog.Debug(ctx, "spawnMultiple is spawning max servers already")

		return
	}

	sr, perServer, flavor, ok := s.serverReqsForSpawn(ctx, req, call)
	if !ok {
		return
	}

	todo, allowed := s.spawnTodo(ctx, desired, perServer, sr.spawnable, spawningTotal, spawningCmd, cmd)
	if todo <= 0 {
		return
	}

	// spawn servers in the background
	clog.Debug(ctx, "spawnMultiple will spawn new servers", "cmd", cmd, "desired", desired, "perserver",
		perServer, "spawnable", sr.spawnable, "allowed", allowed, "already", spawningCmd, "actual", todo)

	s.startSpawns(ctx, todo, sr, flavor, cmd)
}

// startSpawns launches todo background spawns for cmd, recording each in
// spawningNow.
func (s *opst) startSpawns(ctx context.Context, todo int, sr spawnReqs, flavor *cloud.Flavor, cmd string) {
	for range todo {
		s.spawningNow[cmd]++

		go s.spawnOneInBackground(ctx, sr.reqForSpawn, flavor, sr.requestedOS, sr.requestedScript,
			sr.requestedConfigFiles, sr.needsSharedDisk, cmd)
	}
}

// spawnReqs gathers the inputs spawnMultiple needs to start spawning servers.
type spawnReqs struct {
	reqForSpawn          *Requirements
	requestedScript      []byte
	requestedOS          string
	requestedConfigFiles string
	spawnable            int
	needsSharedDisk      bool
}

// serverReqsForSpawn determines the server requirements and how many servers of
// what flavor we can spawn for req. ok is false (with a reason logged) if we
// can't spawn anything.
func (s *opst) serverReqsForSpawn(ctx context.Context, req *Requirements, call string,
) (sr spawnReqs, perServer int, flavor *cloud.Flavor, ok bool) {
	var requestedFlavor *cloud.Flavor

	var err error

	sr.requestedOS, sr.requestedScript, sr.requestedConfigFiles, requestedFlavor, sr.needsSharedDisk, err =
		s.serverReqs(ctx, req)
	if err != nil {
		clog.Warn(ctx, "Failed to determine server requirements", "err", err)

		return sr, 0, nil, false
	}

	sr.reqForSpawn = s.reqForSpawn(req)

	// work out how many we should spawn at once
	sr.spawnable, flavor = s.checkQuota(ctx, sr.reqForSpawn, requestedFlavor, call)
	if sr.spawnable == 0 {
		clog.Debug(ctx, "spawnMultiple can't spawn due to lack of quota")

		return sr, 0, nil, false
	}

	// servers we spawn can have more disk than in the flavor, so we don't
	// consider reqForSpawn.Disk here
	perServer = flavor.HasSpaceFor(sr.reqForSpawn.Cores, sr.reqForSpawn.RAM, 0)
	if perServer == 0 {
		clog.Error(ctx, "determined flavor doesn't have space for req", "flavor", flavor, "req", sr.reqForSpawn)

		return sr, 0, nil, false
	}

	return sr, perServer, flavor, true
}

// countSpawning returns the total number of servers currently spawning, and the
// number spawning for the given cmd.
func (s *opst) countSpawning(cmd string) (spawningTotal, spawningCmd int) {
	for thisCmd, spawning := range s.spawningNow {
		spawningTotal += spawning
		if thisCmd == cmd {
			spawningCmd = spawning
		}
	}

	return spawningTotal, spawningCmd
}

// spawnTodo works out how many servers we should actually spawn now (capped by
// what's spawnable and any SimultaneousSpawns limit), along with the
// SimultaneousSpawns allowance used for logging. It returns todo <= 0 if we
// shouldn't spawn any.
func (s *opst) spawnTodo(ctx context.Context, desired, perServer, spawnable, spawningTotal, spawningCmd int,
	cmd string,
) (todo, allowed int) {
	todo = int(math.Ceil(float64(desired) / float64(perServer)))

	needed := todo - spawningCmd
	if needed <= 0 {
		clog.Debug(ctx, "spawnMultiple is spawning enough for cmd already", "cmd", cmd, "todo", todo,
			"already", spawningCmd)

		return 0, 0
	}

	todo = min(spawnable, needed)

	if s.config.SimultaneousSpawns > 0 {
		allowed = s.config.SimultaneousSpawns - spawningTotal
		if allowed < todo {
			todo = allowed
		}
	}

	return todo, allowed
}

// spawnOneInBackground spawns a single server and, once done, decrements the
// spawning count and recalls processQueue. Intended to be run in a goroutine.
func (s *opst) spawnOneInBackground(ctx context.Context, reqForSpawn *Requirements, flavor *cloud.Flavor,
	requestedOS string, requestedScript []byte, requestedConfigFiles string, needsSharedDisk bool, cmd string,
) {
	defer internal.LogPanic(ctx, "spawnMultiple", false)

	s.spawn(ctx, reqForSpawn, flavor, requestedOS, requestedScript, requestedConfigFiles, needsSharedDisk, cmd)

	s.spawnMutex.Lock()

	s.spawningNow[cmd]--
	if s.spawningNow[cmd] <= 0 {
		delete(s.spawningNow, cmd)
	}
	s.spawnMutex.Unlock()

	errp := s.processQueue(ctx, "post spawn")
	if errp != nil {
		clog.Error(ctx, "processQueue recall failed", "err", errp)
	}
}

// checkQuota sees if there's enough quota to spawn a server suitable for the
// given requirements.
//
// If requestedFlavor is nil, the smallest suitable server flavor will be
// determined.
//
// Returns the number of servers that can be spawned, and the flavor that should
// be spawned (if number greater than 0). Errors are simply Warn()ed.
func (s *opst) checkQuota(ctx context.Context, req *Requirements, requestedFlavor *cloud.Flavor, call string,
) (int, *cloud.Flavor) {
	ctx = clog.ContextWithCallValue(ctx, call)

	s.resourceMutex.RLock()
	defer s.resourceMutex.RUnlock()

	flavor, quota, ok := s.flavorAndQuota(ctx, req, requestedFlavor, call)
	if !ok {
		return 0, nil
	}

	remainingInstances := s.remainingInstanceQuota(ctx, quota)
	remainingRAM := s.remainingRAMQuota(ctx, quota, flavor)
	remainingCores := s.remainingCoresQuota(ctx, quota, flavor)

	checkVolume := req.Disk > flavor.Disk // we'll only use up volume if we need more than the flavor offers
	remainingVolume := s.remainingVolumeQuota(ctx, quota, req, flavor, checkVolume)

	if remainingInstances < 1 || remainingRAM < flavor.RAM || remainingCores < flavor.Cores || remainingVolume < req.Disk {
		return 0, nil
	}

	spawnable := calcSpawnable(remainingInstances, remainingRAM, remainingCores, remainingVolume, req, flavor, checkVolume)

	return spawnable, flavor
}

// flavorAndQuota determines the flavor to spawn (unless requestedFlavor is
// given) and fetches the current quota. ok is false (with a reason logged) on
// any error.
func (s *opst) flavorAndQuota(ctx context.Context, req *Requirements, requestedFlavor *cloud.Flavor, call string,
) (*cloud.Flavor, *cloud.Quota, bool) {
	flavor := requestedFlavor

	var err error
	if flavor == nil {
		flavor, err = s.determineFlavor(ctx, req, call)
		if err != nil {
			clog.Warn(ctx, "Failed to determine a server flavor", "err", err)

			return nil, nil, false
		}
	}

	quota, err := s.provider.GetQuota(ctx) // this includes resources used by currently spawning servers
	if err != nil {
		clog.Warn(ctx, "Failed to GetQuota", "err", err)

		return nil, nil, false
	}

	return flavor, quota, true
}

// remainingInstanceQuota returns how many more instances we can spawn given the
// provider quota and the user's configured max instances.
func (s *opst) remainingInstanceQuota(ctx context.Context, quota *cloud.Quota) int {
	remainingInstances := unquotadVal
	if quota.MaxInstances > 0 {
		remainingInstances = quota.MaxInstances - quota.UsedInstances - s.reservedInstances
		if remainingInstances < 1 {
			clog.Debug(ctx, "lack of instance quota", "remaining", remainingInstances, "max", quota.MaxInstances,
				"used", quota.UsedInstances, "reserved", s.reservedInstances)
			s.notifyMessage("OpenStack: Not enough instance quota to create another server")
		}
	}

	if remainingInstances > 0 && s.quotaMaxInstances > -1 && s.quotaMaxInstances < quota.MaxInstances {
		remainingInstances = s.applyConfiguredMaxInstances(ctx, remainingInstances)
	}

	return remainingInstances
}

// applyConfiguredMaxInstances reduces remainingInstances if the user's
// configured max instances would be breached.
func (s *opst) applyConfiguredMaxInstances(ctx context.Context, remainingInstances int) int {
	// also check that the users configured max instances hasn't been breached
	s.serversMutex.RLock()
	numServers := len(s.servers)
	s.serversMutex.RUnlock()
	used := numServers + s.reservedInstances

	remaining := s.quotaMaxInstances - used
	if remaining < remainingInstances {
		remainingInstances = remaining
	}

	if remainingInstances < 1 {
		clog.Debug(ctx, "instances over configured max", "remaining", remainingInstances, "configuredMax",
			s.quotaMaxInstances, "usedPersonally", numServers, "reserved", s.reservedInstances)
	}

	return remainingInstances
}

// remainingRAMQuota returns how much more RAM quota we have for spawning a
// server of the given flavor.
func (s *opst) remainingRAMQuota(ctx context.Context, quota *cloud.Quota, flavor *cloud.Flavor) int {
	remainingRAM := unquotadVal
	if quota.MaxRAM > 0 {
		remainingRAM = quota.MaxRAM - quota.UsedRAM - s.reservedRAM
		if remainingRAM < flavor.RAM {
			clog.Debug(ctx, "lack of ram quota", "remaining", remainingRAM, "max", quota.MaxRAM, "used", quota.UsedRAM,
				"reserved", s.reservedRAM)
			s.notifyMessage(fmt.Sprintf(
				"OpenStack: Not enough RAM quota to create another server (need %d, have %d)", flavor.RAM, remainingRAM))
		}
	}

	return remainingRAM
}

// remainingCoresQuota returns how many more cores of quota we have for spawning
// a server of the given flavor.
func (s *opst) remainingCoresQuota(ctx context.Context, quota *cloud.Quota, flavor *cloud.Flavor) int {
	remainingCores := unquotadVal
	if quota.MaxCores > 0 {
		remainingCores = quota.MaxCores - quota.UsedCores - s.reservedCores
		if remainingCores < flavor.Cores {
			clog.Debug(ctx, "lack of cores quota", "remaining", remainingCores, "max", quota.MaxCores,
				"used", quota.UsedCores, "reserved", s.reservedCores)
			s.notifyMessage(fmt.Sprintf(
				"OpenStack: Not enough cores quota to create another server (need %d, have %d)",
				flavor.Cores, remainingCores))
		}
	}

	return remainingCores
}

// remainingVolumeQuota returns how much more volume quota we have for the given
// req/flavor, or unquotadVal if we won't be using volume.
func (s *opst) remainingVolumeQuota(ctx context.Context, quota *cloud.Quota, req *Requirements,
	flavor *cloud.Flavor, checkVolume bool,
) int {
	remainingVolume := unquotadVal
	if quota.MaxVolume > 0 && checkVolume {
		remainingVolume = quota.MaxVolume - quota.UsedVolume - s.reservedVolume
		if remainingVolume < req.Disk {
			clog.Debug(ctx, "lack of volume quota", "remaining", remainingVolume, "max", quota.MaxVolume, "used",
				quota.UsedVolume, "reserved", s.reservedVolume)
			s.notifyMessage(fmt.Sprintf(
				"OpenStack: Not enough volume quota to create another server (need %d, have %d)",
				flavor.Disk, remainingVolume))
		}
	}

	return remainingVolume
}

// calcSpawnable works out how many servers of the given flavor we can spawn
// given the remaining quota for each resource.
//
// (we only care that we can spawn at least 1, but calculate the actual
// spawnable number in case we want to spawn multiple at once in the future).
func calcSpawnable(remainingInstances, remainingRAM, remainingCores, remainingVolume int, req *Requirements,
	flavor *cloud.Flavor, checkVolume bool,
) int {
	spawnable := remainingInstances
	if spawnable <= 1 {
		return spawnable
	}

	if n := remainingRAM / flavor.RAM; n < spawnable { // dividing ints == floor
		spawnable = n
	}

	if n := remainingCores / flavor.Cores; n < spawnable {
		spawnable = n
	}

	if checkVolume {
		if n := remainingVolume / req.Disk; n < spawnable {
			spawnable = n
		}
	}

	return spawnable
}

// reqForSpawn checks the input Requirements and if the configured OSRAM (or
// overriding that, the Requirements.Other["cloud_os_ram"]) is higher that the
// Requirements.RAM, or Requirements.Disk is not set and OSDisk is configured,
// returns a new Requirements with the higher RAM/ configured Disk value.
// Otherwise returns the input.
func (s *opst) reqForSpawn(req *Requirements) *Requirements {
	reqForSpawn := req

	osRAM := s.osRAMForReq(req)
	if req.RAM < osRAM {
		reqForSpawn = &Requirements{
			RAM:   osRAM,
			Time:  req.Time,
			Cores: req.Cores,
			Disk:  req.Disk,
			Other: req.Other,
		}
	}

	disk := req.Disk
	if disk == 0 {
		disk = s.config.OSDisk
	}

	if req.Disk < disk {
		reqForSpawn = &Requirements{
			RAM:   reqForSpawn.RAM,
			Time:  reqForSpawn.Time,
			Cores: reqForSpawn.Cores,
			Disk:  disk,
			Other: reqForSpawn.Other,
		}
	}

	return reqForSpawn
}

// osRAMForReq returns the minimum RAM (MB) needed to bring up a server for req:
// the Requirements.Other["cloud_os_ram"] value if set and valid, else the
// configured OSRAM.
func (s *opst) osRAMForReq(req *Requirements) int {
	val, defined := req.Other["cloud_os_ram"]
	if !defined {
		return s.config.OSRAM
	}

	if i, err := strconv.Atoi(val); err == nil {
		return i
	}

	return s.config.OSRAM
}

// spawn creates a new instance in OpenStack. Errors are not returned but are
// logged, and problematic servers are terminated.
func (s *opst) spawn(ctx context.Context, req *Requirements, flavor *cloud.Flavor, requestedOS string,
	requestedScript []byte, requestedConfigFiles string, needsSharedDisk bool, cmd string,
) {
	ctx = clog.ContextWithServerFlavor(ctx, flavor.Name)
	volumeAffected := req.Disk > flavor.Disk

	usingQuotaCB := s.reserveSpawnResources(flavor, req.Disk, volumeAffected)

	thisDebugCount := nextDebugCount()
	if debugEffect == "slowSecondSpawn" && thisDebugCount == debugSlowSecondSpawnCount {
		<-time.After(slowSecondSpawnDelay)
	}

	server, failMsg, err := s.doSpawn(ctx, req, flavor, requestedOS, cmd, usingQuotaCB, thisDebugCount)
	if server != nil {
		ctx = clog.ContextWithServerID(ctx, server.ID)
	}

	if err == nil && server != nil {
		failMsg, err = s.prepareServer(ctx, server, cmd, requestedConfigFiles, requestedScript, needsSharedDisk)
	}

	err = s.overrideSpawnErr(err, thisDebugCount)
	if err != nil {
		s.handleFailedSpawn(ctx, server, flavor, failMsg, err, usingQuotaCB)

		return
	}

	s.recordSpawnedServer(ctx, server, flavor)
}

// overrideSpawnErr applies the failFirstSpawn debugEffect and the cleanedUp
// check, which can force a spawn to be treated as failed.
func (s *opst) overrideSpawnErr(err error, thisDebugCount int) error {
	if debugEffect == "failFirstSpawn" && thisDebugCount == 1 {
		err = errDebugForcedFail
	}

	if s.cleanedUp() {
		err = errServerNotNeeded
	}

	return err
}

// recordSpawnedServer marks the successfully spawned server as usable, clearing
// any prior failed-flavor marker.
func (s *opst) recordSpawnedServer(ctx context.Context, server *cloud.Server, flavor *cloud.Flavor) {
	if _, failed := s.ffCache.Get(flavor.ID); failed {
		clog.Debug(ctx, "server successfully spawned on previously failed flavor")
		s.ffCache.Delete(flavor.ID)
	}

	s.serversMutex.Lock()
	s.spawnedServers[server.ID] = server
	s.serversMutex.Unlock()
	clog.Debug(ctx, "server became usable")
}

// reserveSpawnResources records that we're going to use up some of our quota
// spawning a server of the given flavor, and returns a callback that releases
// that reservation (only once, however many times it is called).
//
// Because spawning can take a while, we reserve up front and release as soon as
// the spawn request goes through (and so presumably is using up quota), but
// before the new server powers up, or we'll end up double-counting resource
// usage in checkQuota(), since that takes into account resources used by an
// in-progress spawn.
func (s *opst) reserveSpawnResources(flavor *cloud.Flavor, disk int, volumeAffected bool) func() {
	s.resourceMutex.Lock()
	s.reservedInstances++
	s.reservedCores += flavor.Cores

	s.reservedRAM += flavor.RAM
	if volumeAffected {
		s.reservedVolume += disk
	}
	s.resourceMutex.Unlock()

	var releaseReservedOnce sync.Once

	return func() {
		releaseReservedOnce.Do(func() {
			s.resourceMutex.Lock()
			s.reservedInstances--
			s.reservedCores -= flavor.Cores

			s.reservedRAM -= flavor.RAM
			if volumeAffected {
				s.reservedVolume -= disk
			}
			s.resourceMutex.Unlock()
		})
	}
}

// doSpawn asks the provider to spawn a server, returning it (or nil), the
// failure message to use if a later step fails, and any spawn error.
func (s *opst) doSpawn(ctx context.Context, req *Requirements, flavor *cloud.Flavor, requestedOS, cmd string,
	usingQuotaCB func(), thisDebugCount int,
) (*cloud.Server, string, error) {
	clog.Debug(ctx, "will spawn new server", "cmd", cmd)

	tSpawn := time.Now()

	var (
		server *cloud.Server
		err    error
	)

	if debugEffect == "failBeforeUsingQuota" && thisDebugCount == 1 {
		err = errDebugFailBeforeUsingQuota
	} else {
		server, err = s.provider.Spawn(ctx, requestedOS, s.osUserForReq(req), flavor.ID, req.Disk,
			s.config.ServerKeepTime, false, usingQuotaCB)
	}

	serverID := "failed"
	if server != nil {
		serverID = server.ID
	}

	clog.Debug(clog.ContextWithServerID(ctx, serverID), "spawned server", "took", time.Since(tSpawn))

	return server, "server failed spawn", err
}

// osUserForReq returns the login username to use for a server spawned for req:
// the Requirements.Other["cloud_user"] value if set, else the configured
// OSUser.
func (s *opst) osUserForReq(req *Requirements) string {
	if val, defined := req.Other["cloud_user"]; defined {
		return val
	}

	return s.config.OSUser
}

// prepareServer waits for a freshly spawned server to become ready, mounts a
// shared disk if needed, ensures the cmd's exe is present, and runs any
// post-creation forced command. It returns the failure message to log and any
// error encountered.
func (s *opst) prepareServer(ctx context.Context, server *cloud.Server, cmd, requestedConfigFiles string,
	requestedScript []byte, needsSharedDisk bool,
) (string, error) {
	if s.config.PreDestroyScript != nil {
		server.SetDestroyScript(s.config.PreDestroyScript)
	}

	if err := s.waitServerReady(ctx, server, cmd, requestedConfigFiles, requestedScript, needsSharedDisk); err != nil {
		return "server failed ready", err
	}

	failMsg := "server failed uploads"
	err := s.ensureExeOnServer(ctx, server, cmd)

	if err == nil && s.config.PostCreationForcedCommand != "" {
		err = s.actOnServerIfNeeded(ctx, server, cmd, func(ctx context.Context) error {
			_, _, errRun := server.RunCmd(ctx, s.config.PostCreationForcedCommand, false)

			return errRun
		})
	}

	return failMsg, err
}

// waitServerReady waits until the server has booted, ssh is ready and its
// osScript has completed, then mounts a shared disk if needed.
func (s *opst) waitServerReady(ctx context.Context, server *cloud.Server, cmd, requestedConfigFiles string,
	requestedScript []byte, needsSharedDisk bool,
) error {
	// wait until boot is finished, ssh is ready and osScript has completed
	clog.Debug(ctx, "waiting for server to become ready")

	tReady := time.Now()
	err := s.actOnServerIfNeeded(ctx, server, cmd, func(ctx context.Context) error {
		return server.WaitUntilReady(ctx, requestedConfigFiles, requestedScript)
	})
	clog.Debug(ctx, "waited for server to become ready", "took", time.Since(tReady), "err", err)

	if err == nil && needsSharedDisk {
		s.serversMutex.RLock()
		localhostIP := s.servers[localhostName].IP
		s.serversMutex.RUnlock()
		err = s.actOnServerIfNeeded(ctx, server, cmd, func(ctx context.Context) error {
			return server.MountSharedDisk(ctx, localhostIP)
		})
	}

	return err
}

// ensureExeOnServer checks that the exe of the cmd we're supposed to run exists
// on the new server, and if not, copies it over.
//
// *** this is just a hack to get wr working, need to think of a better way of
// doing this...
func (s *opst) ensureExeOnServer(ctx context.Context, server *cloud.Server, cmd string) error {
	return s.ensureExeOnRemoteServer(ctx, server.ID, server, cmd)
}

func (s *opst) ensureExeOnRemoteServer(ctx context.Context, serverID string, server exeServer, cmd string) error {
	exe, err := commandExecutable(cmd)
	if err != nil {
		return err
	}

	present, err := s.remoteExeTokenResolves(ctx, serverID, server, cmd, exe)
	if err != nil || present {
		return err
	}

	return s.ensureExePathOnRemoteServer(ctx, serverID, server, cmd, exe)
}

func commandExecutable(cmd string) (string, error) {
	tokens, err := shellquote.Split(cmd)
	if err != nil {
		return "", fmt.Errorf("could not parse command executable [%s]: %w", cmd, err)
	}

	if len(tokens) == 0 || tokens[0] == "" {
		return "", fmt.Errorf("could not parse command executable [%s]: %w", cmd, errCommandHasNoExe)
	}

	return tokens[0], nil
}

func (s *opst) remoteExeTokenResolves(
	ctx context.Context, serverID string, server exeServer, cmd, exe string,
) (bool, error) {
	if strings.Contains(exe, "/") {
		return false, nil
	}

	present, err := s.exeTokenPresentOnServer(ctx, serverID, server, cmd, exe)
	if err == nil || errors.Is(err, errServerNotNeeded) {
		return present, err
	}

	return false, fmt.Errorf("could not resolve exe with [command -v -- %s]: %w", exe, err)
}

func (s *opst) ensureExePathOnRemoteServer(
	ctx context.Context, serverID string, server exeServer, cmd, exe string,
) error {
	exePath, err := exec.LookPath(exe)
	if err != nil {
		return fmt.Errorf("could not look for exe [%s]: %w", exe, err)
	}

	present, err := s.exePathPresentOnServer(ctx, serverID, server, cmd, exePath)
	if err != nil {
		if errors.Is(err, errServerNotNeeded) {
			return err
		}

		return fmt.Errorf("could not check exe with [test -x %s]: %w", exePath, err)
	}

	if !present {
		return s.uploadExe(ctx, serverID, server, cmd, exePath)
	}

	return nil
}

func (s *opst) exeTokenPresentOnServer(ctx context.Context, serverID string, server exeServer, cmd, exe string) (
	bool, error,
) {
	return s.exeCheckOnServer(ctx, serverID, server, cmd, remoteExeTokenCheckCmd(exe))
}

func remoteExeTokenCheckCmd(exe string) string {
	return fmt.Sprintf("if %s >/dev/null 2>&1; then %s; else %s; fi",
		shellquote.Join("command", "-v", "--", exe),
		shellquote.Join("printf", "%s", remoteExePresent),
		shellquote.Join("printf", "%s", remoteExeMissing),
	)
}

// exePathPresentOnServer checks whether exePath exists and is executable on the
// server using POSIX shell builtins.
func (s *opst) exePathPresentOnServer(ctx context.Context, serverID string, server exeServer, cmd, exePath string) (
	bool, error,
) {
	return s.exeCheckOnServer(ctx, serverID, server, cmd, remoteExePathCheckCmd(exePath))
}

func remoteExePathCheckCmd(exePath string) string {
	return fmt.Sprintf("if %s; then %s; else %s; fi",
		shellquote.Join("test", "-x", exePath),
		shellquote.Join("printf", "%s", remoteExePresent),
		shellquote.Join("printf", "%s", remoteExeMissing),
	)
}

func (s *opst) exeCheckOnServer(ctx context.Context, serverID string, server exeServer, cmd, checkCmd string) (
	bool, error,
) {
	stdout := ""

	err := s.actOnServerIDIfNeeded(ctx, serverID, cmd, func(ctx context.Context) error {
		std, _, errRun := server.RunCmd(ctx, checkCmd, false)
		stdout = strings.TrimSpace(std)

		return errRun
	})
	if err != nil {
		return false, err
	}

	switch stdout {
	case remoteExePresent:
		return true, nil
	case remoteExeMissing:
		return false, nil
	default:
		return false, fmt.Errorf("%w: %q", errUnexpectedExeCheck, stdout)
	}
}

// uploadExe uploads exePath to the same path on server and makes it executable.
//
// *** NB the upload will fail if exePath is in a dir we can't create on the
// remote server, eg. if it is in our home dir, but the remote server has a
// different user, or presumably if it is somewhere requiring root permission.
func (s *opst) uploadExe(ctx context.Context, serverID string, server exeServer, cmd, exePath string) error {
	err := s.actOnServerIDIfNeeded(ctx, serverID, cmd, func(ctx context.Context) error {
		return server.UploadFile(ctx, exePath, exePath)
	})
	if err != nil {
		if !errors.Is(err, errServerNotNeeded) {
			return fmt.Errorf("could not upload exe [%s]: %w (try putting the exe in /tmp?)", exePath, err)
		}

		return err
	}

	return s.actOnServerIDIfNeeded(ctx, serverID, cmd, func(ctx context.Context) error {
		_, _, errRun := server.RunCmd(ctx, shellquote.Join("chmod", "u+x", exePath), false)

		return errRun
	})
}

// handleFailedSpawn deals with a Spawn() or upload-of-exe error by destroying
// the server and noting we failed.
func (s *opst) handleFailedSpawn(ctx context.Context, server *cloud.Server, flavor *cloud.Flavor,
	failMsg string, err error, usingQuotaCB func(),
) {
	usingQuotaCB()

	if errors.Is(err, errServerNotNeeded) {
		clog.Debug(ctx, failMsg, "err", err)
	} else {
		clog.Warn(ctx, failMsg, "err", err)
	}

	s.destroyOrMarkFailedFlavor(ctx, server, flavor, err)

	if !errors.Is(err, errServerNotNeeded) {
		s.notifyMessage(fmt.Sprintf("OpenStack: Failed to create a usable server: %s", err))
	}
}

// destroyOrMarkFailedFlavor destroys the server if it exists, or otherwise
// marks its flavor as failed if the spawn failed due to lack of hardware.
func (s *opst) destroyOrMarkFailedFlavor(ctx context.Context, server *cloud.Server, flavor *cloud.Flavor, err error) {
	if server != nil {
		if errd := server.Destroy(ctx); errd != nil {
			clog.Debug(ctx, "server also failed to destroy", "err", errd)
		}

		return
	}

	if s.provider != nil && s.provider.ErrIsNoHardware(err) {
		s.ffCache.Set(flavor.ID, true, cache.DefaultExpiration)
		clog.Warn(ctx, "server failed to spawn due to lack of hardware")
	}
}

// nextDebugCount increments and returns the global debug counter when a
// debugEffect is active, else returns 0.
func nextDebugCount() int {
	if debugEffect == "" {
		return 0
	}

	debugCounter++

	return debugCounter
}

// actOnServerIfNeeded runs the given code unless cleanup() has been called, or
// cmd no longer needs to be run, in which case an error is returned instead.
// It will also periodiclly check if the cmd still needs to be run, and return
// early with an error if not, even while the given code is still running.
//
// The given ctx is used for logging only; the code is deliberately run with a
// fresh context that is NOT derived from ctx, so that an in-progress spawn
// action is not aborted just because the scheduling context that triggered it
// is cancelled (spawns are meant to run to completion).
func (s *opst) actOnServerIfNeeded(ctx context.Context, server *cloud.Server, cmd string,
	code func(ctx context.Context) error,
) error {
	return s.actOnServerIDIfNeeded(ctx, server.ID, cmd, code)
}

func (s *opst) actOnServerIDIfNeeded(
	ctx context.Context, serverID, cmd string, code func(ctx context.Context) error,
) error {
	if s.cleanedUp() {
		return errServerNotNeeded
	}

	// actionCtx is intentionally detached from ctx so spawns run to completion
	// (see method doc).
	actionCtx, cancel := context.WithCancel(context.Background())

	canceller := s.registerSpawnCanceller(serverID, cmd)

	defer func() {
		cancel()
		s.deregisterSpawnCanceller(serverID, cmd)
	}()

	if s.cmdCountRemaining(cmd) <= 0 {
		clog.Debug(ctx, "bailing on a spawn early since no longer needed", "server", serverID)

		return errServerNotNeeded
	}

	return runSpawnAction(ctx, actionCtx, cancel, canceller, code, serverID)
}

// runSpawnAction runs code(actionCtx) in a goroutine, returning its error, or
// returning errServerNotNeeded (and cancelling actionCtx) if canceller fires
// first.
func runSpawnAction(ctx, actionCtx context.Context, cancel context.CancelFunc, canceller <-chan struct{},
	code func(ctx context.Context) error, serverID string,
) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- code(actionCtx)
	}()

	select {
	case err := <-errCh:
		return err
	case <-canceller:
		cancel()
		clog.Debug(ctx, "bailing on a spawn mid-action since no longer needed", "server", serverID)

		return errServerNotNeeded
	}
}

// registerSpawnCanceller records a canceller channel for the given cmd/server
// so that cmdNotNeeded can cancel an in-progress actOnServerIfNeeded, and
// returns that channel.
func (s *opst) registerSpawnCanceller(serverID string, cmd string) chan struct{} {
	s.scMutex.Lock()
	defer s.scMutex.Unlock()

	canceller := make(chan struct{}, 1)
	if _, exists := s.spawnCanceller[cmd]; !exists {
		s.spawnCanceller[cmd] = make(map[string]chan struct{})
	}

	s.spawnCanceller[cmd][serverID] = canceller

	return canceller
}

// deregisterSpawnCanceller removes the canceller channel registered for the
// given cmd/server.
func (s *opst) deregisterSpawnCanceller(serverID string, cmd string) {
	s.scMutex.Lock()
	defer s.scMutex.Unlock()

	delete(s.spawnCanceller[cmd], serverID)
}

// cmdNotNeeded cancels the context set by actOnServerIfNeeded(), if any.
func (s *opst) cmdNotNeeded(cmd string) {
	s.scMutex.Lock()
	defer s.scMutex.Unlock()

	if serverMap, exists := s.spawnCanceller[cmd]; exists {
		delete(s.spawnCanceller, cmd)

		for _, canceller := range serverMap {
			close(canceller)
		}
	}
}

// runCmd runs the command on next available server. NB: we only return an error
// if we can't start the cmd, not if the command fails (schedule() only
// guarantees that the cmds are run count times, not that they are /successful/
// that many times).
func (s *opst) runCmd(ctx context.Context, cmd string, req *Requirements, reservedCh chan bool) error {
	requestedOS, requestedScript, requestedConfigFiles, requestedFlavor, needsSharedDisk, err := s.serverReqs(ctx, req)
	if err != nil {
		return err
	}

	if s.cleanedUp() {
		reservedCh <- false

		return nil
	}

	server := s.pickServer(ctx, req, reservedCh, requestedOS, requestedScript, requestedConfigFiles,
		requestedFlavor, needsSharedDisk)
	if server == nil {
		reservedCh <- false

		return errNoAvailableServer
	}

	return s.runOnServerAndRelease(ctx, server, cmd, req)
}

// runOnServerAndRelease runs cmd on the given (already allocated) server, then
// releases its resources so the local scheduler will trigger a new
// processQueue() call.
func (s *opst) runOnServerAndRelease(ctx context.Context, server *cloud.Server, cmd string, req *Requirements) error {
	// later, after we've run the command, this server will be available for
	// another; release resources, and local scheduler will trigger a new
	// processQueue() call
	defer func() {
		if !server.Destroyed() && server.PermanentProblem() == "" {
			server.Release(ctx, req.Cores, req.RAM, req.Disk)
		}
	}()

	err := s.runCmdOnServer(ctx, server, cmd, req)
	if err == nil {
		clog.Debug(ctx, "ran command", "cmd", cmd)
	} else {
		clog.Warn(ctx, "failed to run command", "cmd", cmd, "err", err)
	}

	return err
}

// pickServer looks through space on existing servers to find one we can run cmd
// on, allocating resources on it and signalling reservedCh if found. Returns
// nil if no suitable server has space.
func (s *opst) pickServer(ctx context.Context, req *Requirements, reservedCh chan bool, requestedOS string,
	requestedScript []byte, requestedConfigFiles string, requestedFlavor *cloud.Flavor, needsSharedDisk bool,
) *cloud.Server {
	s.serversMutex.RLock()
	defer s.serversMutex.RUnlock()

	for sid, thisServer := range s.servers {
		if thisServer.IsBad() {
			continue
		}

		matches := thisServer.Matches(requestedOS, requestedScript, requestedConfigFiles, requestedFlavor,
			needsSharedDisk)
		if !matches || !thisServer.Allocate(ctx, req.Cores, req.RAM, req.Disk) {
			continue
		}

		s.signalReserved(ctx, reservedCh, sid)
		clog.Debug(ctx, "picked server")

		return thisServer
	}

	return nil
}

// signalReserved sends true on reservedCh, guarding against getting stuck.
//
// *** reservedCh is buffered and sending on it should never block, but somehow
// we have gotten stuck here before; make sure we don't get stuck on this send.
func (s *opst) signalReserved(ctx context.Context, reservedCh chan bool, sid string) {
	ch := make(chan bool, 1)
	done := make(chan bool, 1)

	go func() {
		reservedCh <- true

		done <- true

		ch <- true
	}()
	go func() {
		select {
		case <-time.After(reserveChTimeout):
			ch <- false
		case <-done:
			return
		}
	}()

	if sentReserved := <-ch; !sentReserved {
		clog.Warn(ctx, "failed to send on reservedCh", "server", sid)
	}
}

// runCmdOnServer ssh's over to the given server (or runs locally if it's the
// localhost) and runs cmd on it, marking the server bad if a remote run errors.
func (s *opst) runCmdOnServer(ctx context.Context, server *cloud.Server, cmd string, req *Requirements) error {
	if server.Name == localhostName {
		clog.Debug(ctx, "running command locally", "cmd", cmd)

		reserved := make(chan bool)
		go func() {
			<-reserved
		}()

		return s.local.runCmd(ctx, cmd, req, reserved)
	}

	if s.config.Umask > 0 {
		cmd = fmt.Sprintf("(umask %d && %s)", s.config.Umask, cmd)
	}

	clog.Debug(ctx, "running command remotely", "cmd", cmd)

	_, _, err := server.RunCmd(ctx, cmd, false)
	// if we got an error running the command, we won't use this server again
	if err != nil && !server.Destroyed() {
		// tell the user about why we're not using this server, but don't just
		// Destroy it: let them investigate the server manually if they wish,
		// and let them Destroy when they wish.
		server.GoneBad(err.Error())
		s.notifyBadServer(server)
		clog.Warn(ctx, "server went bad, won't be used again")
	}

	return err
}

// stateUpdate checks all our servers are really alive, and adds newly spawned
// servers to the map that runCmd will check.
func (s *opst) stateUpdate(ctx context.Context) {
	s.stateMutex.Lock()
	defer s.stateMutex.Unlock()

	s.promoteSpawnedServers(ctx)

	servers := s.aliveServers()

	if s.updatingState {
		return
	}

	s.updatingState = true

	// stateUpdate must return quickly, but checking on the servers with the
	// Alive() call can take too long, so we do the rest in a goroutine
	go func() {
		defer internal.LogPanic(ctx, "stateUpdate", true)

		for _, server := range servers {
			s.refreshServerState(ctx, server)
		}

		s.stateMutex.Lock()
		defer s.stateMutex.Unlock()

		s.updatingState = false
	}()
}

// promoteSpawnedServers moves servers that spawn() has finished creating into
// the servers map that runCmd will check.
//
// When spawn() has finished creating a server and it is usable, it doesn't
// immediately add it to s.servers, since if this happens during a
// processQueue() call then we could break bin-packing, with low priority jobs
// getting allocated to the new server. Instead spawn() adds the new server to
// spawnedServers, and we move them to servers here, since stateUpdate is called
// once at the start of processQueue().
func (s *opst) promoteSpawnedServers(ctx context.Context) {
	s.serversMutex.Lock()
	defer s.serversMutex.Unlock()

	for id, server := range s.spawnedServers {
		s.servers[id] = server
		delete(s.spawnedServers, id)
		clog.Debug(ctx, "made server eligible for use", "id", id)
	}
}

// aliveServers returns the servers that have an ID and aren't destroyed,
// removing any destroyed servers from the servers map as it goes.
func (s *opst) aliveServers() []*cloud.Server {
	s.serversMutex.Lock()
	defer s.serversMutex.Unlock()

	var servers []*cloud.Server

	for _, server := range s.servers {
		if server.ID == "" {
			continue
		}

		if server.Destroyed() {
			delete(s.servers, server.ID)

			continue
		}

		servers = append(servers, server)
	}

	return servers
}

// refreshServerState checks whether the given server is alive, transitioning it
// between good and bad as appropriate.
func (s *opst) refreshServerState(ctx context.Context, server *cloud.Server) {
	if server.Destroyed() {
		return
	}

	alive := server.Alive(ctx, true)

	if !server.IsBad() {
		if !alive {
			server.GoneBad()
			s.notifyBadServer(server)
			clog.Debug(ctx, "server went bad", "server", server.ID)
		}

		return
	}

	// check if the server is fine now
	if alive && server.PermanentProblem() == "" {
		if worked := server.NotBad(); worked {
			s.notifyBadServer(server)
			clog.Debug(ctx, "server became good", "server", server.ID)
		}
	}
}

// postProcess checks that all our newly spawned servers have been used, and if
// not, initiates the countdown to their destruction.
func (s *opst) postProcess(ctx context.Context) {
	s.serversMutex.Lock()
	for _, server := range s.servers {
		if server.Name != localhostName && !server.Used() {
			clog.Debug(ctx, "placing unused server on deathrow", "server", server.ID)
			server.Allocate(ctx, 0, 1, 1)
			server.Release(ctx, 0, 1, 1)
		}
	}
	s.serversMutex.Unlock()
}

// recover achieves the aims of Recover(). Here we find the given host, and
// start tracking it to know when it is no longer running any of the given cmds
// for it, at which point we destroy it. If the supplied UserName for the host
// is wrong, or we otherwise can't ssh to it, the host will be destroyed
// immediately. NB: the host checking only works on machines with the 'pgrep'
// command, such as linux etc.
func (s *opst) recover(ctx context.Context, cmd string, _ *Requirements, host *RecoveredHostDetails) error {
	server, monitor := s.serverToMonitor(ctx, host)
	if !monitor {
		return nil
	}

	s.rsMutex.Lock()
	defer s.rsMutex.Unlock()

	if s.recoveredServers[host.Host] {
		return nil
	}

	go s.monitorRecoveredServer(ctx, server, recoverCheckCmd(cmd), host.TTD)

	s.recoveredServers[host.Host] = true

	return nil
}

// serverToMonitor finds the server for the given recovered host and reports
// whether it needs periodic monitoring. A server that doesn't exist, is kept
// forever (TTD 0), or isn't reachable (and so gets destroyed) doesn't need
// monitoring.
func (s *opst) serverToMonitor(ctx context.Context, host *RecoveredHostDetails) (*cloud.Server, bool) {
	server := s.provider.GetServerByName(host.Host)
	if server == nil {
		clog.Warn(ctx, "recover called for non-existent server", "host", host)

		return nil, false
	}

	if host.TTD == 0 {
		// we keep servers for ever, so no need to monitor it
		return nil, false
	}

	server.UserName = host.UserName
	if !server.Alive(ctx, true) {
		clog.Warn(ctx, "recover called for server that is not alive (or username was wrong?)", "host", host.Host)

		errd := server.Destroy(ctx)
		if errd != nil {
			clog.Warn(ctx, "recovered server destruction failed", "server", server.ID, "err", errd)
		}

		return nil, false
	}

	return server, true
}

// recoverCheckCmd returns the command string to pgrep for when checking whether
// a recovered server is still running jobs.
//
// *** we will only check against the first 2 words of cmd, which for our
// purposes of wr will be 'wr runner'. This lets us do a single check per
// server, and reduces possible issues with trying to get a process name match
// on a long, complex command line. However it might not work properly with the
// arbitrary commands that people could in theory schedule.
func recoverCheckCmd(cmd string) string {
	cmdSplit := strings.Split(cmd, " ")

	checkCmd := cmdSplit[0]
	if len(cmdSplit) > 1 {
		checkCmd += " " + cmdSplit[1]
	}

	return checkCmd
}

// monitorRecoveredServer periodically checks on a recovered server; when it is
// no longer running cmd, it destroys it and recalls processQueue. It stops when
// stopRSMonitoring is closed.
func (s *opst) monitorRecoveredServer(ctx context.Context, server *cloud.Server, cmd string, ttd time.Duration) {
	defer internal.LogPanic(ctx, "recover", true)

	clog.Debug(ctx, "recovered server will be checked for running jobs periodically", "server", server.ID)

	ticker := time.NewTicker(ttd)

	for {
		select {
		case <-ticker.C:
			if s.recoveredServerActive(ctx, server, cmd) {
				continue
			}

			ticker.Stop()
			s.destroyRecoveredServer(ctx, server)

			return
		case <-s.stopRSMonitoring:
			ticker.Stop()

			return
		}
	}
}

// recoveredServerActive reports whether the recovered server is still running
// cmd.
func (s *opst) recoveredServerActive(ctx context.Context, server *cloud.Server, cmd string) bool {
	so, se, errr := server.RunCmd(ctx, "pgrep -f '"+cmd+"'", false)
	if errr != nil {
		// *** assume the error is because a process with cmd doesn't exist, not
		// because prgrep failed for some other reason
		clog.Debug(ctx, "recovered server is no longer running anything", "server", server.ID, "checkCmd",
			"pgrep -f '"+cmd+"'", "stdout", so, "stderr", se, "err", errr)

		return false
	}

	return true
}

// destroyRecoveredServer destroys a recovered server that has gone idle and
// recalls processQueue.
func (s *opst) destroyRecoveredServer(ctx context.Context, server *cloud.Server) {
	errd := server.Destroy(ctx)
	if errd != nil {
		clog.Warn(ctx, "recovered server destruction failed", "server", server.ID, "err", errd)
	} else {
		clog.Debug(ctx, "recovered server was destroyed after going idle", "server", server.ID)
	}

	errp := s.processQueue(ctx, "openstack recover")
	if errp != nil {
		clog.Error(ctx, "processQueue call after recovery failed", "err", errp)
	}
}

// hostToID does the necessary lookup to convert hostname to instance id.
func (s *opst) hostToID(host string) string {
	server := s.provider.GetServerByName(host)
	if server == nil {
		return ""
	}

	return server.ID
}

// getHost returns a cloud.Server for the given host.
func (s *opst) getHost(host string) (Host, bool) {
	server := s.provider.GetServerByName(host)
	if server == nil {
		return nil, false
	}

	return server, true
}

// setMessageCallBack sets the given callback.
func (s *opst) setMessageCallBack(_ context.Context, cb MessageCallBack) {
	s.cbmutex.Lock()
	defer s.cbmutex.Unlock()

	s.msgCB = cb
}

// notifyMessage calls the message callback with the given message in a
// goroutine, if that callback has been set.
func (s *opst) notifyMessage(msg string) {
	s.cbmutex.RLock()
	defer s.cbmutex.RUnlock()

	if s.msgCB != nil {
		go s.msgCB(msg)
	}
}

// setBadServerCallBack sets the given callback.
func (s *opst) setBadServerCallBack(_ context.Context, cb BadServerCallBack) {
	s.cbmutex.Lock()
	defer s.cbmutex.Unlock()

	s.badServerCB = cb
}

// notifyBadServer calls the bad server callback with the given server in a
// goroutine, if that callback has been set.
func (s *opst) notifyBadServer(server *cloud.Server) {
	s.cbmutex.RLock()
	defer s.cbmutex.RUnlock()

	if s.badServerCB != nil {
		go s.badServerCB(server)
	}
}

// cleanup destroys our internal queues and brings down our servers.
func (s *opst) cleanup(ctx context.Context) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.runMutex.Lock()
	defer s.runMutex.Unlock()

	s.cleanMutex.Lock()
	defer s.cleanMutex.Unlock()

	s.spawnMutex.Lock()
	defer s.spawnMutex.Unlock()

	// prevent any further scheduling and queue processing, and destroy the
	// queue
	s.cleaned = true

	if err := s.queue.Destroy(); err != nil {
		clog.Warn(ctx, "cleanup queue destruction failed", "err", err)
	}

	// wait for any ongoing state update to complete, then keep stateMutex held
	// so none can start while we destroy our servers
	s.stateMutex.Lock()
	for s.updatingState {
		s.stateMutex.Unlock()
		<-time.After(cleanupStatePollFreq)
		s.stateMutex.Lock()
	}

	defer s.stateMutex.Unlock()

	s.destroyServersAndTeardown(ctx)
}

// destroyServersAndTeardown brings down all our servers and tears down any
// created cloud resources.
func (s *opst) destroyServersAndTeardown(ctx context.Context) {
	s.serversMutex.Lock()
	defer s.serversMutex.Unlock()

	close(s.stopRSMonitoring)
	s.destroyAllSpawnedServers(ctx)

	// teardown any cloud resources created
	err := s.provider.TearDown(ctx)
	if err != nil && !strings.Contains(err.Error(), "nothing to tear down") {
		clog.Warn(ctx, "cleanup teardown failed", "err", err)
	}
}

// destroyAllSpawnedServers promotes any pending spawned servers and destroys
// all servers except the localhost. The caller must hold serversMutex.
func (s *opst) destroyAllSpawnedServers(ctx context.Context) {
	for id, server := range s.spawnedServers {
		s.servers[id] = server
		delete(s.spawnedServers, id)
	}

	for sid, server := range s.servers {
		if sid == localhostName {
			continue
		}

		errd := server.Destroy(ctx)
		if errd != nil {
			clog.Warn(ctx, "cleanup server destruction failed", "server", server.ID, "err", errd)
		}

		delete(s.servers, sid)
	}
}
