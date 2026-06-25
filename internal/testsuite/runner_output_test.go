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

package testsuite

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestFormatLaneLog(t *testing.T) {
	Convey("GoConvey JSON output is replaced with failed assertion context", t, func() {
		raw := `before
>->->OPEN-JSON->->->
{
  "Title": "Once a server is up",
  "File": "/tmp/test.go",
  "Line": 10,
  "Depth": 1,
  "Assertions": [],
  "Output": ""
},
{
  "Title": "You can add a job",
  "File": "/tmp/test.go",
  "Line": 20,
  "Depth": 2,
  "Assertions": [
    {
      "File": "/tmp/test.go",
      "Line": 25,
      "Expected": "nil",
      "Actual": "'boom'",
      "Failure": "Expected: nil\nActual:   'boom'",
      "Error": null,
      "StackTrace": "",
      "Skipped": false
    }
  ],
  "Output": ""
},
<-<-<-CLOSE-JSON<-<-<
--- FAIL: TestThing (0.01s)
FAIL
`

		formatted := formatLaneLog(raw)

		So(formatted, ShouldContainSubstring, "Context:\n  Once a server is up\n    You can add a job\n")
		So(formatted, ShouldContainSubstring, "Failures:\n\n")
		So(formatted, ShouldContainSubstring, "  * /tmp/test.go \n  Line 25:\n  Expected: nil\n  Actual:   'boom'\n")
		So(formatted, ShouldContainSubstring, "--- FAIL: TestThing")
		So(formatted, ShouldNotContainSubstring, ">->->OPEN-JSON")
	})

	Convey("ordinary logs are left untouched", t, func() {
		raw := "plain failure\nwithout convey json\n"

		So(formatLaneLog(raw), ShouldEqual, raw)
	})

	Convey("successful scopes do not add context noise", t, func() {
		raw := `>->->OPEN-JSON->->->
{
  "Title": "Only passing behaviour",
  "File": "/tmp/test.go",
  "Line": 10,
  "Depth": 1,
  "Assertions": [
    {
      "File": "",
      "Line": 0,
      "Expected": "",
      "Actual": "",
      "Failure": "",
      "Error": null,
      "StackTrace": "",
      "Skipped": false
    }
  ],
  "Output": ""
},
<-<-<-CLOSE-JSON<-<-<
PASS
`

		formatted := formatLaneLog(raw)

		So(strings.TrimSpace(formatted), ShouldEqual, "PASS")
	})
}
