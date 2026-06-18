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
	"fmt"
	"net"
)

const maxPort = 65535
const maxTries = maxPort - 10000

// listener interface is used instead of *net.TCPListener directly, so that we
// can test with a mock version.
type listener interface {
	Addr() net.Addr
	Close() error
}

// Checker is used to check for available ports on a host.
type Checker struct {
	Addr      *net.TCPAddr
	listeners []listener
	ports     map[int]bool
}

// NewChecker returns a Checker that can check ports on the given host.
func NewChecker(host string) (*Checker, error) {
	addr, err := net.ResolveTCPAddr("tcp", host+":0")
	if err != nil {
		return nil, err
	}

	return &Checker{
		Addr:  addr,
		ports: make(map[int]bool),
	}, nil
}

// AvailableRange asks the operating system for ports until it is given a
// contiguous range of ports size long. It returns the first and last of those
// port numbers, having released all the ports it took, so they are ready for
// you to use.
//
// NB: there is the potential for a race condition here, where once released,
// another process gets one of the ports before you use it, so start listening
// on all the returned ports as soon as possible after calling this.
func (c *Checker) AvailableRange(size int) (int, int, error) {
	var err error

	defer func() {
		err = c.release(err)
	}()

	var min, max int

	for i := 0; i < maxTries; i++ {
		var port int

		port, err = c.availablePort()
		if err != nil {
			break
		}

		if set, has := c.checkRange(port, size); has {
			min, max = firstAndLast(set)

			break
		}
	}

	return min, max, err
}

func (c *Checker) availablePort() (int, error) {
	l, err := net.ListenTCP("tcp", c.Addr)
	if err != nil {
		return 0, err
	}

	c.listeners = append(c.listeners, l)
	port := l.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert
	c.ports[port] = true

	return port, nil
}

func (c *Checker) checkRange(start, size int) ([]int, bool) {
	if len(c.ports) < size {
		return nil, false
	}

	after := c.portsAfter(start)
	if len(after)+1 >= size {
		return append([]int{start}, after[0:size-1]...), true
	}

	before := c.portsBefore(start)
	if len(before)+1 >= size {
		return append(before[len(before)-size+1:], start), true
	}

	if len(before)+len(after)+1 >= size {
		combined := append(before, append([]int{start}, after...)...) //nolint:gocritic

		return combined[0:size], true
	}

	return nil, false
}

func (c *Checker) portsAfter(start int) []int {
	var ports []int

	for i := start + 1; i <= maxPort; i++ {
		if c.ports[i] {
			ports = append(ports, i)
		} else {
			break
		}
	}

	return ports
}

func (c *Checker) portsBefore(start int) []int {
	var ports []int

	for i := start - 1; i >= 1; i-- {
		if c.ports[i] {
			ports = append([]int{i}, ports...)
		} else {
			break
		}
	}

	return ports
}

func (c *Checker) release(err error) error {
	for _, l := range c.listeners {
		errl := l.Close()
		if errl != nil {
			err = fmt.Errorf("%w; %s", err, errl.Error())
		}
	}

	c.listeners = nil
	c.ports = make(map[int]bool)

	return err
}

func firstAndLast(s []int) (min, max int) {
	return s[0], s[len(s)-1]
}
