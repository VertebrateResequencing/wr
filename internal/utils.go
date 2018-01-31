// Copyright © 2016 Genome Research Limited
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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var username string
var userid int

// SortMapKeysByIntValue sorts the keys of a map[string]int by its values,
// reversed if you supply true as the second arg.
func SortMapKeysByIntValue(imap map[string]int, reverse bool) (sortedKeys []string) {
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

	for _, val := range vals {
		sortedKeys = append(sortedKeys, valToKeys[val]...)
	}

	return
}

// SortMapKeysByMapIntValue sorts the keys of a map[string]map[string]int by
// a the values found at a given sub value, reversed if you supply true as the
// second arg.
func SortMapKeysByMapIntValue(imap map[string]map[string]int, criterion string, reverse bool) (sortedKeys []string) {
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

	for _, val := range criterionValues {
		sortedKeys = append(sortedKeys, criterionValueToKeys[val]...)
	}

	return
}

// Username returns the username of the current user. This avoids problems
// with static compilation as it avoids the use of os/user. It will only work
// on linux-like systems where 'id -u -n' works.
func Username() (uname string, err error) {
	if username == "" {
		username, err = parseIDCmd("-u", "-n")
		if err != nil {
			return
		}
	}
	uname = username
	return
}

// Userid returns the user id of the current user. This avoids problems
// with static compilation as it avoids the use of os/user. It will only work
// on linux-like systems where 'id -u' works.
func Userid() (uid int, err error) {
	if userid == 0 {
		var uidStr string
		uidStr, err = parseIDCmd("-u")
		if err != nil {
			return
		}
		userid, err = strconv.Atoi(uidStr)
		if err != nil {
			return
		}
	}
	uid = userid
	return
}

// parseIDCmd parses the output of the unix 'id' command.
func parseIDCmd(idopts ...string) (user string, err error) {
	idcmd := exec.Command("/usr/bin/id", idopts...) // #nosec
	var idout []byte
	idout, err = idcmd.Output()
	if err != nil {
		return
	}
	user = strings.TrimSuffix(string(idout), "\n")
	return
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
