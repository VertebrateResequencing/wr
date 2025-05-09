// Copyright © 2025 Genome Research Limited
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

package internal

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/shlex"
)

const (
	equalSplitParts = 2
)

var globRegex = regexp.MustCompile(`^(\./)?\*$`)

// CmdlineHasRelativePaths checks if the given command line has any arguments
// that are relative to the given directory. There may be false negatives, but
// the only false positives will be commands where a file in the dir has the
// same basename as an executable in the PATH, or mentions of the basename in
// text strings within the cmd.
func CmdlineHasRelativePaths(dir, cmdline string) bool {
	args, err := shlex.Split(cmdline)
	if err != nil {
		return false
	}

	for i, arg := range args {
		if i == 0 && isExe(arg) {
			continue
		}

		if globRegex.MatchString(arg) {
			return true
		}

		if argIsARelativePath(dir, arg) {
			return true
		}
	}

	return false
}

func isExe(arg string) bool {
	exe, _ := exec.LookPath(arg) //nolint:errcheck

	return exe != ""
}

// argIsARelativePath checks if the given fragment that came from a command line
// argument is a path relative to the given directory.
//
// NB: use github.com/google/shlex to split your command line into arguments,
// not github.com/mattn/go-shellwords, as the latter stops at ; and && etc.
func argIsARelativePath(dir, arg string) bool {
	if fileInDir(dir, arg) {
		return true
	}

	arg = cleanArg(arg)
	if arg == "" {
		return false
	}

	return fileInDir(dir, arg)
}

func fileInDir(dir, arg string) bool {
	_, err := os.Stat(filepath.Join(dir, arg))

	return err == nil
}

func cleanArg(arg string) string {
	arg = strings.TrimSuffix(arg, ")")
	arg = strings.TrimSuffix(arg, ";")

	parts := strings.Split(arg, "=")
	if len(parts) == equalSplitParts {
		arg = parts[1]
	}

	return arg
}
