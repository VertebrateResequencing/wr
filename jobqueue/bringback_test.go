/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
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

package jobqueue

import (
	"context"
	"strconv"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// TestBringBacksRetention proves bringBacks lets go of the memory of the keys
// an add held once every hold on them is released, since every add holds the
// key of every job it adds and map buckets outlive their deleted keys.
func TestBringBacksRetention(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given overlapping adds holding many keys, some with archive cleanups deferred to them", t, func() {
		s := &Server{}

		keys := make([]string, 10000)
		for i := range keys {
			keys[i] = strconv.Itoa(i)
		}

		s.holdBringBacks(keys)
		s.holdBringBacks(keys[:len(keys)/2])

		for _, key := range keys[:len(keys)/2] {
			s.cleanUpArchived(context.Background(), key, "rg")
		}

		So(heldShards(s), ShouldEqual, depGroupShards)

		Convey("once every hold is released, no shard keeps a map", func() {
			s.releaseBringBacks(context.Background(), keys[len(keys)/2:])
			s.releaseBringBacks(context.Background(), keys[:len(keys)/2])

			var deferred int

			for _, key := range keys[:len(keys)/2] {
				shard := s.bringBacks.shard(key)

				shard.mu.Lock()

				if _, ok := shard.releaseLocked(key); ok {
					deferred++
				}

				shard.mu.Unlock()
			}

			So(deferred, ShouldEqual, len(keys)/2)
			So(heldShards(s), ShouldEqual, 0)
		})
	})
}

// heldShards returns how many of s's bringBacks shards have a holds or deferred
// map.
func heldShards(s *Server) int {
	var held int

	for i := range s.bringBacks.shards {
		if s.bringBacks.shards[i].holds != nil || s.bringBacks.shards[i].deferred != nil {
			held++
		}
	}

	return held
}
