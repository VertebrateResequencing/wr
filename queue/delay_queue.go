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

package queue

// delay_queue implements a heap structure for items in the delay sub-queue, so
// that we can efficiently and ~immediately react when the delay on an item
// runs out

import (
	"container/heap"
	"sync"
)

type delayQueue struct {
	mutex sync.Mutex
	items []*Item
}

func newDelayQueue() *delayQueue {
	queue := &delayQueue{}
	heap.Init(queue)
	return queue
}

func (q *delayQueue) update(item *Item) {
	heap.Fix(q, item.delayIndex)
}

func (q *delayQueue) push(item *Item) {
	heap.Push(q, item)
}

func (q *delayQueue) pop() *Item {
	if q.Len() == 0 {
		return nil
	}
	return heap.Pop(q).(*Item)
}

func (q *delayQueue) remove(item *Item) {
	heap.Remove(q, item.delayIndex)
}

func (q delayQueue) Len() int {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	return len(q.items)
}

func (q delayQueue) Less(i, j int) bool {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	return q.items[i].readyAt.Before(q.items[j].readyAt)
}

func (q delayQueue) Swap(i, j int) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.items[i], q.items[j] = q.items[j], q.items[i]
	q.items[i].delayIndex = i
	q.items[j].delayIndex = j
}

func (q *delayQueue) Push(x interface{}) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	item := x.(*Item)
	item.delayIndex = len(q.items)
	q.items = append(q.items, item)
}

func (q *delayQueue) Pop() interface{} {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	lasti := len(q.items) - 1
	item := q.items[lasti]
	item.delayIndex = -1
	q.items = q.items[:lasti]
	return item
}
