/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
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

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

// Env vars of the crash test (spec A4): the number of kill rounds and the
// directory the rounds' stores go in, so a gate can run many rounds on NFS.
const (
	envCrashTestRounds = "WR_CRASHTEST_ROUNDS"
	envCrashTestDir    = "WR_CRASHTEST_DIR"
)

// Env vars by which TestStoreCrash tells its child, TestStoreCrashChild, what to
// do: the round's directory, the round number, the store kind, and which
// test-only wrapper, if any, to put around the store.
const (
	envCrashChildDir     = "WR_CRASHTEST_CHILD_DIR"
	envCrashChildRound   = "WR_CRASHTEST_CHILD_ROUND"
	envCrashChildKind    = "WR_CRASHTEST_CHILD_KIND"
	envCrashChildWrapper = "WR_CRASHTEST_CHILD_WRAPPER"
)

// The values of envCrashChildWrapper: none, ackEarlyStore, or dropReleaseStore.
const (
	crashWrapNone        = ""
	crashWrapAckEarly    = "ackearly"
	crashWrapDropRelease = "droprelease"
)

const (
	crashDefaultRounds = 2
	crashDropRounds    = 4
	crashNewJobs       = 300
	crashOldJobs       = 50
	crashCycleSpread   = 6
	crashKillMin       = 1500 * time.Millisecond
	crashKillSpread    = 3 * time.Second
	crashDamagedCopies = 20

	// crashChildReadyTimeout bounds the wait for a child to open its store and
	// add its first wave, which takes well under a second even under the race
	// detector; it only stops a wedged child hanging the test.
	crashChildReadyTimeout = 2 * time.Minute

	// crashChildMaxRun is how long a child writes before it gives up waiting
	// to be killed and exits, failing its round. The parent kills it within
	// crashKillMin+crashKillSpread of it being ready; this only stops a child
	// whose parent died writing for ever.
	crashChildMaxRun = time.Minute

	crashReadyMarker = "CRASHTEST_READY"

	crashAcksFile    = "acks"
	crashIntentsFile = "intents"
	crashStoreFile   = "queue.db"
	crashWalSuffix   = ".wal"
	crashFileMode    = 0o600
	crashDirMode     = 0o700
	crashFieldCount  = 3
	crashRepGroup    = "crashtest"
	crashMissingShow = 5
)

// crashStepNames are the names of each crashStep in the ack and intent files.
//
//nolint:gochecknoglobals // a read-only lookup table
var crashStepNames = [crashSteps]string{"added", "reserved", "started", "released", "archived"}

// crashComplete is the progress of an archived job, at or after every ack.
const crashComplete = math.MaxInt

// errCrashUnknownStep is parseCrashLine's error for a step name it does not
// know.
var errCrashUnknownStep = errors.New("unknown crash test step")

// crashStep is a transition the crash test's child writes. Within an attempt
// count, steps order as declared, so that a job's progress is totally ordered
// (see crashProgress).
type crashStep int

const (
	crashAdded crashStep = iota
	crashReserve
	crashStart
	crashRelease
	crashArchive
	crashSteps
)

// parseCrashLine parses a key<TAB>attempts<TAB>step line.
func parseCrashLine(line string) (string, int, error) {
	fields := strings.Split(line, "\t")
	if len(fields) != crashFieldCount {
		return "", 0, fmt.Errorf("%w: %q", errCrashUnknownStep, line)
	}

	attempts, err := strconv.ParseUint(fields[1], 10, 32)
	if err != nil {
		return "", 0, err
	}

	step := slices.Index(crashStepNames[:], fields[2])
	if step < 0 {
		return "", 0, fmt.Errorf("%w: %q", errCrashUnknownStep, fields[2])
	}

	return fields[0], crashProgress(uint32(attempts), crashStep(step)), nil
}

// crashProgress orders (attempts, step): steps order within an attempt count, and
// every step of one attempt count is before every step of the next. A release
// is recorded at the attempt count it released, though it leaves the job with
// one more.
func crashProgress(attempts uint32, step crashStep) int {
	return int(attempts)*int(crashSteps) + int(step)
}

// ackEarlyStore is a store that acknowledges each transition write before it
// is durable: it runs the wrapped write in a new goroutine and returns nil at
// once. It exists to prove the crash test can fail. Before a write's goroutine
// starts the write, it calls crashInFlightWrite if that is set, so a child can
// kill itself between the acknowledgement and the write.
type ackEarlyStore struct {
	store

	crashInFlightWrite func()
}

// inFlight runs write in a new goroutine, after crashInFlightWrite.
func (a *ackEarlyStore) inFlight(write func()) {
	go func() {
		if a.crashInFlightWrite != nil {
			a.crashInFlightWrite()
		}

		write()
	}()
}

// updateJobRunStateDurable acknowledges at once, writing in a new goroutine.
func (a *ackEarlyStore) updateJobRunStateDurable(job *Job) error {
	a.inFlight(func() { _ = a.store.updateJobRunStateDurable(job) }) //nolint:errcheck

	return nil
}

// updateJobAfterExitDurable acknowledges at once, writing in a new goroutine.
func (a *ackEarlyStore) updateJobAfterExitDurable(job *Job, stdo, stde []byte, forceStorage bool) error {
	a.inFlight(func() { _ = a.store.updateJobAfterExitDurable(job, stdo, stde, forceStorage) }) //nolint:errcheck

	return nil
}

// archiveCompletion acknowledges at once, writing in a new goroutine.
func (a *ackEarlyStore) archiveCompletion(key string, job *Job) (archiveOutcome, error) {
	a.inFlight(func() { _, _ = a.store.archiveCompletion(key, job) }) //nolint:errcheck

	return archiveRemovedLive, nil
}

// dropReleaseStore is a store whose updateJobAfterExitDurable acknowledges a
// release without writing it. It exists to prove the crash test catches a lost
// acknowledged release.
type dropReleaseStore struct {
	store
}

// updateJobAfterExitDurable returns nil without writing.
func (dropReleaseStore) updateJobAfterExitDurable(*Job, []byte, []byte, bool) error {
	return nil
}

// crashCall is one call the child made: a key and the progress it wrote.
type crashCall struct {
	key      string
	progress int
}

// crashRoundResult is what the parent read of one kill round: the highest
// progress acked for each key, every progress the child intended for each key,
// how many intended calls have no ack, how many archives were acked, and how
// many waves of new jobs the child added.
type crashRoundResult struct {
	acks      map[string]int
	intents   map[string][]int
	unacked   int
	archived  int
	waves     int
	killDelay time.Duration
}

// runCrashRound starts TestStoreCrashChild for round in dir and, once it has
// added its first wave of jobs, SIGKILLs that exact child after a random
// 1.5-4.5s. wrapper names the test-only store wrapper the child uses; with
// crashWrapAckEarly the child kills itself after its first transition's ack,
// and the parent kills it only if it is still alive after the delay.
func runCrashRound(t *testing.T, kind storeKind, dir string, round int, wrapper string) crashRoundResult {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^TestStoreCrashChild$") //nolint:gosec

	cmd.Env = append(os.Environ(),
		envCrashChildDir+"="+dir,
		envCrashChildRound+"="+strconv.Itoa(round),
		envCrashChildKind+"="+storeKindName(kind),
		envCrashChildWrapper+"="+wrapper,
	)

	out := &crashChildOutput{ready: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = out, out

	So(cmd.Start(), ShouldBeNil)

	exited := make(chan error, 1)

	go func() { exited <- cmd.Wait() }()

	killChild := func() error {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}

		return <-exited
	}

	var (
		waitErr   error
		selfEnded bool
	)

	select {
	case <-out.ready:
	case waitErr = <-exited:
		// Wait returns only once the child's output is copied, so a child that
		// reported ready and then ended (as an ackEarly child kills itself) has
		// closed out.ready by now.
		select {
		case <-out.ready:
			selfEnded = true
		default:
			t.Fatalf("crash child exited before it was ready (%v):\n%s", waitErr, out)
		}
	case <-time.After(crashChildReadyTimeout):
		t.Fatalf("crash child not ready after %s (%v):\n%s", crashChildReadyTimeout, killChild(), out)
	}

	delay := crashKillMin + rand.N(crashKillSpread) //nolint:gosec // a kill time, not a secret

	if !selfEnded {
		select {
		case waitErr = <-exited:
			selfEnded = true
		case <-time.After(delay):
			waitErr = killChild()
		}
	}

	if selfEnded && wrapper != crashWrapAckEarly {
		t.Fatalf("crash child exited before it was killed (%v):\n%s", waitErr, out)
	}

	So(killedBySIGKILL(waitErr), ShouldBeTrue)

	return readCrashRound(t, dir, round, delay)
}

// readCrashRound reads round's ack and intent files in dir.
func readCrashRound(t *testing.T, dir string, round int, delay time.Duration) crashRoundResult {
	t.Helper()

	result := crashRoundResult{acks: make(map[string]int), intents: make(map[string][]int), killDelay: delay}
	acked := make(map[crashCall]bool)

	readCrashFile(t, filepath.Join(dir, crashFileName(crashAcksFile, round)), func(key string, p int) {
		result.acks[key] = max(result.acks[key], p)
		acked[crashCall{key, p}] = true

		if isCrashArchive(p) {
			result.archived++
		}
	})

	added := 0

	readCrashFile(t, filepath.Join(dir, crashFileName(crashIntentsFile, round)), func(key string, p int) {
		result.intents[key] = append(result.intents[key], p)

		if !acked[crashCall{key, p}] {
			result.unacked++
		}

		if p == crashProgress(0, crashAdded) {
			added++
		}
	})

	result.waves = (added + crashNewJobs - 1) / crashNewJobs

	return result
}

// crashRecovery is what the parent found on reopening the store after a round.
type crashRecovery struct {
	live, complete int
	missing        []string
}

// crashRoundRecovered runs a kill round, merges its acks and intents into those
// of earlier rounds, and recovers the store, returning the round and which keys
// acked in any round so far the store lost.
func crashRoundRecovered(t *testing.T, ctx context.Context, kind storeKind, dir string, round int,
	wrapper string, acks map[string]int, intents map[string][]int,
) (crashRoundResult, crashRecovery) {
	t.Helper()

	result := runCrashRound(t, kind, dir, round, wrapper)

	for key, p := range result.acks {
		acks[key] = max(acks[key], p)
	}

	for key, ps := range result.intents {
		intents[key] = append(intents[key], ps...)
	}

	recovery := recoverCrashStore(ctx, kind, filepath.Join(dir, crashStoreFile), acks)
	t.Logf("%s %q round %d: killed after %s, %d waves, %d keys acked this round (%d archived), %d in all, "+
		"%d calls unacked; recovered %d live and %d complete; %d missing",
		storeKindName(kind), wrapper, round, result.killDelay, result.waves, len(result.acks), result.archived,
		len(acks), result.unacked, recovery.live, recovery.complete, len(recovery.missing))

	return result, recovery
}

// recoverCrashStore opens the store at dbFile as a restarting manager would,
// recovers its live jobs, and returns which acked keys it lost: those neither
// complete nor live at or after their ack's progress.
func recoverCrashStore(ctx context.Context, kind storeKind, dbFile string, acks map[string]int) crashRecovery {
	s, err := openCrashStore(ctx, kind, dbFile)
	So(err, ShouldBeNil)

	defer func() { So(s.close(ctx), ShouldBeNil) }()

	jobs, _, err := s.recoverIncompleteJobs()
	So(err, ShouldBeNil)

	live := make(map[string]*Job, len(jobs))
	for _, job := range jobs {
		live[job.Key()] = job
	}

	recovery := crashRecovery{live: len(live)}

	var errs []error

	for key, acked := range acks {
		if job, ok := live[key]; ok {
			if recoveredCrashProgress(job) < acked {
				recovery.missing = append(recovery.missing, key)
			}

			continue
		}

		complete, errc := s.checkIfComplete(key)
		if errc != nil {
			errs = append(errs, errc)
		}

		if complete {
			recovery.complete++

			continue
		}

		recovery.missing = append(recovery.missing, key)
	}

	So(errors.Join(errs...), ShouldBeNil)

	return recovery
}

// TestStoreCrash is spec A4: a child process writes transitions to a store and
// records each in an ack file once the store has acknowledged it; the child is
// killed with SIGKILL at a random moment while it is still writing, and the
// reopened store must hold every acknowledged transition.
func TestStoreCrash(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a child writing transitions to a %s store, SIGKILLed at a random moment each round",
			storeKindName(kind)), t, func() {
			dir := crashTestDir(t)
			acks := make(map[string]int)
			intents := make(map[string][]int)

			for round := range crashTestRounds(t) {
				result, recovery := crashRoundRecovered(t, ctx, kind, dir, round, crashWrapNone, acks, intents)

				So(len(result.acks), ShouldBeGreaterThan, 0)
				So(result.unacked, ShouldBeGreaterThan, 0)
				So(shownMissing(recovery.missing), ShouldBeEmpty)

				if kind == storeKindLog {
					checkDamagedCrashCopies(ctx, kind, dir, intents)
				}
			}
		})

		Convey(fmt.Sprintf("Given a %s store wrapped in ackEarlyStore, killed after an ack and before its write",
			storeKindName(kind)), t, func() {
			dir := crashTestDir(t)

			result, recovery := crashRoundRecovered(t, ctx, kind, dir, 0, crashWrapAckEarly,
				make(map[string]int), make(map[string][]int))
			So(len(result.acks), ShouldBeGreaterThan, 0)

			Convey("the round reports missing acked transitions", func() {
				So(len(recovery.missing), ShouldBeGreaterThan, 0)
			})
		})

		Convey(fmt.Sprintf("Given a %s store wrapped to drop acknowledged releases, killed at a random moment",
			storeKindName(kind)), t, func() {
			dir := crashTestDir(t)
			acks := make(map[string]int)
			intents := make(map[string][]int)
			missingRounds := 0

			// The outcome needs one round with missing keys out of
			// crashDropRounds, so later rounds cannot change it once one has.
			for round := range crashDropRounds {
				_, recovery := crashRoundRecovered(t, ctx, kind, dir, round, crashWrapDropRelease, acks, intents)
				if len(recovery.missing) > 0 {
					missingRounds++

					break
				}
			}

			Convey("a round reports missing acked transitions", func() {
				So(missingRounds, ShouldBeGreaterThan, 0)
			})
		})
	})
}

// crashTestDir returns a new directory for one crash test's rounds: inside
// envCrashTestDir if that is set, removed when the test ends, or else a test
// temp dir.
func crashTestDir(t *testing.T) string {
	t.Helper()

	base := os.Getenv(envCrashTestDir)
	if base == "" {
		return t.TempDir()
	}

	dir, err := os.MkdirTemp(base, "crashtest-")
	if err != nil {
		t.Fatalf("could not make a crash test dir in %s: %s", base, err)
	}

	t.Cleanup(func() {
		if errr := os.RemoveAll(dir); errr != nil {
			t.Errorf("could not remove %s: %s", dir, errr)
		}
	})

	return dir
}

// crashTestRounds returns the number of rounds envCrashTestRounds asks for, or
// crashDefaultRounds.
func crashTestRounds(t *testing.T) int {
	t.Helper()

	value := os.Getenv(envCrashTestRounds)
	if value == "" {
		return crashDefaultRounds
	}

	rounds, err := strconv.Atoi(value)
	if err != nil || rounds < 1 {
		t.Fatalf("%s=%q: want a positive number of rounds", envCrashTestRounds, value)
	}

	return rounds
}

// shownMissing is missing, cut to its first crashMissingShow keys, for a
// failure message.
func shownMissing(missing []string) []string {
	if len(missing) <= crashMissingShow {
		return missing
	}

	return append(slices.Clone(missing[:crashMissingShow]), fmt.Sprintf("and %d more", len(missing)-crashMissingShow))
}

// checkDamagedCrashCopies is spec A4 acceptance test 2, for the log store: it
// makes crashDamagedCopies copies of the store in dir, alternately truncating
// its last wal file at a random offset and flipping one random byte of it, and
// requires each copy to recover without error, with every job in a state the
// child wrote for it (intents).
func checkDamagedCrashCopies(ctx context.Context, kind storeKind, dir string, intents map[string][]int) {
	storeDir := filepath.Join(dir, crashStoreFile)

	wals, err := filepath.Glob(filepath.Join(storeDir, "*"+crashWalSuffix))
	So(err, ShouldBeNil)
	So(wals, ShouldNotBeEmpty)

	slices.Sort(wals)
	lastWal := filepath.Base(wals[len(wals)-1])

	keys := slices.Collect(maps.Keys(intents))

	var unwritten []string //nolint:prealloc // each copy adds any number

	for i := range crashDamagedCopies {
		copyDir := filepath.Join(dir, fmt.Sprintf("damaged.%d", i))
		So(copyCrashStore(storeDir, copyDir), ShouldBeNil)
		So(damageCrashFile(filepath.Join(copyDir, lastWal), i%2 == 0), ShouldBeNil)

		unwritten = append(unwritten, recoverDamagedCrashCopy(ctx, kind, copyDir, keys, intents)...)

		So(os.RemoveAll(copyDir), ShouldBeNil)
	}

	So(unwritten, ShouldBeEmpty)
}

// copyCrashStore copies the regular files of the store directory src into a
// new directory dst.
func copyCrashStore(src, dst string) error {
	if err := os.MkdirAll(dst, crashDirMode); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}

		if err = copyCrashFile(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}

	return nil
}

// copyCrashFile copies the file src to dst.
func copyCrashFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, crashFileMode)
	if err != nil {
		return err
	}

	if _, err = io.Copy(out, in); err != nil {
		out.Close()

		return err
	}

	return out.Close()
}

// damageCrashFile truncates the file at path at a random offset, or else flips
// every bit of one random byte of it.
func damageCrashFile(path string, truncate bool) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return err
	}

	offset := rand.Int64N(info.Size()) //nolint:gosec // a damage offset, not a secret

	if truncate {
		return os.Truncate(path, offset)
	}

	f, err := os.OpenFile(path, os.O_RDWR, crashFileMode)
	if err != nil {
		return err
	}

	b := make([]byte, 1)
	if _, err = f.ReadAt(b, offset); err != nil {
		f.Close()

		return err
	}

	b[0] ^= 0xff

	if _, err = f.WriteAt(b, offset); err != nil {
		f.Close()

		return err
	}

	return f.Close()
}

// recoverDamagedCrashCopy opens the store copy at copyDir and returns the keys
// it recovered in a state the child never wrote for them.
func recoverDamagedCrashCopy(ctx context.Context, kind storeKind, copyDir string, keys []string,
	intents map[string][]int,
) []string {
	s, err := openCrashStore(ctx, kind, copyDir)
	So(err, ShouldBeNil)

	defer func() { So(s.close(ctx), ShouldBeNil) }()

	jobs, _, err := s.recoverIncompleteJobs()
	So(err, ShouldBeNil)

	unwritten := make([]string, 0, len(jobs))

	for _, job := range jobs {
		if !slices.Contains(intents[job.Key()], recoveredCrashProgress(job)) {
			unwritten = append(unwritten, job.Key())
		}
	}

	complete, err := s.retrieveCompleteJobsByKeys(keys)
	So(err, ShouldBeNil)

	for _, job := range complete {
		if !slices.ContainsFunc(intents[job.Key()], isCrashArchive) {
			unwritten = append(unwritten, job.Key())
		}
	}

	return unwritten
}

// openCrashStore opens the store of kind at dbFile, as a restarting development
// manager does, without wiping it and with no backups.
func openCrashStore(ctx context.Context, kind storeKind, dbFile string) (store, error) {
	s, _, err := openStore(ctx, storeConfig{
		dbFile:     dbFile,
		dbBkFile:   dbFile + ".bak",
		deployment: internal.Development,
		kind:       kind,
	})

	return s, err
}

// recoveredCrashProgress is the progress implied by the state of a job
// recovered from the store. A ready job with Attempts n > 0 counts as released
// at attempt count n-1: the child only makes a job ready by releasing it, and a
// release leaves it with one more attempt than the count it released (see
// setCrashStep); a job with no attempts has only been added, and is stored as
// added, with no State, until its first transition. A state the child never
// writes is -1, before every ack.
func recoveredCrashProgress(job *Job) int {
	switch job.State {
	case JobStateReserved:
		return crashProgress(job.Attempts, crashReserve)
	case JobStateRunning:
		return crashProgress(job.Attempts, crashStart)
	case JobStateReady, "":
		if job.Attempts == 0 {
			return crashProgress(0, crashAdded)
		}

		return crashProgress(job.Attempts-1, crashRelease)
	case JobStateComplete:
		return crashComplete
	default:
		return -1
	}
}

// crashChildOutput collects a child's combined output, closing ready once the
// child reports it has added its jobs.
type crashChildOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	ready     chan struct{}
	readyOnce sync.Once
}

// Write is io.Writer.
func (o *crashChildOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	n, err := o.buf.Write(p)

	if bytes.Contains(o.buf.Bytes(), []byte(crashReadyMarker)) {
		o.readyOnce.Do(func() { close(o.ready) })
	}

	return n, err
}

// String returns everything written so far.
func (o *crashChildOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.buf.String()
}

// crashChild is the writer TestStoreCrashChild runs: it takes jobs through
// transitions in its store, writing each call to the intent file before it
// makes it, and to the ack file, synced, once the store has acknowledged it.
type crashChild struct {
	s        store
	acks     *os.File
	intents  *os.File
	mu       sync.Mutex
	acked    chan struct{}
	ackedOne sync.Once
}

// openCrashChild opens the child's store, ack and intent files, and returns the
// child with up to crashOldJobs randomly chosen live jobs recovered from earlier
// rounds. The store is wrapped in the wrapper the parent asked for: an
// ackEarlyStore that kills this process after its first transition's ack and
// before that transition's write, or a dropReleaseStore.
func openCrashChild(t *testing.T, dir string, round int) (*crashChild, []*Job) {
	t.Helper()

	kind := storeKindBolt
	if os.Getenv(envCrashChildKind) == storeKindName(storeKindLog) {
		kind = storeKindLog
	}

	s, err := openCrashStore(context.Background(), kind, filepath.Join(dir, crashStoreFile))
	if err != nil {
		t.Fatal(err)
	}

	recovered, _, err := s.recoverIncompleteJobs()
	if err != nil {
		t.Fatal(err)
	}

	rand.Shuffle(len(recovered), func(i, j int) { //nolint:gosec // a test's choice of jobs, not a secret
		recovered[i], recovered[j] = recovered[j], recovered[i]
	})
	oldJobs := recovered[:min(len(recovered), crashOldJobs)]

	child := &crashChild{s: s, acked: make(chan struct{})}

	switch wrapper := os.Getenv(envCrashChildWrapper); wrapper {
	case crashWrapNone:
	case crashWrapAckEarly:
		child.s = &ackEarlyStore{store: s, crashInFlightWrite: child.killAfterFirstAck}
	case crashWrapDropRelease:
		child.s = dropReleaseStore{store: s}
	default:
		t.Fatalf("%s=%q: unknown wrapper", envCrashChildWrapper, wrapper)
	}

	child.acks = openCrashAppend(t, filepath.Join(dir, crashFileName(crashAcksFile, round)))
	child.intents = openCrashAppend(t, filepath.Join(dir, crashFileName(crashIntentsFile, round)))

	return child, oldJobs
}

// killAfterFirstAck is ackEarlyStore's crashInFlightWrite in the child: it waits
// for the first transition's ack to be synced, then SIGKILLs this process, so
// that write is acknowledged and never started.
func (c *crashChild) killAfterFirstAck() {
	<-c.acked

	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		panic(err)
	}

	select {}
}

// runWaves adds overlapping waves of crashNewJobs new jobs until deadline,
// reporting it is ready once the first is added, and takes each wave's jobs
// through their transitions, one goroutine per job. The next wave starts when
// half of the current wave's goroutines have finished, so the previous wave is
// still writing while the next one adds and a kill never lands in an idle gap.
// It returns the first error.
func (c *crashChild) runWaves(round int, deadline time.Time) error {
	errs := make(chan error, 1)

	for wave := 0; time.Now().Before(deadline); wave++ {
		jobs, err := c.addWave(round, wave)
		if err != nil {
			return err
		}

		if wave == 0 {
			fmt.Println(crashReadyMarker) //nolint:forbidigo // the parent waits for it on stdout
		}

		select {
		case <-c.runNewJobs(jobs, errs):
		case err = <-errs:
			return err
		}
	}

	return nil
}

// addWave adds wave's crashNewJobs new jobs in one call, recording the intent
// to add each before it and the ack of each after it.
func (c *crashChild) addWave(round, wave int) ([]*Job, error) {
	jobs := make([]*Job, crashNewJobs)
	keys := make([]string, crashNewJobs)

	for i := range jobs {
		jobs[i] = testDBJob(fmt.Sprintf("crashtest r%d w%d j%d", round, wave, i), crashRepGroup)
		keys[i] = jobs[i].Key()
	}

	if err := c.record(c.intents, false, 0, crashAdded, keys...); err != nil {
		return nil, err
	}

	if _, err := c.s.storeNewJobsGuarded(context.Background(), jobs, false, nil); err != nil {
		return nil, err
	}

	return jobs, c.record(c.acks, true, 0, crashAdded, keys...)
}

// runNewJobs takes jobs through their transitions, one goroutine per job, and
// returns a channel closed once half of the goroutines have finished. A
// goroutine's error goes to errs, unless errs already holds one.
func (c *crashChild) runNewJobs(jobs []*Job, errs chan<- error) <-chan struct{} {
	halfDone := make(chan struct{})

	var (
		finished atomic.Int32
		closeOne sync.Once
	)

	for i, job := range jobs {
		go func() {
			if err := c.runNewJob(job, uint32(i%crashCycleSpread)); err != nil { //nolint:gosec // i < crashNewJobs
				select {
				case errs <- err:
				default:
				}
			}

			if int(finished.Add(1)) >= len(jobs)/2 {
				closeOne.Do(func() { close(halfDone) })
			}
		}()
	}

	return halfDone
}

// runNewJob takes a newly added job through the given number of cycles of
// reserve, start and release, then reserve, start and archive.
func (c *crashChild) runNewJob(job *Job, cycles uint32) error {
	for cycle := range cycles {
		if err := c.cycle(job, cycle); err != nil {
			return err
		}
	}

	for _, step := range []crashStep{crashReserve, crashStart, crashArchive} {
		if err := c.transition(job, cycles, step); err != nil {
			return err
		}
	}

	return nil
}

// runOldJob takes a job recovered from an earlier round through cycles of
// reserve, start and release until deadline, starting after its recovered
// progress: at its attempt count if it was last released or added, or else at
// the next.
func (c *crashChild) runOldJob(job *Job, deadline time.Time) error {
	attempts := job.Attempts
	if job.State != JobStateReady {
		attempts++
	}

	for ; time.Now().Before(deadline); attempts++ {
		if err := c.cycle(job, attempts); err != nil {
			return err
		}
	}

	return nil
}

// cycle takes job through a reserve, start and release at attempt count
// attempts.
func (c *crashChild) cycle(job *Job, attempts uint32) error {
	for _, step := range []crashStep{crashReserve, crashStart, crashRelease} {
		if err := c.transition(job, attempts, step); err != nil {
			return err
		}
	}

	return nil
}

// transition sets job to step at attempt count attempts, records the intent,
// writes it with the store call the server makes for that step, and records the
// ack once the store returns.
func (c *crashChild) transition(job *Job, attempts uint32, step crashStep) error {
	key := job.Key()

	setCrashStep(job, attempts, step)

	if err := c.record(c.intents, false, attempts, step, key); err != nil {
		return err
	}

	var err error

	switch step {
	case crashRelease:
		err = c.s.updateJobAfterExitDurable(job, nil, []byte("crash"), false)
	case crashArchive:
		_, err = c.s.archiveCompletion(key, job)
	default:
		err = c.s.updateJobRunStateDurable(job)
	}

	if err != nil {
		return err
	}

	if err = c.record(c.acks, true, attempts, step, key); err != nil {
		return err
	}

	c.ackedOne.Do(func() { close(c.acked) })

	return nil
}

// setCrashStep sets job's state to that of step at attempt count attempts. A
// release leaves it ready with one more attempt.
func setCrashStep(job *Job, attempts uint32, step crashStep) {
	job.Lock()
	defer job.Unlock()

	job.Attempts = attempts

	switch step {
	case crashReserve:
		job.State = JobStateReserved
		job.Exited = false
		job.Exitcode = 0
	case crashStart:
		job.State = JobStateRunning
		job.Pid = os.Getpid()
		job.Host = localhost
		job.StartTime = time.Now()
	case crashRelease:
		job.State = JobStateReady
		job.Exited = true
		job.Exitcode = 1
		job.EndTime = time.Now()
		job.Attempts = attempts + 1
	case crashArchive:
		job.State = JobStateComplete
		job.Exited = true
		job.Exitcode = 0
		job.EndTime = time.Now()
	case crashAdded, crashSteps:
	}
}

// record appends key<TAB>attempts<TAB>step to f for each of keys in one write,
// syncing it if sync is true.
func (c *crashChild) record(f *os.File, sync bool, attempts uint32, step crashStep, keys ...string) error {
	var lines strings.Builder

	for _, key := range keys {
		fmt.Fprintf(&lines, "%s\t%d\t%s\n", key, attempts, crashStepNames[step])
	}

	c.mu.Lock()
	_, err := f.WriteString(lines.String())
	c.mu.Unlock()

	if err != nil || !sync {
		return err
	}

	return f.Sync()
}

// TestStoreCrashChild is the child of TestStoreCrash. It opens the store in
// its round's directory and recovers the live jobs of earlier rounds. Then,
// until it is killed, it adds overlapping waves of crashNewJobs new jobs,
// reporting it is ready once the first is added, and takes job i of each wave,
// one goroutine per job, through i%crashCycleSpread cycles of reserve, start
// and release and then a reserve, start and archive; meanwhile it cycles up to
// crashOldJobs of the earlier rounds' live jobs through reserve, start and
// release.
func TestStoreCrashChild(t *testing.T) {
	dir := os.Getenv(envCrashChildDir)
	if dir == "" {
		t.Skip("child of TestStoreCrash")
	}

	round, err := strconv.Atoi(os.Getenv(envCrashChildRound))
	if err != nil {
		t.Fatal(err)
	}

	child, oldJobs := openCrashChild(t, dir, round)
	deadline := time.Now().Add(crashChildMaxRun)
	errs := make(chan error, len(oldJobs)+1)

	for _, job := range oldJobs {
		go func() { errs <- child.runOldJob(job, deadline) }()
	}

	go func() { errs <- child.runWaves(round, deadline) }()

	if err = <-errs; err != nil {
		t.Fatal(err)
	}

	t.Fatalf("not killed within %s", crashChildMaxRun)
}

// isCrashArchive reports whether progress is an archive at some attempt count.
func isCrashArchive(progress int) bool {
	return progress%int(crashSteps) == int(crashArchive)
}

// readCrashFile calls fn with the key and progress of each line of a child's
// ack or intent file. A torn last line, which a kill can leave in a file that
// was not synced, is ignored.
func readCrashFile(t *testing.T, path string, fn func(key string, progress int)) {
	t.Helper()

	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}

	if err != nil {
		t.Fatalf("could not open %s: %s", path, err)
	}

	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, progress, errp := parseCrashLine(scanner.Text())
		if errp != nil {
			continue
		}

		fn(key, progress)
	}

	if err = scanner.Err(); err != nil {
		t.Fatalf("could not read %s: %s", path, err)
	}
}

// crashFileName is the name of round's ack or intent file.
func crashFileName(name string, round int) string {
	return fmt.Sprintf("%s.%d", name, round)
}

// killedBySIGKILL reports whether err, from a child's Wait, says SIGKILL ended
// it.
func killedBySIGKILL(err error) bool {
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return false
	}

	status, ok := exitErr.Sys().(syscall.WaitStatus)

	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL //nolint:misspell // syscall's method name
}

// openCrashAppend opens path for appending, creating it.
func openCrashAppend(t *testing.T, path string) *os.File {
	t.Helper()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, crashFileMode)
	if err != nil {
		t.Fatal(err)
	}

	return f
}
