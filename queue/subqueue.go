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

package queue

// subqueue implements a heap structure for items in the various sub-queues, so
// that they can efficiently get the next item in desired order.

import (
	"container/heap"
	"sort"
	"sync"
)

type subQueue struct {
	mutex     sync.RWMutex
	items     []*Item
	sqIndex   int
	needsSort bool
}

// create a new subQueue that can hold *Items in "priority" order. sqIndex is
// one of 0 (priority is based on the item's delay), 1 (priority is based on the
// item's priority or creation) or 2 (priority is based on the item's ttr).
func newSubQueue(sqIndex int) *subQueue {
	queue := &subQueue{sqIndex: sqIndex}
	heap.Init(queue)
	return queue
}

// push adds an item to the queue
func (q *subQueue) push(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	heap.Push(q, item)
	q.needsSort = true
}

// pop removes the next item from the queue according to its "priority"
func (q *subQueue) pop() *Item {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	return heap.Pop(q).(*Item)
}

// remove removes a given item from the queue
func (q *subQueue) remove(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	heap.Remove(q, item.queueIndexes[q.sqIndex])
}

// len tells you how many items are in the queue
func (q *subQueue) len() int {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	return len(q.items)
}

// update ensures that if an item's "priority" characteristic(s) change, that
// its order in the queue is corrected
func (q *subQueue) update(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	heap.Fix(q, item.queueIndexes[q.sqIndex])
	q.needsSort = true
}

// empty clears out a queue, setting it back to its new state
func (q *subQueue) empty() {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.items = nil
}

// all correctly sorts all items in the queue and returns a new slice of them.
func (q *subQueue) all() (items []*Item) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	if len(q.items) == 0 {
		return
	}

	// q.items is not sorted, and we don't have a way of directly accessing the
	// heap (?), so we must trigger a sort manually. We create a new slice for
	// them to avoid issues with items being added to q.items while the user
	// is looking through q.items.
	if q.needsSort {
		sort.Sort(q)
		q.needsSort = false
	}
	for _, item := range q.items {
		items = append(items, item)
	}
	return
}

// the following functions are required for the heap implementation, and though
// they are exported they are not supposed to be used directly - use the above
// methods instead

func (q *subQueue) Len() int {
	return len(q.items)
}

func (q *subQueue) Less(i, j int) bool {
	switch q.sqIndex {
	case 0:
		return q.items[i].readyAt.Before(q.items[j].readyAt)
	case 1:
		if q.items[i].priority == q.items[j].priority {
			return q.items[i].creation.Before(q.items[j].creation)
		}
		return q.items[i].priority > q.items[j].priority
	}
	// case 2, outside the switch, because we need to return
	return q.items[i].releaseAt.Before(q.items[j].releaseAt)
}

func (q *subQueue) Swap(i, j int) {
	q.items[i], q.items[j] = q.items[j], q.items[i]
	q.items[i].queueIndexes[q.sqIndex] = i
	q.items[j].queueIndexes[q.sqIndex] = j
}

func (q *subQueue) Push(x interface{}) {
	item := x.(*Item)
	item.queueIndexes[q.sqIndex] = len(q.items)
	q.items = append(q.items, item)
}

func (q *subQueue) Pop() interface{} {
	lasti := len(q.items) - 1
	item := q.items[lasti]
	item.queueIndexes[q.sqIndex] = -1
	q.items = q.items[:lasti]
	return item
}
