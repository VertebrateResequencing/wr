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
	"fmt"
	"net"
)

const (
	maxPort = 65535
	// minSearchPort is the lowest port a range can start at, keeping the search
	// clear of the well-known and registered ports below it.
	minSearchPort = 10000
	// searchablePorts is how many ports the search covers, and so the longest
	// range it could ever hand back.
	searchablePorts = maxPort - minSearchPort + 1
)

var (
	errListenerAddrNotTCP = errors.New("listener address was not *net.TCPAddr")
	errNoContiguousRange  = errors.New("no contiguous range of available ports")
	errInvalidRangeSize   = errors.New("invalid port range size")
)

// listener interface is used instead of *net.TCPListener directly, so that we
// can test with a mock version.
type listener interface {
	Addr() net.Addr
	Close() error
}

func listenTCP(addr *net.TCPAddr) (listener, error) {
	return net.ListenTCP("tcp", addr)
}

// listenFunc takes a port by listening on addr. Checker holds one so that a
// test can see which ports a search takes, and how many of them it holds at
// once.
type listenFunc func(addr *net.TCPAddr) (listener, error)

// Checker is used to check for available ports on a host.
type Checker struct {
	Addr      *net.TCPAddr
	listeners []listener
	listen    listenFunc
}

// NewChecker returns a Checker that can check ports on the given host.
func NewChecker(host string) (*Checker, error) {
	addr, err := net.ResolveTCPAddr("tcp", host+":0")
	if err != nil {
		return nil, err
	}

	return &Checker{
		Addr:   addr,
		listen: listenTCP,
	}, nil
}

// AvailableRange walks up from a free port the operating system picked, until
// it holds size ports in a row, wrapping round to minSearchPort at the top. It
// returns the first and last of those port numbers, having released all the
// ports it took, so they are ready for you to use. Starting where the operating
// system's own pick falls keeps the usual answer inside the range it hands out.
//
// It holds at most size ports at once, releasing a candidate range before
// trying the next. Testing candidate ranges directly is what keeps it to that:
// waiting instead for the operating system to hand out size adjacent ports by
// chance meant holding a quarter of the whole ephemeral port range at once,
// which on a busy machine took what was left of that range and then failed with
// "bind: address already in use" on a request for any port at all.
//
// A size below 1, or above the number of ports the search covers, is turned
// down outright: no host could satisfy it, so it is a mistake in the request
// rather than a host that happens to be full.
//
// A port it could not give back is an error too, and comes back with no range
// at all: a range it may still be holding one of is not one you can use.
//
// NB: there is the potential for a race condition here, where once released,
// another process gets one of the ports before you use it, so start listening
// on all the returned ports as soon as possible after calling this.
func (c *Checker) AvailableRange(size int) (first, last int, err error) {
	if size < 1 || size > searchablePorts {
		return 0, 0, fmt.Errorf("%w: %d is not between 1 and %d", errInvalidRangeSize, size, searchablePorts)
	}

	defer func() { first, last, err = c.releasedRange(first, last, err) }()

	lastStart := maxPort - size + 1
	starts := lastStart - minSearchPort + 1

	offered, err := c.offeredPort()
	if err != nil {
		return 0, 0, err
	}

	origin := max(offered, minSearchPort)

	for try := range starts {
		start := minSearchPort + (origin-minSearchPort+try)%starts

		if c.claimRange(start, size) {
			return start, start + size - 1, nil
		}

		if err = c.release(nil); err != nil {
			return 0, 0, err
		}
	}

	return 0, 0, fmt.Errorf("%w of %d", errNoContiguousRange, size)
}

// releasedRange gives back every port the search is still holding, and turns
// the range it found into no range at all if any of them would not close: a
// range we might still be holding one of is not one a caller can use.
func (c *Checker) releasedRange(first, last int, err error) (int, int, error) {
	if err = c.release(err); err != nil {
		return 0, 0, err
	}

	return first, last, nil
}

// offeredPort has the operating system pick a free port, and releases it again.
// It both proves the host can be listened on at all, and gives the sweep a
// starting point that differs between calls, so concurrent searches do not all
// walk the same ports in the same order. It gives the port back on every way
// out, so that it holds nothing whether it found a port number or not.
func (c *Checker) offeredPort() (int, error) {
	l, err := c.listen(c.Addr)
	if err != nil {
		return 0, err
	}

	c.listeners = append(c.listeners, l)

	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, c.release(fmt.Errorf("%w: %T", errListenerAddrNotTCP, l.Addr()))
	}

	return addr.Port, c.release(nil)
}

// claimRange takes the size ports starting at first, reporting whether it took
// them all. It leaves the ones it did take held, for release to close.
func (c *Checker) claimRange(first, size int) bool {
	for port := first; port < first+size; port++ {
		l, err := c.listen(&net.TCPAddr{IP: c.Addr.IP, Port: port, Zone: c.Addr.Zone})
		if err != nil {
			return false
		}

		c.listeners = append(c.listeners, l)
	}

	return true
}

func (c *Checker) release(err error) error {
	for _, l := range c.listeners {
		errl := l.Close()
		if errl == nil {
			continue
		}

		if err == nil {
			err = errl

			continue
		}

		err = fmt.Errorf("%w; %w", err, errl)
	}

	c.listeners = nil

	return err
}
