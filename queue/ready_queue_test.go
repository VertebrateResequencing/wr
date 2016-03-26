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

import (
	"testing"
    "fmt"
    "math/rand"
    "time"
)

func BenchmarkReadyQueue(b *testing.B) {
    readyQueue := newReadyQueue()
    b.ResetTimer()
    k := 1
    for i := 0; i < b.N; i++ {
        k++
        p := uint8(rand.Intn(255))
        item := newItem(fmt.Sprintf("%d.%d", k, p), "data", p, 0*time.Second, 0*time.Second)
        readyQueue.push(item)
    }
}

func TestReadyQueue(t *testing.T) {
    fmt.Println("test")
}