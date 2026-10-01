//go:build linux

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
	"errors"
	"os"
	"syscall"
)

// bindPortReservation binds a socket to port on every address with
// SO_REUSEADDR, but does not listen on it. SO_REUSEADDR lets it bind past the
// TIME_WAITs a previous manager's own connections leave on the port, and lets
// our real listener bind alongside it.
//
// The socket is IPv6 with IPV6_V6ONLY off, bound to [::], so it covers both
// IPv4 and IPv6, as the manager's own listener does: an IPv4 socket would
// reserve a port that an IPv6-only listener already holds, and the manager's
// listener would then fail to bind it. If the IPv6 socket cannot be created or
// bound for any reason other than the port being in use (as on a host without
// IPv6), it is IPv4 on 0.0.0.0 instead.
//
// The socket is created close-on-exec (SOCK_CLOEXEC), so no runner forked at
// the same time can inherit it: one that did would hold the port for as long as
// it lived.
func bindPortReservation(port int) (*portReservation, error) {
	res, err := bindPortReservationOn(syscall.AF_INET6, &syscall.SockaddrInet6{Port: port})
	if err == nil || errors.Is(err, syscall.EADDRINUSE) {
		return res, err
	}

	return bindPortReservationOn(syscall.AF_INET, &syscall.SockaddrInet4{Port: port})
}

// bindPortReservationOn is bindPortReservation for one address family, with
// addr the wildcard address of that family on the port.
func bindPortReservationOn(family int, addr syscall.Sockaddr) (*portReservation, error) {
	fd, err := syscall.Socket(family, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}

	if err = setReservationSockopts(fd, family); err != nil {
		_ = syscall.Close(fd)

		return nil, err
	}

	if err = syscall.Bind(fd, addr); err != nil {
		_ = syscall.Close(fd)

		return nil, os.NewSyscallError("bind", err)
	}

	return &portReservation{fd: fd}, nil
}

// setReservationSockopts sets SO_REUSEADDR on fd, and for an IPv6 socket turns
// IPV6_V6ONLY off, whatever the host's net.ipv6.bindv6only default.
func setReservationSockopts(fd, family int) error {
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		return os.NewSyscallError("setsockopt", err)
	}

	if family != syscall.AF_INET6 {
		return nil
	}

	if err := syscall.SetsockoptInt(fd, syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 0); err != nil {
		return os.NewSyscallError("setsockopt", err)
	}

	return nil
}
