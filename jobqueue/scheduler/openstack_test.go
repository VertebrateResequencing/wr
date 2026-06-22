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

package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	. "github.com/smartystreets/goconvey/convey"
)

func TestOpenstackSpawnReleasesReservedQuotaOnEarlySpawnError(t *testing.T) {
	Convey("OpenStack spawn releases reserved quota when spawn fails before using quota", t, func() {
		debugCounter = 0
		debugEffect = "failBeforeUsingQuota"

		defer func() {
			debugCounter = 0
			debugEffect = ""
		}()

		s := &opst{
			config: &ConfigOpenStack{
				ServerKeepTime: time.Minute,
			},
		}
		req := &Requirements{
			RAM:   1024,
			Time:  time.Minute,
			Cores: 2,
			Disk:  20,
			Other: map[string]string{},
		}
		flavor := &cloud.Flavor{
			ID:    "tiny",
			Name:  "tiny",
			Cores: 2,
			RAM:   1024,
			Disk:  10,
		}

		s.spawn(context.Background(), req, flavor, "missing-os", nil, "", false, "true")

		s.resourceMutex.RLock()
		defer s.resourceMutex.RUnlock()

		So(s.reservedInstances, ShouldEqual, 0)
		So(s.reservedCores, ShouldEqual, 0)
		So(s.reservedRAM, ShouldEqual, 0)
		So(s.reservedVolume, ShouldEqual, 0)
	})
}
