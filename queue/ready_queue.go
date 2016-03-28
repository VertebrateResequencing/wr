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

// ready_queue implements a heap structure for items in the ready
// sub-queue, so that we can efficiently pop items off in priority || fifo
// order

import (
	"container/heap"
	"sync"
)

type readyQueue struct {
	mutex sync.Mutex
	items []*Item
}

func newReadyQueue() *readyQueue {
	queue := &readyQueue{}
	heap.Init(queue)
	return queue
}

func (q *readyQueue) update(item *Item) {
	heap.Fix(q, item.readyIndex)
}

func (q *readyQueue) push(item *Item) {
	heap.Push(q, item)
}

func (q *readyQueue) pop() *Item {
	if q.Len() == 0 {
		return nil
	}
	return heap.Pop(q).(*Item)
}

func (q *readyQueue) remove(item *Item) {
	heap.Remove(q, item.readyIndex)
}

func (q readyQueue) Len() int {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	length := len(q.items)
	return length
}

func (q readyQueue) Less(i, j int) bool {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	if q.items[i].priority == q.items[j].priority {
		return q.items[i].creation.Before(q.items[j].creation)
	}
	return q.items[i].priority > q.items[j].priority
}

func (q readyQueue) Swap(i, j int) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.items[i], q.items[j] = q.items[j], q.items[i]
	q.items[i].readyIndex = i
	q.items[j].readyIndex = j
}

func (q *readyQueue) Push(x interface{}) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	item := x.(*Item)
	item.readyIndex = len(q.items)
	q.items = append(q.items, item)
}

func (q *readyQueue) Pop() interface{} {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	lasti := len(q.items) - 1
	item := q.items[lasti]
	item.readyIndex = -1
	q.items = q.items[:lasti]
	return item
}
