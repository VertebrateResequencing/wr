// Copyright © 2016-2018 Genome Research Limited
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

// this file has general utility functions

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/inconshreveable/log15"
	"github.com/ricochet2200/go-disk-usage/du"
	"github.com/shirou/gopsutil/mem"
)

const gb = uint64(1.07374182e9) // for byte to GB conversion

var username string
var userid int

// SortMapKeysByIntValue sorts the keys of a map[string]int by its values,
// reversed if you supply true as the second arg.
func SortMapKeysByIntValue(imap map[string]int, reverse bool) []string {
	// from http://stackoverflow.com/a/18695428/675083 *** should also try the
	// idiomatic way to see if that's better in any way
	valToKeys := map[int][]string{}
	for key, val := range imap {
		valToKeys[val] = append(valToKeys[val], key)
	}
	var vals []int
	for val := range valToKeys {
		vals = append(vals, val)
	}

	if reverse {
		sort.Sort(sort.Reverse(sort.IntSlice(vals)))
	} else {
		sort.Sort(sort.IntSlice(vals))
	}

	var sortedKeys []string
	for _, val := range vals {
		sortedKeys = append(sortedKeys, valToKeys[val]...)
	}
	return sortedKeys
}

// SortMapKeysByMapIntValue sorts the keys of a map[string]map[string]int by
// a the values found at a given sub value, reversed if you supply true as the
// second arg.
func SortMapKeysByMapIntValue(imap map[string]map[string]int, criterion string, reverse bool) []string {
	criterionValueToKeys := make(map[int][]string)
	for key, submap := range imap {
		val := submap[criterion]
		criterionValueToKeys[val] = append(criterionValueToKeys[val], key)
	}
	var criterionValues []int
	for val := range criterionValueToKeys {
		criterionValues = append(criterionValues, val)
	}

	if reverse {
		sort.Sort(sort.Reverse(sort.IntSlice(criterionValues)))
	} else {
		sort.Sort(sort.IntSlice(criterionValues))
	}

	var sortedKeys []string
	for _, val := range criterionValues {
		sortedKeys = append(sortedKeys, criterionValueToKeys[val]...)
	}
	return sortedKeys
}

// Username returns the username of the current user. This avoids problems
// with static compilation as it avoids the use of os/user. It will only work
// on linux-like systems where 'id -u -n' works.
func Username() (string, error) {
	if username == "" {
		var err error
		username, err = parseIDCmd("-u", "-n")
		if err != nil {
			return "", err
		}
	}
	return username, nil
}

// Userid returns the user id of the current user. This avoids problems
// with static compilation as it avoids the use of os/user. It will only work
// on linux-like systems where 'id -u' works.
func Userid() (int, error) {
	if userid == 0 {
		uidStr, err := parseIDCmd("-u")
		if err != nil {
			return 0, err
		}
		userid, err = strconv.Atoi(uidStr)
		if err != nil {
			return 0, err
		}
	}
	return userid, nil
}

// parseIDCmd parses the output of the unix 'id' command.
func parseIDCmd(idopts ...string) (string, error) {
	idcmd := exec.Command("/usr/bin/id", idopts...) // #nosec
	idout, err := idcmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(idout), "\n"), err
}

// TildaToHome converts a path beginning with ~/ to the absolute path based in
// the current home directory (according to the environment variable $HOME).
func TildaToHome(path string) string {
	home := os.Getenv("HOME")
	if home != "" && strings.HasPrefix(path, "~/") {
		path = strings.TrimLeft(path, "~/")
		path = filepath.Join(home, path)
	}
	return path
}

// ProcMeminfoMBs uses gopsutil (amd64 freebsd, linux, windows, darwin, openbds
// only!) to find the total number of MBs of memory physically installed on the
// current system.
func ProcMeminfoMBs() (int, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return 0, err
	}

	// convert bytes to MB
	return int((v.Total / 1024) / 1024), err
}

// DiskSize returns the size of the disk (mounted at the given directory, "."
// for current) in GB.
func DiskSize() int {
	usage := du.NewDiskUsage(".")
	return int(usage.Size() / gb)
}

// LogClose is for use to Close() an object during a defer when you don't care
// if the Close() returns an error, but do want non-EOF errors logged. Extra
// args are passed as additional context for the logger.
func LogClose(logger log15.Logger, obj io.Closer, msg string, extra ...interface{}) {
	err := obj.Close()
	if err != nil && err.Error() != "EOF" {
		extra = append(extra, "err", err)
		logger.Warn("failed to close "+msg, extra...)
	}
}

// LogPanic is for use in a go routines, deferred at the start of them, to
// figure out what is causing runtime panics. If the die bool is true, the
// program exits, otherwise it continues, after logging the error message and
// stack trace. Desc string should be used to describe briefly what the
// goroutine you call this in does.
func LogPanic(logger log15.Logger, desc string, die bool) {
	if err := recover(); err != nil {
		logger.Crit(desc+" panic", "err", err)
		if die {
			os.Exit(1)
		}
	}
}
