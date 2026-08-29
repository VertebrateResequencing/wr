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

package cmd

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// nothingOnExit is what a job stores for its on_exit trigger once that trigger
// has been turned off; it is what a REST PATCH of {"on_exit":[]} stores.
const nothingOnExit = `{"on_exit":[{"nothing":true}]}`

func TestModBehaviourFlags(t *testing.T) {
	Convey("Given wr mod's behaviour flags", t, func() {
		defer resetModBehaviourFlags()

		Convey("an explicitly empty --on_exit array turns that trigger off", func() {
			setModFlag("on_exit", "[]")

			behaviours, set := modBehaviours(modCmd)

			So(behaviours.String(), ShouldEqual, nothingOnExit)
			So(set, ShouldBeTrue)
		})

		Convey("an explicitly empty --on_exit string turns that trigger off the same way", func() {
			setModFlag("on_exit", "")

			behaviours, set := modBehaviours(modCmd)

			So(behaviours.String(), ShouldEqual, nothingOnExit)
			So(set, ShouldBeTrue)
		})

		Convey("turning one trigger off does not mention the triggers that were not supplied", func() {
			setModFlag("on_exit", "[]")

			behaviours, set := modBehaviours(modCmd)

			// on_failure and on_success are absent, so a job's existing
			// behaviours for them survive the modification
			So(behaviours.String(), ShouldEqual, nothingOnExit)
			So(len(behaviours), ShouldEqual, 1)
			So(set, ShouldBeTrue)
		})

		Convey("not supplying a behaviour flag at all leaves every trigger untouched", func() {
			behaviours, set := modBehaviours(modCmd)

			So(behaviours.String(), ShouldEqual, "")
			So(len(behaviours), ShouldEqual, 0)
			So(set, ShouldBeFalse)
		})

		Convey("supplied behaviours are all kept, in the supplied order", func() {
			setModFlag("on_exit", `[{"run":"echo x"},{"cleanup":true}]`)
			setModFlag("on_failure", `[{"run":"echo failed"}]`)

			behaviours, set := modBehaviours(modCmd)

			So(behaviours.String(), ShouldEqual,
				`{"on_failure":[{"run":"echo failed"}],"on_exit":[{"run":"echo x"},{"cleanup":true}]}`)
			So(set, ShouldBeTrue)
		})
	})
}

// resetModBehaviourFlags returns the mod sub-command's behaviour flags to their
// unsupplied state, so each Convey starts from a command line that did not
// mention them.
func resetModBehaviourFlags() {
	for _, name := range []string{"on_failure", "on_success", "on_exit"} {
		flag := modCmd.Flags().Lookup(name)
		So(flag, ShouldNotBeNil)
		So(flag.Value.Set(flag.DefValue), ShouldBeNil)

		flag.Changed = false
	}
}

// setModFlag supplies one of the mod sub-command's flags the way parsing a
// command line that mentioned it would.
func setModFlag(name, value string) {
	So(modCmd.Flags().Set(name, value), ShouldBeNil)
}
