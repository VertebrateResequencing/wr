// Copyright © 2016, 2018, 2019 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// This file was based on: Diego Bernardes de Sousa Pinto's
// https://github.com/diegobernardes/ttlcache
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

package queue

// This file implements the items that are added to queues.

import (
	"sync"
	"sync/atomic"
	"time"
)

// ItemState is how we describe the possible item states.
type ItemState string

// ItemState* constants represent all the possible item states.
const (
	ItemStateDelay     ItemState = "delay"
	ItemStateReady     ItemState = "ready"
	ItemStateRun       ItemState = "run"
	ItemStateBury      ItemState = "bury"
	ItemStateDependent ItemState = "dependent"
	ItemStateRemoved   ItemState = "removed"
)

// iid is used to give each item a unique incrementing id, necessay when locking
// items during a sort, for lock order consistency.
var iid uint64

// Item holds the information about each item in our queue, and has thread-safe
// functions to update properties as we switch between sub-queues.
type Item struct {
	Key           string
	ReserveGroup  string
	data          interface{}
	state         ItemState
	reserves      uint32
	timeouts      uint32
	releases      uint32
	buries        uint32
	kicks         uint32
	priority      uint8 // highest priority is 255
	size          uint8
	delay         time.Duration
	ttr           time.Duration
	readyAt       time.Time
	releaseAt     time.Time
	creation      time.Time
	dependencies  []string
	remainingDeps map[string]bool
	mutex         sync.RWMutex
	queueIndexes  [5]int
	iid           uint64
}

// ItemStats holds information about the Item's state. Remaining is the time
// remaining in the current sub-queue. This will be a duration of zero for all
// but the delay and run states. In the delay state it tells you how long before
// it can be reserved, and in the run state it tells you how long before it will
// be released automatically.
type ItemStats struct {
	State     ItemState
	Age       time.Duration
	Remaining time.Duration
	Delay     time.Duration
	TTR       time.Duration
	Reserves  uint32
	Timeouts  uint32
	Releases  uint32
	Buries    uint32
	Kicks     uint32
	Priority  uint8
	Size      uint8
}

func newItem(key string, reserveGroup string, data interface{}, priority uint8, delay time.Duration, ttr time.Duration) *Item {
	return &Item{
		Key:          key,
		ReserveGroup: reserveGroup,
		data:         data,
		state:        ItemStateDelay,
		reserves:     0,
		timeouts:     0,
		releases:     0,
		buries:       0,
		kicks:        0,
		priority:     priority,
		delay:        delay,
		ttr:          ttr,
		readyAt:      time.Now().Add(delay),
		creation:     time.Now(),
		iid:          atomic.AddUint64(&iid, 1),
	}
}

// Data returns the data of this item.
func (item *Item) Data() interface{} {
	item.mutex.RLock()
	defer item.mutex.RUnlock()
	return item.data
}

// SetData stores new data in this item.
func (item *Item) SetData(data interface{}) {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.data = data
}

// Stats returns some information about the item.
func (item *Item) Stats() *ItemStats {
	item.mutex.RLock()
	defer item.mutex.RUnlock()
	age := time.Since(item.creation)
	var remaining time.Duration
	switch item.state {
	case ItemStateDelay:
		remaining = time.Until(item.readyAt)
	case ItemStateRun:
		remaining = time.Until(item.releaseAt)
	default:
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
		Size:      item.size,
		Delay:     item.delay,
		TTR:       item.ttr,
	}
}

// State is a thread-safe way of getting just the state of an item, when you
// don't need all of the other information from Stats().
func (item *Item) State() ItemState {
	item.mutex.RLock()
	defer item.mutex.RUnlock()
	return item.state
}

// ReleaseAt is a thread-safe way of getting just the releaseAt of an item, when
// you don't need all of the other information from Stats().
func (item *Item) ReleaseAt() time.Time {
	item.mutex.RLock()
	defer item.mutex.RUnlock()
	return item.releaseAt
}

// ReadyAt is a thread-safe way of getting just the readyAt of an item, when you
// don't need all of the other information from Stats().
func (item *Item) ReadyAt() time.Time {
	item.mutex.RLock()
	defer item.mutex.RUnlock()
	return item.readyAt
}

// Dependencies returns the keys of the other items we are dependent upon. Note,
// do not add these back during a queue.Update(), or you could end up adding
// back dependencies that already got resolved, leaving you in a permanent
// dependent state; use UnresolvedDependencies() for that purpose instead.
func (item *Item) Dependencies() []string {
	return item.dependencies
}

// UnresolvedDependencies returns the keys of the other items we are still
// dependent upon.
func (item *Item) UnresolvedDependencies() []string {
	item.mutex.RLock()
	defer item.mutex.RUnlock()
	deps := make([]string, len(item.remainingDeps))
	i := 0
	for dep := range item.remainingDeps {
		deps[i] = dep
		i++
	}
	return deps
}

// ChangedKey updates this item by changing its Key if old matches it, or by
// updating the key in any dependencies of this item.
func (item *Item) ChangedKey(old, new string) {
	item.mutex.Lock()
	defer item.mutex.Unlock()

	if item.Key == old {
		item.Key = new
		return
	}

	for i, dep := range item.dependencies {
		if dep == old {
			item.dependencies[i] = new
			break
		}
	}

	if item.remainingDeps[old] {
		delete(item.remainingDeps, old)
		item.remainingDeps[new] = true
	}
}

// setDependencies sets the keys of the other items we are dependent upon. This
// only records the dependencies on the item; it does not trigger any dependency
// related actions or updates.
func (item *Item) setDependencies(deps []string) {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.dependencies = deps
	item.remainingDeps = make(map[string]bool)
	for _, key := range item.dependencies {
		item.remainingDeps[key] = true
	}
}

// resolveDependency takes the key of an item this item depends on, and marks
// that as a resolved dependency. Returns false if this item is not currently in
// the dependency sub queue. Otherwise, if all of this item's dependencies have
// now been resolved in this way, returns true.
func (item *Item) resolveDependency(key string) bool {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	delete(item.remainingDeps, key)
	if item.state == ItemStateDependent {
		return len(item.remainingDeps) == 0
	}
	return false
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

// tempDisableTTR is a thread-safe way to effectively temporarily disable an
// item's time to release by setting it to a year from now.
func (item *Item) tempDisableTTR() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.releaseAt = time.Now().Add(8760 * time.Hour)
}

// update after we've switched from the delay to the ready sub-queue
func (item *Item) switchDelayReady() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[0] = -1
	item.readyAt = time.Time{}
	item.state = ItemStateReady
}

// update after we've switched from the delay to the dependent sub-queue
func (item *Item) switchDelayDependent() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[0] = -1
	item.readyAt = time.Time{}
	item.state = ItemStateDependent
}

// update after we've switched from the dependent to the ready sub-queue
func (item *Item) switchDependentReady() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[4] = -1
	item.state = ItemStateReady
}

// update after we've switched from the ready to the run sub-queue
func (item *Item) switchReadyRun() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[1] = -1
	item.reserves++
	item.state = ItemStateRun
}

// update after we've switched from the ready to the dependent sub-queue
func (item *Item) switchReadyDependent() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[1] = -1
	item.state = ItemStateDependent
}

// update after we've switched from the run to the ready sub-queue
func (item *Item) switchRunReady() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[2] = -1
	item.releaseAt = time.Time{}
	item.timeouts++
	item.state = ItemStateReady
}

// update after we've switched from the run to the delay sub-queue
func (item *Item) switchRunDelay(timedOut ...bool) {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[2] = -1
	item.releaseAt = time.Time{}
	if len(timedOut) == 1 && timedOut[0] {
		item.timeouts++
	} else {
		item.releases++
	}
	item.state = ItemStateDelay
}

// update after we've switched from the run to the bury sub-queue
func (item *Item) switchRunBury(timedOut ...bool) {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[2] = -1
	item.releaseAt = time.Time{}
	if len(timedOut) == 1 && timedOut[0] {
		item.timeouts++
	}
	item.buries++
	item.state = ItemStateBury
}

// update after we've switched from the run to the dependent sub-queue
func (item *Item) switchRunDependent() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[2] = -1
	item.releaseAt = time.Time{}
	item.releases++
	item.state = ItemStateDependent
}

// update after we've switched from the bury to the ready sub-queue
func (item *Item) switchBuryReady() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[3] = -1
	item.kicks++
	item.state = ItemStateReady
}

// update after we've switched from the bury to the dependent sub-queue
func (item *Item) switchBuryDependent() {
	item.mutex.Lock()
	defer item.mutex.Unlock()
	item.queueIndexes[3] = -1
	item.kicks++
	item.state = ItemStateDependent
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
	item.queueIndexes[4] = -1
	item.state = ItemStateRemoved
}
