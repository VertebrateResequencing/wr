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

// This file contains the one account of the directories wr made for a Job's run
// and of what they mean to the environment its commands get.
//
// Both of the commands wr runs for a Job take their environment from
// envWithRunDirs - Client.Execute the Job's own Cmd, and
// jobWorkSpaceSnapshot.runEnv a `run` Behaviour's - so a variable put in here
// cannot reach one of them and miss the other.

import (
	"slices"
	"strings"
)

// jobRunDirs are the directories a Job's commands run in and are told about: the
// Cwd the Job was added with, the working directory wr created below it, and the
// tmp dir wr created beside that.
//
// cwd is always the Job's own Cwd. actualCwd and tmp are the two wr makes, and
// it makes them only for a Job whose Cwd does not matter, so those two arrive
// together or not at all; see wrMade.
type jobRunDirs struct {
	// cwd is the Job's own Cwd, which is what JobCwdEnvVar names - not the
	// working directory below it, so that every generation of Jobs added by Jobs
	// works in a sibling of this one rather than inside it.
	cwd string

	// actualCwd is the directory the commands run in, which --change_home makes
	// HOME. Where wr made one below cwd that is it; where it made none this is
	// cwd itself and nothing reads it, since --change_home has no working
	// directory of wr's to point HOME at.
	actualCwd string

	// tmp is the dir wr made beside actualCwd to be TMPDIR.
	tmp string
}

// wrMade says whether wr made these directories, ie. whether it moved the run
// out of the Job's Cwd.
//
// The tmp dir is the condition rather than a flag of its own because
// Client.resolveWorkingDir makes no directories at all for a Job whose Cwd
// matters, and mkHashedDir returns the working directory and the tmp dir
// together, so a tmp dir exists exactly when wr created a workspace.
func (d jobRunDirs) wrMade() bool {
	return d.tmp != ""
}

// envWithRunDirs returns env with the directories wr made for this run put into
// it: TMPDIR, JobCwdEnvVar, and, when changeHome is set, the working directory
// as HOME.
//
// Where wr made no directories the run happens in the Job's Cwd itself, so there
// is nothing to override - and any JobCwdEnvVar inherited from the Job that added
// this one has to go, because os.Getwd() is then the right answer for anything
// this run adds, while the inherited value names a different directory. So the
// variable is present exactly when wr moved the run out of its Cwd, in both
// directions.
//
// A nil env comes back nil, since there is neither anything to delete from nor
// anything to override; that is not the invariant holding, but there being no
// environment to hold it over. Naming one - this process's, where the Job named
// none of its own - is the caller's business; see jobWorkSpaceSnapshot.runEnv.
func envWithRunDirs(env []string, dirs jobRunDirs, changeHome bool) []string {
	if !dirs.wrMade() {
		return slices.DeleteFunc(env, func(envvar string) bool {
			return strings.HasPrefix(envvar, JobCwdEnvVar+"=")
		})
	}

	// (this works fine even if a dir has a space in one of its names)
	over := []string{"TMPDIR=" + dirs.tmp, JobCwdEnvVar + "=" + dirs.cwd}

	if changeHome {
		over = append(over, "HOME="+dirs.actualCwd)
	}

	return envOverride(env, over)
}
