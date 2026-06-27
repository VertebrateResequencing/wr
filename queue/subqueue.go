/*******************************************************************************
 * Copyright (c) 2016-2017, 2019-2021, 2024, 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
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

package queue

// subqueue implements a heap structure for items in the various sub-queues, so
// that they can efficiently get the next item in desired order.

import (
	"container/heap"
	"sync"
	"time"

	logext "github.com/inconshreveable/log15/v3/ext"
)

// notificationIDLength is the length of the random id used to track each
// pending push notification registration.
const notificationIDLength = 8

// The sqIndex values identify which characteristic a subQueue orders its items
// by. The ready sub-queue (readyQueueIndex) groups its items by ReserveGroup
// rather than using a single flat slice.
const (
	delayQueueIndex = 0
	readyQueueIndex = 1
	runQueueIndex   = 2
)

type subQueue struct {
	mutex                    sync.RWMutex
	items                    []*Item
	groupedItems             map[string][]*Item
	sqIndex                  int
	reserveGroup             string
	pushNotificationChannels map[string]map[string]*pushNotification
}

// pushNotification holds the channel that a notifyPush() caller is waiting on,
// along with the timer responsible for the timeout. Keeping the timer lets us
// stop it the moment the notification is triggered, preventing the timeout
// callback from starting if the timer has not already fired instead of leaving
// it pending until the full timeout elapses.
type pushNotification struct {
	ch    chan bool
	timer *time.Timer
}

// create a new subQueue that can hold *Items in "priority" order. sqIndex is
// one of 0 (priority is based on the item's delay), 1 (priority is based on the
// item's priority or creation) or 2 (priority is based on the item's ttr).
func newSubQueue(sqIndex int) *subQueue {
	queue := &subQueue{
		sqIndex:                  sqIndex,
		pushNotificationChannels: make(map[string]map[string]*pushNotification),
	}
	if sqIndex == readyQueueIndex {
		queue.groupedItems = make(map[string][]*Item)
	}

	heap.Init(queue)

	return queue
}

// notifyPush lets you supply a channel that will then receive true whenever the
// next item with the given ReserveGroup (of if reserveGroup is blank, no
// ReserveGroup) is push()ed to this subQueue. It will also receive true if an
// item that has already been pushed this subQueue has its ReserveGroup updated
// to the given reserverGroup.
//
// After 1 matching item has been pushed or updated, the supplied ch will not be
// used again.
//
// If timeout duration passes before a matching item is pushed, the ch will
// receive false and not be used again.
func (q *subQueue) notifyPush(reserveGroup string, ch chan bool, timeout time.Duration) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	chans, ok := q.pushNotificationChannels[reserveGroup]
	if !ok {
		chans = make(map[string]*pushNotification)
	}

	id := logext.RandId(notificationIDLength)

	timer := time.AfterFunc(timeout, func() {
		q.notifyPushTimeout(reserveGroup, id)
	})

	chans[id] = &pushNotification{ch: ch, timer: timer}
	q.pushNotificationChannels[reserveGroup] = chans
}

// notifyPushTimeout is the timeout callback registered by notifyPush: it sends
// false on the waiting channel and cleans up the registration for the given
// reserveGroup and id, if it still exists.
func (q *subQueue) notifyPushTimeout(reserveGroup, id string) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	chans, ok := q.pushNotificationChannels[reserveGroup]
	if !ok {
		return
	}

	pn, ok := chans[id]
	if !ok {
		return
	}

	pn.ch <- false

	delete(chans, id)

	if len(q.pushNotificationChannels[reserveGroup]) == 0 {
		delete(q.pushNotificationChannels, reserveGroup)
	}
}

// triggerNotify is used to check if we should notify about the given
// reserveGroup and send true on the registered channels if so. You
// must hold the mutex lock before calling this.
func (q *subQueue) triggerNotify(reserveGroup string) {
	if chans, ok := q.pushNotificationChannels[reserveGroup]; ok {
		for id, pn := range chans {
			pn.timer.Stop()

			pn.ch <- true

			delete(chans, id)

			break
		}

		if len(chans) == 0 {
			delete(q.pushNotificationChannels, reserveGroup)
		}
	}
}

// push adds an item to the queue.
func (q *subQueue) push(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if q.sqIndex == readyQueueIndex {
		q.reserveGroup = item.ReserveGroup
	}
	defer q.triggerNotify(q.reserveGroup)

	heap.Push(q, item)
}

// pop removes the next item from the queue according to its "priority".
func (q *subQueue) pop(reserveGroup ...string) *Item {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	itemList, ok := q.popItemList(reserveGroup...)
	if !ok || len(itemList) == 0 {
		return nil
	}

	item, ok := heap.Pop(q).(*Item)
	if !ok {
		return nil
	}

	return item
}

// popItemList resolves the slice that pop should operate on, also setting
// q.reserveGroup for the ready sub-queue. It returns false if the requested
// ready group does not exist.
func (q *subQueue) popItemList(reserveGroup ...string) ([]*Item, bool) {
	if q.sqIndex != readyQueueIndex {
		return q.items, true
	}

	group := firstOrEmpty(reserveGroup)

	itemList, existed := q.groupedItems[group]
	if !existed {
		return nil, false
	}

	q.reserveGroup = group

	return itemList, true
}

// remove removes a given item from the queue.
func (q *subQueue) remove(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if q.sqIndex == readyQueueIndex {
		q.reserveGroup = item.ReserveGroup
	}

	heap.Remove(q, item.queueIndexes[q.sqIndex])
}

// len tells you how many items are in the queue.
func (q *subQueue) len(reserveGroup ...string) int {
	q.mutex.RLock()
	defer q.mutex.RUnlock()

	if q.sqIndex == readyQueueIndex {
		return q.groupedLen(reserveGroup...)
	}

	return len(q.items)
}

// groupedLen returns the number of items in the ready sub-queue. If a single
// reserveGroup is supplied it counts only that group (0 if it does not exist);
// otherwise it sums the counts of all groups. You must hold the read lock.
func (q *subQueue) groupedLen(reserveGroup ...string) int {
	if len(reserveGroup) == 1 {
		return len(q.groupedItems[reserveGroup[0]])
	}

	num := 0
	for _, il := range q.groupedItems {
		num += len(il)
	}

	return num
}

// firstItem is useful in testing to get the first item in the queue in a
// thread-safe way.
func (q *subQueue) firstItem() *Item {
	q.mutex.RLock()
	defer q.mutex.RUnlock()

	return q.items[0]
}

// update ensures that if an item's "priority" characteristic(s) change, that
// its order in the queue is corrected. Optional oldGroup is the previous
// ReserveGroup that this item had, supplied if the group changed.
func (q *subQueue) update(item *Item, oldGroup ...string) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if q.sqIndex == readyQueueIndex && len(oldGroup) == 1 && oldGroup[0] != item.ReserveGroup {
		q.reserveGroup = oldGroup[0]
		heap.Remove(q, item.queueIndexes[q.sqIndex])

		q.reserveGroup = item.ReserveGroup
		defer q.triggerNotify(q.reserveGroup)

		heap.Push(q, item)

		return
	}

	heap.Fix(q, item.queueIndexes[q.sqIndex])
}

// empty clears out a queue, setting it back to its new state.
func (q *subQueue) empty() {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if q.sqIndex == readyQueueIndex {
		q.groupedItems = make(map[string][]*Item)
	} else {
		q.items = nil
	}
}

// firstOrEmpty returns the first element of the given variadic strings, or the
// empty string if none were supplied.
func firstOrEmpty(strs []string) string {
	if len(strs) == 1 {
		return strs[0]
	}

	return ""
}

// currentItemList returns the slice of items that the heap interface methods
// should operate on. For the ready sub-queue this is the slice for the current
// reserveGroup, with false if no such group exists; for the other sub-queues it
// is the flat items slice, and true is always returned.
func (q *subQueue) currentItemList() ([]*Item, bool) {
	if q.sqIndex == readyQueueIndex {
		itemList, existed := q.groupedItems[q.reserveGroup]

		return itemList, existed
	}

	return q.items, true
}

// setItemList stores the given slice back as the items the heap interface
// methods operate on, in the same place currentItemList reads them from.
func (q *subQueue) setItemList(itemList []*Item) {
	if q.sqIndex == readyQueueIndex {
		q.groupedItems[q.reserveGroup] = itemList
	} else {
		q.items = itemList
	}
}

// the following functions are required for the heap implementation, and though
// they are exported they are not supposed to be used directly - use the above
// methods instead

func (q *subQueue) Len() int {
	itemList, _ := q.currentItemList()

	return len(itemList)
}

func (q *subQueue) Less(i, j int) bool {
	switch q.sqIndex {
	case delayQueueIndex:
		return lessByReadyAt(q.items[i], q.items[j])
	case readyQueueIndex:
		itemList, existed := q.groupedItems[q.reserveGroup]
		if !existed {
			return false
		}

		return lessByPriority(itemList[i], itemList[j])
	}
	// run sub-queue, outside the switch because we need to return
	return lessByReleaseAt(q.items[i], q.items[j])
}

// lessByReadyAt orders delay sub-queue items by their readyAt time, falling
// back to insertion order (iid) when they are equal.
func lessByReadyAt(a, b *Item) bool {
	if a.readyAt.Equal(b.readyAt) {
		return a.iid < b.iid
	}

	return a.readyAt.Before(b.readyAt)
}

// lessByReleaseAt orders run sub-queue items by their releaseAt time, falling
// back to insertion order (iid) when they are equal.
func lessByReleaseAt(a, b *Item) bool {
	if a.releaseAt.Equal(b.releaseAt) {
		return a.iid < b.iid
	}

	return a.releaseAt.Before(b.releaseAt)
}

// lessByPriority orders ready sub-queue items by priority (highest first), then
// size (highest first), then creation time, then insertion order (iid).
func lessByPriority(a, b *Item) bool {
	if a.priority != b.priority {
		return a.priority > b.priority
	}

	if a.size != b.size {
		return a.size > b.size
	}

	if a.creation.Equal(b.creation) {
		return a.iid < b.iid
	}

	return a.creation.Before(b.creation)
}

func (q *subQueue) Swap(i, j int) {
	itemList, existed := q.currentItemList()
	if !existed {
		return
	}

	itemList[i], itemList[j] = itemList[j], itemList[i]
	lockFirst := i

	lockSecond := j
	if itemList[i].iid > itemList[j].iid {
		lockFirst = j
		lockSecond = i
	}

	itemList[lockFirst].mutex.Lock()
	defer itemList[lockFirst].mutex.Unlock()

	if i != j {
		itemList[lockSecond].mutex.Lock()
		defer itemList[lockSecond].mutex.Unlock()
	}

	itemList[i].queueIndexes[q.sqIndex] = i
	itemList[j].queueIndexes[q.sqIndex] = j
}

func (q *subQueue) Push(x any) {
	item, ok := x.(*Item)
	if !ok {
		return
	}

	itemList, _ := q.currentItemList()

	item.mutex.Lock()
	item.queueIndexes[q.sqIndex] = len(itemList)
	item.mutex.Unlock()

	itemList = append(itemList, item)
	q.setItemList(itemList)
}

func (q *subQueue) Pop() any {
	itemList, existed := q.currentItemList()
	if !existed {
		return nil
	}

	lasti := len(itemList) - 1
	item := itemList[lasti]
	item.mutex.Lock()
	item.queueIndexes[q.sqIndex] = -1
	item.mutex.Unlock()

	q.setItemList(itemList[:lasti])

	return item
}
