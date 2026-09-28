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

package cmd

import (
	"slices"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/shirou/gopsutil/v4/process"
)

// deploymentFlag is the root flag naming the deployment a command acts on.
const deploymentFlag = "--deployment"

// sshForwarderFlags is the distinctive ssh option cluster startForwarding()
// runs every forwarder with, by which isForwarderProcess recognises one.
const sshForwarderFlags = "-qngNTL"

// isManagerProcess reports whether pid is a live daemonized wr manager for
// deployment, judged by its argv. The argv is what daemonize() gave the child,
// so it is unaffected by the binary being renamed, moved or replaced in place
// since the manager started (which would fool a check of /proc/<pid>/exe, that
// reads "... (deleted)" after an upgrade).
func isManagerProcess(pid int, deployment string) bool {
	return isManagerArgs(processArgs(pid), deployment)
}

// isManagerArgs reports whether args are those of `wr manager start` for
// deployment: argv[0] is the binary, whatever it is called, "manager" is
// followed somewhere by "start", and daemonize() always adds a deploymentFlag
// for the deployment.
func isManagerArgs(args []string, deployment string) bool {
	manager := slices.Index(args, "manager")
	if manager < 1 || !slices.Contains(args[manager+1:], "start") {
		return false
	}

	return hasDeploymentArg(args, deployment)
}

// hasDeploymentArg reports whether args set deploymentFlag to deployment, in
// either its "--deployment x" or "--deployment=x" form.
func hasDeploymentArg(args []string, deployment string) bool {
	for i, arg := range args {
		if arg == deploymentFlag+"="+deployment {
			return true
		}

		if arg == deploymentFlag && i+1 < len(args) && args[i+1] == deployment {
			return true
		}
	}

	return false
}

// isForwarderProcess reports whether pid is a live ssh port forwarder started by
// startForwarding(), judged by its argv.
func isForwarderProcess(pid int) bool {
	return slices.Contains(processArgs(pid), sshForwarderFlags)
}

// processArgs returns the argv of the live process pid, or nil if there is no
// such process or its argv cannot be read. It reads /proc/<pid>/cmdline on
// Linux and the kern.procargs2 sysctl on macOS (via gopsutil, without cgo). A
// zombie has an empty argv, so it also gives nil.
func processArgs(pid int) []string {
	if !internal.ValidPid(pid) {
		return nil
	}

	p, err := process.NewProcess(int32(pid)) //nolint:gosec // ValidPid bounds pid to int32
	if err != nil {
		return nil
	}

	args, err := p.CmdlineSlice()
	if err != nil || len(args) == 0 {
		return nil
	}

	return args
}
