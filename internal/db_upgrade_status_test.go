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
