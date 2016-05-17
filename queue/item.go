// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// This file was based on: Diego Bernardes de Sousa Pinto's
// https://github.com/diegobernardes/ttlcache
//
//  This file is part of VRPipe.
//
//  VRPipe is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  VRPipe is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with VRPipe. If not, see <http://www.gnu.org/licenses/>.

package queue

import (
	"sync"
	"time"
)

// Item holds the information about each item in our queue, and has thread-safe
// functions to update properties as we switch between sub-queues. The 'state'
// property can have one of the values 'delay', 'ready', 'run', 'bury' or
// 'removed'.
type Item struct {
	Key          string
	Data         interface{}
	state        string
	reserves     uint32
	timeouts     uint32
	releases     uint32
	buries       uint32
	kicks        uint32
	priority     uint8 // highest priority is 255
	delay        time.Duration
	ttr          time.Duration
	readyAt      time.Time
	releaseAt    time.Time
	creation     time.Time
	mutex        sync.RWMutex
	queueIndexes [4]int
}

// ItemStats holds information about the Item's state. The 'state' property can
// have one of the values 'delay', 'ready', 'run', 'bury' or 'removed'.
// Remaining is the time remaining in the current sub-queue. This will be a
// duration of zero for all but the delay and run states. In the delay state it
// tells you how long before it can be reserved, and in the run state it tells
// you how long before it will be released automatically.
type ItemStats struct {
	State     string
	Reserves  uint32
	Timeouts  uint32
	Releases  uint32
	Buries    uint32
	Kicks     uint32
	Age       time.Duration
	Remaining time.Duration
	Priority  uint8
	Delay     time.Duration
	TTR       time.Duration
}

func newItem(key string, data interface{}, priority uint8, delay time.Duration, ttr time.Duration) *Item {
	return &Item{
		Key:      key,
		Data:     data,
		state:    "delay",
		reserves: 0,
		timeouts: 0,
		releases: 0,
		buries:   0,
		kicks:    0,
		priority: priority,
		delay:    delay,
		ttr:      ttr,
		readyAt:  time.Now().Add(delay),
		creation: time.Now(),
	}
}

// Stats returns some information about the item
func (item *Item) Stats() *ItemStats {
	item.mutex.RLock()
	defer item.mutex.RUnlock()
	age := time.Since(item.creation)
	var remaining time.Duration
	if item.state == "delay" {
		remaining = item.readyAt.Sub(time.Now())
	} else if item.state == "run" {
		remaining = item.releaseAt.Sub(time.Now())
	} else {
		remaining = time.Duration(0) * time.Second
	}
	return &ItemStats{
		State:     item.state,
		Reserves:  item.reserves,
		Timeouts:  item.timeouts,
		Releases:  item.releases,
		Buries:    item.buries,
		Kicks:     item.kicks,
		Age:       age,
		Remaining: remaining,
		Priority:  item.priority,
		Delay:     item.delay,
		TTR:       item.ttr,
	}
}

// restart is a thread-safe way to reset the readyAt time, for when the item
// is put back in to the delay queue
func (item *Item) restart() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.readyAt = time.Now().Add(item.delay)
}

// touch is a thread-safe way to (re)set the item's release time, to allow it
// more time on the run sub-queue.
func (item *Item) touch() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.releaseAt = time.Now().Add(item.ttr)
}

// Verify if the item is ready
func (item *Item) isready() bool {
	item.mutex.RLock()
	defer item.mutex.RUnlock()
	return item.readyAt.Before(time.Now())
}

// Verify if the item should be released
func (item *Item) releasable() bool {
	item.mutex.RLock()
	defer item.mutex.RUnlock()
	if item.releaseAt.IsZero() {
		return false
	}
	return item.releaseAt.Before(time.Now())
}

// update after we've switched from the delay to the ready sub-queue
func (item *Item) switchDelayReady() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[0] = -1
	item.readyAt = time.Time{}
	item.state = "ready"
}

// update after we've switched from the ready to the run sub-queue
func (item *Item) switchReadyRun() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[1] = -1
	item.reserves += 1
	item.state = "run"
}

// update after we've switched from the run to the ready sub-queue
func (item *Item) switchRunReady() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[2] = -1
	item.releaseAt = time.Time{}
	item.timeouts += 1
	item.state = "ready"
}

// update after we've switched from the run to the delay sub-queue
func (item *Item) switchRunDelay() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[2] = -1
	item.releaseAt = time.Time{}
	item.releases += 1
	item.state = "delay"
}

// update after we've switched from the run to the bury sub-queue
func (item *Item) switchRunBury() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[2] = -1
	item.releaseAt = time.Time{}
	item.buries += 1
	item.state = "bury"
}

// update after we've switched from the bury to the ready sub-queue
func (item *Item) switchBuryReady() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[3] = -1
	item.kicks += 1
	item.state = "ready"
}

// once removed from its queue, we clear out various properties just in case
func (item *Item) removalCleanup() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[2] = -1
	item.releaseAt = time.Time{}
	item.queueIndexes[1] = -1
	item.queueIndexes[0] = -1
	item.readyAt = time.Time{}
	item.queueIndexes[3] = -1
	item.state = "removed"
}
