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

package queue

import (
	"context"
	"fmt"
	"testing"
	"time"
)

const benchQueueItems = 1000

// BenchmarkQueueLifecycle measures the queue's per-job hot path: each op adds
// benchQueueItems items, then reserves, touches and removes every one of them,
// as the manager does for add, Reserve, Touch and Archive. The groups variant
// spreads the items over many reserve groups, as a real manager's scheduler
// groups do. It only uses the exported API, so it can be copied into an older
// tree to compare versions.
func BenchmarkQueueLifecycle(b *testing.B) {
	for _, groups := range []int{1, 100} {
		b.Run(fmt.Sprintf("groups=%d", groups), func(b *testing.B) {
			benchQueueLifecycle(b, groups)
		})
	}
}

func benchQueueLifecycle(b *testing.B, groups int) {
	b.Helper()

	ctx := context.Background()
	keys := make([]string, benchQueueItems)
	rgs := make([]string, benchQueueItems)

	for i := range keys {
		keys[i] = fmt.Sprintf("key%d", i)
		rgs[i] = fmt.Sprintf("rg%d", i%groups)
	}

	b.ReportAllocs()

	for range b.N {
		q := New(ctx, "bench")

		for i, key := range keys {
			if _, err := q.Add(ctx, key, rgs[i], i, uint8(i%255), 0, time.Hour, SubQueueReady); err != nil {
				b.Fatal(err)
			}
		}

		for i := range keys {
			item, err := q.Reserve(rgs[i], 0)
			if err != nil {
				b.Fatal(err)
			}

			if err = q.Touch(item.Key); err != nil {
				b.Fatal(err)
			}

			if err = q.Remove(ctx, item.Key); err != nil {
				b.Fatal(err)
			}
		}

		if err := q.Destroy(); err != nil {
			b.Fatal(err)
		}
	}
}
