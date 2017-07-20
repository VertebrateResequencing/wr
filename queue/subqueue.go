// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
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

// subqueue implements a heap structure for items in the various sub-queues, so
// that they can efficiently get the next item in desired order.

import (
	"container/heap"
	"sync"
)

type subQueue struct {
	mutex        sync.RWMutex
	items        []*Item
	groupedItems map[string][]*Item
	sqIndex      int
	reserveGroup string
}

// create a new subQueue that can hold *Items in "priority" order. sqIndex is
// one of 0 (priority is based on the item's delay), 1 (priority is based on the
// item's priority or creation) or 2 (priority is based on the item's ttr).
func newSubQueue(sqIndex int) *subQueue {
	queue := &subQueue{sqIndex: sqIndex}
	if sqIndex == 1 {
		queue.groupedItems = make(map[string][]*Item)
	}
	heap.Init(queue)
	return queue
}

// push adds an item to the queue
func (q *subQueue) push(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	if q.sqIndex == 1 {
		q.reserveGroup = item.ReserveGroup
	}
	heap.Push(q, item)
}

// pop removes the next item from the queue according to its "priority"
func (q *subQueue) pop(reserveGroup ...string) *Item {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	var itemList []*Item
	if q.sqIndex == 1 {
		var group string
		if len(reserveGroup) == 1 {
			group = reserveGroup[0]
		}
		var existed bool
		if itemList, existed = q.groupedItems[group]; !existed {
			return nil
		}
		q.reserveGroup = group
	} else {
		itemList = q.items
	}
	if len(itemList) == 0 {
		return nil
	}
	return heap.Pop(q).(*Item)
}

// remove removes a given item from the queue
func (q *subQueue) remove(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	if q.sqIndex == 1 {
		q.reserveGroup = item.ReserveGroup
	}
	heap.Remove(q, item.queueIndexes[q.sqIndex])
}

// len tells you how many items are in the queue
func (q *subQueue) len(reserveGroup ...string) int {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	var itemList []*Item
	if q.sqIndex == 1 {
		if len(reserveGroup) == 1 {
			group := reserveGroup[0]
			var existed bool
			if itemList, existed = q.groupedItems[group]; !existed {
				return 0
			}
		} else {
			num := 0
			for _, il := range q.groupedItems {
				num += len(il)
			}
			return num
		}
	} else {
		itemList = q.items
	}
	return len(itemList)
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
	if q.sqIndex == 1 && len(oldGroup) == 1 && oldGroup[0] != item.ReserveGroup {
		q.reserveGroup = oldGroup[0]
		heap.Remove(q, item.queueIndexes[q.sqIndex])
		q.reserveGroup = item.ReserveGroup
		heap.Push(q, item)
		return
	}
	heap.Fix(q, item.queueIndexes[q.sqIndex])
}

// empty clears out a queue, setting it back to its new state
func (q *subQueue) empty() {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	if q.sqIndex == 1 {
		q.groupedItems = make(map[string][]*Item)
	} else {
		q.items = nil
	}
}

// the following functions are required for the heap implementation, and though
// they are exported they are not supposed to be used directly - use the above
// methods instead

func (q *subQueue) Len() int {
	var itemList []*Item
	if q.sqIndex == 1 {
		var existed bool
		if itemList, existed = q.groupedItems[q.reserveGroup]; !existed {
			return 0
		}
	} else {
		itemList = q.items
	}
	return len(itemList)
}

func (q *subQueue) Less(i, j int) bool {
	switch q.sqIndex {
	case 0:
		return q.items[i].readyAt.Before(q.items[j].readyAt)
	case 1:
		if itemList, existed := q.groupedItems[q.reserveGroup]; existed {
			if itemList[i].priority == itemList[j].priority {
				return itemList[i].creation.Before(itemList[j].creation)
			}
			return itemList[i].priority > itemList[j].priority
		}
		return false
	}
	// case 2, outside the switch, because we need to return
	return q.items[i].releaseAt.Before(q.items[j].releaseAt)
}

func (q *subQueue) Swap(i, j int) {
	var itemList []*Item
	if q.sqIndex == 1 {
		var existed bool
		if itemList, existed = q.groupedItems[q.reserveGroup]; !existed {
			return
		}
	} else {
		itemList = q.items
	}
	itemList[i], itemList[j] = itemList[j], itemList[i]
	itemList[i].queueIndexes[q.sqIndex] = i
	itemList[j].queueIndexes[q.sqIndex] = j
}

func (q *subQueue) Push(x interface{}) {
	item := x.(*Item)
	var itemList []*Item
	if q.sqIndex == 1 {
		var existed bool
		if itemList, existed = q.groupedItems[q.reserveGroup]; !existed {
			q.groupedItems[q.reserveGroup] = itemList
		}
	} else {
		itemList = q.items
	}
	item.queueIndexes[q.sqIndex] = len(itemList)
	itemList = append(itemList, item)
	if q.sqIndex == 1 {
		q.groupedItems[q.reserveGroup] = itemList
	} else {
		q.items = itemList
	}
}

func (q *subQueue) Pop() interface{} {
	var itemList []*Item
	if q.sqIndex == 1 {
		var existed bool
		if itemList, existed = q.groupedItems[q.reserveGroup]; !existed {
			return nil
		}
	} else {
		itemList = q.items
	}
	lasti := len(itemList) - 1
	item := itemList[lasti]
	item.queueIndexes[q.sqIndex] = -1
	itemList = itemList[:lasti]
	if q.sqIndex == 1 {
		q.groupedItems[q.reserveGroup] = itemList
	} else {
		q.items = itemList
	}
	return item
}
