/*******************************************************************************
 * Copyright (c) 2016-2021, 2024, 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: [Theo Barber-Bany] <theobarberbany@gmail.com>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

// This file was based on: Diego Bernardes de Sousa Pinto's
// https://github.com/diegobernardes/ttlcache

/*
Package queue provides an in-memory queue structure suitable for the safe and
low latency implementation of a real job queue.

It's like beanstalkd, but faster, with the ability to query the queue for
desired items, reject duplicates, and wait on dependencies.

Like beanstalkd, when you add items to the queue, they move between different
sub-queues:

Items start in the delay queue. After the item's delay time, they automatically
move to the ready queue. From there you can Reserve() an item to get the highest
priority (or for those with equal priority, the oldest - fifo) one which
switches it from the ready queue to the run queue. Items can also have
dependencies, in which case they start in the dependency queue and only move to
the ready queue (bypassing the delay queue) once all its dependencies have been
Remove()d from the queue. Items can also belong to a reservation group, in which
case you can Reserve() an item in a desired group.

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

	    import "github.com/VertebrateResequencing/wr/queue"
	    q = queue.New("myQueue")
	    q.SetReadyAddedCallback(func(queuename string, allitemdata []any) {
	        for _, item := range allitemdata {
	            // cast item to the original type, then arrange to do something now
	            // you know that the item is ready to be processed
	        }
	    })

	    // add an item to the queue
	    ttr := 30 * time.Second
	    item, err := q.Add("uuid1", "", "item data1", 0, 0 * time.Second, ttr)
	    item, err := q.Add("uuid2", "group", "item data2", 0, 0 * time.Second, ttr)

	    // get it back out
	    item, err = queue.Get("uuid1")

	    // reserve the next item with no group
	    item, err = queue.Reserve("", 0)

	    // or reserve the next item in a particular group
		item, err = queue.Reserve("group", 0)

		// or reserve even if there are no items in the queue right now, waiting
		// until something gets added or otherwise becomes ready
		item, err = queue.Reserve("group", 1 * time.Second)

	    // queue.Touch() every < ttr seconds if you might take longer than ttr to
	    // process the item

	    // say you successfully handled the item
	    item.Remove()
*/
package queue

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
)

const (
	// recallBreak is how long we wait before recalling readyAdded.
	recallBreak = 500 * time.Millisecond

	// idleSleep is how long the delay and ttr processing goroutines wait before
	// re-checking their sub-queues when those sub-queues are empty.
	idleSleep = 1 * time.Hour

	// op* are the operation names recorded in an Error for each method.
	opSuspend   = "Suspend"
	opChangeKey = "ChangeKey"
	opTouch     = "Touch"
	opRelease   = "Release"
	opBury      = "Bury"
	opKick      = "Kick"
	opRemove    = "Remove"
)

// Queue has some typical errors.
var (
	ErrQueueClosed    = errors.New("queue closed")
	ErrNothingReady   = errors.New("ready queue is empty")
	ErrAlreadyExists  = errors.New("already exists")
	ErrNotFound       = errors.New("not found")
	ErrNotReady       = errors.New("not ready")
	ErrNotRunning     = errors.New("not running")
	ErrNotBuried      = errors.New("not buried")
	ErrNotSuspendable = errors.New("not suspendable")
	ErrNotSuspended   = errors.New("not suspended")
)

// SubQueue is how we name the sub-queues of a Queue.
type SubQueue string

// SubQueue* constants represent all the possible sub-queues. For use in
// changedCallback(), there are also the fake sub-queues representing items
// new to the queue and items removed from the queue.
const (
	SubQueueNew       SubQueue = "new"
	SubQueueDelay     SubQueue = "delay"
	SubQueueReady     SubQueue = "ready"
	SubQueueRun       SubQueue = "run"
	SubQueueBury      SubQueue = "bury"
	SubQueueDependent SubQueue = "dependent"
	SubQueueSuspended SubQueue = "suspended"
	SubQueueRemoved   SubQueue = "removed"
)

// defaultTTRCallback is used if the user never calls SetTTRCallback() and
// always moves the items to the ready sub-queue.
func defaultTTRCallback(_ any) SubQueue {
	return SubQueueReady
}

type changedNotification struct {
	callback ChangedCallback
	from     SubQueue
	to       SubQueue
	data     []any
}

// Suspend moves a delayed, ready, or dependent item to the suspended sub-queue.
func (queue *Queue) Suspend(_ context.Context, key string) error {
	queue.mutex.Lock()

	if queue.closed {
		queue.mutex.Unlock()

		return Error{queue.Name, opSuspend, key, ErrQueueClosed}
	}

	item, ok := queue.items[key]
	if !ok {
		queue.mutex.Unlock()

		return Error{queue.Name, opSuspend, key, ErrNotSuspendable}
	}

	from, moved := queue.suspendItem(item)
	if !moved {
		queue.mutex.Unlock()

		return Error{queue.Name, opSuspend, key, ErrNotSuspendable}
	}

	queue.suspendedQueue.push(item)
	queue.changed(from, SubQueueSuspended, []*Item{item})
	queue.mutex.Unlock()

	return nil
}

func (queue *Queue) suspendItem(item *Item) (SubQueue, bool) {
	switch item.state {
	case ItemStateDelay:
		queue.delayQueue.remove(item)
		item.switchDelaySuspended()

		return SubQueueDelay, true
	case ItemStateReady:
		queue.readyQueue.remove(item)
		item.switchReadySuspended()

		return SubQueueReady, true
	case ItemStateDependent:
		queue.depQueue.remove(item)
		item.switchDependentSuspended()

		return SubQueueDependent, true
	default:
		return "", false
	}
}

// Resume moves a suspended item back to ready, or dependent if dependencies
// remain unresolved.
func (queue *Queue) Resume(ctx context.Context, key string) error {
	queue.mutex.Lock()

	if queue.closed {
		queue.mutex.Unlock()

		return Error{queue.Name, "Resume", key, ErrQueueClosed}
	}

	item, ok := queue.items[key]
	if !ok || item.state != ItemStateSuspended {
		queue.mutex.Unlock()

		return Error{queue.Name, "Resume", key, ErrNotSuspended}
	}

	queue.resumeSuspendedItem(ctx, item)

	return nil
}

func (queue *Queue) resumeSuspendedItem(ctx context.Context, item *Item) {
	queue.suspendedQueue.remove(item)

	if len(item.UnresolvedDependencies()) > 0 {
		queue.depQueue.push(item)
		item.switchSuspendedDependent()
		queue.changed(SubQueueSuspended, SubQueueDependent, []*Item{item})
		queue.mutex.Unlock()

		return
	}

	queue.readyQueue.push(item)
	item.switchSuspendedReady()
	queue.changed(SubQueueSuspended, SubQueueReady, []*Item{item})
	queue.mutex.Unlock()
	queue.readyAdded(ctx, "resumed")
}

// runChangedCallbacks drains pending transition notifications sequentially.
func (queue *Queue) runChangedCallbacks() {
	defer queue.finishChangedCallbacks()

	for {
		// changed records notifications while the transition mutex is held;
		// wait for that transition to finish before dequeuing its notification.
		queue.mutex.RLock()
		notification, ok := queue.nextChangedCallback()
		queue.mutex.RUnlock()

		if !ok {
			return
		}

		notification.callback(notification.from, notification.to, notification.data)
	}
}

// finishChangedCallbacks releases the worker slot, or resumes draining after
// a callback terminated the worker goroutine with runtime.Goexit.
func (queue *Queue) finishChangedCallbacks() {
	queue.changedCbMutex.Lock()
	defer queue.changedCbMutex.Unlock()

	if len(queue.changedCbPending) == 0 {
		queue.changedCbRunning = false

		return
	}

	go queue.runChangedCallbacks()
}

// nextChangedCallback returns the next pending transition notification.
func (queue *Queue) nextChangedCallback() (changedNotification, bool) {
	queue.changedCbMutex.Lock()
	defer queue.changedCbMutex.Unlock()

	if len(queue.changedCbPending) == 0 {
		queue.changedCbPending = nil

		return changedNotification{}, false
	}

	notification := queue.changedCbPending[0]
	queue.changedCbPending[0] = changedNotification{}
	queue.changedCbPending = queue.changedCbPending[1:]

	return notification, true
}

// notifyManyAddedChanges queues changed callbacks for the items accumulated by
// AddMany. The queue mutex must be held.
func (queue *Queue) notifyManyAddedChanges(buckets *manyBuckets) {
	if len(buckets.ready) > 0 {
		queue.changed(SubQueueNew, SubQueueReady, buckets.ready)
	}

	if len(buckets.delay) > 0 {
		queue.changed(SubQueueNew, SubQueueDelay, buckets.delay)
	}

	if len(buckets.dep) > 0 {
		queue.changed(SubQueueNew, SubQueueDependent, buckets.dep)
	}

	if len(buckets.run) > 0 {
		queue.changed(SubQueueNew, SubQueueRun, buckets.run)
	}

	if len(buckets.bury) > 0 {
		queue.changed(SubQueueNew, SubQueueBury, buckets.bury)
	}

	if len(buckets.suspended) > 0 {
		queue.changed(SubQueueNew, SubQueueSuspended, buckets.suspended)
	}
}

// notifyTTRMoveChanges queues changed callbacks for items whose TTR expired.
// The queue mutex must be held.
func (queue *Queue) notifyTTRMoveChanges(moves ttrMoves) {
	if len(moves.delayed) > 0 {
		queue.changed(SubQueueRun, SubQueueDelay, moves.delayed)
	}

	if len(moves.buried) > 0 {
		queue.changed(SubQueueRun, SubQueueBury, moves.buried)
	}

	if len(moves.ready) > 0 {
		queue.changed(SubQueueRun, SubQueueReady, moves.ready)
	}
}

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

// ReadyAddedCallback is used as a callback to know when new items have been
// added to the ready sub-queue, getting /all/ items in the ready sub-queue.
type ReadyAddedCallback func(queuename string, allitemdata []any)

// ChangedCallback is used as a callback to know when items change sub-queues,
// telling you what item.Data() moved from which sub-queue to which other sub-
// queue. For new items in the queue, `from` will be SubQueueNew, and for items
// leaving the queue, `to` will be SubQueueRemoved.
type ChangedCallback func(from, to SubQueue, data []any)

// TTRCallback is used as a callback to decide which sub-queue an item should
// move to when a an item in the run sub-queue hits its TTR, based on that
// item's data. Valid return values are SubQueueDelay, SubQueueReady and
// SubQueueBury. SubQueueRun can be used to keep the item in the run sub-queue,
// giving it a fresh TTR so it is re-checked after another full TTR. Other
// values will be treated as SubQueueReady).
type TTRCallback func(data any) SubQueue

// Queue is a synchronised map of items that can shift to different sub-queues,
// automatically depending on their delay or ttr expiring, or manually by
// calling certain methods.
type Queue struct {
	delayTime              time.Time
	ttrTime                time.Time
	Name                   string
	items                  map[string]*Item
	dependants             map[string]map[string]*Item
	delayQueue             *subQueue
	readyQueue             *subQueue
	runQueue               *subQueue
	buryQueue              *buryQueue
	depQueue               *depQueue
	suspendedQueue         *depQueue
	delayNotification      chan bool
	startedDelayProcessing chan bool
	delayClose             chan bool
	ttrNotification        chan bool
	startedTTRProcessing   chan bool
	ttrClose               chan bool
	readyAddedCb           ReadyAddedCallback
	changedCb              ChangedCallback
	ttrCb                  TTRCallback
	mutex                  sync.RWMutex
	readyAddedCbMutex      sync.Mutex
	changedCbMutex         sync.Mutex
	changedCbPending       []changedNotification
	closed                 bool
	readyAddedCbRunning    bool
	readyAddedCbRecall     bool
	changedCbRunning       bool
}

// Stats holds information about the Queue's state.
type Stats struct {
	Items     int
	Delayed   int
	Ready     int
	Running   int
	Buried    int
	Dependant int
	Suspended int
}

// ItemDef makes it possible to supply a slice of Add() args to AddMany().
type ItemDef struct {
	Key          string
	ReserveGroup string
	Data         any
	Priority     uint8 // highest priority is 255
	Delay        time.Duration
	TTR          time.Duration
	StartQueue   SubQueue // blank, or one of SubQueueRun or SubQueueBury
	Dependencies []string
}

// New is a helper to create instance of the Queue struct.
func New(ctx context.Context, name string) *Queue {
	queue := &Queue{
		Name:                   name,
		items:                  make(map[string]*Item),
		dependants:             make(map[string]map[string]*Item),
		delayQueue:             newSubQueue(delayQueueIndex),
		readyQueue:             newSubQueue(readyQueueIndex),
		runQueue:               newSubQueue(runQueueIndex),
		buryQueue:              newBuryQueue(),
		depQueue:               newDependencyQueue(),
		suspendedQueue:         newSuspendedQueue(),
		ttrNotification:        make(chan bool, 1),
		startedTTRProcessing:   make(chan bool),
		ttrClose:               make(chan bool, 1),
		ttrTime:                time.Now(),
		delayNotification:      make(chan bool, 1),
		startedDelayProcessing: make(chan bool),
		delayClose:             make(chan bool, 1),
		delayTime:              time.Now(),
		ttrCb:                  defaultTTRCallback,
	}
	go queue.startDelayProcessing(ctx)

	<-queue.startedDelayProcessing
	go queue.startTTRProcessing(ctx)

	<-queue.startedTTRProcessing

	return queue
}

// SetReadyAddedCallback sets a callback that will be called when new items have
// been added to the ready sub-queue. The callback will receive the name of the
// queue, and a slice of the Data properties of every item currently in the
// ready sub-queue. The callback will be initiated in a go routine.
//
// Note that we will wait for the callback to finish running before calling it
// again. If new items enter the ready sub-queue while your callback is still
// running, you will only know about them when your callback is called again,
// immediately after the previous call completes.
func (queue *Queue) SetReadyAddedCallback(callback ReadyAddedCallback) {
	queue.readyAddedCb = callback
}

// TriggerReadyAddedCallback allows you to manually trigger your
// readyAddedCallback at times when no new items have been added to the ready
// queue. It will receive the current set of ready item data.
func (queue *Queue) TriggerReadyAddedCallback(ctx context.Context) {
	queue.readyAdded(ctx, "triggered")
}

// readyAdded checks if a readyAddedCallback has been set, and if so calls it
// in a go routine. It never runs the callback concurrently though: if it is
// still running from a previous call, we only schedule that the callback be
// called (once) after the current call completes.
func (queue *Queue) readyAdded(ctx context.Context, source string) {
	if queue.readyAddedCb == nil {
		return
	}

	queue.readyAddedCbMutex.Lock()
	if queue.readyAddedCbRunning {
		queue.readyAddedCbRecall = true
		queue.readyAddedCbMutex.Unlock()

		return
	}

	queue.readyAddedCbRunning = true
	queue.readyAddedCbMutex.Unlock()

	go queue.runReadyAddedCb(ctx, source)
}

// runReadyAddedCb gathers the current ready item data, calls the
// readyAddedCallback, and then reschedules itself if another call was requested
// while it was running. It is always launched in its own goroutine by
// readyAdded.
func (queue *Queue) runReadyAddedCb(ctx context.Context, source string) {
	data := queue.readyItemData()

	clog.Debug(ctx, "ready items available, triggering callback", "source", source, "items", len(data))
	queue.readyAddedCb(queue.Name, data)
	clog.Debug(ctx, "finished triggering callback for ready items", "source", source, "items", len(data))

	queue.rescheduleReadyAddedIfRecall(ctx)
}

// readyItemData returns the Data() of every item currently in the ready
// sub-queue.
func (queue *Queue) readyItemData() []any {
	queue.mutex.RLock()
	defer queue.mutex.RUnlock()

	data := make([]any, 0, queue.readyQueue.len())

	for _, il := range queue.readyQueue.groupedItems {
		for _, item := range il {
			data = append(data, item.Data())
		}
	}

	return data
}

// rescheduleReadyAddedIfRecall finishes a runReadyAddedCb call: if another call
// was requested while it was running, it waits a short while and triggers
// readyAdded again; otherwise it marks the callback as no longer running.
func (queue *Queue) rescheduleReadyAddedIfRecall(ctx context.Context) {
	queue.readyAddedCbMutex.Lock()

	recall := false
	if queue.readyAddedCbRecall {
		recall = true
	} else {
		queue.readyAddedCbRunning = false
	}
	queue.readyAddedCbMutex.Unlock()

	if !recall {
		return
	}

	// wait before the recall to stop us being constantly locked getting ready
	// items
	<-time.After(recallBreak)
	queue.readyAddedCbMutex.Lock()
	queue.readyAddedCbRunning = false

	queue.readyAddedCbRecall = false
	defer queue.readyAdded(ctx, "recall")
	queue.readyAddedCbMutex.Unlock()
}

// SetChangedCallback sets a callback that will be called when items move from
// one sub-queue to another. The callback receives the name of the moved-from
// sub-queue ('new' in the case of entering the queue for the first time), the
// name of the moved-to sub-queue ('removed' in the case of the item being
// removed from the queue), and a slice of item.Data() of everything that moved
// in this way. Callbacks are initiated in a goroutine and run sequentially in
// transition-notification order.
func (queue *Queue) SetChangedCallback(callback ChangedCallback) {
	queue.changedCbMutex.Lock()
	defer queue.changedCbMutex.Unlock()

	queue.changedCb = callback
}

// changed queues a changedCallback notification. The queue mutex must be held
// so notifications are recorded in queue-transition order. A single goroutine
// drains the notifications, so mutations do not wait for callbacks.
func (queue *Queue) changed(from, to SubQueue, items []*Item) {
	queue.changedCbMutex.Lock()
	defer queue.changedCbMutex.Unlock()

	if queue.changedCb == nil {
		return
	}

	data := make([]any, 0, len(items))
	for _, item := range items {
		data = append(data, item.Data())
	}

	queue.changedCbPending = append(queue.changedCbPending, changedNotification{
		callback: queue.changedCb,
		from:     from,
		to:       to,
		data:     data,
	})
	if queue.changedCbRunning {
		return
	}

	queue.changedCbRunning = true

	go queue.runChangedCallbacks()
}

// SetTTRCallback sets a callback that will be called when an item in the run
// sub-queue hits its TTR. The callback receives an item's data and should
// return the sub-queue the item should be moved to. If you don't set this, the
// default will be to move all items to the ready sub-queue.
func (queue *Queue) SetTTRCallback(callback TTRCallback) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	queue.ttrCb = callback
}

// Destroy shuts down a queue, destroying any contents. You can't do anything
// useful with it after that.
func (queue *Queue) Destroy() error {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if queue.closed {
		return Error{queue.Name, "Destroy", "", ErrQueueClosed}
	}

	queue.ttrClose <- true

	queue.delayClose <- true

	queue.items = nil
	queue.delayQueue.empty()
	queue.readyQueue.empty()
	queue.runQueue.empty()
	queue.buryQueue.empty()
	queue.depQueue.empty()
	queue.suspendedQueue.empty()
	queue.closed = true

	return nil
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
		Suspended: queue.suspendedQueue.len(),
	}
}

// Add is a thread-safe way to add new items to the queue.
//
// After delay they will switch to the ready sub-queue from where they can be
// Reserve()d. Once reserved, they have ttr to Remove() the item, otherwise it
// gets released back to the ready sub-queue.
//
// The priority determines which item will be next to be Reserve()d, with
// priority 255 (the max) items coming before lower priority ones (with 0 being
// the lowest). Items with the same priority number are Reserve()d on a fifo
// basis.
//
// reserveGroup can be left as an empty string, but specifying it then lets you
// provide the same to Reserve() to get the next item with the given
// reserveGroup.
//
// startQueue should normally be supplied as an empty string, meaning the item
// will start in the delay or ready sub-queue as described above. For the
// purpose of recovering a queue following a crash, however, you can supply
// either SubQueueRun or SubQueueBury to start the item in one of those
// sub-queues. If the item has unmet dependencies, startQueue is ignored.
//
// The final argument to Add() is an optional slice of item ids on which this
// item depends: this item will first enter the dependency sub-queue and only
// transfer to the ready sub-queue when items with these ids get Remove()d from
// the queue.
//
// Add() returns an item, which may have already existed (in which case, nothing
// was actually added or changed).
func (queue *Queue) Add(ctx context.Context, key string, reserveGroup string, data any, priority uint8,
	delay time.Duration, ttr time.Duration, startQueue SubQueue, deps ...[]string,
) (*Item, error) {
	queue.mutex.Lock()

	item, err := queue.newItemForAdd(key, reserveGroup, data, priority, 0, delay, ttr)
	if err != nil {
		queue.mutex.Unlock()

		return item, err
	}

	queue.handleItemForAdd(ctx, item, startQueue, delay, deps...)

	return item, nil
}

// newItemForAdd prepares a new item for Add() and AddWithSize() methods. You
// must hold the mutex lock before calling this.
func (queue *Queue) newItemForAdd(key string, reserveGroup string, data any, priority uint8, size uint8,
	delay time.Duration, ttr time.Duration,
) (*Item, error) {
	if queue.closed {
		return nil, Error{queue.Name, "Add", key, ErrQueueClosed}
	}

	item, existed := queue.items[key]
	if existed {
		return item, Error{queue.Name, "Add", key, ErrAlreadyExists}
	}

	item = newItem(key, reserveGroup, data, priority, delay, ttr)
	item.size = size
	queue.items[key] = item

	return item, nil
}

// handleItemForAdd checks dependencies and then pushes the item to the desired
// subqueue. You must hold the mutex lock before calling this. It will unlock.
func (queue *Queue) handleItemForAdd(ctx context.Context, item *Item, startQueue SubQueue, delay time.Duration,
	deps ...[]string,
) {
	// check dependencies
	if len(deps) == 1 && len(deps[0]) > 0 {
		queue.addDependentItem(item, startQueue, deps[0])

		return
	}

	switch startQueue {
	case SubQueueRun:
		queue.addRunItem(item)
	case SubQueueBury:
		queue.addBuryItem(item)
	case SubQueueSuspended:
		item.switchDelaySuspended()
		queue.suspendedQueue.push(item)
		queue.changed(SubQueueNew, SubQueueSuspended, []*Item{item})
		queue.mutex.Unlock()
	default:
		queue.addReadyOrDelayedItem(ctx, item, delay)
	}
}

// addRunItem places a newly-added item directly onto the run sub-queue (used
// when recovering a queue). You must hold the mutex lock before calling this;
// it will unlock.
func (queue *Queue) addRunItem(item *Item) {
	item.switchDelayReady()
	item.touch()
	queue.runQueue.push(item)
	item.switchReadyRun()
	queue.changed(SubQueueNew, SubQueueRun, []*Item{item})
	queue.mutex.Unlock()
	queue.ttrNotificationTrigger(item)
}

// addBuryItem places a newly-added item directly onto the bury sub-queue (used
// when recovering a queue). You must hold the mutex lock before calling this;
// it will unlock.
func (queue *Queue) addBuryItem(item *Item) {
	item.switchDelayReady()
	queue.buryQueue.push(item)
	item.switchRunBury()
	queue.changed(SubQueueNew, SubQueueBury, []*Item{item})
	queue.mutex.Unlock()
}

// addDependentItem places a newly-added item that has dependencies onto either
// the suspended or dependent sub-queue. You must hold the mutex lock before
// calling this; it will unlock.
func (queue *Queue) addDependentItem(item *Item, startQueue SubQueue, deps []string) {
	if startQueue == SubQueueSuspended {
		item.setDependencies(deps)
		queue.setQueueDeps(item)
		item.switchDelaySuspended()
		queue.suspendedQueue.push(item)
		queue.changed(SubQueueNew, SubQueueSuspended, []*Item{item})
		queue.mutex.Unlock()

		return
	}

	queue.setItemDependencies(item, deps)
	queue.changed(SubQueueNew, SubQueueDependent, []*Item{item})
	queue.mutex.Unlock()
}

// addReadyOrDelayedItem places a newly-added item with no dependencies and no
// special startQueue directly onto the ready sub-queue (if it has no delay) or
// the delay sub-queue. You must hold the mutex lock before calling this; it
// will unlock.
func (queue *Queue) addReadyOrDelayedItem(ctx context.Context, item *Item, delay time.Duration) {
	if delay.Nanoseconds() == 0 {
		// put it directly on the ready queue
		item.switchDelayReady()
		queue.readyQueue.push(item)
		queue.changed(SubQueueNew, SubQueueReady, []*Item{item})
		queue.mutex.Unlock()
		queue.readyAdded(ctx, "new")

		return
	}

	queue.delayQueue.push(item)
	queue.changed(SubQueueNew, SubQueueDelay, []*Item{item})
	queue.mutex.Unlock()
	queue.delayNotificationTrigger(item)
}

// AddWithSize is like Add(), but the item also gets a "size" property.
// Size alters the way priority is handled. For items with the same priority,
// the next to be Reserve()d will be the item with the highest size. If they
// also have the same size, then they will be Reserve()d in fifo order.
func (queue *Queue) AddWithSize(ctx context.Context, key string, reserveGroup string, data any, priority uint8,
	size uint8, delay time.Duration, ttr time.Duration, startQueue SubQueue, deps ...[]string,
) (*Item, error) {
	queue.mutex.Lock()

	item, err := queue.newItemForAdd(key, reserveGroup, data, priority, size, delay, ttr)
	if err != nil {
		queue.mutex.Unlock()

		return item, err
	}

	queue.handleItemForAdd(ctx, item, startQueue, delay, deps...)

	return item, nil
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
func (queue *Queue) AddMany(ctx context.Context, items []*ItemDef) (added, dups int, err error) {
	queue.mutex.Lock()

	if queue.closed {
		queue.mutex.Unlock()

		return 0, 0, Error{queue.Name, "AddMany", "", ErrQueueClosed}
	}

	buckets, added, dups := queue.collectManyItems(items)

	// these triggers are deferred so they fire after the lock is released and
	// the changed callbacks have been issued, matching the original behaviour.
	// The two triggers act on independent channels and queue fields, so their
	// relative order has no observable effect.
	if buckets.ttrTriggerItem != nil {
		defer queue.ttrNotificationTrigger(buckets.ttrTriggerItem)
	}

	if buckets.delayTriggerItem != nil {
		defer queue.delayNotificationTrigger(buckets.delayTriggerItem)
	}

	queue.notifyManyAddedChanges(&buckets)
	queue.mutex.Unlock()

	if len(buckets.ready) > 0 {
		queue.readyAdded(ctx, "new")
	}

	return added, dups, err
}

// collectManyItems creates and places every non-duplicate item from defs onto
// its sub-queue, returning the accumulated buckets along with how many items
// were added and how many were skipped as duplicates. You must hold the mutex
// lock before calling this.
func (queue *Queue) collectManyItems(defs []*ItemDef) (manyBuckets, int, int) {
	var buckets manyBuckets

	added, dups := 0, 0

	for _, def := range defs {
		if _, existed := queue.items[def.Key]; existed {
			dups++

			continue
		}

		item := newItem(def.Key, def.ReserveGroup, def.Data, def.Priority, def.Delay, def.TTR)
		queue.items[def.Key] = item

		queue.addManyItem(def, item, &buckets)

		added++
	}

	return buckets, added, dups
}

// manyBuckets accumulates the items added by AddMany, grouped by the sub-queue
// they were placed in, plus the (first) items that need a delay or ttr
// notification trigger after the add completes.
type manyBuckets struct {
	ready            []*Item
	delay            []*Item
	dep              []*Item
	run              []*Item
	bury             []*Item
	suspended        []*Item
	ttrTriggerItem   *Item
	delayTriggerItem *Item
}

// addManyItem places a single newly-created AddMany item onto the appropriate
// sub-queue and records it in buckets. You must hold the mutex lock before
// calling this.
func (queue *Queue) addManyItem(def *ItemDef, item *Item, buckets *manyBuckets) {
	switch {
	case len(def.Dependencies) > 0 && def.StartQueue == SubQueueSuspended:
		item.setDependencies(def.Dependencies)
		queue.setQueueDeps(item)
		item.switchDelaySuspended()
		queue.suspendedQueue.push(item)
		buckets.suspended = append(buckets.suspended, item)
	case len(def.Dependencies) > 0:
		queue.setItemDependencies(item, def.Dependencies)
		buckets.dep = append(buckets.dep, item)
	default:
		queue.addManyItemToStartQueue(def, item, buckets)
	}
}

// addManyItemToStartQueue handles the AddMany items that have no dependencies,
// placing them according to their StartQueue (or delay/ready by default). You
// must hold the mutex lock before calling this.
func (queue *Queue) addManyItemToStartQueue(def *ItemDef, item *Item, buckets *manyBuckets) {
	switch def.StartQueue {
	case SubQueueRun:
		queue.addManyRunItem(item, buckets)
	case SubQueueBury:
		item.switchDelayReady()
		queue.buryQueue.push(item)
		item.switchReadyRun()
		item.switchRunBury()
		buckets.bury = append(buckets.bury, item)
	case SubQueueSuspended:
		item.switchDelaySuspended()
		queue.suspendedQueue.push(item)
		buckets.suspended = append(buckets.suspended, item)
	default:
		queue.addManyReadyOrDelayedItem(def, item, buckets)
	}
}

// addManyRunItem places an AddMany item directly onto the run sub-queue,
// recording it and (if it is the first such item that needs one) the deferred
// ttr trigger in buckets. You must hold the mutex lock before calling this.
func (queue *Queue) addManyRunItem(item *Item, buckets *manyBuckets) {
	item.switchDelayReady()
	item.touch()
	queue.runQueue.push(item)
	item.switchReadyRun()

	buckets.run = append(buckets.run, item)
	if buckets.ttrTriggerItem == nil && queue.ttrTime.After(time.Now().Add(item.ttr)) {
		buckets.ttrTriggerItem = item
	}
}

// addManyReadyOrDelayedItem places an AddMany item with no dependencies and no
// special StartQueue onto the ready sub-queue (if it has no delay) or the delay
// sub-queue, recording it in buckets. You must hold the mutex lock before
// calling this.
func (queue *Queue) addManyReadyOrDelayedItem(def *ItemDef, item *Item, buckets *manyBuckets) {
	if def.Delay.Nanoseconds() == 0 {
		// put it directly on the ready queue
		item.switchDelayReady()
		queue.readyQueue.push(item)
		buckets.ready = append(buckets.ready, item)

		return
	}

	queue.delayQueue.push(item)

	buckets.delay = append(buckets.delay, item)
	if buckets.delayTriggerItem == nil && queue.delayTime.After(time.Now().Add(item.delay)) {
		buckets.delayTriggerItem = item
	}
}

// Get is a thread-safe way to get an item by the key you used to Add() it.
func (queue *Queue) Get(key string) (*Item, error) {
	queue.mutex.RLock()
	defer queue.mutex.RUnlock()

	if queue.closed {
		return nil, Error{queue.Name, "Get", key, ErrQueueClosed}
	}

	item, exists := queue.items[key]
	if !exists {
		return nil, Error{queue.Name, "Get", key, ErrNotFound}
	}

	return item, nil
}

// GetRunningData gets all the item.Data() of items currently in the run sub-
// queue.
func (queue *Queue) GetRunningData() []any {
	queue.mutex.RLock()
	defer queue.mutex.RUnlock()

	data := make([]any, 0, len(queue.runQueue.items))
	for _, item := range queue.runQueue.items {
		data = append(data, item.Data())
	}

	return data
}

// AllItems returns the items in the queue. NB: You should NOT do anything
// to these items - use for read-only purposes.
func (queue *Queue) AllItems() []*Item {
	queue.mutex.RLock()
	defer queue.mutex.RUnlock()

	items := make([]*Item, 0, len(queue.items))
	for _, item := range queue.items {
		items = append(items, item)
	}

	return items
}

// Update is a thread-safe way to change the data, ReserveGroup, priority, delay,
// ttr or dependencies of an item. You must supply all of these as per Add() -
// just supply the old values of those you are not changing (except for
// dependencies, which remain optional). The old values can be found by getting
// the item with Get() (giving you item.Key, item.ReserveGroup, item.Data() and
// item.UnresolvedDependencies()), and then calling item.Stats() to get
// stats.Priority, stats.Delay and stats.TTR.
func (queue *Queue) Update(ctx context.Context, key string, reserveGroup string, data any, priority uint8,
	delay time.Duration, ttr time.Duration, deps ...[]string,
) error {
	item, err := queue.lockExistingItem("Update", key)
	if err != nil {
		return err
	}

	var (
		changedFrom SubQueue
		addedReady  bool
	)

	item.SetData(data)

	if len(deps) == 1 {
		changedFrom, addedReady = queue.updateDependencies(item, key, deps[0])
	}

	queue.applyItemPropertyChanges(item, reserveGroup, priority, delay, ttr, addedReady)

	queue.notifyUpdate(ctx, item, changedFrom, addedReady)

	return nil
}

// notifyUpdate queues changed callbacks while the mutex held on entry still
// establishes transition order, then releases it and reports newly ready work.
func (queue *Queue) notifyUpdate(ctx context.Context, item *Item, changedFrom SubQueue, addedReady bool) {
	if addedReady {
		queue.changed(SubQueueDependent, SubQueueReady, []*Item{item})
	}

	if changedFrom != "" {
		queue.changed(changedFrom, SubQueueDependent, []*Item{item})
	}

	queue.mutex.Unlock()

	if addedReady {
		queue.readyAdded(ctx, "updated")
	}
}

// updateDependencies applies a new set of dependencies to an item during
// Update(), moving it between the dependent and other sub-queues as needed. It
// returns the sub-queue the item moved from (if it became dependent) and
// whether it became ready. You must hold the mutex lock before calling this.
func (queue *Queue) updateDependencies(item *Item, key string, newDeps []string) (SubQueue, bool) {
	toRemove, added := diffDependencies(item, newDeps)
	if len(toRemove) == 0 && added == 0 {
		return "", false
	}

	// remove any invalid dependencies from our lookup
	queue.pruneDependants(key, toRemove)

	// set the new dependencies and update our lookup
	item.setDependencies(newDeps)
	queue.setQueueDeps(item)

	iState := item.State()

	// if we now have unresolved dependencies and we're not in dependent state,
	// switch to the dependent queue
	if len(newDeps) > 0 && iState != ItemStateDependent {
		return queue.moveToDependentQueue(item, iState), false
	}

	if len(newDeps) == 0 {
		return "", queue.moveDependentToReady(item)
	}

	return "", false
}

// diffDependencies compares an item's current unresolved dependencies against
// the proposed newDeps, returning the dependencies to remove and the number of
// newly-added dependencies.
func diffDependencies(item *Item, newDeps []string) ([]string, int) {
	oldDeps := make(map[string]bool)
	for _, dep := range item.UnresolvedDependencies() {
		oldDeps[dep] = true
	}

	added := 0

	for _, dep := range newDeps {
		if !oldDeps[dep] {
			added++
		}

		delete(oldDeps, dep)
	}

	toRemove := make([]string, 0, len(oldDeps))
	for dep := range oldDeps {
		toRemove = append(toRemove, dep)
	}

	return toRemove, added
}

// pruneDependants removes the given key from the dependants lookup for each of
// the toRemove parent keys that still exist in the queue. You must hold the
// mutex lock before calling this.
func (queue *Queue) pruneDependants(key string, toRemove []string) {
	for _, dep := range toRemove {
		if _, exists := queue.items[dep]; exists {
			delete(queue.dependants[dep], key)

			if len(queue.dependants[dep]) == 0 {
				delete(queue.dependants, dep)
			}
		}
	}
}

// moveToDependentQueue moves an item in the given state out of its current
// sub-queue and onto the dependent queue (unless it is buried or suspended, in
// which case it stays put). It returns the sub-queue the item moved from, or
// the empty string if it did not move. You must hold the mutex lock before
// calling this.
func (queue *Queue) moveToDependentQueue(item *Item, iState ItemState) SubQueue {
	changedFrom, pushToDep := queue.detachForDependentMove(item, iState)
	if pushToDep {
		queue.depQueue.push(item)
	}

	return changedFrom
}

// detachForDependentMove removes an item from its current sub-queue ahead of it
// becoming dependent, returning the sub-queue it moved from and whether it
// should now be pushed onto the dependent queue. Buried and suspended items are
// left in place. You must hold the mutex lock before calling this.
func (queue *Queue) detachForDependentMove(item *Item, iState ItemState) (SubQueue, bool) {
	switch iState {
	case ItemStateDelay:
		queue.delayQueue.remove(item)
		item.switchDelayDependent()

		return SubQueueDelay, true
	case ItemStateReady:
		queue.readyQueue.remove(item)
		item.switchReadyDependent()

		return SubQueueReady, true
	case ItemStateRun:
		queue.runQueue.remove(item)
		item.switchRunDependent()

		return SubQueueRun, true
	case ItemStateBury:
		// leave buried things buried; Kick() will put it on the dependent queue
		// if they are still unresolved by then
		return "", false
	case ItemStateSuspended:
		return "", false
	default:
		// any other state is left in place but still pushed onto the dependent
		// queue, matching the original fall-through behaviour
		return "", true
	}
}

// moveDependentToReady moves an item that is currently in the dependent
// sub-queue to the ready sub-queue, returning true if it did so. You must hold
// the mutex lock before calling this.
func (queue *Queue) moveDependentToReady(item *Item) bool {
	if item.State() != ItemStateDependent {
		return false
	}

	queue.depQueue.remove(item)
	item.switchDependentReady()
	queue.readyQueue.push(item)

	return true
}

// applyItemPropertyChanges updates an item's delay, priority/ReserveGroup or
// ttr (whichever changed), correcting its position in its current sub-queue. It
// mirrors the original single switch: at most one category of change is applied
// per call, in that priority order. You must hold the mutex lock before calling
// this.
func (queue *Queue) applyItemPropertyChanges(item *Item, reserveGroup string, priority uint8,
	delay time.Duration, ttr time.Duration, addedReady bool,
) {
	item.mutex.Lock()
	switch {
	case item.delay != delay:
		queue.applyDelayUpdate(item, delay)
	case item.priority != priority || item.ReserveGroup != reserveGroup || addedReady:
		queue.applyPriorityGroupUpdate(item, priority, reserveGroup)
	case item.ttr != ttr:
		queue.applyTTRUpdate(item, ttr)
	default:
		item.mutex.Unlock()
	}
}

// applyDelayUpdate sets a new delay on the item and, if it is in the delay
// sub-queue, restarts it there. You must hold the item lock on entry; it
// unlocks the item.
func (queue *Queue) applyDelayUpdate(item *Item, delay time.Duration) {
	item.delay = delay
	inState := item.state == ItemStateDelay
	item.mutex.Unlock()

	if inState {
		item.restart()
		queue.delayQueue.update(item)
	}
}

// applyPriorityGroupUpdate sets a new priority and ReserveGroup on the item and,
// if it is in the ready sub-queue, corrects its position there. You must hold
// the item lock on entry; it unlocks the item.
func (queue *Queue) applyPriorityGroupUpdate(item *Item, priority uint8, reserveGroup string) {
	item.priority = priority
	oldGroup := item.ReserveGroup

	item.ReserveGroup = reserveGroup
	inState := item.state == ItemStateReady
	item.mutex.Unlock()

	if inState {
		queue.readyQueue.update(item, oldGroup)
	}
}

// applyTTRUpdate sets a new ttr on the item and, if it is in the run sub-queue,
// touches it and corrects its position there. You must hold the item lock on
// entry; it unlocks the item.
func (queue *Queue) applyTTRUpdate(item *Item, ttr time.Duration) {
	item.ttr = ttr
	inState := item.state == ItemStateRun
	item.mutex.Unlock()

	if inState {
		item.touch()
		queue.runQueue.update(item)
	}
}

// ChangeKey is a thread-safe way to change the key an item can be found with
// using Get() (and also ensures any dependencies involving the old key will
// continue to work). If an item already exists in the queue with the new key,
// this will fail.
func (queue *Queue) ChangeKey(old, newKey string) error {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if queue.closed {
		return Error{queue.Name, opChangeKey, old, ErrQueueClosed}
	}

	if _, exists := queue.items[newKey]; exists {
		return Error{queue.Name, opChangeKey, newKey, ErrAlreadyExists}
	}

	item, exists := queue.items[old]
	if !exists {
		return Error{queue.Name, opChangeKey, old, ErrNotFound}
	}

	delete(queue.items, old)
	queue.items[newKey] = item

	queue.renameInDependants(old, newKey)

	for _, item := range queue.items {
		item.ChangedKey(old, newKey)
	}

	return nil
}

// renameInDependants updates the dependants lookup so that the parent key old
// and any child entries keyed by old are re-keyed to new. You must hold the
// mutex lock before calling this.
func (queue *Queue) renameInDependants(old, newKey string) {
	if val, exists := queue.dependants[old]; exists {
		delete(queue.dependants, old)
		queue.dependants[newKey] = val
	}

	for _, items := range queue.dependants {
		if val, exists := items[old]; exists {
			delete(items, old)
			items[newKey] = val
		}
	}
}

// SetDelay is a thread-safe way to change the delay of an item.
func (queue *Queue) SetDelay(key string, delay time.Duration) error {
	item, err := queue.lockExistingItem("SetDelay", key)
	if err != nil {
		return err
	}

	queue.applyDelayChange(item, delay)

	return nil
}

// applyDelayChange sets a new delay on the item and, if it is currently in the
// delay sub-queue, restarts its countdown and triggers delay processing. You
// must hold the mutex lock before calling this; it will unlock.
func (queue *Queue) applyDelayChange(item *Item, delay time.Duration) {
	item.mutex.Lock()
	if item.delay != delay {
		item.delay = delay
		if item.state == ItemStateDelay {
			item.mutex.Unlock()
			item.restart()
			queue.delayQueue.update(item)
			queue.mutex.Unlock()
			queue.delayNotificationTrigger(item)

			return
		}
	}
	item.mutex.Unlock()
	queue.mutex.Unlock()
}

// SetReserveGroup is a thread-safe way to change the ReserveGroup of an item.
func (queue *Queue) SetReserveGroup(key string, newGroup string) error {
	item, err := queue.lockExistingItem("SetReserveGroup", key)
	if err != nil {
		return err
	}

	queue.applyReserveGroupChange(item, newGroup)
	queue.mutex.Unlock()

	return nil
}

// applyReserveGroupChange sets a new ReserveGroup on the item and, if it is
// currently in the ready sub-queue, corrects its position there. You must hold
// the mutex lock before calling this; it does not unlock.
func (queue *Queue) applyReserveGroupChange(item *Item, newGroup string) {
	item.mutex.Lock()

	oldGroup := item.ReserveGroup
	if oldGroup == newGroup {
		item.mutex.Unlock()

		return
	}

	item.ReserveGroup = newGroup
	if item.state == ItemStateReady {
		item.mutex.Unlock()
		queue.readyQueue.update(item, oldGroup)

		return
	}

	item.mutex.Unlock()
}

// Reserve is a thread-safe way to get the highest priority (or for those with
// equal priority, the oldest (by time since the item was first Add()ed) item in
// the queue, switching it from the ready sub-queue to the run sub-queue, and in
// so doing starting its ttr countdown.
//
// If reserveGroup is not blank, you will get the next item that was added with
// the given ReserveGroup (conversely, if your items were added with
// ReserveGroups but you don't supply one here, you will not get an item).
//
// If wait is greater than 0, we will wait for up to that much time for an item
// to appear in the ready sub-queue, if at least 1 isn't already there. If after
// this time there is still nothing in the ready sub-queue, no item and a
// ErrNothingReady error is returned.
//
// You need to Remove() the item when you're done with it. If you're still doing
// something and ttr is approaching, Touch() it, otherwise it will be assumed
// you died and the item will be released back to the ready sub-queue
// automatically, to be handled by someone else that gets it from a Reserve()
// call. If you know you can't handle it right now, but someone else might be
// able to later, you can manually call Release(), which moves it to the delay
// sub-queue.
func (queue *Queue) Reserve(reserveGroup string, wait time.Duration) (*Item, error) {
	queue.mutex.Lock()

	if queue.closed {
		queue.mutex.Unlock()

		return nil, Error{queue.Name, "Reserve", "", ErrQueueClosed}
	}

	// pop an item from the ready queue and add it to the run queue
	item := queue.readyQueue.pop(reserveGroup)
	if item == nil {
		item = queue.waitForReadyItem(reserveGroup, wait)
		if item == nil {
			return nil, Error{queue.Name, "Reserve", "", ErrNothingReady}
		}
	}

	item.touch()
	queue.runQueue.push(item)
	item.switchReadyRun()

	queue.changed(SubQueueReady, SubQueueRun, []*Item{item})
	queue.mutex.Unlock()
	queue.ttrNotificationTrigger(item)

	return item, nil
}

// waitForReadyItem is called by Reserve when the ready sub-queue had no
// matching item. On entry the mutex lock is held. If wait is greater than zero
// it waits up to that long for a matching item to be pushed and then retries
// the pop. It returns the popped item with the lock still held, or nil with the
// lock released if nothing became ready in time.
func (queue *Queue) waitForReadyItem(reserveGroup string, wait time.Duration) *Item {
	if wait <= 0 {
		queue.mutex.Unlock()

		return nil
	}

	ch := make(chan bool, 1)
	queue.readyQueue.notifyPush(reserveGroup, ch, wait)
	queue.mutex.Unlock()

	// wait until something is pushed to the ready queue or we hit the timeout
	if tryAgain := <-ch; !tryAgain {
		return nil
	}

	queue.mutex.Lock()

	item := queue.readyQueue.pop(reserveGroup)
	if item == nil {
		queue.mutex.Unlock()
	}

	return item
}

// lockExistingItem locks the queue and looks up the item with the given key. On
// success the mutex lock is held and the item is returned. On failure the lock
// is released and an Error is returned using op and one of ErrQueueClosed or
// ErrNotFound.
func (queue *Queue) lockExistingItem(op, key string) (*Item, error) {
	queue.mutex.Lock()

	if queue.closed {
		queue.mutex.Unlock()

		return nil, Error{queue.Name, op, key, ErrQueueClosed}
	}

	item, exists := queue.items[key]
	if !exists {
		queue.mutex.Unlock()

		return nil, Error{queue.Name, op, key, ErrNotFound}
	}

	return item, nil
}

// lockItemInState locks the queue and looks up the item with the given key,
// requiring it to currently be in wantState. On success the mutex lock is held
// and the item is returned. On failure the lock is released and an Error is
// returned using op and one of ErrQueueClosed, ErrNotFound or wrongStateErr.
func (queue *Queue) lockItemInState(op, key string, wantState ItemState, wrongStateErr error) (*Item, error) {
	queue.mutex.Lock()

	if queue.closed {
		queue.mutex.Unlock()

		return nil, Error{queue.Name, op, key, ErrQueueClosed}
	}

	// check it's actually still in the queue first
	item, ok := queue.items[key]
	if !ok {
		queue.mutex.Unlock()

		return nil, Error{queue.Name, op, key, ErrNotFound}
	}

	// and it must be in the expected sub-queue
	if item.state != wantState {
		queue.mutex.Unlock()

		return nil, Error{queue.Name, op, key, wrongStateErr}
	}

	return item, nil
}

// Touch is a thread-safe way to extend the amount of time a Reserve()d item
// is allowed to run.
func (queue *Queue) Touch(key string) error {
	item, err := queue.lockItemInState(opTouch, key, ItemStateRun, ErrNotRunning)
	if err != nil {
		return err
	}

	// touch and update the heap
	item.touch()
	queue.runQueue.update(item)

	queue.mutex.Unlock()
	queue.ttrNotificationTrigger(item)

	return nil
}

// Release is a thread-safe way to switch an item in the run sub-queue to the
// delay sub-queue, for when the item should be dealt with later, not now.
func (queue *Queue) Release(ctx context.Context, key string) error {
	item, err := queue.lockItemInState(opRelease, key, ItemStateRun, ErrNotRunning)
	if err != nil {
		return err
	}

	// switch from run to delay queue (unless there is no delay, in which case
	// straight to ready)
	queue.runQueue.remove(item)

	if item.delay.Nanoseconds() == 0 {
		item.switchRunReady()
		queue.readyQueue.push(item)
		queue.changed(SubQueueRun, SubQueueReady, []*Item{item})
		queue.mutex.Unlock()
		queue.readyAdded(ctx, "released")
	} else {
		item.restart()
		queue.delayQueue.push(item)
		item.switchRunDelay()
		queue.changed(SubQueueRun, SubQueueDelay, []*Item{item})
		queue.mutex.Unlock()
		queue.delayNotificationTrigger(item)
	}

	return nil
}

// Bury is a thread-safe way to switch an item in the run sub-queue to the
// bury sub-queue, for when the item can't be dealt with ever, at least until
// the user takes some action and changes something.
func (queue *Queue) Bury(key string) error {
	item, err := queue.lockItemInState(opBury, key, ItemStateRun, ErrNotRunning)
	if err != nil {
		return err
	}

	// switch from run to bury queue
	queue.runQueue.remove(item)
	queue.buryQueue.push(item)
	item.switchRunBury()
	queue.changed(SubQueueRun, SubQueueBury, []*Item{item})
	queue.mutex.Unlock()

	return nil
}

// Kick is a thread-safe way to switch an item in the bury sub-queue to the
// ready sub-queue, for when a previously buried item can now be handled.
func (queue *Queue) Kick(ctx context.Context, key string) error {
	item, err := queue.lockItemInState(opKick, key, ItemStateBury, ErrNotBuried)
	if err != nil {
		return err
	}

	// switch from bury to ready or dependent queue
	queue.buryQueue.remove(item)

	if queue.itemHasDeps(item) {
		queue.depQueue.push(item)
		item.switchBuryDependent()
		queue.changed(SubQueueBury, SubQueueDependent, []*Item{item})
		queue.mutex.Unlock()
	} else {
		queue.readyQueue.push(item)
		item.switchBuryReady()
		queue.changed(SubQueueBury, SubQueueReady, []*Item{item})
		queue.mutex.Unlock()
		queue.readyAdded(ctx, "kicked")
	}

	return nil
}

// Remove is a thread-safe way to remove an item from the queue.
func (queue *Queue) Remove(ctx context.Context, key string) error {
	item, err := queue.lockExistingItem(opRemove, key)
	if err != nil {
		return err
	}

	addedReadyItems := queue.removeItem(item, key)

	if len(addedReadyItems) > 0 {
		queue.changed(SubQueueDependent, SubQueueReady, addedReadyItems)
	}

	queue.mutex.Unlock()

	if len(addedReadyItems) > 0 {
		queue.readyAdded(ctx, "dependent")
	}

	return nil
}

// removeItem performs the core of Remove(): it resolves dependants, detaches
// the item from its parents, deletes it from the queue and its sub-queue, and
// cleans it up. It returns any dependent items that became ready as a result.
// You must hold the mutex lock before calling this.
func (queue *Queue) removeItem(item *Item, key string) []*Item {
	// transfer any dependants to the ready queue
	addedReadyItems := queue.promoteDependants(key)

	// if this item is dependent on other items, update those items that this is
	// no longer dependent upon them
	queue.detachFromParents(item, key)

	// remove from the queue
	delete(queue.items, key)

	// remove from the current sub-queue
	queue.removeFromSubQueue(item)

	item.removalCleanup()

	return addedReadyItems
}

// promoteDependants resolves the dependency on key for every item that depends
// on it, moving any now-unblocked dependent items straight onto the ready
// sub-queue and returning them. You must hold the mutex lock before calling
// this.
func (queue *Queue) promoteDependants(key string) []*Item {
	deps, exists := queue.dependants[key]
	if !exists {
		return nil
	}

	var addedReadyItems []*Item

	for _, dep := range deps {
		done := dep.resolveDependency(key)
		if done && dep.state == ItemStateDependent {
			queue.depQueue.remove(dep)

			// put it straight on the ready queue, regardless of delay value
			dep.switchDependentReady()
			queue.readyQueue.push(dep)
			addedReadyItems = append(addedReadyItems, dep)
		}
	}

	delete(queue.dependants, key)

	return addedReadyItems
}

// detachFromParents removes the given key from the dependants lookup of each of
// the item's parent dependencies. You must hold the mutex lock before calling
// this.
func (queue *Queue) detachFromParents(item *Item, key string) {
	for _, parent := range item.dependencies {
		if deps, exists := queue.dependants[parent]; exists {
			delete(deps, key)

			if len(deps) == 0 {
				delete(queue.dependants, parent)
			}
		}
	}
}

// removeFromSubQueue removes an item from whichever sub-queue its state says it
// is in, and fires the corresponding changed callback. You must hold the mutex
// lock before calling this.
func (queue *Queue) removeFromSubQueue(item *Item) {
	switch item.state {
	case ItemStateDelay:
		queue.delayQueue.remove(item)
		queue.changed(SubQueueDelay, SubQueueRemoved, []*Item{item})
	case ItemStateReady:
		queue.readyQueue.remove(item)
		queue.changed(SubQueueReady, SubQueueRemoved, []*Item{item})
	case ItemStateRun:
		queue.runQueue.remove(item)
		queue.changed(SubQueueRun, SubQueueRemoved, []*Item{item})
	case ItemStateBury:
		queue.buryQueue.remove(item)
		queue.changed(SubQueueBury, SubQueueRemoved, []*Item{item})
	case ItemStateDependent:
		queue.depQueue.remove(item)
		queue.changed(SubQueueDependent, SubQueueRemoved, []*Item{item})
	case ItemStateSuspended:
		queue.suspendedQueue.remove(item)
		queue.changed(SubQueueSuspended, SubQueueRemoved, []*Item{item})
	default:
		// an already-removed item is in no sub-queue, so there is nothing to
		// remove it from
	}
}

// HasDependents tells you if the item with the given key has any other items
// depending upon it. You'd want to check this before Remove()ing this item if
// you're removing it because it was undesired as opposed to complete, as
// Remove() always triggers dependent items to become ready.
func (queue *Queue) HasDependents(key string) (bool, error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if queue.closed {
		return false, Error{queue.Name, opRemove, key, ErrQueueClosed}
	}

	_, has := queue.dependants[key]

	return has, nil
}

func (queue *Queue) startDelayProcessing(ctx context.Context) {
	sendStarted := true

	for {
		queue.updateDelayTime()

		if sendStarted {
			queue.startedDelayProcessing <- true
		}

		select {
		case <-time.After(time.Until(queue.delayTime)):
			queue.moveReadyDelayedItems(ctx)

			sendStarted = false
		case <-queue.delayNotification:
			sendStarted = true

			continue
		case <-queue.delayClose:
			return
		}
	}
}

// updateDelayTime sets delayTime to when the next delayed item becomes ready,
// or an hour from now if there are none.
func (queue *Queue) updateDelayTime() {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	sleepTime := idleSleep
	if queue.delayQueue.len() > 0 {
		sleepTime = time.Until(queue.delayQueue.firstItem().ReadyAt())
	}

	queue.delayTime = time.Now().Add(sleepTime)
}

// moveReadyDelayedItems moves any now-ready items from the delay sub-queue to
// the ready sub-queue, and triggers the relevant callbacks.
func (queue *Queue) moveReadyDelayedItems(ctx context.Context) {
	queue.mutex.Lock()

	numDelayed := queue.delayQueue.len()

	var items []*Item

	for range numDelayed {
		item := queue.delayQueue.firstItem()

		if !item.isready() {
			break
		}

		// remove it from the delay sub-queue and add it to the ready sub-queue
		queue.delayQueue.remove(item)
		queue.readyQueue.push(item)
		item.switchDelayReady()
		items = append(items, item)
	}

	if len(items) > 0 {
		queue.changed(SubQueueDelay, SubQueueReady, items)
	}

	queue.mutex.Unlock()

	if len(items) > 0 {
		queue.readyAdded(ctx, "delayed")
	}
}

func (queue *Queue) delayNotificationTrigger(item *Item) {
	queue.mutex.RLock()

	if queue.delayTime.After(time.Now().Add(item.delay)) {
		queue.mutex.RUnlock()

		queue.delayNotification <- true

		<-queue.startedDelayProcessing
	} else {
		queue.mutex.RUnlock()
	}
}

func (queue *Queue) startTTRProcessing(ctx context.Context) {
	sendStarted := true

	for {
		queue.updateTTRTime()

		if sendStarted {
			queue.startedTTRProcessing <- true
		}

		select {
		case <-time.After(time.Until(queue.ttrTime)):
			queue.processTimedOutItems(ctx)

			sendStarted = false
		case <-queue.ttrNotification:
			sendStarted = true

			continue
		case <-queue.ttrClose:
			return
		}
	}
}

// updateTTRTime sets ttrTime to when the next running item's TTR expires, or an
// hour from now if there are none.
func (queue *Queue) updateTTRTime() {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	sleepTime := idleSleep
	if queue.runQueue.len() > 0 {
		sleepTime = time.Until(queue.runQueue.firstItem().ReleaseAt())
	}

	queue.ttrTime = time.Now().Add(sleepTime)
}

// ttrMoves holds the items that moved out of the run sub-queue when their TTR
// expired, grouped by the sub-queue they moved to.
type ttrMoves struct {
	delayed []*Item
	buried  []*Item
	ready   []*Item
}

// processTimedOutItems moves any run sub-queue items whose TTR has expired to
// the sub-queue chosen by the TTR callback, then triggers the relevant
// callbacks.
func (queue *Queue) processTimedOutItems(ctx context.Context) {
	queue.mutex.Lock()
	moves := queue.releaseTimedOutItems()
	queue.notifyTTRMoveChanges(moves)
	queue.mutex.Unlock()

	queue.notifyTTRMoves(ctx, moves)
}

// releaseTimedOutItems moves expired items out of the run sub-queue and returns
// where each moved to. You must hold the mutex lock before calling this.
func (queue *Queue) releaseTimedOutItems() ttrMoves {
	length := queue.runQueue.len()

	var moves ttrMoves

	for range length {
		item := queue.runQueue.firstItem()

		if !item.releasable() {
			break
		}

		// obey the ttr callback
		moveTo := queue.ttrCb(item.Data())
		if moveTo == SubQueueRun {
			// keep it in the run queue, but give it a fresh TTR rather than
			// disabling the TTR indefinitely: a still-running item whose handling
			// merely lagged is re-checked after another full TTR, so a runner that
			// then goes silent is still detected within a bounded time instead of
			// being parked forever.
			item.touch()
			queue.runQueue.update(item)

			continue
		}

		// remove it from the ttr sub-queue and move to another
		queue.runQueue.remove(item)
		queue.moveTimedOutItem(item, moveTo, &moves)
	}

	return moves
}

// moveTimedOutItem pushes a single timed-out item onto the sub-queue indicated
// by moveTo and records it in moves. You must hold the mutex lock before
// calling this.
func (queue *Queue) moveTimedOutItem(item *Item, moveTo SubQueue, moves *ttrMoves) {
	switch moveTo {
	case SubQueueDelay:
		item.restart()
		queue.delayQueue.push(item)
		item.switchRunDelay(true)
		moves.delayed = append(moves.delayed, item)
	case SubQueueBury:
		queue.buryQueue.push(item)
		item.switchRunBury(true)
		moves.buried = append(moves.buried, item)
	default:
		queue.readyQueue.push(item)
		item.switchRunReady()
		moves.ready = append(moves.ready, item)
	}
}

// notifyTTRMoves triggers queue processing and ready-added callbacks after the
// TTR transitions and changed notifications have been recorded.
func (queue *Queue) notifyTTRMoves(ctx context.Context, moves ttrMoves) {
	for _, item := range moves.delayed {
		queue.delayNotificationTrigger(item)
	}

	if len(moves.ready) > 0 {
		queue.readyAdded(ctx, "ttr")
	}
}

func (queue *Queue) ttrNotificationTrigger(item *Item) {
	queue.mutex.RLock()

	if queue.ttrTime.After(time.Now().Add(item.ttr)) {
		queue.mutex.RUnlock()

		queue.ttrNotification <- true

		<-queue.startedTTRProcessing
	} else {
		queue.mutex.RUnlock()
	}
}

// Unwrap returns the underlying queue sentinel error.
func (e Error) Unwrap() error {
	return e.Err
}
