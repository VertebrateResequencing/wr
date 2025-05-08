// Copyright © 2019 Genome Research Limited
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

package limiter

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This file contains the implementation of the group stuct.

type groupMode uint8

const (
	groupModeUnknown groupMode = iota
	groupModeCount
	groupModeBeforeTime
	groupModeAfterTime
	groupModeBetweenTimes
	groupModeBeforeDateTime
	groupModeAfterDateTime
	groupModeBetweenDateTimes
)

// GroupData represents the data of a limit group.
type GroupData struct {
	mode    groupMode
	limit   int64 // min
	current int64 // max
}

var (
	beforeTimeLimit       = regexp.MustCompile(`^time *< *(\d\d:\d\d:\d\d)$`)
	afterTimeLimit        = regexp.MustCompile(`^(\d\d:\d\d:\d\d) *< *time$`)
	betweenTimesLimit     = regexp.MustCompile(`^(\d\d:\d\d:\d\d) *< *time *< *(\d\d:\d\d:\d\d)$`)
	beforeDateTimeLimit   = regexp.MustCompile(`^datetime *< *(\d\d\d\d-\d\d-\d\d \d\d:\d\d:\d\d)$`)
	afterDateTimeLimit    = regexp.MustCompile(`^(\d\d\d\d-\d\d-\d\d \d\d:\d\d:\d\d) *< *datetime$`)
	betweenDateTimesLimit = regexp.MustCompile(`^(\d\d\d\d-\d\d-\d\d \d\d:\d\d:\d\d) *< *datetime *< *(\d\d\d\d-\d\d-\d\d \d\d:\d\d:\d\d)$`) //nolint:lll
)

const maxRunLimitGroupParts = 2

func countLimitParse(name string) (string, *GroupData) {
	parts := strings.SplitN(name, ":", maxRunLimitGroupParts)
	if len(parts) > 1 {
		limit, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return "", nil
		}

		return parts[0], NewCountGroupData(limit)
	}

	return name, nil
}

func beforeTimeLimitParse(name string) (string, *GroupData) {
	matches := beforeTimeLimit.FindStringSubmatch(name)
	if len(matches) == 0 {
		return "", nil
	}

	t, err := timeToSecs(matches[1])
	if err != nil {
		return "", nil
	}

	return name, &GroupData{mode: groupModeBeforeTime, limit: t}
}

func timeToSecs(timestr string) (int64, error) {
	t, err := time.Parse(time.TimeOnly, timestr)
	if err != nil {
		return 0, err
	}

	return int64(t.Hour())*3600 + int64(t.Minute())*60 + int64(t.Second()), nil
}

func afterTimeLimitParse(name string) (string, *GroupData) {
	matches := afterTimeLimit.FindStringSubmatch(name)
	if len(matches) == 0 {
		return "", nil
	}

	t, err := timeToSecs(matches[1])
	if err != nil {
		return "", nil
	}

	return name, &GroupData{mode: groupModeAfterTime, current: t}
}

func betweenTimesLimitParse(name string) (string, *GroupData) {
	matches := betweenTimesLimit.FindStringSubmatch(name)
	if len(matches) == 0 {
		return "", nil
	}

	t, err := timeToSecs(matches[1])
	if err != nil {
		return "", nil
	}

	u, err := timeToSecs(matches[2])
	if err != nil {
		return "", nil
	}

	return name, &GroupData{mode: groupModeBetweenTimes, current: t, limit: u}
}

func beforeDateTimeLimitParse(name string) (string, *GroupData) {
	matches := beforeDateTimeLimit.FindStringSubmatch(name)
	if len(matches) == 0 {
		return "", nil
	}

	t, err := time.ParseInLocation(time.DateTime, matches[1], time.Local) //nolint:gosmopolitan
	if err != nil {
		return "", nil
	}

	return name, &GroupData{mode: groupModeBeforeDateTime, limit: t.Unix()}
}

func afterDateTimeLimitParse(name string) (string, *GroupData) {
	matches := afterDateTimeLimit.FindStringSubmatch(name)
	if len(matches) == 0 {
		return "", nil
	}

	t, err := time.ParseInLocation(time.DateTime, matches[1], time.Local) //nolint:gosmopolitan
	if err != nil {
		return "", nil
	}

	return name, &GroupData{mode: groupModeAfterDateTime, current: t.Unix()}
}

func betweenDateTimesLimitParse(name string) (string, *GroupData) {
	matches := betweenDateTimesLimit.FindStringSubmatch(name)
	if len(matches) == 0 {
		return "", nil
	}

	t, err := time.ParseInLocation(time.DateTime, matches[1], time.Local) //nolint:gosmopolitan
	if err != nil {
		return "", nil
	}

	u, err := time.ParseInLocation(time.DateTime, matches[2], time.Local) //nolint:gosmopolitan
	if err != nil {
		return "", nil
	}

	return name, &GroupData{mode: groupModeBetweenDateTimes, current: t.Unix(), limit: u.Unix()}
}

// NameToGroupData parses the limit group name to determine the type of limit
// group, returning the group name and the limit group data.
func NameToGroupData(name string) (string, *GroupData) {
	for _, fn := range [...]func(string) (string, *GroupData){
		beforeTimeLimitParse,
		afterTimeLimitParse,
		betweenTimesLimitParse,
		beforeDateTimeLimitParse,
		afterDateTimeLimitParse,
		betweenDateTimesLimitParse,
		countLimitParse,
	} {
		n, d := fn(name)
		if d != nil {
			return n, d
		}
	}

	return name, nil
}

// NewCountGroupData returns a GroupData for a simple run limit group with the
// given limit.
func NewCountGroupData(limit int64) *GroupData {
	if limit < 0 {
		return &GroupData{}
	}

	return &GroupData{mode: groupModeCount, limit: limit}
}

// IsCount returns true if this group data represents a simple run limit group.
func (g *GroupData) IsCount() bool {
	return g.mode == groupModeCount
}

// IsValid returns true if this GroupData represents a valid limit group.
func (g *GroupData) IsValid() bool {
	return g != nil && g.mode >= groupModeCount && g.mode <= groupModeBetweenDateTimes
}

// Limit returns the maximum number of jobs that can be scheduled with this
// limit group.
func (g *GroupData) Limit() int64 {
	if g.IsCount() {
		return g.limit
	}

	return math.MaxInt64
}

// group struct describes an individual limit group.
type group struct {
	name string
	GroupData
	toNotify []chan bool
}

// newGroup creates a new group.
func newGroup(name string, data GroupData) *group {
	return &group{
		name:      name,
		GroupData: data,
	}
}

// setLimit updates the group's limit.
func (g *group) setLimit(limit int64) {
	if g.IsCount() {
		g.limit = limit
	}
}

// canIncrement tells you if the current count of this group is less than the
// limit.
func (g *group) canIncrement() bool {
	switch g.mode {
	case groupModeCount:
		return g.current < g.limit
	case groupModeBeforeTime:
		return secondsInDay() < g.limit
	case groupModeAfterTime:
		return secondsInDay() > g.current
	case groupModeBetweenTimes:
		t := secondsInDay()

		if g.current > g.limit {
			return g.limit < t && t < g.current
		}

		return g.current < t && t < g.limit
	case groupModeBeforeDateTime:
		return time.Now().Unix() < g.limit
	case groupModeAfterDateTime:
		return time.Now().Unix() > g.current
	case groupModeBetweenDateTimes:
		t := time.Now().Unix()

		return g.current < t && t < g.limit
	default:
		return false
	}
}

func secondsInDay() int64 {
	now := time.Now()
	y, m, d := now.Date()

	return int64(now.Sub(time.Date(y, m, d, 0, 0, 0, 0, now.Location())).Seconds())
}

// increment increases the current count of this group. You must call
// canIncrement() first to make sure you won't go over the limit (and hold a
// lock over the 2 calls to avoid a race condition).
func (g *group) increment() {
	if g.IsCount() {
		g.current++
	}
}

// decrement decreases the current count of this group. Returns true if the
// current count indicates the group is unused.
func (g *group) decrement() bool {
	if !g.IsCount() {
		return false
	}

	// (decrementing a uint under 0 makes it a large positive value, so we must
	// check first)
	if g.current == 0 {
		return true
	}

	g.current--

	// notify callers who passed a channel to notifyDecrement(), but do it
	// defensively in a go routine so if the caller doesn't read from the
	// channel, we don't block forever
	if len(g.toNotify) > 0 {
		chans := g.toNotify
		g.toNotify = []chan bool{}
		go func() {
			for _, ch := range chans {
				ch <- true
			}
		}()
	}

	return g.current < 1
}

// capacity tells you how many more increments you could do on this group before
// breaching the limit.
func (g *group) capacity() int {
	if g.canIncrement() {
		if g.IsCount() {
			return int(g.limit - g.current)
		}

		return -1
	}

	return 0
}

// notifyDecrement will result in true being sent on the given channel the next
// time decrement() is called. (And then the channel is discarded.)
func (g *group) notifyDecrement(ch chan bool) {
	if g.IsCount() {
		g.toNotify = append(g.toNotify, ch)
	}
}
