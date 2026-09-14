/*******************************************************************************
 * Copyright (c) 2016-2018, 2021, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: [Theo Barber-Bany] <theobarberbany@gmail.com>
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

package cmd

// this is the cobra file that enables subcommands and handles command-line args

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
)

// maxCloudResourceUsernameLength is the maximum length that cloud username can
// be. It is limited because it will form part of cloudResourceName(), which
// will in turn form hostnames, which have max length 63. cloudResourceName()
// has a fixed prefix of length up to 8, and host names will include a UUID of
// length 36 and a prefix length 1, leaving 18 characters for the username.
const maxCloudResourceUsernameLength = 18

// defaultManagerConnectTimeout is the default number of seconds the client
// waits for a reply from 'wr manager'.
const defaultManagerConnectTimeout = 120

// defaultJobRetries is the default number of automatic retries for a failed
// command.
const defaultJobRetries = 3

// lsfCommandName is the name of the lsf sub-command, also prepended to args when
// emulating LSF.
const lsfCommandName = "lsf"

// daemon-related constants.
const (
	daemonPidFilePerm  = 0o644
	daemonStopGiveupS  = 120
	daemonStopPollFreq = 50 * time.Millisecond
)

// managerDirPerm is the permission createWorkingDir makes the manager's working
// directory with. That directory holds the database, the client token and the
// TLS key, and deleting or replacing any of those needs write permission on the
// DIRECTORY, not on the file, so however tightly the files themselves are
// locked down the directory has to be the owner's alone.
const managerDirPerm = 0o700

// managerDirOtherWritePerms are the bits that would let somebody other than the
// working directory's owner delete, replace or plant a file inside it.
const managerDirOtherWritePerms = 0o022

// uploadDirOtherPerms are every permission bit a user other than the owner has
// on a directory. Clearing them repairs an old upload tree to the 0700 that
// jobqueue now makes new upload directories with.
const uploadDirOtherPerms = os.ModePerm &^ managerDirPerm

// uploadHashedLevels is how many single-character directory levels
// jobqueue's calculateHashedDir puts below the upload directory:
// mkHashedLevels (4) parts of an md5, the last of which is the file name. The
// deepest level holds only uploaded files, so nothing below it is walked.
const uploadHashedLevels = 3

// these variables are accessible by all subcommands.
var (
	deployment string
	config     *internal.Config
)

// these are shared by some of the subcommands.
var (
	caFile     string
	timeoutint int
	cmdCwd     string
)

// RootCmd represents the base command when called without any subcommands.
var RootCmd = &cobra.Command{
	Use:   "wr",
	Short: "wr is a software workflow management system.",
	Long: `wr is a software workflow management system and command runner.

You use it to run the same sequence of commands (a "workflow") on many different
input files (which comprise a "datasource").

Initially, you start the management system, which maintains a queue of the
commands you want to run:
$ wr manager start

Then you add commands you want to run to the queue:
$ wr add

At this point your commands should be running, and you can monitor their
progress with:
$ wr status`,
}

// cmdExit lets tests drive a command's Run past the point where die() would
// otherwise terminate the test process (mirroring statusExit in status.go). In
// production it is os.Exit, so die() ends the process non-zero exactly as
// before.
var cmdExit = os.Exit

// errDirIsSymlink is why wr will not take write permission off a path that is
// itself a symlink.
var errDirIsSymlink = errors.New("it is a symlink")

// closeWorkingDirToOthers takes other users' write permission off an existing
// working directory, which is how an older wr's 0777 or 0775 stops letting
// anybody on the machine delete, replace or plant a file in it.
//
// ONLY those bits are cleared, and the rest of the mode is left as it was.
// Clearing just them closes the hole without disturbing a directory somebody
// opened up on purpose: a deliberate 0750, to let a colleague read ca.pem,
// still reads 0750 afterwards. A sticky directory is not exempt, because
// sticky only stops others REMOVING a file they do not own - it still lets
// them create one, and a file they own, sitting where wr expects to write
// client.token, is read by them after wr fills it in.
func closeWorkingDirToOthers(mode os.FileMode) {
	closed, err := takeOtherWriteOff(config.ManagerDir, mode)

	switch {
	case errors.Is(err, errDirIsSymlink):
		// no mode is quoted here: the one we were given came from a stat that
		// followed the link, so it describes the target, not what the user
		// would be looking at.
		warn("the working directory '%s' is a symlink, so wr will not change its mode - following it "+
			"would chmod whatever it points at. Check the target yourself: users other than you must "+
			"not be able to write to the directory holding the database, the client token and the TLS "+
			"key, and 'chmod g-w,o-w %s' does that",
			config.ManagerDir, config.ManagerDir)
	case err != nil:
		warn("the working directory '%s' is %s, so users other than you can delete or replace the "+
			"database, the client token and the TLS key in it, and wr could not change that: %s. "+
			"Run 'chmod g-w,o-w %s' yourself, or ask the directory's owner to",
			config.ManagerDir, mode, err, config.ManagerDir)
	case closed != mode:
		// the modes are logged in the Go spelling of a mode rather than in
		// octal, because the sticky bit this deliberately preserves has no
		// digit in os.FileMode.Perm() and would silently vanish from the
		// message. Note that Go writes a sticky directory 'dtrwxr-xr-x' where
		// ls -l writes 'drwxr-xr-t'.
		info("changed the working directory '%s' from %s to %s, so that users other than you can no "+
			"longer delete or replace the database, the client token and the TLS key in it",
			config.ManagerDir, mode, closed)
	}
}

// takeOtherWriteOff takes the write permission of users other than the owner
// off dir, which currently has mode, and returns the mode it now has. Every
// other bit, the sticky and setgid bits included, is left exactly as it was.
//
// A dir that is ITSELF a symlink is refused rather than followed. os.Chmod
// follows one, so a symlink planted where wr expects its directory would aim
// this chmod at a target of somebody else's choosing. Planting it needs write
// permission on an ancestor - normally the user's own home - so this is a hole
// only for an unusual layout or a manager run as root, but refusing costs
// nothing.
func takeOtherWriteOff(dir string, mode os.FileMode) (os.FileMode, error) {
	closed := mode &^ managerDirOtherWritePerms
	if closed == mode {
		return mode, nil
	}

	fi, err := os.Lstat(dir)
	if err != nil {
		return mode, err
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		return mode, errDirIsSymlink
	}

	file, err := openWorkingDir(dir)
	if err != nil {
		return mode, err
	}
	defer file.Close()

	changed, err := chmodWorkingDir(file, closed)
	if err != nil {
		return mode, err
	}

	if !changed {
		return mode, nil
	}

	return closed, nil
}

func openWorkingDir(dir string) (*os.File, error) {
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return nil, err
	}
	defer parent.Close()

	file, err := parent.Open(filepath.Base(dir))
	if err != nil {
		// os.Root.Open can report a rejected final symlink as its internal
		// path-escape error rather than syscall.ELOOP. Recheck the final
		// component so both forms receive the same user-facing warning.
		if isFinalSymlink(dir) || errors.Is(err, syscall.ELOOP) {
			return nil, errDirIsSymlink
		}

		return nil, err
	}

	// os.Root.Open reports a final symlink through an internal error while it
	// follows the link. Check the name after opening so that error is converted
	// to the user-facing classification without depending on os internals.
	fi, err := os.Lstat(dir)
	if err != nil {
		file.Close()

		return nil, err
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		file.Close()

		return nil, errDirIsSymlink
	}

	return file, nil
}

// closeUploadDirToOthers takes every permission other users have off each
// directory in the manager's upload tree, which an older wr made 0777 before
// the umask.
//
// os.MkdirAll leaves the mode of a directory that already exists alone,
// whatever mode jobqueue gives the ones it makes, so the install that needs
// this most is the one that has been running for years. The exposure is not
// just the files: what `wr add --cloud_config_files` uploads is copied to
// every cloud server the manager spawns and defaults to the submitter's
// ~/.s3cfg and AWS credentials, and write permission on a directory is what it
// takes to rename the directories INSIDE it aside and put your own there. So
// every level is closed, not just the top: write on the upload directory moves
// the hashed levels, write on a hashed level moves the one below it, and a
// closed parent does not stop a process whose cwd or descriptor is already on
// one.
//
// Read and search are taken off along with write, so a repaired level is the
// same 0700 as one jobqueue makes today. Unlike the working directory, nothing
// in the tree is readable by anybody but the owner - every file in it is 0600
// - so no other user has a use for those bits, and leaving them would only
// let them list what was uploaded and when.
//
// The walk is bounded by the hash fan-out rather than by the upload count:
// calculateHashedDir splits an md5 into mkHashedLevels (4) parts, so the tree
// is uploadHashedLevels single-hex-character levels deep and can never hold
// more than 16 + 256 + 4096 = 4368 directories however many files are
// uploaded. The deepest level holds only the files, so it is closed but not
// read, and the READDIR work is bounded the same way as the chmods. It is paid
// once per manager start.
//
// A relocated manageruploaddir is deliberately left alone: wr repairs the tree
// it owns inside its own working directory, and must not chmod a path an
// operator has pointed somewhere else, which could be shared with other people
// or could be /tmp - the fallback jobqueue uses when a Server is built without
// an upload directory at all.
func closeUploadDirToOthers() {
	rel, inside := uploadDirInManagerDir()
	if !inside {
		return
	}

	root, err := openManagerDirRoot()
	if err != nil {
		return
	}

	defer root.Close()

	if closed := closeUploadTreeIn(root, rel); closed > 0 {
		// "%d of the directories" rather than "%d directories" so that the
		// count does not have to agree with the noun after it: this line is
		// user-facing and there is regularly only one.
		info("made %d of the directories in the upload directory '%s' yours alone, so that other "+
			"users can no longer replace the config files wr copies to cloud servers",
			closed, config.ManagerUploadDir)
	}
}

// uploadDirInManagerDir returns the upload directory as a slash-separated path
// relative to the working directory, and whether it is strictly below it.
//
// An upload directory that IS the working directory is refused, although
// filepath.IsLocal accepts ".": walking from there would make the working
// directory itself owner-only, overriding a deliberate 0750 that
// closeWorkingDirToOthers leaves alone, along with every directory in it.
func uploadDirInManagerDir() (string, bool) {
	rel, err := filepath.Rel(config.ManagerDir, config.ManagerUploadDir)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}

	return filepath.ToSlash(rel), true
}

// openManagerDirRoot opens the working directory as an os.Root, so that every
// path resolved below it is checked against escaping it.
//
// It REFUSES a working directory that is itself a symlink, and that refusal is
// what stops the walk being a way round takeOtherWriteOff's. os.OpenRoot
// resolves the path it is given like any other, and the upload directory is
// <ManagerDir>/uploads, so without this a symlink planted where the working
// directory should be would be followed on the way to the walk root - aiming
// the same chmod primitive at more targets than the single one ever could,
// one line after wr had refused to follow that link.
func openManagerDirRoot() (*os.Root, error) {
	if isFinalSymlink(config.ManagerDir) {
		return nil, errDirIsSymlink
	}

	return os.OpenRoot(config.ManagerDir)
}

func isFinalSymlink(path string) bool {
	fi, err := os.Lstat(path)

	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// closeUploadTreeIn closes every directory of the upload tree at rel inside
// root to other users, returning how many it changed.
func closeUploadTreeIn(root *os.Root, rel string) int {
	closed := 0

	// the walk's own error is discarded along with every error inside it: the
	// callback only ever stops descending, never the walk, so the only one it
	// can return is a failure to read the top of a tree wr is merely trying to
	// repair, and a level wr cannot read is a level it cannot fix.
	_ = fs.WalkDir(root.FS(), rel, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
		if err != nil || !d.IsDir() {
			return nil //nolint:nilerr // see above: an unreadable entry is one wr cannot fix
		}

		if closedToOthersIn(root, path) {
			closed++
		}

		if strings.Count(strings.TrimPrefix(path, rel), "/") >= uploadHashedLevels {
			return fs.SkipDir
		}

		return nil
	})

	return closed
}

// closedToOthersIn takes every permission other users have off the directory
// at path inside root, reporting whether that changed its mode.
//
// The directory is OPENED and then read and changed through that one
// descriptor, rather than stat'd and chmod'd by path. The walk's whole premise
// is a tree another user can write to, so that user can swap a directory for a
// symlink between the two calls; fstat and fchmod on a descriptor cannot be
// redirected that way, and the os.Root the descriptor came from will not
// resolve a path out of the working directory in the first place. What remains
// is bounded rather than eliminated: a swap made before the open can still
// point wr at a different directory INSIDE its own working directory, which is
// wr's to chmod anyway.
func closedToOthersIn(root *os.Root, path string) bool {
	dir, err := root.Open(filepath.FromSlash(path))
	if err != nil {
		return false
	}

	defer dir.Close()

	fi, err := dir.Stat()
	if err != nil {
		return false
	}

	closed := fi.Mode() &^ uploadDirOtherPerms
	if closed == fi.Mode() {
		return false
	}

	changed, err := chmodWorkingDir(dir, closed)

	return err == nil && changed
}

func chmodWorkingDir(file *os.File, mode os.FileMode) (bool, error) {
	fi, err := file.Stat()
	if err != nil {
		return false, err
	}

	if !fi.IsDir() {
		return false, nil
	}

	if err = file.Chmod(mode); err != nil {
		return false, err
	}

	return true, nil
}

// Execute adds all child commands to the root command and sets flags
// appropriately. This is called by main.main(). It only needs to happen once to
// the rootCmd.
func Execute() {
	if err := RootCmd.Execute(); err != nil {
		die("%s", err.Error())
	}
}

// ExecuteLSF is for treating a call to wr as if `wr lsf xxx` was called, for
// the LSF emulation to work.
func ExecuteLSF(cmd string) {
	args := append([]string{lsfCommandName, cmd}, os.Args[1:]...)

	command, _, err := RootCmd.Find(args)
	if err != nil {
		die("%s", err.Error())
	}

	RootCmd.SetArgs(args)

	if err := command.Execute(); err != nil {
		die("%s", err.Error())
	}
}

func init() {
	// set up logging to stderr
	clog.ToDefaultAtLevel("info")

	// global flags
	RootCmd.PersistentFlags().StringVar(&deployment, "deployment", internal.DefaultDeployment(context.Background()),
		"use production or development config")

	cobra.OnInitialize(initConfig)
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	config = internal.ConfigLoadFromCurrentDir(context.Background(), deployment)
	clog.ConfigureFileRotation(clog.FileRotationConfig{
		MaxSizeMB:  config.LogsMaxSizeMB,
		MaxBackups: config.LogsMaxBackups,
		MaxAgeDays: config.LogsMaxAgeDays,
		Compress:   config.LogsCompress,
	})
	caFile = config.ManagerCAFile
}

// managerAddrFile returns the path to the file that stores the manager's actual address.
func managerAddrFile() string {
	return filepath.Join(filepath.Dir(config.ManagerTokenFile), "manager.addr")
}

// token reads and returns the token from the file created when the manager
// starts.
func token() ([]byte, error) {
	token, err := os.ReadFile(config.ManagerTokenFile)
	if err != nil {
		return nil, err
	}

	return token, nil
}

// managerAddr reads and returns the address from the file created when the manager
// starts.
func managerAddr() (string, error) {
	addrBytes, err := os.ReadFile(managerAddrFile())
	if err != nil {
		return "", err
	}

	return string(addrBytes), nil
}

// realUsername returns the username of the current user.
func realUsername() string {
	username, err := internal.Username()
	if err != nil {
		die("could not get username: %s", err)
	}

	return username
}

// cloudResourceName returns a user and deployment specific string that can be
// used to name cloud resources so they can be identified as having been created
// by wr. username arg defaults to the real username of the user running wr.
func cloudResourceName(username string) string {
	if username == "" {
		username = realUsername()
	}

	var dep string
	if config.Deployment == internal.Production {
		dep = "prod"
	} else {
		dep = "dev"
	}

	return "wr-" + dep + "-" + username
}

// info is a convenience to log a message at the Info level.
func info(msg string, a ...any) {
	clog.Info(context.Background(), fmt.Sprintf(msg, a...))
}

// warn is a convenience to log a message at the Warn level.
func warn(msg string, a ...any) {
	clog.Warn(context.Background(), fmt.Sprintf(msg, a...))
}

// die is a convenience to log a message at the Error level and exit non zero.
func die(msg string, a ...any) {
	clog.Error(context.Background(), fmt.Sprintf(msg, a...))
	cmdExit(1)
}

// createWorkingDir ensures the main working directory is available, and that
// no user other than its owner can write to it or reach into the upload
// directory inside it.
//
// An EXISTING directory is not forced to managerDirPerm, because this runs on
// every manager start and every cloud deploy, and re-imposing a whole mode
// would silently undo an owner's chmod every time. Only the bits that are a
// hole are cleared; see closeWorkingDirToOthers.
func createWorkingDir() {
	fi, err := os.Stat(config.ManagerDir)
	if err == nil {
		// a working directory that is not a directory at all is left entirely
		// alone: wr dies on it moments later either way, and chmodding a file
		// somebody else put there is a side effect nobody asked for.
		if fi.IsDir() {
			closeWorkingDirToOthers(fi.Mode())
			closeUploadDirToOthers()
		}

		return
	}

	if !os.IsNotExist(err) {
		die("could not access or create the working directory '%s': %v", config.ManagerDir, err)
	}

	// try and create the directory
	if err = os.MkdirAll(config.ManagerDir, managerDirPerm); err != nil {
		die("could not create the working directory '%s': %v", config.ManagerDir, err)
	}
}

// daemonize spawns a child copy of ourselves with the correct deployment (we
// need to be careful because the default deployment depends on current dir, and
// the child is forced to run from /). Supplying extraArgs can override earlier
// args (to eg. re-specify an option with a relative path with an absolute
// path).
func daemonize(pidFile string, umask int, extraArgs ...string) (*os.Process, *daemon.Context) {
	args := os.Args

	hadDeployment := slices.Contains(args, "--deployment")
	if !hadDeployment {
		args = append(args, "--deployment")
		args = append(args, config.Deployment)
	}

	args = append(args, extraArgs...)

	dContext := &daemon.Context{
		PidFileName: pidFile,
		PidFilePerm: daemonPidFilePerm,
		WorkDir:     "/",
		Args:        args,
		Umask:       umask,
	}

	return reborn(dContext, pidFile), dContext
}

// reborn calls Reborn() on the given context, retrying once after deleting the
// pid file if the first attempt fails. Dies if the retry also fails.
func reborn(dContext *daemon.Context, pidFile string) *os.Process {
	child, err := dContext.Reborn()
	if err == nil {
		return child
	}

	// try again, deleting the pidFile first
	if errr := os.Remove(pidFile); errr != nil && !os.IsNotExist(errr) {
		warn("failed to delete existing pid file: %s", errr)
	}

	child, err = dContext.Reborn()
	if err != nil {
		die("failed to daemonize: %s", err)
	}

	return child
}

// stopdaemon stops the daemon created by daemonize() by sending it SIGTERM and
// checking it really exited.
func stopdaemon(pid int, source string) bool {
	err := syscall.Kill(pid, syscall.SIGTERM)
	if err != nil {
		warn("wr manager is running with pid %d according to %s, but failed to send it SIGTERM: %s", pid, source, err)

		return false
	}

	ok := waitForDaemonStop(pid)

	// if it didn't stop, offer to force kill it? That's a bit dangerous...
	// just warn for now
	if !ok {
		warn("wr manager, running with pid %d according to %s, is still running %ds after I sent it a SIGTERM",
			pid, source, daemonStopGiveupS)
	}

	return ok
}

// waitForDaemonStop polls the given pid until it is no longer running, or until
// we give up after daemonStopGiveupS seconds. It returns true if the pid
// stopped.
func waitForDaemonStop(pid int) bool {
	giveup := time.After(time.Duration(daemonStopGiveupS) * time.Second)
	ticker := time.NewTicker(daemonStopPollFreq)
	stopped := make(chan bool, 1)

	go func() {
		for {
			select {
			case <-ticker.C:
				if syscall.Kill(pid, syscall.Signal(0)) == nil {
					// pid is still running
					continue
				}
				// assume the error was "no such process" *** should I do a string comparison to confirm?
				ticker.Stop()

				stopped <- true

				return
			case <-giveup:
				ticker.Stop()

				stopped <- false

				return
			}
		}
	}()

	return <-stopped
}

// sAddr gets a nice manager address to report in logs, preferring hostname,
// falling back on the ip address if that wasn't set.
func sAddr(s *jobqueue.ServerInfo) string {
	saddr := s.Host
	if saddr == "localhost" {
		saddr = s.Addr
	} else {
		saddr += ":" + s.Port
	}

	return saddr
}

// connect gives you a connected client. Dies on error. Dies if there is no
// token file. Does not die or report any kind of error if an optional bool is
// supplied true.
func connect(wait time.Duration, expectedToBeDown ...bool) *jobqueue.Client {
	shouldWarn := len(expectedToBeDown) != 1 || !expectedToBeDown[0]

	token, err := token()
	if err != nil && shouldWarn {
		die("could not read token file; has the manager been started? [%s]", err)
	}

	// try to get the actual address from the manager.addr file first
	if jq := connectViaAddrFile(token, wait, shouldWarn); jq != nil {
		return jq
	}

	// fall back to using the config-defined address
	jq, err := jobqueue.Connect(config.ManagerHost+":"+config.ManagerPort, caFile, config.ManagerCertDomain, token, wait)
	if err != nil && shouldWarn {
		die("%s", err)
	}

	return jq
}

// connectViaAddrFile attempts to connect using the address stored in the
// manager.addr file. It returns nil if there is no such file or the connection
// fails (warning in the latter case if shouldWarn is true).
func connectViaAddrFile(token []byte, wait time.Duration, shouldWarn bool) *jobqueue.Client {
	serverAddr, addrErr := managerAddr()
	if addrErr != nil {
		return nil
	}

	jq, err := jobqueue.Connect(serverAddr, caFile, config.ManagerCertDomain, token, wait)
	if err == nil {
		return jq
	}

	if shouldWarn {
		warn("failed to connect to manager at address from file (%s): %s, falling back to config address",
			serverAddr, err)
	}

	return nil
}
