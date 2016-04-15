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

/*
Package queue provides a queue structure where you can add items to the
queue that can then then switch between 4 sub-queues.

This package provides the functions for a server process to do the work of a
jobqueue like beanstalkd. See the jobqueue package for the functions that allow
interaction with clients on the network.

Items start in the delay queue. After the item's delay time, they automatically
move to the ready queue. From there you can Reserve() an item to get the highest
priority (or for those with equal priority, the oldest one (fifo)) which
switches it from the ready queue to the run queue.

In the run queue the item starts a time-to-release (ttr) countdown; when that
runs out the item is placed back on the ready queue. This is to handle a
process Reserving an item but then crashing before it deals with the item;
with it back on the ready queue, some other process can pick it up.

To stop it going back to the ready queue you either Remove() the item (you dealt
with the item successfully), Touch() it to give yourself more time to handle the
item, or you Bury() the item (the item can't be dealt with until the user takes
some action). When you know you have a transient problem preventing you from
handling the item right now, you can manually Release() the item back to the
ready queue.
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

// Queue is a synchronized map of items that can shift to different sub-queues,
// automatically depending on their delay or ttr expiring, or manually by
// calling certain methods.
type Queue struct {
	Name              string
	mutex             sync.RWMutex
	items             map[string]*Item
	delayQueue        *subQueue
	readyQueue        *subQueue
	runQueue          *subQueue
	buryQueue         *buryQueue
	delayNotification chan bool
	delayClose        chan bool
	delayTime         time.Time
	ttrNotification   chan bool
	ttrClose          chan bool
	ttrTime           time.Time
	closed            bool
}

// Stats holds information about the Queue's state.
type Stats struct {
	Items   int
	Delayed int
	Ready   int
	Running int
	Buried  int
}

// New is a helper to create instance of the Queue struct.
func New(name string) *Queue {
	queue := &Queue{
		Name:              name,
		items:             make(map[string]*Item),
		delayQueue:        newSubQueue(0),
		readyQueue:        newSubQueue(1),
		runQueue:          newSubQueue(2),
		buryQueue:         newBuryQueue(),
		ttrNotification:   make(chan bool, 1),
		ttrClose:          make(chan bool),
		ttrTime:           time.Now(),
		delayNotification: make(chan bool, 1),
		delayClose:        make(chan bool),
		delayTime:         time.Now(),
	}
	go queue.startDelayProcessing()
	go queue.startTTRProcessing()
	return queue
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
	queue.closed = true
	return
}

// Stats returns information about the number of items in the queue and each
// sub-queue.
func (queue *Queue) Stats() *Stats {
	queue.mutex.RLock()
	defer queue.mutex.RUnlock()

	return &Stats{
		Items:   len(queue.items),
		Delayed: queue.delayQueue.len(),
		Ready:   queue.readyQueue.len(),
		Running: queue.runQueue.len(),
		Buried:  queue.buryQueue.len(),
	}
}

// Add is a thread-safe way to add new items to the queue. After delay they
// will switch to the ready sub-queue from where they can be Reserve()d. Once
// reserved, they have ttr to Remove() the item, otherwise it gets released
// back to the ready sub-queue. The priority determines which item will be
// next to be Reserve()d, with priority 255 (the max) items coming before lower
// priority ones (with 0 being the lowest). Items with the same priority
// number are Reserve()d on a fifo basis. It returns an item, which may have
// already existed (in which case, nothing was actually added or changed).
func (queue *Queue) Add(key string, data interface{}, priority uint8, delay time.Duration, ttr time.Duration) (item *Item, err error) {
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

	if delay.Nanoseconds() == 0 {
		// put it directly on the ready queue
		item.switchDelayReady()
		queue.readyQueue.push(item)
		queue.mutex.Unlock()
	} else {
		queue.delayQueue.push(item)
		queue.mutex.Unlock()
		queue.delayNotificationTrigger(item)
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

// Update is a thread-safe way to change the data, priority, delay or ttr of an
// item. You must supply all of these as per Add() - just supply the old values
// of those you are not changing. The old values can be found by getting the
// item with Get() (giving you item.Key and item.Data), and then calling
// item.Stats to get stats.Priority, stats.Delay and stats.TTR.
func (queue *Queue) Update(key string, data interface{}, priority uint8, delay time.Duration, ttr time.Duration) (err error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if queue.closed {
		err = Error{queue.Name, "Update", key, ErrQueueClosed}
		return
	}

	item, exists := queue.items[key]
	if !exists {
		err = Error{queue.Name, "Update", key, ErrNotFound}
		return
	}

	item.Data = data

	if item.state == "delay" && item.delay != delay {
		item.delay = delay
		item.restart()
		queue.delayQueue.update(item)
	} else if item.state == "ready" && item.priority != priority {
		item.priority = priority
		queue.readyQueue.update(item)
	} else if item.state == "run" && item.ttr != ttr {
		item.ttr = ttr
		item.touch()
		queue.runQueue.update(item)
	}

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
// Release().
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
// ready sub-queue, for when the item should be dealt with later, not now.
func (queue *Queue) Release(key string) (err error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if queue.closed {
		err = Error{queue.Name, "Release", key, ErrQueueClosed}
		return
	}

	// check it's actually still in the queue first
	item, ok := queue.items[key]
	if !ok {
		err = Error{queue.Name, "Release", key, ErrNotFound}
		return
	}

	// and it must be in the run queue
	if ok = item.state == "run"; !ok {
		err = Error{queue.Name, "Release", key, ErrNotRunning}
		return
	}

	// switch from run to ready queue
	queue.runQueue.remove(item)
	queue.readyQueue.push(item)
	item.switchRunReady("release")

	return
}

// Bury is a thread-safe way to switch an item in the run sub-queue to the
// bury sub-queue, for when the item can't be dealt with ever, at least until
// the user takes some action and changes something.
func (queue *Queue) Bury(key string) (err error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if queue.closed {
		err = Error{queue.Name, "Bury", key, ErrQueueClosed}
		return
	}

	// check it's actually still in the queue first
	item, ok := queue.items[key]
	if !ok {
		err = Error{queue.Name, "Bury", key, ErrNotFound}
		return
	}

	// and it must be in the run queue
	if ok = item.state == "run"; !ok {
		err = Error{queue.Name, "Bury", key, ErrNotRunning}
		return
	}

	// switch from run to bury queue
	queue.runQueue.remove(item)
	queue.buryQueue.push(item)
	item.switchRunBury()

	return
}

// Kick is a thread-safe way to switch an item in the bury sub-queue to the
// delay sub-queue, for when a previously buried item can now be handled.
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

	// switch from bury to delay queue
	queue.buryQueue.remove(item)
	item.restart()
	queue.delayQueue.push(item)
	item.switchBuryDelay()

	queue.mutex.Unlock()
	queue.delayNotificationTrigger(item)

	return
}

// Remove is a thread-safe way to remove an item from the queue.
func (queue *Queue) Remove(key string) (err error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if queue.closed {
		err = Error{queue.Name, "Remove", key, ErrQueueClosed}
		return
	}

	// check it's actually still in the queue first
	item, existed := queue.items[key]
	if !existed {
		err = Error{queue.Name, "Remove", key, ErrNotFound}
		return
	}

	// remove from the queue
	delete(queue.items, key)

	// remove from the current sub-queue
	switch item.state {
	case "delay":
		queue.delayQueue.remove(item)
	case "ready":
		queue.readyQueue.remove(item)
	case "run":
		queue.runQueue.remove(item)
	case "bury":
		queue.buryQueue.remove(item)
	}
	item.removalCleanup()

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
			}
			queue.mutex.Unlock()
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
			for i := 0; i < len; i++ {
				item := queue.runQueue.items[0]

				if !item.releasable() {
					break
				}

				// remove it from the ttr sub-queue and add it back to the
				// ready sub-queue
				queue.runQueue.remove(item)
				queue.readyQueue.push(item)
				item.switchRunReady("timeout")
			}
			queue.mutex.Unlock()
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
