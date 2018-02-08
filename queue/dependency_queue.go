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

// dependency_queue is just a simple slice, implementing an efficient way of
// removing items. The actual dependency handling code is in *Queue.Add*() and
// *Queue.Remove().

// *** virtually identical to bury_queue.go; would be nice to avoid the code
// duplication...

import (
	"sync"
)

type depQueue struct {
	mutex sync.RWMutex
	items []*Item
}

func newDependencyQueue() *depQueue {
	return &depQueue{}
}

func (q *depQueue) push(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	item.queueIndexes[4] = len(q.items)
	q.items = append(q.items, item)
}

func (q *depQueue) pop() *Item {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	lasti := len(q.items) - 1
	if lasti == -1 {
		return nil
	}
	item := q.items[lasti]
	item.queueIndexes[4] = -1
	q.items = q.items[:lasti]
	return item
}

func (q *depQueue) remove(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	lasti := len(q.items) - 1
	thisi := item.queueIndexes[4]

	if lasti == 0 {
		// this item was the only one in the queue, just make a new slice
		q.items = []*Item{}
	} else {
		q.items[thisi] = q.items[lasti]        // copy the item at the end to where this item was
		q.items[thisi].queueIndexes[4] = thisi // update the index of the item we just moved
		q.items[lasti] = nil                   // set the value at the end to nil so it can be garbage collected
		q.items = q.items[:lasti]              // reduce the length of the slice
	}

	item.queueIndexes[4] = -1
}

func (q *depQueue) len() int {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	return len(q.items)
}

func (q *depQueue) empty() {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.items = nil
}
