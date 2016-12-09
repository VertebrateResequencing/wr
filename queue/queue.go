// Copyright © 2016 Genome Research Limited
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

/*
Package queue provides a queue structure where you can add items to the
queue that can then then switch between 4 sub-queues.

This package provides the functions for a server process to do the work of a
jobqueue like beanstalkd. See the jobqueue package for the functions that allow
interaction with clients on the network.

Items start in the delay queue. After the item's delay time, they automatically
move to the ready queue. From there you can Reserve() an item to get the highest
priority (or for those with equal priority, the oldest one (fifo)) which
switches it from the ready queue to the run queue. Items can also have
dependencies, in which case they start in the dependency queue and only move to
the ready queue (bypassing the delay queue) once all its dependencies have been
Remove()d from the queue.

In the run queue the item starts a time-to-release (ttr) countdown; when that
runs out the item is placed back on the ready queue. This is to handle a
process Reserving an item but then crashing before it deals with the item;
with it back on the ready queue, some other process can pick it up.

To stop it going back to the ready queue you either Remove() the item (you dealt
with the item successfully), Touch() it to give yourself more time to handle the
item, or you Bury() the item (the item can't be dealt with until the user takes
some action). When you know you have a transient problem preventing you from
handling the item right now, you can manually Release() the item back to the
delay queue.
*/
package queue

import (
	"errors"
	"sync"
	"time"
)

// queue has some typical errors
var (
	ErrQueueClosed   = errors.New("queue closed")
	ErrNothingReady  = errors.New("ready queue is empty")
	ErrAlreadyExists = errors.New("already exists")
	ErrNotFound      = errors.New("not found")
	ErrNotReady      = errors.New("not ready")
	ErrNotRunning    = errors.New("not running")
	ErrNotBuried     = errors.New("not buried")
)

// Error records an error and the operation, item and queue that caused it.
type Error struct {
	Queue string // the queue's Name
	Op    string // name of the method
	Item  string // the item's key
	Err   error  // one of our Err vars
}

func (e Error) Error() string {
	return "queue(" + e.Queue + ") " + e.Op + "(" + e.Item + "): " + e.Err.Error()
}

// readyAddedCallback is used as a callback to know when new items have been
// added to the ready sub-queue, getting /all/ items in the ready sub-queue.
type readyAddedCallback func(queuename string, allitemdata []interface{})

// changedCallback is used as a callback to know when items change sub-queues,
// telling you what item.Data moved from which sub-queue to which other sub-
// queue. For new items in the queue, `from` will be 'new', and for items
// leaving the queue, `to` will be 'removed'.
type changedCallback func(from string, to string, data []interface{})

// ReserveFilter is a callback for use when calling ReserveFiltered(). It will
// receive an item's Data property and should return false if that is not
// desired, true if it is.
type ReserveFilter func(data interface{}) bool

// Queue is a synchronized map of items that can shift to different sub-queues,
// automatically depending on their delay or ttr expiring, or manually by
// calling certain methods.
type Queue struct {
	Name              string
	mutex             sync.RWMutex
	items             map[string]*Item
	dependants        map[string]map[string]*Item
	delayQueue        *subQueue
	readyQueue        *subQueue
	runQueue          *subQueue
	buryQueue         *buryQueue
	depQueue          *depQueue
	delayNotification chan bool
	delayClose        chan bool
	delayTime         time.Time
	ttrNotification   chan bool
	ttrClose          chan bool
	ttrTime           time.Time
	closed            bool
	readyAddedCb      readyAddedCallback
	changedCb         changedCallback
}

// Stats holds information about the Queue's state.
type Stats struct {
	Items     int
	Delayed   int
	Ready     int
	Running   int
	Buried    int
	Dependant int
}

// ItemDef makes it possible to supply a slice of Add() args to AddMany().
type ItemDef struct {
	Key          string
	Data         interface{}
	Priority     uint8 // highest priority is 255
	Delay        time.Duration
	TTR          time.Duration
	Dependencies []string
}

// New is a helper to create instance of the Queue struct.
func New(name string) *Queue {
	queue := &Queue{
		Name:              name,
		items:             make(map[string]*Item),
		dependants:        make(map[string]map[string]*Item),
		delayQueue:        newSubQueue(0),
		readyQueue:        newSubQueue(1),
		runQueue:          newSubQueue(2),
		buryQueue:         newBuryQueue(),
		depQueue:          newDependencyQueue(),
		ttrNotification:   make(chan bool, 1),
		ttrClose:          make(chan bool, 1),
		ttrTime:           time.Now(),
		delayNotification: make(chan bool, 1),
		delayClose:        make(chan bool, 1),
		delayTime:         time.Now(),
	}
	go queue.startDelayProcessing()
	go queue.startTTRProcessing()
	return queue
}

// SetReadyAddedCallback sets a callback that will be called when new items have
// been added to the ready sub-queue. The callback will receive the name of the
// queue, and a slice of the Data properties of every item currently in the
// ready sub-queue. The callback will be initiated in a go routine.
func (queue *Queue) SetReadyAddedCallback(callback readyAddedCallback) {
	queue.readyAddedCb = callback
}

// TriggerReadyAddedCallback allows you to manually trigger your
// readyAddedCallback at times when no new items have been added to the ready
// queue.
func (queue *Queue) TriggerReadyAddedCallback() {
	queue.readyAdded()
}

// readyAdded checks if a readyAddedCallback has been set, and if so calls it
// in a go routine.
func (queue *Queue) readyAdded() {
	if queue.readyAddedCb != nil {
		queue.mutex.RLock()
		var data []interface{}
		for _, item := range queue.readyQueue.items {
			data = append(data, item.Data)
		}
		queue.mutex.RUnlock()
		go queue.readyAddedCb(queue.Name, data)
	}
}

// SetChangedCallback sets a callback that will be called when items move from
// one sub-queue to another. The callback receives the name of the moved-from
// sub-queue ('new' in the case of entering the queue for the first time), the
// name of the moved-to sub-queue ('removed' in the case of the item being
// removed from the queue), and a slice of item.Data of everything that moved in
// this way. The callback will be initiated in a go routine.
func (queue *Queue) SetChangedCallback(callback changedCallback) {
	queue.changedCb = callback
}

// changed checks if a changedCallback has been set, and if so calls it in a go
// routine.
func (queue *Queue) changed(from string, to string, items []*Item) {
	if queue.changedCb != nil {
		var data []interface{}
		for _, item := range items {
			data = append(data, item.Data)
		}
		go queue.changedCb(from, to, data)
	}
}

// Destroy shuts down a queue, destroying any contents. You can't do anything
// useful with it after that.
func (queue *Queue) Destroy() (err error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if queue.closed {
		err = Error{queue.Name, "Destroy", "", ErrQueueClosed}
		return
	}

	queue.ttrClose <- true
	queue.delayClose <- true
	queue.items = nil
	queue.delayQueue.empty()
	queue.readyQueue.empty()
	queue.runQueue.empty()
	queue.buryQueue.empty()
	queue.depQueue.empty()
	queue.closed = true
	return
}

// Stats returns information about the number of items in the queue and each
// sub-queue.
func (queue *Queue) Stats() *Stats {
	queue.mutex.RLock()
	defer queue.mutex.RUnlock()

	return &Stats{
		Items:     len(queue.items),
		Delayed:   queue.delayQueue.len(),
		Ready:     queue.readyQueue.len(),
		Running:   queue.runQueue.len(),
		Buried:    queue.buryQueue.len(),
		Dependant: queue.depQueue.len(),
	}
}

// Add is a thread-safe way to add new items to the queue. After delay they will
// switch to the ready sub-queue from where they can be Reserve()d. Once
// reserved, they have ttr to Remove() the item, otherwise it gets released back
// to the ready sub-queue. The priority determines which item will be next to be
// Reserve()d, with priority 255 (the max) items coming before lower priority
// ones (with 0 being the lowest). Items with the same priority number are
// Reserve()d on a fifo basis. The final argument to Add() is an optional slice
// of item ids on which this item depends: this item will first enter the
// dependency sub-queue and only transfer to the ready sub-queue when items with
// these ids get Remove()d from the queue. Add() returns an item, which may have
// already existed (in which case, nothing was actually added or changed).
func (queue *Queue) Add(key string, data interface{}, priority uint8, delay time.Duration, ttr time.Duration, deps ...[]string) (item *Item, err error) {
	queue.mutex.Lock()

	if queue.closed {
		queue.mutex.Unlock()
		err = Error{queue.Name, "Add", key, ErrQueueClosed}
		return
	}

	item, existed := queue.items[key]
	if existed {
		queue.mutex.Unlock()
		err = Error{queue.Name, "Add", key, ErrAlreadyExists}
		return
	}

	item = newItem(key, data, priority, delay, ttr)
	queue.items[key] = item

	// check dependencies
	if len(deps) == 1 && len(deps[0]) > 0 {
		queue.setItemDependencies(item, deps[0])
		queue.mutex.Unlock()
		queue.changed("new", "dependant", []*Item{item})
		return
	}

	if delay.Nanoseconds() == 0 {
		// put it directly on the ready queue
		item.switchDelayReady()
		queue.readyQueue.push(item)
		queue.mutex.Unlock()
		queue.changed("new", "ready", []*Item{item})
		queue.readyAdded()
	} else {
		queue.delayQueue.push(item)
		queue.mutex.Unlock()
		queue.changed("new", "delay", []*Item{item})
		queue.delayNotificationTrigger(item)
	}

	return
}

// setItemDependencies sets the given item keys as the dependencies of the given
// item, and places the item in the dependency queue. Note that you can be
// dependent on items that do not exist in the queue; the item will remain in
// dependent queue until you add items with the given deps keys and then
// Remove() them.
func (queue *Queue) setItemDependencies(item *Item, deps []string) {
	item.setDependencies(deps)
	queue.setQueueDeps(item)
	item.switchDelayDependent()
	queue.depQueue.push(item)
}

// setQueueDeps updates the queue's lookup of parent items to their dependent
// children when you give it a child item (that has had some dependencies set on
// it).
func (queue *Queue) setQueueDeps(item *Item) {
	for _, dep := range item.Dependencies() {
		if _, exists := queue.dependants[dep]; !exists {
			queue.dependants[dep] = make(map[string]*Item)
		}
		queue.dependants[dep][item.Key] = item
	}
}

// itemHasDeps returns true if the item has unresolved dependencies according
// to the queue's lookup of parent items to their dependent children.
func (queue *Queue) itemHasDeps(item *Item) bool {
	for _, dep := range item.Dependencies() {
		if _, exists := queue.items[dep]; exists {
			return true
		}
	}
	return false
}

// AddMany is like Add(), except that you supply a slice of *ItemDef, and it
// returns the number that were actually added and the number of items that were
// not added because they were duplicates of items already in the queue. If an
// error occurs, nothing will have been added.
func (queue *Queue) AddMany(items []*ItemDef) (added int, dups int, err error) {
	queue.mutex.Lock()

	if queue.closed {
		queue.mutex.Unlock()
		err = Error{queue.Name, "AddMany", "", ErrQueueClosed}
		return
	}

	deferredTrigger := false
	var addedReadyItems []*Item
	var addedDelayItems []*Item
	var addedDepItems []*Item
	for _, def := range items {
		_, existed := queue.items[def.Key]
		if existed {
			dups++
			continue
		}

		item := newItem(def.Key, def.Data, def.Priority, def.Delay, def.TTR)
		queue.items[def.Key] = item

		if len(def.Dependencies) > 0 {
			queue.setItemDependencies(item, def.Dependencies)
			addedDepItems = append(addedDepItems, item)
		} else if def.Delay.Nanoseconds() == 0 {
			// put it directly on the ready queue
			item.switchDelayReady()
			queue.readyQueue.push(item)
			addedReadyItems = append(addedReadyItems, item)
		} else {
			queue.delayQueue.push(item)
			addedDelayItems = append(addedDelayItems, item)
			if !deferredTrigger && queue.delayTime.After(time.Now().Add(item.delay)) {
				defer queue.delayNotificationTrigger(item)
				deferredTrigger = true
			}
		}

		added++
	}

	queue.mutex.Unlock()
	if len(addedReadyItems) > 0 {
		queue.changed("new", "ready", addedReadyItems)
		queue.readyAdded()
	}
	if len(addedDelayItems) > 0 {
		queue.changed("new", "delay", addedDelayItems)
	}
	if len(addedDepItems) > 0 {
		queue.changed("new", "dependent", addedDepItems)
	}
	return
}

// Get is a thread-safe way to get an item by the key you used to Add() it.
func (queue *Queue) Get(key string) (item *Item, err error) {
	queue.mutex.RLock()
	defer queue.mutex.RUnlock()

	if queue.closed {
		err = Error{queue.Name, "Get", key, ErrQueueClosed}
		return
	}

	item, exists := queue.items[key]

	if !exists {
		err = Error{queue.Name, "Get", key, ErrNotFound}
	}

	return
}

// GetRunningData gets all the item.Data of items currently in the run sub-
// queue.
func (queue *Queue) GetRunningData() (data []interface{}) {
	queue.mutex.RLock()
	for _, item := range queue.runQueue.items {
		data = append(data, item.Data)
	}
	queue.mutex.RUnlock()
	return
}

// AllItems returns the items in the queue. NB: You should NOT do anything
// to these items - use for read-only purposes.
func (queue *Queue) AllItems() (items []*Item) {
	for _, item := range queue.items {
		items = append(items, item)
	}
	return
}

// Update is a thread-safe way to change the data, priority, delay, ttr or
// dependencies of an item. You must supply all of these as per Add() - just
// supply the old values of those you are not changing (except for dependencies,
// which remain optional). The old values can be found by getting the item with
// Get() (giving you item.Key, item.Data and item.UnresolvedDependencies()), and
// then calling item.Stats() to get stats.Priority, stats.Delay and stats.TTR.
func (queue *Queue) Update(key string, data interface{}, priority uint8, delay time.Duration, ttr time.Duration, deps ...[]string) (err error) {
	queue.mutex.Lock()

	if queue.closed {
		err = Error{queue.Name, "Update", key, ErrQueueClosed}
		queue.mutex.Unlock()
		return
	}

	item, exists := queue.items[key]
	if !exists {
		err = Error{queue.Name, "Update", key, ErrNotFound}
		queue.mutex.Unlock()
		return
	}

	var changedFrom string
	var addedReady bool
	item.Data = data
	if len(deps) == 1 {
		// check if dependencies actually changed
		oldDeps := make(map[string]bool)
		for _, dep := range item.UnresolvedDependencies() {
			oldDeps[dep] = true
		}
		newDeps := 0
		for _, dep := range deps[0] {
			if !oldDeps[dep] {
				newDeps++
			}
			delete(oldDeps, dep)
		}
		var toRemove []string
		for dep := range oldDeps {
			toRemove = append(toRemove, dep)
		}

		if len(toRemove) > 0 || newDeps > 0 {
			// remove any invalid dependencies from our lookup
			for _, dep := range toRemove {
				if _, exists := queue.items[dep]; exists {
					delete(queue.dependants[dep], key)
					if len(queue.dependants[dep]) == 0 {
						delete(queue.dependants, dep)
					}
				}
			}

			// set the new dependencies and update our lookup
			item.setDependencies(deps[0])
			queue.setQueueDeps(item)

			// if we now have unresolved dependencies and we're not in dependent
			// state, switch to dependent queue
			if item.state != "dependent" {
				pushToDep := true
				switch item.state {
				case "delay":
					queue.delayQueue.remove(item)
					item.switchDelayDependent()
					changedFrom = "delay"
				case "ready":
					queue.readyQueue.remove(item)
					item.switchReadyDependent()
					changedFrom = "ready"
				case "run":
					queue.runQueue.remove(item)
					item.switchRunDependent()
					changedFrom = "run"
				case "bury":
					// leave buried things buried; Kick() will put it on the
					// dependent queue if they are still unresolved by then
					pushToDep = false
				}
				if pushToDep {
					queue.depQueue.push(item)
				}
			} else {
				// switch to ready queue
				queue.depQueue.remove(item)
				item.switchDependentReady()
				queue.readyQueue.push(item)
				addedReady = true
			}
		}
	}

	if item.delay != delay {
		item.delay = delay
		if item.state == "delay" {
			item.restart()
			queue.delayQueue.update(item)
		}
	} else if item.priority != priority || addedReady {
		item.priority = priority
		if item.state == "ready" {
			queue.readyQueue.update(item)
		}
	} else if item.ttr != ttr {
		item.ttr = ttr
		if item.state == "run" {
			item.touch()
			queue.runQueue.update(item)
		}
	}

	if addedReady {
		queue.mutex.Unlock()
		queue.readyAdded()
		queue.changed("dependent", "ready", []*Item{item})
	} else {
		queue.mutex.Unlock()
	}

	if changedFrom != "" {
		queue.changed(changedFrom, "dependent", []*Item{item})
	}

	return
}

// SetDelay is a thread-safe way to change the delay of an item.
func (queue *Queue) SetDelay(key string, delay time.Duration) (err error) {
	queue.mutex.Lock()
	if queue.closed {
		err = Error{queue.Name, "SetDelay", key, ErrQueueClosed}
		queue.mutex.Unlock()
		return
	}

	item, exists := queue.items[key]
	if !exists {
		err = Error{queue.Name, "SetDelay", key, ErrNotFound}
		queue.mutex.Unlock()
		return
	}

	if item.delay != delay {
		item.delay = delay
		if item.state == "delay" {
			item.restart()
			queue.delayQueue.update(item)
			queue.mutex.Unlock()
			queue.delayNotificationTrigger(item)
			return
		}
	}
	queue.mutex.Unlock()
	return
}

// Reserve is a thread-safe way to get the highest priority (or for those with
// equal priority, the oldest (by time since the item was first Add()ed) item
// in the queue, switching it from the ready sub-queue to the run sub-queue, and
// in so doing starting its ttr countdown. You need to Remove() the item when
// you're done with it. If you're still doing something and ttr is approaching,
// Touch() it, otherwise it will be assumed you died and the item will be
// released back to the ready sub-queue automatically, to be handled by someone
// else that gets it from a Reserve() call. If you know you can't handle it
// right now, but someone else might be able to later, you can manually call
// Release(), which moves it to the delay sub-queue.
func (queue *Queue) Reserve() (item *Item, err error) {
	queue.mutex.Lock()

	if queue.closed {
		queue.mutex.Unlock()
		err = Error{queue.Name, "Reserve", "", ErrQueueClosed}
		return
	}

	// pop an item from the ready queue and add it to the run queue
	item = queue.readyQueue.pop()
	if item == nil {
		queue.mutex.Unlock()
		err = Error{queue.Name, "Reserve", "", ErrNothingReady}
		return
	}

	item.touch()
	queue.runQueue.push(item)
	item.switchReadyRun()

	queue.mutex.Unlock()
	queue.ttrNotificationTrigger(item)
	queue.changed("ready", "run", []*Item{item})

	return
}

// ReserveFiltered is like Reserve(), except you provide a callback function
// that will receive the next (highest priority || oldest) item's Data. If you
// don't want that one your callback returns false, and then it will be called
// again with the following item's Data, and so on until your say you want it by
// returning true. That will be the item that gets moved from the ready to the
// run queue and returned to you. If you don't want any you'll get an error as
// if the ready queue was empty.
func (queue *Queue) ReserveFiltered(filter ReserveFilter) (item *Item, err error) {
	queue.mutex.Lock()

	if queue.closed {
		queue.mutex.Unlock()
		err = Error{queue.Name, "ReserveFiltered", "", ErrQueueClosed}
		return
	}

	// go through the ready queue in order and offer each item's Data to filter
	// until it returns true: that's the item we'll remove from ready
	var poppedItems []*Item
	for {
		thisItem := queue.readyQueue.pop()
		if thisItem == nil {
			break
		}
		poppedItems = append(poppedItems, thisItem)

		ok := filter(thisItem.Data)
		if ok {
			item = thisItem
			break
		}
	}
	for _, thisItem := range poppedItems {
		queue.readyQueue.push(thisItem)
	}

	if item == nil {
		queue.mutex.Unlock()
		err = Error{queue.Name, "ReserveFiltered", "", ErrNothingReady}
		return
	}

	queue.readyQueue.remove(item)
	item.touch()
	queue.runQueue.push(item)
	item.switchReadyRun()

	queue.mutex.Unlock()
	queue.ttrNotificationTrigger(item)
	queue.changed("ready", "run", []*Item{item})

	return
}

// Touch is a thread-safe way to extend the amount of time a Reserve()d item
// is allowed to run.
func (queue *Queue) Touch(key string) (err error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if queue.closed {
		err = Error{queue.Name, "Touch", key, ErrQueueClosed}
		return
	}

	// check it's actually still in the queue first
	item, ok := queue.items[key]
	if !ok {
		err = Error{queue.Name, "Touch", key, ErrNotFound}
		return
	}

	// and it must be in the run queue
	if ok = item.state == "run"; !ok {
		err = Error{queue.Name, "Touch", key, ErrNotRunning}
		return
	}

	// touch and update the heap
	item.touch()
	queue.runQueue.update(item)

	return
}

// Release is a thread-safe way to switch an item in the run sub-queue to the
// delay sub-queue, for when the item should be dealt with later, not now.
func (queue *Queue) Release(key string) (err error) {
	queue.mutex.Lock()

	if queue.closed {
		err = Error{queue.Name, "Release", key, ErrQueueClosed}
		queue.mutex.Unlock()
		return
	}

	// check it's actually still in the queue first
	item, ok := queue.items[key]
	if !ok {
		err = Error{queue.Name, "Release", key, ErrNotFound}
		queue.mutex.Unlock()
		return
	}

	// and it must be in the run queue
	if ok = item.state == "run"; !ok {
		err = Error{queue.Name, "Release", key, ErrNotRunning}
		queue.mutex.Unlock()
		return
	}

	// switch from run to delay queue (unless there is no delay, in which case
	// straight to ready)
	queue.runQueue.remove(item)
	if item.delay.Nanoseconds() == 0 {
		item.switchRunReady()
		queue.readyQueue.push(item)
		queue.mutex.Unlock()
		queue.changed("run", "ready", []*Item{item})
		queue.readyAdded()
	} else {
		item.restart()
		queue.delayQueue.push(item)
		item.switchRunDelay()
		queue.mutex.Unlock()
		queue.delayNotificationTrigger(item)
		queue.changed("run", "delay", []*Item{item})
	}

	return
}

// Bury is a thread-safe way to switch an item in the run sub-queue to the
// bury sub-queue, for when the item can't be dealt with ever, at least until
// the user takes some action and changes something.
func (queue *Queue) Bury(key string) (err error) {
	queue.mutex.Lock()

	if queue.closed {
		err = Error{queue.Name, "Bury", key, ErrQueueClosed}
		queue.mutex.Unlock()
		return
	}

	// check it's actually still in the queue first
	item, ok := queue.items[key]
	if !ok {
		err = Error{queue.Name, "Bury", key, ErrNotFound}
		queue.mutex.Unlock()
		return
	}

	// and it must be in the run queue
	if ok = item.state == "run"; !ok {
		err = Error{queue.Name, "Bury", key, ErrNotRunning}
		queue.mutex.Unlock()
		return
	}

	// switch from run to bury queue
	queue.runQueue.remove(item)
	queue.buryQueue.push(item)
	item.switchRunBury()
	queue.mutex.Unlock()
	queue.changed("run", "bury", []*Item{item})

	return
}

// Kick is a thread-safe way to switch an item in the bury sub-queue to the
// ready sub-queue, for when a previously buried item can now be handled.
func (queue *Queue) Kick(key string) (err error) {
	queue.mutex.Lock()

	if queue.closed {
		queue.mutex.Unlock()
		err = Error{queue.Name, "Kick", key, ErrQueueClosed}
		return
	}

	// check it's actually still in the queue first
	item, ok := queue.items[key]
	if !ok {
		queue.mutex.Unlock()
		err = Error{queue.Name, "Kick", key, ErrNotFound}
		return
	}

	// and it must be in the bury queue
	if ok = item.state == "bury"; !ok {
		queue.mutex.Unlock()
		err = Error{queue.Name, "Kick", key, ErrNotBuried}
		return
	}

	// switch from bury to ready or dependent queue
	queue.buryQueue.remove(item)
	if queue.itemHasDeps(item) {
		queue.depQueue.push(item)
		item.switchBuryDependent()
		queue.mutex.Unlock()
		queue.changed("bury", "dependent", []*Item{item})
	} else {
		queue.readyQueue.push(item)
		item.switchBuryReady()
		queue.mutex.Unlock()
		queue.changed("bury", "ready", []*Item{item})
		queue.readyAdded()
	}
	return
}

// Remove is a thread-safe way to remove an item from the queue.
func (queue *Queue) Remove(key string) (err error) {
	queue.mutex.Lock()

	if queue.closed {
		err = Error{queue.Name, "Remove", key, ErrQueueClosed}
		queue.mutex.Unlock()
		return
	}

	// check it's actually still in the queue first
	item, existed := queue.items[key]
	if !existed {
		err = Error{queue.Name, "Remove", key, ErrNotFound}
		queue.mutex.Unlock()
		return
	}

	// transfer any dependants to the ready queue
	addedReady := false
	var addedReadyItems []*Item
	if deps, exists := queue.dependants[key]; exists {
		for _, dep := range deps {
			done := dep.resolveDependency(key)
			if done && dep.state == "dependent" {
				queue.depQueue.remove(dep)

				// put it straight on the ready queue, regardless of delay value
				dep.switchDependentReady()
				queue.readyQueue.push(dep)
				addedReadyItems = append(addedReadyItems, dep)
				addedReady = true
			}
		}
		delete(queue.dependants, key)
	}

	// if this item is dependent on other items, update those items that this is
	// no longer dependent upon them
	for _, parent := range item.dependencies {
		if deps, exists := queue.dependants[parent]; exists {
			delete(deps, key)
			if len(deps) == 0 {
				delete(queue.dependants, parent)
			}
		}
	}

	// remove from the queue
	delete(queue.items, key)

	// remove from the current sub-queue
	switch item.state {
	case "delay":
		queue.delayQueue.remove(item)
		queue.changed("delay", "removed", []*Item{item})
	case "ready":
		queue.readyQueue.remove(item)
		queue.changed("ready", "removed", []*Item{item})
	case "run":
		queue.runQueue.remove(item)
		queue.changed("run", "removed", []*Item{item})
	case "bury":
		queue.buryQueue.remove(item)
		queue.changed("bury", "removed", []*Item{item})
	case "dependent":
		queue.depQueue.remove(item)
		queue.changed("dependent", "removed", []*Item{item})
	}
	item.removalCleanup()

	queue.mutex.Unlock()
	if addedReady {
		queue.changed("dependent", "ready", addedReadyItems)
		queue.readyAdded()
	}

	return
}

// HasDependents tells you if the item with the given key has any other items
// depending upon it. You'd want to check this before Remove()ing this item if
// you're removing it because it was undesired as opposed to complete, as
// Remove() always triggers dependent items to become ready.
func (queue *Queue) HasDependents(key string) (has bool, err error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if queue.closed {
		err = Error{queue.Name, "Remove", key, ErrQueueClosed}
		return
	}

	_, has = queue.dependants[key]
	return
}

func (queue *Queue) startDelayProcessing() {
	for {
		var sleepTime time.Duration
		queue.mutex.Lock()
		if queue.delayQueue.len() > 0 {
			sleepTime = queue.delayQueue.items[0].readyAt.Sub(time.Now())
		} else {
			sleepTime = time.Duration(1 * time.Hour)
		}

		queue.delayTime = time.Now().Add(sleepTime)
		queue.mutex.Unlock()

		select {
		case <-time.After(queue.delayTime.Sub(time.Now())):
			queue.mutex.Lock()
			len := queue.delayQueue.len()
			addedReady := false
			var items []*Item
			for i := 0; i < len; i++ {
				item := queue.delayQueue.items[0]

				if !item.isready() {
					break
				}

				// remove it from the delay sub-queue and add it to the ready
				// sub-queue
				queue.delayQueue.remove(item)
				queue.readyQueue.push(item)
				item.switchDelayReady()
				items = append(items, item)
				addedReady = true
			}
			queue.mutex.Unlock()
			if addedReady {
				queue.changed("delay", "ready", items)
				queue.readyAdded()
			}
		case <-queue.delayNotification:
			continue
		case <-queue.delayClose:
			return
		}
	}
}

func (queue *Queue) delayNotificationTrigger(item *Item) {
	if queue.delayTime.After(time.Now().Add(item.delay)) {
		queue.delayNotification <- true
	}
}

func (queue *Queue) startTTRProcessing() {
	for {
		var sleepTime time.Duration
		queue.mutex.Lock()
		if queue.runQueue.len() > 0 {
			sleepTime = queue.runQueue.items[0].releaseAt.Sub(time.Now())
		} else {
			sleepTime = time.Duration(1 * time.Hour)
		}

		queue.ttrTime = time.Now().Add(sleepTime)
		queue.mutex.Unlock()

		select {
		case <-time.After(queue.ttrTime.Sub(time.Now())):
			queue.mutex.Lock()
			len := queue.runQueue.len()
			addedReady := false
			var items []*Item
			for i := 0; i < len; i++ {
				item := queue.runQueue.items[0]

				if !item.releasable() {
					break
				}

				// remove it from the ttr sub-queue and add it back to the
				// ready sub-queue
				queue.runQueue.remove(item)
				queue.readyQueue.push(item)
				item.switchRunReady()
				items = append(items, item)
				addedReady = true
			}
			queue.mutex.Unlock()
			if addedReady {
				queue.changed("run", "ready", items)
				queue.readyAdded()
			}
		case <-queue.ttrNotification:
			continue
		case <-queue.ttrClose:
			return
		}
	}
}

func (queue *Queue) ttrNotificationTrigger(item *Item) {
	if queue.ttrTime.After(time.Now().Add(item.ttr)) {
		queue.ttrNotification <- true
	}
}
