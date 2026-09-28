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

package testcerts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

func TestWrite(t *testing.T) {
	Convey("Write gives valid TLS files, the same set each time, and never overwrites", t, func() {
		paths := func(dir string) (string, string, string) {
			return filepath.Join(dir, "ca.pem"), filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
		}

		ca1, cert1, key1 := paths(t.TempDir())
		So(Write(ca1, cert1, key1, "localhost"), ShouldBeNil)
		So(internal.CheckCerts(cert1, key1), ShouldBeNil)

		info, err := os.Stat(key1)
		So(err, ShouldBeNil)
		So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o600))

		ca2, cert2, key2 := paths(t.TempDir())
		So(Write(ca2, cert2, key2, "localhost"), ShouldBeNil)

		for _, pair := range [][2]string{{ca1, ca2}, {cert1, cert2}, {key1, key2}} {
			first, errr := os.ReadFile(pair[0])
			So(errr, ShouldBeNil)
			second, errr := os.ReadFile(pair[1])
			So(errr, ShouldBeNil)
			So(second, ShouldResemble, first)
		}

		So(Write(ca1, filepath.Join(t.TempDir(), "c"), filepath.Join(t.TempDir(), "k"), "localhost"),
			ShouldNotBeNil)
	})
}
