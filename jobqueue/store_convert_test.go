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
	"syscall"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

// downgradeBoltOpenTimeout is the bbolt open timeout C7 test 4 uses, as a
// downgraded wr's initDB would bound its open.
const downgradeBoltOpenTimeout = time.Second

func TestStoreDowngradedWrRefusesStoreDirectory(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a closed log store directory", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		s, _ := seededLogStore(ctx, cfg)
		So(s.close(ctx), ShouldBeNil)

		before := storeDirIdentity(t, cfg.dbFile)

		Convey("opening it as bbolt, as a downgraded wr does, is refused as unreadable, changing nothing", func() {
			opened, err := bolt.Open(cfg.dbFile, dbFilePermission, &bolt.Options{Timeout: downgradeBoltOpenTimeout})
			if opened != nil {
				So(opened.Close(), ShouldBeNil)
			}

			So(opened, ShouldBeNil)
			So(err, ShouldWrap, syscall.EISDIR)
			So(openErrorThatIsNotCorruption(err, cfg.dbFile), ShouldWrap, ErrDBUnreadable)
			So(storeDirIdentity(t, cfg.dbFile), ShouldResemble, before)
		})
	})
}
