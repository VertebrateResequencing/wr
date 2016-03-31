// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
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

// itemError lets us return an item and an error message over a channel
type itemError struct {
	item *Item
	err  error
}

// itemUpdate lets us send an item and an its update version over a channel
type itemUpdate struct {
	item   *Item
	update *Item
}

// Queue is a synchronized map of items that can shift to different sub-queues,
// automatically depending on their delay or ttr expiring, or manually by
// calling certain methods.
type Queue struct {
	Name           string
	items          map[string]*Item
	addReturn      chan itemError
	addItem        chan *Item
	getItem        chan string
	getReturn      chan itemError
	updateItem     chan *Item
	updateMain     chan bool
	updateReturn   chan itemError
	removeItem     chan string
	closeMain      chan bool
	delayQueue     *delayQueue
	addDelay       chan *Item
	addedDelay     chan bool
	updateDelay    chan itemUpdate
	closeDelay     chan bool
	readyQueue     *readyQueue
	addReady       chan *Item
	addedReady     chan bool
	reserve        chan bool
	reserveReturn  chan itemError
	updatePriority chan itemUpdate
	closeReady     chan bool
	runQueue       *runQueue
	addRun         chan *Item
	addedRun       chan bool
	updateTTR      chan itemUpdate
	closeRun       chan bool
	buryQueue      *buryQueue
	addBury        chan *Item
	addedBury      chan bool
	kick           chan string
	kickReturn     chan itemError
	closeBury      chan bool
	closed         bool
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
		Name:           name,
		items:          make(map[string]*Item),
		addReturn:      make(chan itemError),
		addItem:        make(chan *Item),
		getItem:        make(chan string),
		getReturn:      make(chan itemError),
		updateItem:     make(chan *Item),
		updateMain:     make(chan bool),
		updateReturn:   make(chan itemError),
		removeItem:     make(chan string),
		closeMain:      make(chan bool),
		delayQueue:     newDelayQueue(),
		addDelay:       make(chan *Item),
		addedDelay:     make(chan bool),
		updateDelay:    make(chan itemUpdate),
		closeDelay:     make(chan bool),
		readyQueue:     newReadyQueue(),
		addReady:       make(chan *Item),
		addedReady:     make(chan bool),
		reserve:        make(chan bool),
		reserveReturn:  make(chan itemError),
		updatePriority: make(chan itemUpdate),
		closeReady:     make(chan bool),
		runQueue:       newRunQueue(),
		addRun:         make(chan *Item, 1000),
		addedRun:       make(chan bool),
		updateTTR:      make(chan itemUpdate),
		closeRun:       make(chan bool),
		buryQueue:      newBuryQueue(),
		addBury:        make(chan *Item),
		addedBury:      make(chan bool),
		kick:           make(chan string),
		kickReturn:     make(chan itemError),
		closeBury:      make(chan bool),
		closed:         false,
	}
	go queue.startMain()
	go queue.startDelay()
	go queue.startReady()
	go queue.startRun()
	go queue.startBury()
	return queue
}

// Destroy shuts down a queue, destroying any contents. You can't do anything
// useful with it after that.
func (queue *Queue) Destroy() (err error) {
	// queue.mutex.Lock()
	// defer queue.mutex.Unlock()

	// if queue.closed {
	// 	err = Error{queue.Name, "Destroy", "", ErrQueueClosed}
	// 	return
	// }

	// queue.ttrClose <- true
	// queue.delayClose <- true
	// queue.items = nil
	// queue.delayQueue.empty()
	// queue.readyQueue.empty()
	// queue.runQueue.empty()
	// queue.buryQueue.empty()
	// queue.closed = true
	return
}

// Stats returns information about the number of items in the queue and each
// sub-queue.
func (queue *Queue) Stats() *Stats {
	return &Stats{
		Items:   len(queue.items),
		Delayed: queue.delayQueue.Len(),
		Ready:   queue.readyQueue.Len(),
		Running: queue.runQueue.Len(),
		Buried:  queue.buryQueue.Len(),
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
	item = newItem(key, data, priority, delay, ttr)
	queue.addItem <- item
	itemError := <-queue.addReturn
	item = itemError.item
	err = itemError.err
	return
}

// Get is a thread-safe way to get an item by the key you used to Add() it.
func (queue *Queue) Get(key string) (item *Item, err error) {
	queue.getItem <- key
	itemError := <-queue.getReturn
	item = itemError.item
	err = itemError.err
	return
}

// Update is a thread-safe way to change the data, priority, delay or ttr of an
// item. You must supply all of these as per Add() - just supply the old values
// of those you are not changing. The old values can be found by getting the
// item with Get() (giving you item.Key and item.Data), and then calling
// item.Stats to get stats.Priority, stats.Delay and stats.TTR.
func (queue *Queue) Update(key string, data interface{}, priority uint8, delay time.Duration, ttr time.Duration) (err error) {
	item := newItem(key, data, priority, delay, ttr)
	queue.updateItem <- item
	itemError := <-queue.updateReturn
	err = itemError.err
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
func (queue *Queue) Reserve() (item *Item, err error) { //*** we want a wait time.Duration arg, where we'll wait that long for something to come on the ready queue
	queue.reserve <- true
	itemError := <-queue.reserveReturn
	item = itemError.item
	err = itemError.err
	return
}

// Touch is a thread-safe way to extend the amount of time a Reserve()d item
// is allowed to run.
func (queue *Queue) Touch(key string) (err error) {

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
	queue.kick <- key
	itemError := <-queue.kickReturn
	err = itemError.err
	return
}

// Remove is a thread-safe way to remove an item from the queue.
func (queue *Queue) Remove(key string) (err error) {
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

// checkKey checks things about an item by its key; this should only be called
// from the start*() go routines
func (queue *Queue) checkKey(key string, method string, subqueue string) (item *Item, err error) {
	if queue.closed {
		err = Error{queue.Name, method, key, ErrQueueClosed}
	} else {
		// check it's actually still in the queue first
		item, ok := queue.items[key]
		if !ok {
			err = Error{queue.Name, method, key, ErrNotFound}
		} else if subqueue != "" {
			// and it must be in the desired subqueue
			if ok = item.state == subqueue; !ok {
				var etype error
				switch subqueue {
				case "ready":
					etype = ErrNotReady
				case "run":
					etype = ErrNotRunning
				case "bury":
					etype = ErrNotBuried
				}
				err = Error{queue.Name, method, key, etype}
			}
		}
	}
	return
}

// startMain is run as a go routine that waits for cross-sub-queue events and
// handles them
func (queue *Queue) startMain() {
	for {
		select {
		case item := <-queue.addItem:
			old, err := queue.checkKey(item.Key, "Add", "")
			if err == nil {
				err = Error{queue.Name, "Add", item.Key, ErrAlreadyExists}
				item = old
			} else {
				qerr, _ := err.(Error)
				if qerr.Err == ErrNotFound {
					err = nil
					queue.items[item.Key] = item
					if item.delay.Nanoseconds() == 0 {
						// put it directly on the ready queue
						item.delayIndex = -1
						item.readyAt = time.Time{}
						queue.addReady <- item
						<-queue.addedReady
					} else {
						queue.addDelay <- item
						<-queue.addedDelay
					}
				}
			}
			queue.addReturn <- itemError{item: item, err: err}
		case key := <-queue.getItem:
			item, err := queue.checkKey(key, "Get", "")
			queue.getReturn <- itemError{item: item, err: err}
		case update := <-queue.updateItem:
			item, err := queue.checkKey(update.Key, "Update", "")
			if err == nil {
				item.Data = update.Data
				if item.state == "delay" && item.delay != update.delay {
					queue.updateDelay <- itemUpdate{item: item, update: update}
					<-queue.updateMain
				} else {
					item.delay = update.delay
				}
				if item.state == "ready" && item.priority != update.priority {
					queue.updatePriority <- itemUpdate{item: item, update: update}
					<-queue.updateMain
				} else {
					item.priority = update.priority
				}
				if item.state == "run" && item.ttr != update.ttr {
					queue.updateTTR <- itemUpdate{item: item, update: update}
					<-queue.updateMain
				} else {
					item.ttr = update.ttr
				}
			}
			queue.updateReturn <- itemError{item: nil, err: err}
		case <-queue.closeMain:
			return
		}
	}
}

// startDelay is run as a go routine that waits for requests to add items to the
// delay queue, and auto-moves them to the ready queue after item's delay
func (queue *Queue) startDelay() {
	sleepTime := time.Duration(1 * time.Hour)
	for {
		delayTime := time.Now().Add(sleepTime)

		select {
		case item := <-queue.addDelay:
			queue.delayQueue.push(item)
			item.state = "delay"
			queue.addedDelay <- true
			if delayTime.After(time.Now().Add(item.delay)) {
				sleepTime = item.readyAt.Sub(time.Now())
				continue
			}
		case <-time.After(delayTime.Sub(time.Now())):
			len := queue.delayQueue.Len()
			for i := 0; i < len; i++ {
				item := queue.delayQueue.items[0]
				if !item.isready() {
					break
				}
				// remove it from the delay sub-queue and add it to the ready
				// sub-queue
				queue.delayQueue.remove(item)
				item.delayIndex = -1
				item.readyAt = time.Time{}
				queue.addReady <- item
				<-queue.addedReady
			}
		case iu := <-queue.updateDelay:
			iu.item.delay = iu.update.delay
			iu.item.readyAt = time.Now().Add(iu.item.delay)
			queue.delayQueue.update(iu.item)
			queue.updateMain <- true
		case <-queue.closeDelay:
			return
		}
	}
}

// startReady is run as a go routine that waits for requests to add items to the
// ready queue, and reserve them in to the run queue
func (queue *Queue) startReady() {
	for {
		select {
		case item := <-queue.addReady:
			queue.readyQueue.push(item)
			item.state = "ready"
			queue.addedReady <- true
		case <-queue.reserve:
			var item *Item
			var err error
			if queue.closed {
				err = Error{queue.Name, "Reserve", "", ErrQueueClosed}
				item = nil
			} else {
				// pop an item from the ready queue and add it to the run queue
				item = queue.readyQueue.pop()
				if item == nil {
					err = Error{queue.Name, "Reserve", "", ErrNothingReady}
				} else {
					item.readyIndex = -1
					item.reserves += 1
					queue.addRun <- item
				}
			}
			queue.reserveReturn <- itemError{item: item, err: err}
		case iu := <-queue.updatePriority:
			iu.item.priority = iu.update.priority
			queue.readyQueue.update(iu.item)
			queue.updateMain <- true
		case <-queue.closeReady:
			return
		}
	}
}

// startRun is run as a go routine that waits for requests to add items to the
// run queue, and auto-moves them to the ready queue after item's ttr. It also
// responds to requests to release, touch or bury an item.
func (queue *Queue) startRun() {
	sleepTime := time.Duration(1 * time.Hour)
	for {
		ttrTime := time.Now().Add(sleepTime)

		select {
		case item := <-queue.addRun:
			item.releaseAt = time.Now().Add(item.ttr)
			queue.runQueue.push(item)
			item.state = "run"
			if ttrTime.After(time.Now().Add(item.ttr)) {
				sleepTime = item.releaseAt.Sub(time.Now())
				continue
			}
		case <-time.After(ttrTime.Sub(time.Now())):
			len := queue.runQueue.Len()
			for i := 0; i < len; i++ {
				item := queue.runQueue.items[0]
				if !item.releasable() {
					break
				}
				// remove it from the run sub-queue and add it back to the
				// ready sub-queue
				queue.runQueue.remove(item)
				item.ttrIndex = -1
				item.timeouts += 1
				item.releaseAt = time.Time{}
				queue.addReady <- item
				<-queue.addedReady
			}
		case iu := <-queue.updateTTR:
			iu.item.ttr = iu.update.ttr
			iu.item.releaseAt = time.Now().Add(iu.item.ttr)
			queue.runQueue.update(iu.item)
			queue.updateMain <- true
		case <-queue.closeDelay:
			return
		}
	}
}

// startBury is run as a go routine that waits for requests to add items to the
// bury queue, and kick them in to the delay queue
func (queue *Queue) startBury() {
	for {
		select {
		case item := <-queue.addBury:
			queue.buryQueue.push(item)
			item.state = "bury"
			queue.addedBury <- true
		case key := <-queue.kick:
			var err error
			if queue.closed {
				err = Error{queue.Name, "Kick", "", ErrQueueClosed}
			} else {
				// check it's actually still in the queue first
				item, ok := queue.items[key]
				if !ok {
					err = Error{queue.Name, "Kick", key, ErrNotFound}
				} else {
					// and it must be in the bury queue
					if ok = item.state == "bury"; !ok {
						err = Error{queue.Name, "Kick", key, ErrNotBuried}
					} else {
						// switch from bury to delay queue
						queue.buryQueue.remove(item)
						item.readyAt = time.Now().Add(item.delay)
						item.buryIndex = -1
						item.kicks += 1
						queue.addDelay <- item
						<-queue.addedDelay
					}
				}
			}
			queue.kickReturn <- itemError{item: nil, err: err}
		case <-queue.closeReady:
			return
		}
	}
}
