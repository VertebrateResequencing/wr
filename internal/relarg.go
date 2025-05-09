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
	"path/filepath"
	"strings"

	"github.com/google/shlex"
)

const (
	equalSplitParts = 2
)

// CmdlineHasRelativePaths checks if the given command line has any arguments
// that are relative to the given directory. There may be false negatives, but
// not false positives.
func CmdlineHasRelativePaths(dir, cmdline string) bool {
	args, err := shlex.Split(cmdline)
	if err != nil {
		return false
	}

	for _, arg := range args {
		if argIsARelativePath(dir, arg) {
			return true
		}
	}

	return false
}

// argIsARelativePath checks if the given fragment that came from a command line
// argument is a path relative to the given directory. There may be false
// negatives, but not false positives.
//
// NB: use github.com/google/shlex to split your command line into arguments,
// not github.com/mattn/go-shellwords, as the latter stops at ; and && etc.
func argIsARelativePath(dir, arg string) bool {
	path := filepath.Join(dir, arg)

	_, err := os.Stat(path)
	if err == nil {
		return true
	}

	arg = cleanArg(arg)
	if arg == "" {
		return false
	}

	path = filepath.Join(dir, arg)
	_, err = os.Stat(path)

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
