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
	Key        string
	Data       interface{}
	Priority   uint8  // highest priority is 255
	State      string // one of 'delay', 'ready', 'run', 'bury', 'removed'
	Reserves   uint32
	Timeouts   uint32
	Releases   uint32
	Buries     uint32
	Kicks      uint32
	delay      time.Duration
	ttr        time.Duration
	readyAt    time.Time
	releaseAt  time.Time
	creation   time.Time
	mutex      sync.Mutex
	delayIndex int
	readyIndex int
	ttrIndex   int
	buryIndex  int
}

func newItem(key string, data interface{}, priority uint8, delay time.Duration, ttr time.Duration) *Item {
	return &Item{
		Key:      key,
		Data:     data,
		Priority: priority,
		State:    "delay",
		Reserves: 0,
		Timeouts: 0,
		Releases: 0,
		Buries:   0,
		Kicks:    0,
		delay:    delay,
		ttr:      ttr,
		readyAt:  time.Now().Add(delay),
		creation: time.Now(),
	}
}

// Touch is a thread-safe way to (re)set the item's release time, to allow it
// more time on the run sub-queue.
func (item *Item) touch() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.releaseAt = time.Now().Add(item.ttr)
}

// restart is a thread-safe way to reset the readyAt time, for when the item
// is put back in to the delay queue
func (item *Item) restart() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.readyAt = time.Now().Add(item.delay)
}

// Verify if the item is ready
func (item *Item) isready() bool {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	return item.readyAt.Before(time.Now())
}

// Verify if the item should be released
func (item *Item) releasable() bool {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	if item.releaseAt.IsZero() {
		return false
	}
	return item.releaseAt.Before(time.Now())
}

// update after we've switched from the delay to the ready sub-queue
func (item *Item) switchDelayReady() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.delayIndex = -1
	item.readyAt = time.Time{}
	item.State = "ready"
}

// update after we've switched from the ready to the run sub-queue
func (item *Item) switchReadyRun() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.readyIndex = -1
	item.Reserves += 1
	item.State = "run"
}

// update after we've switched from the run to the ready sub-queue. reason is
// one of 'timeout' or 'release'
func (item *Item) switchRunReady(reason string) {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.ttrIndex = -1
	item.releaseAt = time.Time{}

	switch reason {
	case "timeout":
		item.Timeouts += 1
	case "release":
		item.Releases += 1
	}

	item.State = "ready"
}

// update after we've switched from the run to the bury sub-queue
func (item *Item) switchRunBury() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.ttrIndex = -1
	item.releaseAt = time.Time{}
	item.Buries += 1
	item.State = "bury"
}

// update after we've switched from the bury to the delay sub-queue
func (item *Item) switchBuryDelay() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.buryIndex = -1
	item.Kicks += 1
	item.State = "delay"
}

// once removed from its queue, we clear out various properties just in case
func (item *Item) removalCleanup() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.ttrIndex = -1
	item.releaseAt = time.Time{}
	item.readyIndex = -1
	item.delayIndex = -1
	item.readyAt = time.Time{}
	item.buryIndex = -1
	item.State = "removed"
}
