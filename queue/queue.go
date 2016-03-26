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
jobqueue like beanstalkd. See jobqueue.go for the functions that allow
interaction with clients on the network.

Items start in the delay queue. After the item's delay time, they automatically
move to the ready queue. From there you can Reserve() an item to get the oldest
one (fifo) which switches it from the ready queue to the run queue.

In the run queue the item starts a time-to-release (ttr) countdown; when that
runs out the item is placed back on the ready queue. This is to handle a
process Reserving and item but then crashing before it deals with the item;
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
	"sync"
	"time"
)

// Queue is a synchronized map of items that can shift to different sub-queues,
// automatically depending on their delay or ttr expiring, or manually by
// calling certain functions.
type Queue struct {
    Name              string
	mutex             sync.Mutex
	items             map[string]*Item
    delayQueue        *delayQueue
    readyQueue        *prioFifoQueue
    runQueue          *ttrQueue
    buryQueue         *buryQueue
    delayNotification chan bool
    delayTime         time.Time
	ttrNotification   chan bool
	ttrTime           time.Time
}

// Stats holds information about the Queue's state
type Stats struct {
    Items int
    Delayed int
    Ready int
    Running int
    Buried int
}

// New is a helper to create instance of the Queue struct
func New(name string) *Queue {
    queue := &Queue{
        Name:              name,
        items:             make(map[string]*Item),
        delayQueue:        newDelayQueue(),
        readyQueue:        newPrioFifoQueue(),
        runQueue:          newTTRQueue(),
        buryQueue:         newBuryQueue(),
        ttrNotification:   make(chan bool, 1),
        ttrTime:           time.Now(),
        delayNotification: make(chan bool, 1),
        delayTime:         time.Now(),
    }
    go queue.startDelayProcessing()
    go queue.startTTRProcessing()
    return queue
}

// Stats returns information about the number of items in the queue and each
// sub-queue.
func (queue *Queue) Stats() *Stats {
    queue.mutex.Lock()
    defer queue.mutex.Unlock()
    
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
// number are Reserve()d on a fifo basis.
func (queue *Queue) Add(key string, data interface{}, priority uint8, delay time.Duration, ttr time.Duration) bool {
    queue.mutex.Lock()
    
    _, exists := queue.items[key]
    if exists {
        queue.mutex.Unlock()
        return false
    }
    
    item := newItem(key, data, priority, delay, ttr)
    queue.items[key] = item
    queue.delayQueue.push(item)
    
    queue.mutex.Unlock()
    queue.delayNotificationTrigger(item)
    return true
}

// Get is a thread-safe way to get an item by the key you used to Add() it.
func (queue *Queue) Get(key string) (item *Item, exists bool) {
    queue.mutex.Lock()
    defer queue.mutex.Unlock()
    item, exists = queue.items[key]
    return
}

// Reserve is a thread-safe way to get the oldest (by time since entry to the
// ready sub-queue) item in the queue, switching it from the ready sub-queue to
// the run sub-queue, and in so doing starting its ttr countdown. You need to
// Remove() the item when you're done with it. If you're still doing something
// and ttr is approaching, Touch() it, otherwise it will be assumed you died
// and the item will be released back to the ready sub-queue automatically, to
// be handled by someone else that gets it from a Reserve() call. If you know
// you can't handle it right now, but someone else might be able to later,
// you can manually call Release().
func (queue *Queue) Reserve() (*Item, bool) {
    queue.mutex.Lock()
    
    // pop an item from the ready queue and add it to the run queue
    item := queue.readyQueue.pop()
    if item == nil {
        queue.mutex.Unlock()
        return nil, false
    }
    
    queue.runQueue.push(item)
    item.switchReadyRun()
    
    queue.mutex.Unlock()
    queue.ttrNotificationTrigger(item)
    
    return item, true
}

// Release is a thread-safe way to switch an item in the run sub-queue to the
// ready sub-queue, for when the item should be dealt with later, not now.
func (queue *Queue) Release(key string) (ok bool) {
    queue.mutex.Lock()
    defer queue.mutex.Unlock()
    
    // check it's actually still in the queue first
    item, ok := queue.items[key]
    if !ok {
        return
    }
    
    // and it must be in the run queue
    if ok = item.State == "run"; !ok {
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
func (queue *Queue) Bury(key string) (ok bool) {
    queue.mutex.Lock()
    defer queue.mutex.Unlock()
    
    // check it's actually still in the queue first
    item, ok := queue.items[key]
    if !ok {
        return
    }
    
    // and it must be in the run queue
    if ok = item.State == "run"; !ok {
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
func (queue *Queue) Kick(key string) (ok bool) {
    queue.mutex.Lock()
    
    // check it's actually still in the queue first
    item, ok := queue.items[key]
    if !ok {
        queue.mutex.Unlock()
        return
    }
    
    // and it must be in the bury queue
    if ok = item.State == "bury"; !ok {
        queue.mutex.Unlock()
        return
    }
    
    // switch from bury to delay queue
    queue.buryQueue.remove(item)
    queue.delayQueue.push(item)
    item.switchBuryDelay()
    
    queue.mutex.Unlock()
    queue.delayNotificationTrigger(item)
    
    return
}

// Remove is a thread-safe way to remove an item from the queue.
func (queue *Queue) Remove(key string) (existed bool) {
    queue.mutex.Lock()
    defer queue.mutex.Unlock()
    
    // check it's actually still in the queue first
    item, existed := queue.items[key]
    if !existed {
        return
    }
    
    // remove from the queue
    delete(queue.items, key)
    
    // remove from the current sub-queue
    switch item.State {
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
        if queue.delayQueue.Len() > 0 {
            sleepTime = queue.delayQueue.items[0].readyAt.Sub(time.Now())
        } else {
            sleepTime = time.Duration(1 * time.Hour)
        }

        queue.delayTime = time.Now().Add(sleepTime)
        queue.mutex.Unlock()

        select {
            case <-time.After(queue.delayTime.Sub(time.Now())):
                if queue.delayQueue.Len() == 0 {
                    continue
                }

                queue.mutex.Lock()
                item := queue.delayQueue.items[0]

                if !item.isready() {
                    queue.mutex.Unlock()
                    continue
                }
                
                // remove it from the delay sub-queue and add it to the ready
                // sub-queue
                queue.delayQueue.remove(item)
                queue.readyQueue.push(item)
                item.switchDelayReady()
                
                queue.mutex.Unlock()
            case <-queue.delayNotification:
                continue
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
		if queue.runQueue.Len() > 0 {
			sleepTime = queue.runQueue.items[0].ReleaseAt.Sub(time.Now())
		} else {
			sleepTime = time.Duration(1 * time.Hour)
		}

		queue.ttrTime = time.Now().Add(sleepTime)
		queue.mutex.Unlock()

		select {
    		case <-time.After(queue.ttrTime.Sub(time.Now())):
    			if queue.runQueue.Len() == 0 {
    				continue
    			}

    			queue.mutex.Lock()
    			item := queue.runQueue.items[0]

    			if !item.releasable() {
    				queue.mutex.Unlock()
    				continue
    			}
                
                // remove it from the ttr sub-queue and add it back to the
                // ready sub-queue
    			queue.runQueue.remove(item)
    			queue.readyQueue.push(item)
                item.switchRunReady("timeout")
                
    			queue.mutex.Unlock()
    		case <-queue.ttrNotification:
    			continue
		}
	}
}

func (queue *Queue) ttrNotificationTrigger(item *Item) {
	if queue.ttrTime.After(time.Now().Add(item.ttr)) {
		queue.ttrNotification <- true
	}
}
