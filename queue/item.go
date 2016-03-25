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
    "container/list"
)

// Item holds the information about each item in our queue, and has thread-safe
// functions to update properties as we switch between sub-queues
type Item struct {
    Key          string
    Data         interface{}
    State        string // one of 'delay', 'ready', 'run', 'bury', 'removed'
    ReleaseAt    time.Time
    Reserves     uint32
    Timeouts     uint32
    Releases     uint32
    Buries       uint32
    Kicks        uint32
    delay        time.Duration
    ttr          time.Duration
    readyAt      time.Time
    mutex        sync.Mutex
    delayIndex   int
    readyElement *list.Element
    ttrIndex     int
    buryElement  *list.Element
}

func newItem(key string, data interface{}, delay time.Duration, ttr time.Duration) *Item {
	return &Item{
        Key:     key,
		Data:    data,
        State:   "delay",
        Reserves: 0,
        Timeouts: 0,
        Releases: 0,
        Buries:   0,
        Kicks:    0,
        delay:   delay,
		ttr:     ttr,
        readyAt: time.Now().Add(delay),
	}
}

// Touch is a thread-safe way to (re)set the item's release time, to allow it
// more time on the run sub-queue.
func (item *Item) Touch() {
    item.mutex.Lock()
    defer item.mutex.Unlock()
    item.ReleaseAt = time.Now().Add(item.ttr)
}

// Verify if the item is ready
func (item *Item) isready() (ready bool) {
    item.mutex.Lock()
    defer item.mutex.Unlock()
    ready = item.readyAt.Before(time.Now())
    return
}

// Verify if the item should be released
func (item *Item) releasable() (releasable bool) {
	item.mutex.Lock()
    defer item.mutex.Unlock()
	releasable = item.ReleaseAt.Before(time.Now())
	return
}

// update after we've switched from the delay to the ready sub-queue
func (item *Item) switchDelayReady(e *list.Element) {
    item.mutex.Lock()
    defer item.mutex.Unlock()
    item.delayIndex = -1
    item.readyAt = time.Time{}
    item.readyElement = e
    item.State = "ready"
}

// update after we've switched from the ready to the run sub-queue
func (item *Item) switchReadyRun() {
    item.mutex.Lock()
    defer item.mutex.Unlock()
    item.readyElement = nil
    item.ReleaseAt = time.Now().Add(item.ttr)
    item.Reserves += 1
    item.State = "run"
}

// update after we've switched from the run to the ready sub-queue
func (item *Item) switchRunReady(e *list.Element, reason string) {
    item.mutex.Lock()
    defer item.mutex.Unlock()
    item.ttrIndex = -1
    item.ReleaseAt = time.Time{}
    item.readyElement = e
    
    switch reason {
        case "timeout":
            item.Timeouts += 1
        case "release":
            item.Releases += 1
    }
    
    item.State = "ready"
}

// update after we've switched from the run to the bury sub-queue
func (item *Item) switchRunBury(e *list.Element) {
    item.mutex.Lock()
    defer item.mutex.Unlock()
    item.ttrIndex = -1
    item.ReleaseAt = time.Time{}
    item.buryElement = e
    item.State = "bury"
}

// update after we've switched from the bury to the delay sub-queue
func (item *Item) switchBuryDelay() {
    item.mutex.Lock()
    defer item.mutex.Unlock()
    item.buryElement = nil
    item.readyAt = time.Now().Add(item.delay)
    item.State = "delay"
}

// once removed from its queue, we clear out various properties just in case
func (item *Item) clear() {
    item.mutex.Lock()
    defer item.mutex.Unlock()
    item.ttrIndex = -1
    item.ReleaseAt = time.Time{}
    item.readyElement = nil
    item.delayIndex = -1
    item.readyAt = time.Time{}
    item.buryElement = nil
    item.State = "removed"
}