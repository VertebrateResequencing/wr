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

// bury_queue is just a simple slice, implementing an efficient way of
// removing items

import (
	"sync"
)

type buryQueue struct {
	mutex sync.RWMutex
	items []*Item
}

func newBuryQueue() *buryQueue {
	return &buryQueue{}
}

func (q *buryQueue) push(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	item.queueIndexes[3] = len(q.items)
	q.items = append(q.items, item)
}

func (q *buryQueue) pop() *Item {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	lasti := len(q.items) - 1
	if lasti == -1 {
		return nil
	}
	item := q.items[lasti]
	item.queueIndexes[3] = -1
	q.items = q.items[:lasti]
	return item
}

func (q *buryQueue) remove(item *Item) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	lasti := len(q.items) - 1
	thisi := item.queueIndexes[3]

	if lasti == 0 {
		// this item was the only one in the queue, just make a new slice
		q.items = []*Item{}
	} else {
		q.items[thisi] = q.items[lasti]        // copy the item at the end to where this item was
		q.items[thisi].queueIndexes[3] = thisi // update the index of the item we just moved
		q.items[lasti] = nil                   // set the value at the end to nil so it can be garbage collected
		q.items = q.items[:lasti]              // reduce the length of the slice
	}

	item.queueIndexes[3] = -1
}

func (q *buryQueue) len() int {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	return len(q.items)
}

func (q *buryQueue) empty() {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.items = nil
}
