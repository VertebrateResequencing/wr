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

package port

import (
	"errors"
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

		Convey("The operating system offers a free port, and hands it straight back", func() {
			port, err := checker.offeredPort()
			So(err, ShouldBeNil)
			So(port, ShouldBeBetweenOrEqual, 1, maxPort)
			So(checker.listeners, ShouldBeEmpty)

			Convey("release reports a held port that will not close", func() {
				tcpListener, errl := net.ListenTCP("tcp", checker.Addr)
				So(errl, ShouldBeNil)

				checker.listeners = append(checker.listeners, &mockListener{tcpListener})

				err = checker.release(nil)
				So(err, ShouldNotBeNil)
				So(errors.Is(err, syscall.EINVAL), ShouldBeTrue)
				So(checker.listeners, ShouldBeEmpty)
				So(tcpListener.Close(), ShouldBeNil)
			})
		})

		Convey("You can get a range of available ports multiple times in a row", func() {
			checkAvailableRange(checker, 2)
			checkAvailableRange(checker, 4)
			checkAvailableRange(checker, 4)
		})

		Convey("You can get the smallest range there is", func() {
			checkAvailableRange(checker, 1)
		})

		Convey("AvailableRange turns down a size no range could have", func() {
			checkRejectedRangeSize(checker, 0)
			checkRejectedRangeSize(checker, -1)
			checkRejectedRangeSize(checker, searchablePorts+1)
		})

		Convey("AvailableRange still goes looking for a range as long as the whole search", func() {
			checker.listen = allPortsTaken

			first, last, err := checker.AvailableRange(searchablePorts)
			So(first, ShouldEqual, 0)
			So(last, ShouldEqual, 0)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNoContiguousRange), ShouldBeTrue)
		})

		Convey("AvailableRange never holds more ports than the range asked for", func() {
			counter := &portCounter{}
			checker.listen = counter.listen

			first, last, err := checker.AvailableRange(4)
			So(err, ShouldBeNil)
			So(first, ShouldBeBetweenOrEqual, 1, maxPort)
			So(last, ShouldEqual, first+3)
			So(counter.peak, ShouldEqual, 4)
			So(counter.live, ShouldEqual, 0)
		})

		Convey("AvailableRange reports a host with no free ports to give", func() {
			checker.listen = allPortsTaken

			first, last, err := checker.AvailableRange(4)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNoContiguousRange), ShouldBeTrue)
			So(first, ShouldEqual, 0)
			So(last, ShouldEqual, 0)
		})

		Convey("The operating system offering something that is not a tcp port hands it back anyway", func() {
			offered := &unixAddrListener{}
			checker.listen = offered.listen

			port, err := checker.offeredPort()
			So(port, ShouldEqual, 0)
			So(offered.closed, ShouldBeTrue)
			So(checker.listeners, ShouldBeEmpty)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errListenerAddrNotTCP), ShouldBeTrue)
		})

		Convey("AvailableRange reports ports it could not release, and offers no range", func() {
			checker.listen = portsThatWillNotClose

			first, last, err := checker.AvailableRange(4)
			So(first, ShouldEqual, 0)
			So(last, ShouldEqual, 0)
			So(checker.listeners, ShouldBeEmpty)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, syscall.EINVAL), ShouldBeTrue)
		})

		Convey("AvailableRange fails when tcp listening fails", func() {
			// an address in the documentation range is not on this host, so no
			// user can listen on it, privileged or not
			addr, err := net.ResolveTCPAddr("tcp", "203.0.113.1:0")
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

// checkAvailableRange asserts that the checker hands back a contiguous range
// size long. It needs no tolerance for a low ulimit -n: a search holds only
// size ports at a time.
func checkAvailableRange(checker *Checker, size int) {
	first, last, err := checker.AvailableRange(size)
	So(err, ShouldBeNil)
	So(first, ShouldBeBetweenOrEqual, 1, maxPort)
	So(last, ShouldEqual, first+size-1)
}

// checkRejectedRangeSize asserts that the checker turns down a size it could
// never hand back a range for, saying so instead of offering a range that runs
// backwards.
func checkRejectedRangeSize(checker *Checker, size int) {
	first, last, err := checker.AvailableRange(size)
	So(first, ShouldEqual, 0)
	So(last, ShouldEqual, 0)
	So(err, ShouldNotBeNil)
	So(errors.Is(err, errInvalidRangeSize), ShouldBeTrue)
}

type countedListener struct {
	*net.TCPListener

	counter *portCounter
}

func (c *countedListener) Close() error {
	c.counter.live--

	return c.TCPListener.Close()
}

// portCounter takes real ports, counting how many of them are held at the same
// time. A search that waits for the operating system to hand out adjacent ports
// by chance peaks at a quarter of the host's ephemeral port range, which is
// what exhausted that range in the test suite; one that tries candidate ranges
// by number peaks at the size it was asked for.
type portCounter struct {
	live int
	peak int
}

func (p *portCounter) listen(addr *net.TCPAddr) (listener, error) {
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return nil, err
	}

	p.live++

	if p.live > p.peak {
		p.peak = p.live
	}

	return &countedListener{TCPListener: l, counter: p}, nil
}

type takenPortListener struct{}

func (t takenPortListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: minSearchPort}
}

func (t takenPortListener) Close() error {
	return nil
}

// allPortsTaken stands in for a host that can be listened on, but on which
// every port the sweep asks for by number is already in use.
func allPortsTaken(addr *net.TCPAddr) (listener, error) {
	if addr.Port != 0 {
		return nil, syscall.EADDRINUSE
	}

	return takenPortListener{}, nil
}

// portsThatWillNotClose stands in for a host that hands out every port asked
// for by number, but will not close the ones the sweep claims. The port the
// operating system offers closes cleanly, so the sweep gets as far as holding
// a whole range, and only releasing that range fails.
func portsThatWillNotClose(addr *net.TCPAddr) (listener, error) {
	if addr.Port == 0 {
		return takenPortListener{}, nil
	}

	return unclosableListener{port: addr.Port}, nil
}

// unixAddrListener stands in for a listener on something that is not a tcp
// port, so has no port number for the checker to offer. It records having been
// closed, so a test can see the checker gave it back.
type unixAddrListener struct {
	closed bool
}

func (u *unixAddrListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "wr_port_test.sock", Net: "unix"}
}

func (u *unixAddrListener) Close() error {
	u.closed = true

	return nil
}

func (u *unixAddrListener) listen(_ *net.TCPAddr) (listener, error) {
	return u, nil
}

// unclosableListener stands in for a port that was taken, but that the host
// will not give back.
type unclosableListener struct {
	port int
}

func (u unclosableListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: u.port}
}

func (u unclosableListener) Close() error {
	return syscall.EINVAL
}
