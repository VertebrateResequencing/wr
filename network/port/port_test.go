/*******************************************************************************
 * Copyright (c) 2021 Genome Research Ltd.
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

package port

import (
	"net"
	"syscall"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

type mockListener struct {
	*net.TCPListener
}

func (m *mockListener) Close() error {
	return syscall.EINVAL
}

func TestPort(t *testing.T) {
	Convey("Given a Checker", t, func() {
		checker, err := NewChecker("localhost")
		So(err, ShouldBeNil)
		So(checker, ShouldNotBeNil)

		Convey("You can get an available port number", func() {
			port, err := checker.availablePort()
			So(err, ShouldBeNil)
			So(port, ShouldBeBetweenOrEqual, 1, maxPort)
			So(len(checker.ports), ShouldEqual, 1)
			So(checker.ports[port], ShouldBeTrue)

			Convey("afterwards, release works, and failures are handled", func() {
				err = checker.release(err)
				So(err, ShouldBeNil)

				_, err = checker.availablePort()
				So(err, ShouldBeNil)
				checker.listeners[0] = &mockListener{checker.listeners[0].(*net.TCPListener)} //nolint:forcetypeassert
				err = checker.release(err)
				So(err, ShouldNotBeNil)
			})
		})

		Convey("portsAfter works", func() {
			portsBeforeAfterTest(checker,
				func() []int { return checker.portsAfter(10) },
				[]int{9, 12, 13, 15}, 11, []int{11, 12, 13})
		})

		Convey("portsBefore works", func() {
			portsBeforeAfterTest(checker,
				func() []int { return checker.portsBefore(10) },
				[]int{11, 8, 7, 5}, 9, []int{7, 8, 9})
		})

		Convey("checkRange returns nothing with no available ports", func() {
			set, has := checker.checkRange(10, 4)
			So(has, ShouldBeFalse)
			So(len(set), ShouldEqual, 0)

			Convey("but returns ports above starting point", func() {
				rangeTest(checker, []int{9, 11, 12, 13, 14}, []int{10, 11, 12, 13})
			})

			Convey("but returns ports below starting point", func() {
				rangeTest(checker, []int{11, 9, 8, 7, 6}, []int{7, 8, 9, 10})
			})

			Convey("but returns ports below and above starting point", func() {
				rangeTest(checker, []int{8, 9, 11, 12}, []int{8, 9, 10, 11})
			})

			Convey("and returns nothing with non-contiguous available ports", func() {
				setPortsTrue(checker, 7, 8, 12, 13)

				set, has := checker.checkRange(10, 4)
				So(has, ShouldBeFalse)
				So(len(set), ShouldEqual, 0)
			})
		})

		Convey("You can get a range of available ports multiple times in a row", func() {
			if ok := checkAvailableRange(checker, 2); !ok {
				return
			}

			if ok := checkAvailableRange(checker, 4); !ok {
				return
			}

			checkAvailableRange(checker, 4)
		})

		Convey("AvailableRange fails when tcp listening fails", func() {
			addr, err := net.ResolveTCPAddr("tcp", "localhost:1")
			So(err, ShouldBeNil)
			checker.Addr = addr
			_, _, err = checker.AvailableRange(2)
			So(err, ShouldNotBeNil)
		})
	})

	Convey("You can't make a Checker with a bad host name", t, func() {
		checker, err := NewChecker("wr_port_test_foo")
		So(err, ShouldNotBeNil)
		So(checker, ShouldBeNil)
	})
}

func setPortsTrue(checker *Checker, ports ...int) {
	for _, port := range ports {
		checker.ports[port] = true
	}
}

func portsBeforeAfterTest(checker *Checker, cb func() []int, truePorts []int, changePort int, expected []int) {
	result := cb()
	So(len(result), ShouldEqual, 0)

	setPortsTrue(checker, truePorts...)

	result = cb()
	So(len(result), ShouldEqual, 0)

	setPortsTrue(checker, changePort)

	result = cb()
	So(len(result), ShouldEqual, 3)
	So(result, ShouldResemble, expected)
}

func rangeTest(checker *Checker, truePorts []int, expected []int) {
	setPortsTrue(checker, truePorts...)
	set, has := checker.checkRange(10, 4)
	So(has, ShouldBeTrue)
	So(len(set), ShouldEqual, 4)
	So(set, ShouldResemble, expected)
}

func checkAvailableRange(checker *Checker, size int) bool {
	min, max, err := checker.AvailableRange(size)
	if err != nil {
		So(err.Error(), ShouldContainSubstring, "too many open files")
		SkipConvey("your ulimit -n is too low for AvailableRange to function", func() {})

		return false
	}

	So(min, ShouldBeBetweenOrEqual, 1, maxPort)
	So(max, ShouldEqual, min+size-1)

	return true
}
