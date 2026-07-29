/*******************************************************************************
 * Copyright (c) 2016-2026 Genome Research Ltd.
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

package scheduler

// This file contains a scheduleri implementation for 'lsf': running jobs
// via IBM's (ne Platform's) Load Sharing Facility.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/bsubresource"
	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/mattn/go-shellwords"
)

// scanBufferSize is used when scanning bjobs -w output. The default buffer size
// is 65536, but bjob names can be much bigger, so we allow for a larger buffer.
const scanBufferSize = 1000 * bufio.MaxScanTokenSize

const ErrInvalidBsubOpts = "invalid lsf bsub options"

// errBadLSFConfig is the Error message used when initialize() is not given a
// *ConfigLSF.
const errBadLSFConfig = "SchedulerConfig must be *ConfigLSF"

// queue criteria keys, used both as map keys when ranking queues and when
// applying default values.
const (
	criterionHosts     = "hosts"
	criterionChunkSize = "chunk_size"
	criterionNumUsers  = "num_users"
	criterionMax       = "max"
	criterionMaxUser   = "max_user"
	criterionMemlimit  = "memlimit"
	criterionRunlimit  = "runlimit"
	criterionUsers     = "users"
)

// memLimitMultiplier values for the different units that LSF_UNIT_FOR_LIMITS can
// report memlimit (bsub -M) values in. 'K' (KB) is our default.
const (
	kbMemLimitMultiplier float32 = 1000
	gbMemLimitMultiplier float32 = 0.001
)

// op* are the Op names used in scheduler Errors raised by the named methods.
const (
	opParseBjobs     = "parseBjobs"
	opDetermineQueue = "determineQueue"
	opParseUserArgs  = "parseUserArgs"
)

const (
	// reMatchOneGroup is the length of a FindStringSubmatch result for a regex
	// with a single capturing group (the full match plus the group).
	reMatchOneGroup = 2

	// reMatchTwoGroups is the length of a FindStringSubmatch result for a regex
	// with two capturing groups.
	reMatchTwoGroups = 3

	// secsPerMinute is the number of seconds in a minute.
	secsPerMinute = 60

	// manyUsers is the count used for a queue usable by everyone.
	manyUsers = 10000

	// oneYearInSeconds is the default runlimit used for queues that don't
	// specify one.
	oneYearInSeconds = 31536000

	// hugeQueueLimit is a "very large" default for queue criteria that have no
	// real limit, so they don't unduly affect queue ranking.
	hugeQueueLimit = 10000000

	// bjobsMinJobLineLen is the minimum length of a bjobs -w line for a
	// submitted job to be considered as having appeared in bjobs output.
	bjobsMinJobLineLen = 46

	// bjobsMinFields is the minimum number of whitespace-separated fields a
	// bjobs -w output line must have for us to parse the job id, status and
	// name from it.
	bjobsMinFields = 7

	// bmgroupMinFields is the minimum number of whitespace-separated fields a
	// bmgroup -w output line must have (a group name plus at least one host).
	bmgroupMinFields = 2

	// bjobsAppearTimeout is how long, after a successful bsub, we wait for the
	// submitted job to appear in bjobs output before giving up.
	bjobsAppearTimeout = 10 * time.Second

	// bjobsAppearPollFreq is how often we poll bjobs while waiting for a
	// submitted job to appear.
	bjobsAppearPollFreq = 100 * time.Millisecond

	// defaultMaxBsubArraySize is the conservative default cap on the number of
	// elements wr puts in a single bsub job array (the common LSF MAX_JOB_ARRAY
	// default). See maxBsubArraySize.
	defaultMaxBsubArraySize = 1000

	// defaultBsubExecTimeout is the default bound on how long a single bsub exec
	// may take before it is killed and turned into a retryable error. See
	// bsubExecTimeout.
	defaultBsubExecTimeout = 5 * time.Minute

	// bsubKillGracePeriod is how long, after the bsub exec timeout kills bsub,
	// we wait before force-closing its output pipes so the exec returns even if
	// a child process is still holding them open.
	bsubKillGracePeriod = 2 * time.Second
)

// maxBsubArraySize caps the number of elements wr places in a single bsub job
// array (-J name[1-N]). A same-requirement batch larger than this is split
// across several arrays submitted in one scheduling pass, so an oversized
// single array (which some LSF installations accept but then hang on for
// minutes) is never emitted. It is a package var so tests can lower it.
var maxBsubArraySize = defaultMaxBsubArraySize //nolint:gochecknoglobals

// bsubExecTimeout bounds how long a single bsub invocation may run before it is
// killed, turning a hung/pathological bsub into a logged, retryable error
// rather than a silent indefinite block. It is deliberately applied via a
// context derived from context.Background() (not the scheduling context), so
// scheduling-context cancellation cannot abort an in-flight submission. It is a
// package var so tests can lower it.
var bsubExecTimeout = defaultBsubExecTimeout //nolint:gochecknoglobals

// lsfHost adapts the throwaway cloud.Server that lsf.getHost dials for a single
// confirm-dead ssh command so that Close() actually drops that server's ssh
// connection. Each getHost call makes a FRESH server; without closing it the
// dialled ssh client (its background goroutines and socket) would leak per check.
// (cloud.Server's own Close is a deliberate no-op, correct for the cached, shared
// servers cloud schedulers reuse, so a throwaway server needs this wrapper.)
type lsfHost struct {
	server *cloud.Server
}

// RunCmd runs the command on the underlying throwaway server.
func (h *lsfHost) RunCmd(ctx context.Context, cmd string, background bool) (stdout, stderr string, err error) {
	return h.server.RunCmd(ctx, cmd, background)
}

// Close drops the throwaway server's ssh connection so it does not leak.
func (h *lsfHost) Close(ctx context.Context) {
	h.server.CloseSSHConnections(ctx)
}

// lsf is our implementer of scheduleri.
type lsf struct {
	config             *ConfigLSF
	months             map[string]int
	dateRegex          *regexp.Regexp
	bsubRegex          *regexp.Regexp
	memLimitMultiplier float32
	queues             map[string]map[string]int
	sortedqs           []string
	bsubExe            string
	bjobsExe           string
	bkillExe           string
	privateKey         string
	// reservedElements holds the scheduler element ids (jobid[index] form) that
	// wr has handed a job reservation to, so killExcessCmds never bkills them as
	// excess even before bjobs reports them as RUN. Guarded by reservedMu.
	reservedElements map[string]bool
	reservedMu       sync.Mutex
}

// ConfigLSF represents the configuration options required by the LSF scheduler.
// All are required with no usable defaults.
type ConfigLSF struct {
	// Deployment is one of "development" or "production".
	Deployment string

	// Shell is the shell to use to run the commands to interact with your job
	// scheduler; 'bash' is recommended.
	Shell string

	// PrivateKeyPath is the path to your private key that can be used to ssh
	// to LSF farm nodes to check on jobs if they become non-responsive.
	PrivateKeyPath string
}

// initialize finds out about lsf's hosts and queues.
func (s *lsf) initialize(ctx context.Context, config any) error {
	conf, ok := config.(*ConfigLSF)
	if !ok {
		return Error{lsfScheduler, opInitialize, errBadLSFConfig}
	}

	s.config = conf

	// find the real paths to the main LSF exes, since thanks to wr's LSF
	// compatibility mode, might not be the first in $PATH
	s.bsubExe = internal.Which("bsub")
	s.bjobsExe = internal.Which("bjobs")
	s.bkillExe = internal.Which("bkill")

	s.setupMonthsAndRegexes()

	if err := s.detectMemLimitMultiplier(); err != nil {
		return err
	}

	//nolint:contextcheck // internal.Username manages its own context by design
	highest, err := s.parseBqueues()
	if err != nil {
		return err
	}

	s.rankQueues(highest)

	// if a job becomes lost, scheduler needs to ssh to the host to check on the
	// process, so we store our private key
	s.loadPrivateKey(ctx)

	return nil
}

// loadPrivateKey reads the configured private key (used to ssh to a job's host to
// confirm a lost job's process is really dead) into s.privateKey. A read failure
// is non-fatal - lost-job death-confirmation will simply fail - but when a key
// path was configured it is logged at warn (rather than silently swallowed), so
// an unreadable or mis-pathed key is diagnosable instead of leaving every ssh
// check to fail invisibly.
func (s *lsf) loadPrivateKey(ctx context.Context) {
	if s.config.PrivateKeyPath == "" {
		return
	}

	content, err := os.ReadFile(internal.TildaToHome(s.config.PrivateKeyPath))
	if err != nil {
		clog.Warn(ctx, "could not read the private key needed to confirm lost jobs are dead via ssh",
			"path", s.config.PrivateKeyPath, "err", err)

		return
	}

	s.privateKey = string(content)
}

// setupMonthsAndRegexes sets up what should be global vars, but we don't really
// want these taking up space if the user never uses LSF.
func (s *lsf) setupMonthsAndRegexes() {
	months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

	s.months = make(map[string]int, len(months))
	for i, month := range months {
		s.months[month] = i + 1
	}

	s.dateRegex = regexp.MustCompile(`(\w+)\s+(\d+) (\d+):(\d+):(\d+)`)
	s.bsubRegex = regexp.MustCompile(`^Job <(\d+)>`)
}

// detectMemLimitMultiplier uses lsadmin to see what units memlimit (bsub -M) is
// in, and sets s.memLimitMultiplier accordingly.
func (s *lsf) detectMemLimitMultiplier() error {
	s.memLimitMultiplier = kbMemLimitMultiplier // by default assume it's KB

	//nolint:gosec,noctx // LSF management command; must complete regardless of scheduling ctx cancellation
	cmdout, err := exec.Command(s.config.Shell, "-c", "lsadmin showconf lim | grep LSF_UNIT_FOR_LIMITS").Output()
	if err != nil {
		return Error{
			lsfScheduler, opInitialize,
			fmt.Sprintf("failed to run [lsadmin showconf lim | grep LSF_UNIT_FOR_LIMITS]: %s", err),
		}
	}

	if len(cmdout) == 0 {
		return nil
	}

	uflRegex := regexp.MustCompile(`=\s*(\w)`)

	unit := uflRegex.FindStringSubmatch(string(cmdout))
	if len(unit) == reMatchOneGroup && unit[1] != "" {
		switch unit[1] {
		case "M":
			s.memLimitMultiplier = 1
		case "G":
			s.memLimitMultiplier = gbMemLimitMultiplier
			// 'K' is our default
		}
	}

	return nil
}

// reserved records that the given scheduler element id (an LSF "jobid[index]")
// has been handed a wr job reservation, so killExcessCmds must never bkill it as
// excess, even before bjobs reports it as RUN.
func (s *lsf) reserved(schedulerID string) {
	s.reservedMu.Lock()
	defer s.reservedMu.Unlock()

	if s.reservedElements == nil {
		s.reservedElements = make(map[string]bool)
	}

	s.reservedElements[schedulerID] = true
}

// snapshotReserved returns a copy of the currently reserved element ids, safe to
// read without holding reservedMu.
func (s *lsf) snapshotReserved() map[string]bool {
	s.reservedMu.Lock()
	defer s.reservedMu.Unlock()

	snapshot := make(map[string]bool, len(s.reservedElements))
	for id := range s.reservedElements {
		snapshot[id] = true
	}

	return snapshot
}

// pruneReserved drops any reserved element ids not present in the given full
// snapshot of currently-known LSF element ids (parseBjobs excludes exited
// elements), bounding the reserved set over a long-lived manager.
func (s *lsf) pruneReserved(present map[string]bool) {
	s.reservedMu.Lock()
	defer s.reservedMu.Unlock()

	for id := range s.reservedElements {
		if !present[id] {
			delete(s.reservedElements, id)
		}
	}
}

// bsubStderr returns bsub's stderr, extracted from an error returned by
// exec.Cmd.Output(). When bsub starts but exits non-zero, Output() returns an
// *exec.ExitError whose Stderr field holds the captured stderr (captured because
// submitToQueue leaves Cmd.Stderr nil); that stderr carries the real LSF
// rejection reason. Any other error (e.g. bsub could not be executed at all)
// yields an empty string.
func bsubStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return strings.TrimSpace(string(exitErr.Stderr))
	}

	return ""
}

// bqueuesParser holds the mutable state used while parsing the output of
// `bqueues -l` line by line.
type bqueuesParser struct {
	s                 *lsf
	highest           map[string]int
	bmgroups          map[string]map[string]bool
	queue             string
	nextIsMemlimit    int
	parsedBmgroups    bool
	nextIsPrio        bool
	lookingAtDefaults bool
	nextIsRunlimit    bool

	reQueue            *regexp.Regexp
	rePrio             *regexp.Regexp
	reDefaultLimits    *regexp.Regexp
	reDefaultsFinished *regexp.Regexp
	reMemlimit         *regexp.Regexp
	reNumUnit          *regexp.Regexp
	reRunLimit         *regexp.Regexp
	reParseRunlimit    *regexp.Regexp
	reUserHosts        *regexp.Regexp
	reChunkJobSize     *regexp.Regexp
}

// updateHighest records val as the highest value seen so far for htype.
func (p *bqueuesParser) updateHighest(htype string, val int) {
	if val > p.highest[htype] {
		p.highest[htype] = val
	}
}

// newBqueuesParser creates a bqueuesParser with its regexes compiled and its
// state ready to parse `bqueues -l` output for the given lsf scheduler.
func newBqueuesParser(s *lsf) *bqueuesParser {
	return &bqueuesParser{
		s: s,
		highest: map[string]int{
			criterionRunlimit: 0, criterionMemlimit: 0, criterionMax: 0,
			criterionMaxUser: 0, criterionUsers: 0, criterionHosts: 0,
		},
		bmgroups: make(map[string]map[string]bool),

		reQueue:            regexp.MustCompile(`^QUEUE: (\S+)`),
		rePrio:             regexp.MustCompile(`^PRIO\s+NICE\s+STATUS\s+MAX\s+JL\/U`),
		reDefaultLimits:    regexp.MustCompile(`^DEFAULT LIMITS:`),
		reDefaultsFinished: regexp.MustCompile(`^MAXIMUM LIMITS:|^SCHEDULING PARAMETERS`),
		reMemlimit:         regexp.MustCompile(`MEMLIMIT`),
		reNumUnit:          regexp.MustCompile(`(\d+(?:\.\d+)?) (\w)`),
		reRunLimit:         regexp.MustCompile(`RUNLIMIT`),
		reParseRunlimit:    regexp.MustCompile(`^\s*(\d+)(?:\.\d+)? min`),
		reUserHosts:        regexp.MustCompile(`^(USERS|HOSTS):\s+(.+?)\s*$`),
		reChunkJobSize:     regexp.MustCompile(`^CHUNK_JOB_SIZE:\s+(\d+)`),
	}
}

// parseBqueues parses bqueues -l to figure out what usable queues we have,
// populating s.queues and returning the highest value seen per criterion.
func (s *lsf) parseBqueues() (map[string]int, error) {
	// parse bqueues -l to figure out what usable queues we have
	//nolint:gosec,noctx // LSF management command; must complete regardless of scheduling ctx cancellation
	bqcmd := exec.Command(s.config.Shell, "-c", "bqueues -l")

	bqout, err := bqcmd.StdoutPipe()
	if err != nil {
		return nil, Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to create pipe for [bqueues -l]: %s", err)}
	}

	if err = bqcmd.Start(); err != nil {
		return nil, Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to start [bqueues -l]: %s", err)}
	}

	s.queues = make(map[string]map[string]int)
	p := newBqueuesParser(s)

	bqScanner := bufio.NewScanner(bqout)
	for bqScanner.Scan() {
		if err = p.parseLine(bqScanner.Text()); err != nil {
			return nil, err
		}
	}

	if serr := bqScanner.Err(); serr != nil {
		return nil, Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to read everything from [bqueues -l]: %s", serr)}
	}

	if err = bqcmd.Wait(); err != nil {
		return nil, Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to finish running [bqueues -l]: %s", err)}
	}

	return p.highest, nil
}

// parseLine processes a single line of `bqueues -l` output.
func (p *bqueuesParser) parseLine(line string) error {
	if matches := p.reQueue.FindStringSubmatch(line); len(matches) == reMatchOneGroup {
		p.queue = matches[1]
		p.s.queues[p.queue] = make(map[string]int)

		return nil
	}

	skip, err := p.parseQueueDetail(line)
	if err != nil || skip {
		return err
	}

	if err := p.parseUserHosts(line); err != nil {
		return err
	}

	return p.parseChunkSize(line)
}

// parseQueueDetail handles the prio/limits section of a queue's bqueues -l
// output. It returns skip=true when the rest of the line should not be examined
// (matching the original loop's `continue` behaviour).
func (p *bqueuesParser) parseQueueDetail(line string) (skip bool, err error) {
	switch {
	case p.queue == "":
		return true, nil
	case p.rePrio.MatchString(line):
		p.nextIsPrio = true

		return true, nil
	case p.nextIsPrio:
		return false, p.parsePrio(line)
	case p.reDefaultLimits.MatchString(line):
		p.lookingAtDefaults = true

		return true, nil
	case p.reDefaultsFinished.MatchString(line) || !p.lookingAtDefaults:
		p.lookingAtDefaults = false

		return p.parseLimits(line)
	}

	return false, nil
}

// parsePrio parses the PRIO/MAX/JL/U line for the current queue.
func (p *bqueuesParser) parsePrio(line string) error {
	fields := strings.Fields(line)

	var err error

	p.s.queues[p.queue]["prio"], err = strconv.Atoi(fields[0])
	if err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
	}

	if err := p.parseOptionalLimit(fields[3], criterionMax); err != nil {
		return err
	}

	if err := p.parseOptionalLimit(fields[4], criterionMaxUser); err != nil {
		return err
	}

	p.nextIsPrio = false

	return nil
}

// parseOptionalLimit records field as the value of criterion for the current
// queue, unless field is "-" (meaning unset). field must be an integer.
func (p *bqueuesParser) parseOptionalLimit(field, criterion string) error {
	if field == "-" {
		return nil
	}

	i, err := strconv.Atoi(field)
	if err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
	}

	p.s.queues[p.queue][criterion] = i
	p.updateHighest(criterion, i)

	return nil
}

// parseLimits handles the MEMLIMIT/RUNLIMIT section of a queue's output. It
// returns skip=true when the rest of the line should not be examined.
func (p *bqueuesParser) parseLimits(line string) (skip bool, err error) {
	switch {
	case p.reMemlimit.MatchString(line):
		p.nextIsMemlimit = 0
		for word := range strings.FieldsSeq(line) {
			p.nextIsMemlimit++

			if word == "MEMLIMIT" {
				break
			}
		}

		return true, nil
	case p.nextIsMemlimit > 0:
		return false, p.parseMemlimit(line)
	case p.reRunLimit.MatchString(line):
		p.nextIsRunlimit = true

		return true, nil
	case p.nextIsRunlimit:
		return false, p.parseRunlimit(line)
	}

	return false, nil
}

// parseMemlimit parses the memory limit value for the current queue.
func (p *bqueuesParser) parseMemlimit(line string) error {
	if matches := p.reNumUnit.FindAllStringSubmatch(line, -1); matches != nil && len(matches) >= p.nextIsMemlimit-1 {
		val, err := strconv.ParseFloat(matches[p.nextIsMemlimit-1][1], 32)
		if err != nil {
			return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
		}

		unit := matches[p.nextIsMemlimit-1][2]
		switch unit {
		case "T":
			val *= 1000000
		case "G":
			val *= 1000
		case "K":
			val /= 1000
		}

		p.s.queues[p.queue][criterionMemlimit] = int(val)
		p.updateHighest(criterionMemlimit, int(val))
	}

	p.nextIsMemlimit = 0

	return nil
}

// parseRunlimit parses the run limit value for the current queue.
func (p *bqueuesParser) parseRunlimit(line string) error {
	if matches := p.reParseRunlimit.FindStringSubmatch(line); len(matches) == reMatchOneGroup {
		mins, err := strconv.Atoi(matches[1])
		if err != nil {
			return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
		}

		p.s.queues[p.queue][criterionRunlimit] = mins * secsPerMinute
		// updateHighest(criterionRunlimit, ...) for queues that do not
		// specify a run limit, we won't base the default on the
		// highest value seen on other queues, but on a hard-coded 1
		// year
	}

	p.nextIsRunlimit = false

	return nil
}

// parseUserHosts handles USERS:/HOSTS: lines, recording how many users/hosts a
// queue has, and dropping queues the current user can't submit to.
func (p *bqueuesParser) parseUserHosts(line string) error {
	matches := p.reUserHosts.FindStringSubmatch(line)
	if len(matches) != reMatchTwoGroups {
		return nil
	}

	kind := strings.ToLower(matches[1])
	vals := strings.Fields(matches[2])

	if kind == criterionUsers {
		return p.parseUsers(vals)
	}

	if matches[2] != "all" {
		return p.parseHosts(kind, vals)
	}

	return nil
}

// parseUsers records the number of users that can use the current queue, and
// drops the queue if the current user can't use it.
func (p *bqueuesParser) parseUsers(vals []string) error {
	users := make(map[string]bool)
	for _, val := range vals {
		users[val] = true
	}

	p.s.queues[p.queue][criterionNumUsers] = len(users)
	if users["all"] {
		p.s.queues[p.queue][criterionNumUsers] = manyUsers
	}

	me, err := internal.Username()
	if err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("could not get current user: %s", err)}
	}

	if !users["all"] && !users[me] {
		delete(p.s.queues, p.queue)
		p.queue = ""
	}

	return nil
}

// parseHosts records the number of distinct hosts that back the current queue,
// expanding any host group names via bmgroup.
func (p *bqueuesParser) parseHosts(kind string, vals []string) error {
	hosts := make(map[string]bool)

	for _, val := range vals {
		if err := p.addHost(hosts, val); err != nil {
			return err
		}
	}

	p.s.queues[p.queue][kind] = len(hosts)
	p.updateHighest(kind, len(hosts))

	return nil
}

// addHost adds val to hosts. If val is a host group name (ends in "/"), it is
// looked up in bmgroup and expanded to its member hosts.
func (p *bqueuesParser) addHost(hosts map[string]bool, val string) error {
	if !strings.HasSuffix(val, "/") {
		hosts[val] = true

		return nil
	}

	// this is a group name, look it up in bmgroup
	if !p.parsedBmgroups {
		if perr := p.s.parseBmgroups(p.bmgroups); perr != nil {
			return perr
		}

		p.parsedBmgroups = true
	}

	val = strings.TrimSuffix(val, "/")
	if servers, exists := p.bmgroups[val]; exists {
		for server := range servers {
			hosts[server] = true
		}
	} else {
		hosts[val] = true
	}

	return nil
}

// parseChunkSize records the chunk job size of the current queue, if present.
func (p *bqueuesParser) parseChunkSize(line string) error {
	matches := p.reChunkJobSize.FindStringSubmatch(line)
	if len(matches) != reMatchOneGroup {
		return nil
	}

	chunks, err := strconv.Atoi(matches[1])
	if err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to parse [bqueues -l]: %s", err)}
	}

	p.s.queues[p.queue][criterionChunkSize] = chunks

	return nil
}

// rankQueues fills in default criteria values for all queues, then sorts them
// so that those most likely to run jobs sooner come first in s.sortedqs.
func (s *lsf) rankQueues(highest map[string]int) {
	// for each criteria we're going to sort the queues on later, hard-code
	// [weight, sort-order]. We want to avoid chunked queues because that means
	// jobs will run sequentially instead of in parallel. For time and memory,
	// prefer the queue that is more limited, since we suppose they might be
	// less busy or will at least become free sooner
	criteriaHandling := map[string][]int{
		criterionHosts:     {18, 1}, // weight, sort order
		criterionMaxUser:   {10, 1},
		criterionMax:       {5, 1},
		"prio":             {1, 0},
		criterionChunkSize: {10000, 0},
		criterionRunlimit:  {1, 1},
		criterionMemlimit:  {1, 0},
		criterionNumUsers:  {15, 0},
	}

	s.applyQueueDefaults(highest)

	// sort the queues, those most likely to run jobs sooner coming first
	ranking := make(map[string]int)

	// instead of range over criteriaHandling, because max_user must come first
	for _, criterion := range []string{
		criterionMaxUser, criterionMax, criterionHosts,
		"prio", criterionChunkSize, criterionNumUsers, criterionRunlimit, criterionMemlimit,
	} {
		s.rankByCriterion(ranking, criterion, criteriaHandling[criterion])
	}

	s.sortedqs = internal.SortMapKeysByIntValue(ranking, false)

	// now s.sortedqs has [0] containing our default preferred order or queues,
	// and other numbers which can be tested against any global maximum number
	// of jobs that we should submit to LSF, and if lower than any of those
	// we prefer the order described there
	// *** we probably don't need this if we won't be having a global max
	// specified by the user
}

// applyQueueDefaults fills in default values for the criteria on all the
// queues, basing defaults on the highest value seen per criterion where one
// was seen.
func (s *lsf) applyQueueDefaults(highest map[string]int) {
	defaults := map[string]int{
		criterionNumUsers:  manyUsers,
		criterionRunlimit:  oneYearInSeconds,
		criterionMemlimit:  hugeQueueLimit,
		criterionMax:       hugeQueueLimit,
		criterionMaxUser:   hugeQueueLimit,
		criterionUsers:     hugeQueueLimit,
		criterionHosts:     hugeQueueLimit,
		criterionChunkSize: 0,
	}

	for criterion, highestVal := range highest {
		if highestVal > 0 {
			defaults[criterion] = highestVal + 1
		}
	}

	for _, qmap := range s.queues {
		for criterion, cdefault := range defaults {
			if _, wasSet := qmap[criterion]; !wasSet {
				qmap[criterion] = cdefault
			}
		}
	}
}

// rankByCriterion adds to each queue's entry in ranking based on its position
// when the queues are sorted by the given criterion. handling is the
// [weight, sort-order] pair for that criterion.
func (s *lsf) rankByCriterion(ranking map[string]int, criterion string, handling []int) {
	// sort queues by this criterion
	sorted := internal.SortMapKeysByMapIntValue(s.queues, criterion, handling[1] == 1)

	weight := handling[0]
	prevVal := -1
	rank := 0

	for _, queue := range sorted {
		val := s.queues[queue][criterion]
		if prevVal != -1 {
			diff := int(math.Abs(float64(val) - float64(prevVal)))
			if diff >= 1 {
				rank++
			}
		}

		ranking[queue] += rank * weight

		prevVal = val
	}
}

// parseBmgroups parses the output of `bmgroup`, storing group name as a key in
// the supplied map, with a map of hosts in that group as the value.
func (s *lsf) parseBmgroups(groups map[string]map[string]bool) error {
	//nolint:gosec,noctx // LSF management command; must complete regardless of scheduling ctx cancellation
	bmgcmd := exec.Command(s.config.Shell, "-c", "bmgroup -w")

	bmgout, err := bmgcmd.StdoutPipe()
	if err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to create pipe for [bmgroup]: %s", err)}
	}

	if err = bmgcmd.Start(); err != nil {
		return Error{lsfScheduler, opInitialize, fmt.Sprintf("failed to start [bmgroup]: %s", err)}
	}

	bmgScanner := bufio.NewScanner(bmgout)
	for bmgScanner.Scan() {
		fields := strings.Fields(bmgScanner.Text())
		if len(fields) < bmgroupMinFields {
			continue
		}

		addBmgroup(groups, fields)
	}

	return nil
}

// addBmgroup records the hosts (fields after the first) belonging to the group
// named by fields[0], expanding any nested group references.
func addBmgroup(groups map[string]map[string]bool, fields []string) {
	group := fields[0]

	for _, field := range fields[1:] {
		if groups[group] == nil {
			groups[group] = make(map[string]bool)
		}

		if before, ok := strings.CutSuffix(field, "/"); ok {
			for server := range groups[before] {
				groups[group][server] = true
			}
		} else {
			groups[group][field] = true
		}
	}
}

// reserveTimeout achieves the aims of ReserveTimeout().
func (s *lsf) reserveTimeout(ctx context.Context, req *Requirements) int {
	if val, defined := req.Other["rtimeout"]; defined {
		timeout, err := strconv.Atoi(val)
		if err != nil {
			clog.Error(ctx, fmt.Sprintf("Failed to convert timeout to integer: %s", err))

			return defaultReserveTimeout
		}

		return timeout
	}

	return defaultReserveTimeout
}

// maxQueueTime achieves the aims of MaxQueueTime().
func (s *lsf) maxQueueTime(req *Requirements) time.Duration {
	queue, err := s.determineQueue(req)
	if err == nil {
		return time.Duration(s.queues[queue][criterionRunlimit]) * time.Second
	}

	return infiniteQueueTime
}

// schedule achieves the aims of Schedule(). Note that if rescheduling a cmd
// at a lower count, we cannot guarantee that only that number get run; it may
// end up being a few more.
func (s *lsf) schedule(ctx context.Context, cmd string, req *Requirements, _ uint8, count int) error {
	// use the given queue or find the best queue for these resource
	// requirements
	queue, err := s.determineQueue(req)
	if err != nil {
		return err // impossible to run cmd with these reqs
	}

	// get the details of everything already in the scheduler for this cmd,
	// removing from the queue anything not currently running when we're over
	// the desired count
	scheduledCount, err := s.checkCmd(ctx, cmd, count)
	if err != nil {
		return err
	}

	stillNeeded := count - scheduledCount
	if stillNeeded < 1 {
		return nil
	}

	// split stillNeeded across as many arrays of at most maxBsubArraySize as
	// required, submitting each in this one scheduling pass. An oversized single
	// array is never emitted (some LSF installations accept it but then hang for
	// minutes). Each array gets its own uniquified, cmd-correlated name (via
	// generateBsubName), so checkCmd/killExcessCmds keep working across chunks.
	for remaining := stillNeeded; remaining > 0; remaining -= maxBsubArraySize {
		chunk := remaining
		if chunk > maxBsubArraySize {
			chunk = maxBsubArraySize
		}

		bsubArgs := s.generateBsubArgs(ctx, queue, req, cmd, chunk)

		if err := s.submitToQueue(ctx, bsubArgs); err != nil {
			return err
		}
	}

	return nil
}

// submitToQueue runs bsub with the given args and waits until the submitted job
// appears in bjobs (see waitForBjob for why).
func (s *lsf) submitToQueue(ctx context.Context, bsubArgs []string) error {
	// submit to the queue. We derive the exec context from context.Background()
	// rather than the scheduling ctx so scheduling-context cancellation cannot
	// abort an in-flight submission, but bound it with bsubExecTimeout so a
	// hung/oversized bsub becomes a logged, retryable error instead of blocking
	// this group's scheduling forever.
	execCtx, cancel := context.WithTimeout(context.Background(), bsubExecTimeout)
	defer cancel()

	//nolint:gosec,contextcheck // LSF job submission; execCtx is deliberately detached from the scheduling ctx (see above)
	bsubcmd := exec.CommandContext(execCtx, s.bsubExe, bsubArgs...)

	// WaitDelay ensures Output() returns promptly after the timeout kills bsub,
	// even if a child of bsub is still holding the output pipe open.
	bsubcmd.WaitDelay = bsubKillGracePeriod

	bsubout, err := bsubcmd.Output()
	if err != nil {
		// bsub reports the real reason an LSF submission was rejected (e.g. a
		// pending-job threshold or a restricted queue) on its stderr, while err
		// itself carries only the bare exit status (typically "exit status 255").
		// bsubStderr recovers that stderr (captured by Output() into the returned
		// *exec.ExitError) so we surface it alongside the exit status, making the
		// rejection diagnosable rather than opaque.
		msg := fmt.Sprintf("failed to run %s %s: %s (bsub stderr: %q)",
			s.bsubExe, bsubArgs, err, bsubStderr(err))

		return Error{lsfScheduler, opSchedule, msg}
	}

	matches := s.bsubRegex.FindStringSubmatch(string(bsubout))
	if len(matches) != reMatchOneGroup {
		return Error{lsfScheduler, opSchedule, fmt.Sprintf("bsub %s returned unexpected output: %s", bsubArgs, bsubout)}
	}

	if !s.waitForBjob(ctx, matches[1]) {
		return Error{lsfScheduler, opSchedule, "after running bsub, failed to find the submitted jobs in bjobs"}
	}

	return err
}

// waitForBjob waits until the submitted job with the given id appears in bjobs
// -w output, returning true once it does, or false if it never appears within
// bjobsAppearTimeout.
//
// unfortunately, a job can be successfully submitted to the queue but not
// immediately appear in bjobs, and if it completes in less than a few seconds,
// it will never appear there (unless you supply bjobs the job id). This means
// that our busy() method, if called immediately after the schedule(), would
// return false, even though the job may actually be running. To solve this
// issue we wait until bjobs -w <jobid> is found and only then return. If a
// subsequent busy() call returns false, that means the job completed and we're
// really not busy.
func (s *lsf) waitForBjob(ctx context.Context, jobID string) bool {
	ready := make(chan bool, 1)

	go func() {
		defer internal.LogPanic(ctx, "lsf scheduling", true)

		limit := time.After(bjobsAppearTimeout)
		ticker := time.NewTicker(bjobsAppearPollFreq)

		for {
			select {
			case <-ticker.C:
				if s.bjobAppeared(jobID) {
					ticker.Stop()

					ready <- true

					return
				}
			case <-limit:
				ticker.Stop()

				ready <- false

				return
			}
		}
	}()

	return <-ready
}

// bjobAppeared returns true if bjobs -w now reports the job with the given id.
func (s *lsf) bjobAppeared(jobID string) bool {
	//nolint:gosec,noctx // LSF management command; must complete regardless of scheduling ctx cancellation
	bjcmd := exec.Command(s.bjobsExe, "-w", jobID)

	bjout, errf := bjcmd.CombinedOutput()
	if errf != nil {
		return false
	}

	return len(bjout) > bjobsMinJobLineLen
}

// scheduled achieves the aims of Scheduled().
func (s *lsf) scheduled(ctx context.Context, cmd string) (int, error) {
	return s.checkCmd(ctx, cmd, -1)
}

// generateBsubArgs generates the appropriate bsub args for the given req and
// cmd and queue.
func (s *lsf) generateBsubArgs(ctx context.Context, queue string, req *Requirements, cmd string, needed int) []string {
	args, err := generateBsubArgs(queue, req, cmd, s.config.Deployment, needed, s.memLimitMultiplier)
	if err != nil {
		clog.Warn(ctx, err.Error())
	}

	return args
}

func generateBsubArgs(queue string, req *Requirements, cmd, deployment string,
	needed int, memLimitMultiplier float32) ([]string, error) {
	var bsubArgs []string

	megabytes := req.RAM
	m := float32(megabytes) * memLimitMultiplier

	bsubArgs = append(bsubArgs, "-q", queue, "-M", fmt.Sprintf("%0.0f", m),
		"-R", fmt.Sprintf("select[mem>%[1]d] rusage[mem=%[1]d] span[hosts=1]", megabytes))

	var err error

	if val, ok := req.Other["scheduler_misc"]; ok {
		var parts []string

		parts, err = parseUserArgs(val, strconv.FormatInt(int64(megabytes), 10))
		bsubArgs = append(bsubArgs, parts...)
	}

	if req.Cores > 1 {
		bsubArgs = append(bsubArgs, "-n", strconv.Itoa(int(math.Ceil(req.Cores))))
	}

	name := generateBsubName(cmd, deployment, needed)
	bsubArgs = append(bsubArgs, "-J", name, "-o", "/dev/null", "-e", "/dev/null", cmd)

	return bsubArgs, err
}

func parseUserArgs(userArgs, megabytes string) ([]string, error) {
	words, err := shellwords.Parse(userArgs)
	if err != nil {
		return nil, fmt.Errorf("scheduler misc option ignored since could not be parsed: %w", err)
	}

	for n := 0; n < len(words); n += 2 {
		if !strings.HasPrefix(words[n], "-") {
			return nil, Error{lsfScheduler, opParseUserArgs, ErrInvalidBsubOpts}
		}

		if words[n] != "-R" {
			continue
		}

		reqs, err := bsubresource.ParseBsubR(words[n+1])
		if err != nil {
			return nil, fmt.Errorf("scheduler misc option ignored since could not be parsed: %w", err)
		}

		reqs.ReplaceMemoryAndHosts(megabytes, "1")

		words[n+1] = reqs.String()
	}

	return words, nil
}

// for checkCmd() to work efficiently we must always set a job name that
// corresponds to the cmd. It must also be unique otherwise LSF would not
// start running jobs with duplicate names until previous ones complete.
func generateBsubName(cmd, deployment string, needed int) string {
	name := jobName(cmd, deployment, true)

	if needed > 1 {
		name += fmt.Sprintf("[1-%d]", needed)
	}

	return name
}

// BsubValidator provides a cacheable bsub argument validator.
type BsubValidator map[string]bool

// Validate takes a string of bsub options and confirms that we can understand
// them and the bsub will accept them.
func (s BsubValidator) Validate(opts, queue string) (valid bool) {
	var ok bool

	if valid, ok = s[opts]; ok {
		return valid
	}

	defer func() {
		s[opts] = valid
	}()

	args, err := generateBsubArgs(queue, &Requirements{
		RAM:   1,
		Other: map[string]string{"scheduler_misc": opts},
	}, "echo", "production", 1, 1)
	if err != nil {
		return false
	}

	//nolint:noctx // LSF validation command; must complete regardless of scheduling ctx cancellation
	cmd := exec.Command("bsub", args...)

	cmd.Env = append(os.Environ(), "BSUB_CHK_RESREQ=1")
	err = cmd.Run()
	valid = err == nil

	return valid
}

// recover achieves the aims of Recover(). We don't have to do anything, since
// when the cmd finishes running, LSF itself will clean up.
func (s *lsf) recover(_ context.Context, _ string, _ *Requirements, _ *RecoveredHostDetails) error {
	return nil
}

// busy returns true if there are any jobs with our jobName() prefix in any
// queue. It also returns true if the most recently submitted job is pending or
// running.
func (s *lsf) busy(ctx context.Context) bool {
	count, err := s.checkCmd(ctx, "", -1)
	if err != nil {
		// busy() doesn't return an error, so just assume we're busy
		return true
	}

	return count > 0
}

// determineQueue picks a queue, preferring ones that are more likely to run our
// job the soonest (amongst those that are capable of running it). If req.Other
// contains a scheduler_queue value, returns that instead.
func (s *lsf) determineQueue(req *Requirements) (string, error) {
	queues := s.sortedqs

	if queue, ok := req.Other["scheduler_queue"]; strings.Contains(queue, ",") {
		queues = strings.Split(queue, ",")
	} else if ok {
		return queue, nil
	}

	seconds := req.Time.Seconds() + minimumQueueTime.Seconds()

	var queuesToAvoid []string
	if req.Other["scheduler_queues_avoid"] != "" {
		queuesToAvoid = strings.Split(req.Other["scheduler_queues_avoid"], ",")
	}

	if queue, ok := s.firstSuitableQueue(queues, queuesToAvoid, req, seconds); ok {
		return queue, nil
	}

	return "", Error{lsfScheduler, opDetermineQueue, ErrImpossible}
}

// firstSuitableQueue returns the first queue (from the given preference-ordered
// list) that isn't to be avoided and has enough memory and time for req.
func (s *lsf) firstSuitableQueue(queues, queuesToAvoid []string, req *Requirements, seconds float64) (string, bool) {
	for _, queue := range queues {
		if queueShouldBeAvoided(queue, queuesToAvoid) {
			continue
		}

		if s.queueHasTooLittleMemory(queue, req) {
			continue
		}

		if s.queueHasTooLittleTime(queue, seconds) {
			continue
		}

		return queue, true
	}

	return "", false
}

func queueShouldBeAvoided(queue string, queuesToAvoid []string) bool {
	for _, queueToAvoid := range queuesToAvoid {
		if strings.Contains(queue, queueToAvoid) {
			return true
		}
	}

	return false
}

func (s *lsf) queueHasTooLittleMemory(queue string, req *Requirements) bool {
	return s.queues[queue][criterionMemlimit] > 0 && s.queues[queue][criterionMemlimit] < req.RAM
}

func (s *lsf) queueHasTooLittleTime(queue string, seconds float64) bool {
	return s.queues[queue][criterionRunlimit] > 0 && float64(s.queues[queue][criterionRunlimit]) < seconds
}

// checkCmd asks LSF how many of the supplied cmd are running, and if
// maxAllowed >= 0 is supplied, kills any extraneous non-running jobs for the
// cmd. If the supplied cmd is the empty string, it will report/act on all cmds
// submitted by schedule() for this deployment.
func (s *lsf) checkCmd(ctx context.Context, cmd string, maxAllowed int) (count int, err error) {
	// bjobs -w does not output a column for both array index and the command.
	// The LSF related modules on CPAN either just parse the command line output
	// or don't work. Ideally we'd use the C-API's lsb_readjobinfo call, but we
	// don't want to be troubled by compilation issues and different versions.
	// We REALLY don't want to manually parse the entire output of bjobs -l for
	// all jobs. Instead when submitting we'll have arranged that JOB_NAME be
	// set to jobName(cmd, ..., true), and in the case of a job array it will
	// have [array_index] appended to it. This lets us use a single bjobs -w
	// call to get all that we need. We can't use -J name to limit which jobs
	// bjobs -w reports on, since we may have submitted the cmd multiple times
	// as multiple different arrays, each with a uniqified job name. It gets
	// uniquified because otherwise none of the jobs in the second array would
	// start until the first array with the same name ended.
	// an empty cmd means we scan all of this deployment's wr jobs, giving a full
	// bjobs snapshot we can use to prune the reserved-element set.
	full := cmd == ""

	var jobPrefix string
	if full {
		// must match jobName's "wr<initial><token>_" layout so an isolated
		// manager's WR_JOBNAME_TOKEN-namespaced jobs are still recognised as ours.
		jobPrefix = jobNamePrefix(s.config.Deployment)
	} else {
		jobPrefix = jobName(cmd, s.config.Deployment, false)
	}

	if maxAllowed < 0 {
		return s.countCmds(jobPrefix, full)
	}

	return s.killExcessCmds(ctx, jobPrefix, maxAllowed)
}

// countCmds counts how many jobs with the given prefix are known to LSF. When
// full is true the prefix covers all of this deployment's wr jobs, so the
// scanned element ids are used to prune the reserved-element set (bounded
// memory over a long-lived manager).
func (s *lsf) countCmds(jobPrefix string, full bool) (count int, err error) {
	var (
		present map[string]bool
		reAid   *regexp.Regexp
	)

	if full {
		present = make(map[string]bool)
		reAid = regexp.MustCompile(`\[(\d+)\]$`)
	}

	cb := func(jobID, _, jobName string) {
		count++

		if full {
			if id := killableID(jobID, jobName, reAid); id != "" {
				present[id] = true
			}
		}
	}
	err = s.parseBjobs(jobPrefix, cb)

	if full {
		s.pruneReserved(present)
	}

	return count, err
}

// killCollector counts running cmds and collects the ids of non-running cmds
// beyond a maximum, so they can be killed.
type killCollector struct {
	reAid      *regexp.Regexp
	reserved   map[string]bool
	toKill     []string
	count      int
	maxAllowed int
}

// consider counts the given job, and if we're now over maxAllowed and the job
// isn't running, records it for killing (and doesn't count it).
func (k *killCollector) consider(jobID, stat, jobName string) {
	k.count++
	if k.count > k.maxAllowed && stat != "RUN" {
		sidaid := killableID(jobID, jobName, k.reAid)

		if sidaid != "" && k.reserved[sidaid] {
			// wr has handed this element a job reservation; never kill it, even
			// though bjobs still reports it as non-RUN. Keep it counted toward
			// maxAllowed since it is effectively active.
			return
		}

		if sidaid != "" {
			k.toKill = append(k.toKill, sidaid)
		}

		k.count--
	}
}

// killExcessCmds counts how many jobs with the given prefix are known to LSF
// and kills any non-running ones beyond maxAllowed, returning the resulting
// count.
func (s *lsf) killExcessCmds(ctx context.Context, jobPrefix string, maxAllowed int) (count int, err error) {
	// to avoid a race condition where we collect id[index]s to kill here,
	// then later kill them all, though some may have started running by
	// then, we used to collect the jod ids now, then later bmod to allow
	// 0 running, then repeat the bjobs to find the ones to kill, kill them,
	// then bmod back to allowing lots to run. However, use of bmod resulted
	// in big rescheduling delays, and overall it seemed better (in terms of
	// getting jobs run quicker) to allow the race condition and allow some
	// cmds to start running and then get killed.
	kc := &killCollector{
		reAid:      regexp.MustCompile(`\[(\d+)\]$`),
		reserved:   s.snapshotReserved(),
		toKill:     []string{"-b"},
		maxAllowed: maxAllowed,
	}

	err = s.parseBjobs(jobPrefix, kc.consider)

	if len(kc.toKill) > 1 {
		//nolint:gosec,noctx // LSF management command; must complete regardless of scheduling ctx cancellation
		killcmd := exec.Command(s.bkillExe, kc.toKill...)

		out, errk := killcmd.CombinedOutput()
		if errk != nil && !strings.HasPrefix(string(out), "Job has already finished") {
			clog.Warn(ctx, "checkCmd bkill failed", "cmd", s.bkillExe, "toKill", kc.toKill, "err", errk, "out", string(out))
		}
	}

	return kc.count, err
}

// killableID returns the submission-id[array-index] string to pass to bkill for
// the given bjobs job id and job name, or "" if one can't be determined.
func killableID(jobID, jobName string, reAid *regexp.Regexp) string {
	if strings.HasSuffix(jobID, "]") {
		return jobID
	}

	if aidmatch := reAid.FindStringSubmatch(jobName); len(aidmatch) == reMatchOneGroup {
		return jobID + "[" + aidmatch[1] + "]"
	}

	return ""
}

type bjobsCB func(jobID, stat, jobName string)

// parseBjobs runs bjobs, filters on a job name prefix, excludes exited jobs and
// gives columns 1 (JOBID), 3 (STAT) and 7 (JOB_NAME) to your callback for each
// bjobs output line.
func (s *lsf) parseBjobs(jobPrefix string, callback bjobsCB) error {
	//nolint:gosec,noctx // LSF management command; must complete regardless of scheduling ctx cancellation
	bjcmd := exec.Command(s.config.Shell, "-c", s.bjobsExe+" -w")

	bjout, err := bjcmd.StdoutPipe()
	if err != nil {
		return Error{lsfScheduler, opParseBjobs, fmt.Sprintf("failed to create pipe for [bjobs -w]: %s", err)}
	}

	err = bjcmd.Start()
	if err != nil {
		return Error{lsfScheduler, opParseBjobs, fmt.Sprintf("failed to start [bjobs -w]: %s", err)}
	}

	bjScanner := bufio.NewScanner(bjout)
	bjScanner.Buffer([]byte{}, scanBufferSize)

	for bjScanner.Scan() {
		parseBjobsLine(bjScanner.Text(), jobPrefix, callback)
	}

	if err = bjScanner.Err(); err != nil {
		return Error{lsfScheduler, opParseBjobs, fmt.Sprintf("failed to read everything from [bjobs -w]: %s", err)}
	}

	err = bjcmd.Wait()
	if err != nil {
		err = Error{lsfScheduler, opParseBjobs, fmt.Sprintf("failed to finish running [bjobs -w]: %s", err)}
	}

	return err
}

// parseBjobsLine passes the job id, status and name from a single bjobs -w line
// to callback, unless the line is too short, the job has exited, or its name
// doesn't have the given prefix.
func parseBjobsLine(line, jobPrefix string, callback bjobsCB) {
	fields := strings.Fields(line)
	if len(fields) <= bjobsMinFields {
		return
	}

	if fields[2] == "EXIT" || fields[2] == "DONE" || !strings.HasPrefix(fields[6], jobPrefix) {
		return
	}

	callback(fields[0], fields[2], fields[6])
}

// hostToID always returns an empty string, since we're not in the cloud.
func (s *lsf) hostToID(_ string) string {
	return ""
}

// getHost returns a Host backed by a fresh throwaway cloud.Server for the given
// host, whose Close() closes the ssh connection it dials.
func (s *lsf) getHost(host string) (Host, bool) {
	name := "unknown"
	if user, err := user.Current(); err == nil {
		name = user.Username
	}

	server := cloud.NewServer(name, host, s.privateKey)
	if server == nil {
		return nil, false
	}

	return &lsfHost{server: server}, true
}

// setMessageCallBack does nothing at the moment, since we don't generate any
// messages for the user.
func (s *lsf) setMessageCallBack(_ context.Context, _ MessageCallBack) {}

// setBadServerCallBack does nothing, since we're not a cloud-based scheduler.
func (s *lsf) setBadServerCallBack(_ context.Context, _ BadServerCallBack) {}

// cleanup bkills any remaining jobs we created.
func (s *lsf) cleanup(ctx context.Context) {
	toKill := []string{"-b"}
	cb := func(jobID, _, _ string) {
		toKill = append(toKill, jobID)
	}

	err := s.parseBjobs(jobNamePrefix(s.config.Deployment), cb)
	if err != nil {
		clog.Error(ctx, "cleaup parse bjobs failed", "err", err)
	}

	if len(toKill) > 1 {
		//nolint:gosec,noctx // LSF management command; must complete regardless of scheduling ctx cancellation
		killcmd := exec.Command(s.bkillExe, toKill...)

		err = killcmd.Run()
		if err != nil {
			clog.Warn(ctx, "cleanup bkill failed", "err", err)
		}
	}
}
