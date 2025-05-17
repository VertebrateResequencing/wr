// Copyright © 2025 Genome Research Limited
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

package plist

import (
	"github.com/lindell/go-ordered-set/orderedset"
)

// PList provides pagination for an insertion-ordered de-duplicated list.
type PList[T comparable] struct {
	set *orderedset.OrderedSet[T] // using orderedset is 10x faster than using a map+slice
}

// New creates a new empty PList.
func New[T comparable]() *PList[T] {
	return &PList[T]{
		set: orderedset.New[T](),
	}
}

// Add adds an element to the PList, ignoring duplicates.
func (p *PList[T]) Add(item T) {
	p.set.Add(item)
}

// Delete removes an element from the PList.
func (p *PList[T]) Delete(item T) {
	p.set.Delete(item)
}

// Len returns the number of elements in the PList.
func (p *PList[T]) Len() int {
	return p.set.Size()
}

// Values returns all the elements in the PList in insertion order.
func (p *PList[T]) Values() []T {
	return p.set.Values()
}

// Page returns a subset of values from the PList based on offset and pageSize,
// along with the count of remaining items after this page.
func (p *PList[T]) Page(offset, pageSize int) ([]T, int) {
	totalSize := p.Len()

	if !p.isValidPaging(offset, pageSize, totalSize) {
		return []T{}, 0
	}

	endIndex := p.calculateEndIndex(offset, pageSize, totalSize)
	page := make([]T, 0, endIndex-offset)

	it := p.set.Iter() // using the iterator is 10x faster than using Values()
	p.skipToOffset(it, offset)
	p.collectPage(it, &page, pageSize)

	return page, totalSize - offset - len(page)
}

// isValidPaging checks if the paging parameters are valid.
func (p *PList[T]) isValidPaging(offset, pageSize, totalSize int) bool {
	return offset >= 0 && offset < totalSize && pageSize > 0
}

// calculateEndIndex determines the end index for pagination.
func (p *PList[T]) calculateEndIndex(offset, pageSize, totalSize int) int {
	endIndex := offset + pageSize
	if endIndex > totalSize {
		endIndex = totalSize
	}

	return endIndex
}

// skipToOffset advances the iterator to the specified offset.
func (p *PList[T]) skipToOffset(it *orderedset.Iterator[T], offset int) {
	for i := 0; i < offset; i++ {
		if _, ok := it.Next(); !ok {
			break
		}
	}
}

// collectPage collects items from the iterator into the page slice.
func (p *PList[T]) collectPage(it *orderedset.Iterator[T], page *[]T, pageSize int) {
	for len(*page) < pageSize {
		val, ok := it.Next()
		if !ok {
			break
		}

		*page = append(*page, val)
	}
}
