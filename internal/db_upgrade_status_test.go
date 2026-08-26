/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to permit
 * persons to whom the Software is furnished to do so, subject to the following
 * conditions:
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

package internal

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestDBUpgradeStatusFileReplacement(t *testing.T) {
	Convey("DB upgrade status replacement retries after destination-exists rename failures", t, func() {
		dir := t.TempDir()
		tmpName := filepath.Join(dir, "status.tmp")
		path := filepath.Join(dir, "status")

		So(os.WriteFile(path, []byte("old"), 0o600), ShouldBeNil)

		renameCalls := 0
		rename := func(oldName, newName string) error {
			renameCalls++

			So(oldName, ShouldEqual, tmpName)
			So(newName, ShouldEqual, path)

			if renameCalls == 1 {
				return &os.LinkError{Op: "rename", Old: oldName, New: newName, Err: os.ErrExist}
			}

			return nil
		}

		err := replaceDBUpgradeStatusFileWith(tmpName, path, rename, os.Remove, os.Stat)
		So(err, ShouldBeNil)
		So(renameCalls, ShouldEqual, 2)

		_, err = os.Stat(path)
		So(os.IsNotExist(err), ShouldBeTrue)
	})
}

// TestDBUpgradeStatusTotalRoundTrip covers E4 acceptance test 1: Total is
// additive, so a file written without it is byte-identical to one written before
// the field existed and an older reader ignores it.
//
// The equality is scoped to the four payload fields because
// WriteDBUpgradeStatus overwrites PID and UpdatedAt and fills a zero StartedAt,
// so no whole-struct comparison can hold.
func TestDBUpgradeStatusTotalRoundTrip(t *testing.T) {
	Convey("A status with no total writes no total key", t, func() {
		dbFile := filepath.Join(t.TempDir(), "db")
		original := DBUpgradeStatus{State: DBStartupRecoveryState, Detail: "no total here"}

		So(WriteDBUpgradeStatus(dbFile, original), ShouldBeNil)
		So(readDBUpgradeStatusJSON(t, dbFile), ShouldNotContainSubstring, `"total"`)

		status, _, err := ReadDBUpgradeStatus(dbFile)
		So(err, ShouldBeNil)
		So(status.State, ShouldEqual, original.State)
		So(status.Detail, ShouldEqual, original.Detail)
		So(status.Processed, ShouldEqual, 0)
		So(status.Total, ShouldEqual, 0)
	})

	Convey("A status with a total writes and reads it back", t, func() {
		dbFile := filepath.Join(t.TempDir(), "db")
		original := DBUpgradeStatus{
			State: DBStartupRecoveryState, Detail: "1m2s elapsed", Processed: 9000, Total: 150472,
		}

		So(WriteDBUpgradeStatus(dbFile, original), ShouldBeNil)
		So(readDBUpgradeStatusJSON(t, dbFile), ShouldContainSubstring, `"total": 150472`)

		status, _, err := ReadDBUpgradeStatus(dbFile)
		So(err, ShouldBeNil)
		So(status.State, ShouldEqual, original.State)
		So(status.Detail, ShouldEqual, original.Detail)
		So(status.Processed, ShouldEqual, original.Processed)
		So(status.Total, ShouldEqual, original.Total)
	})
}

func readDBUpgradeStatusJSON(t *testing.T, dbFile string) string {
	t.Helper()

	payload, err := os.ReadFile(DBUpgradeStatusPath(dbFile))
	So(err, ShouldBeNil)

	return string(payload)
}
