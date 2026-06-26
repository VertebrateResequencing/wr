/*******************************************************************************
 * Copyright (c) 2016, 2018, 2020, 2024 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
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

// dependency_queue is just a simple slice, implementing an efficient way of
// removing items. The actual dependency handling code is in *Queue.Add*() and
// *Queue.Remove().

// *** virtually identical to bury_queue.go; would be nice to avoid the code
// duplication...

import (
	"sync"
)

const (
	dependencyQueueIndex = 4
	suspendedQueueIndex  = 5
)

type depQueue struct {
	mutex sync.RWMutex
	items []*Item
	index int
}

func newDependencyQueue() *depQueue {
	return &depQueue{index: dependencyQueueIndex}
}

func newSuspendedQueue() *depQueue {
	return &depQueue{index: suspendedQueueIndex}
}

func (q *depQueue) push(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	item.queueIndexes[q.index] = len(q.items)
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
	item.queueIndexes[q.index] = -1
	q.items = q.items[:lasti]

	return item
}

func (q *depQueue) remove(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	lasti := len(q.items) - 1
	thisi := item.queueIndexes[q.index]

	if lasti == 0 {
		// this item was the only one in the queue, just make a new slice
		q.items = []*Item{}
	} else {
		q.items[thisi] = q.items[lasti]              // copy the item at the end to where this item was
		q.items[thisi].queueIndexes[q.index] = thisi // update the index of the item we just moved
		q.items[lasti] = nil                         // set the value at the end to nil so it can be garbage collected
		q.items = q.items[:lasti]                    // reduce the length of the slice
	}

	item.queueIndexes[q.index] = -1
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
